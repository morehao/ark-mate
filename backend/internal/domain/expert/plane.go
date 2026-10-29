// Package expert 是「专家定义」的领域平面（P0 定义面）。
//
// 专家是一条命名的会话级能力预设：推荐技能/MCP/模型（P1 起带角色设定）。它**不拥有**技能——只引用
// id；因此本平面只有一个事实源（`experts` 表）与一份内存注册表视图（与 skill/mcp 共用
// `capability.Registry`）。写面沿用 skill/mcp 的 body+revision 形态：先校验、再提交载体、最后入表
// （顺序倒过来会留下"内存里有、库里没有"的幻影）。
//
// 与 skill/mcp 的差别只有一处：专家**没有文件载体**（只有 DB），所以不需要 `DefinitionRepo` 的
// 文件纪律，写面直接落 store 的 revision CAS。
package expert

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/definition"
	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// Store 专家定义的持久化窄端口（由装配侧用 store.Store 适配）。
//
// 只暴露写面 CAS 与读面：与 `DefinitionDao` 的 revision CAS 同一条判据（rowsAffected==0 →
// definition_changed），不在这里再判一次并发。
type Store interface {
	ListExpertDefinitions(ctx context.Context) (model.ExpertEntityList, error)
	GetExpertDefinition(ctx context.Context, id string) (*model.ExpertEntity, bool, error)
	UpsertExpertDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error)
	DeleteExpertDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error)
}

// Plane 专家平面的有状态主体：持有能力注册表（专家定义的内存视图）与持久化端口。
type Plane struct {
	caps  *capability.Registry
	store Store
}

// NewPlane 造一个专家平面；store 由 `SetStore` 在装配期接上（构造期可能还没接库）。
func NewPlane(caps *capability.Registry) *Plane {
	return &Plane{caps: caps}
}

// SetStore 装配期登记持久化端口（nil = memory 模式：写面不可用）。
func (p *Plane) SetStore(st Store) { p.store = st }

// View 专家读面的一条（领域不产出对外 JSON 形态；DTO 由 svccapability 投影）。
type View struct {
	ID          string
	Name        string
	Description string
	Avatar      string
	Model       string
	Skills      []string
	Mcps        []string
	Enabled     bool
	Tags        []string
	// Instructions 角色设定全文（列表读面不填，仅详情返回）。
	Instructions string
}

// HasInstructions 该专家是否带角色设定（列表用它标记，不返回正文）。
func (v View) HasInstructions() bool { return strings.TrimSpace(v.Instructions) != "" }

// WriteResult 新建/覆盖专家定义的结果。
type WriteResult struct {
	ID      string
	Path    string
	Created bool
}

// DeleteResult 删除专家定义的结果（不存在 → Deleted=false 且无错：DELETE 幂等）。
type DeleteResult struct {
	ID      string
	Deleted bool
	Path    string
}

// ToggleResult 启停专家定义的结果。
type ToggleResult struct {
	ID      string
	Enabled bool
}

// Locator 专家定义的定位串（给人看的；DB 载体没有文件路径）。
func Locator(id string) string { return "db:expert/" + id }

func viewOf(e capability.Expert) View {
	return View{
		ID: e.ID, Name: e.Name, Description: e.Description, Avatar: e.Avatar, Model: e.Model,
		Skills: append([]string(nil), e.Skills...), Mcps: append([]string(nil), e.Mcps...),
		Enabled: e.IsEnabled(), Tags: append([]string(nil), e.Tags...), Instructions: e.Instructions,
	}
}

// List 返回全部专家（含禁用；列表不返回 instructions 正文——由调用方投影时决定是否带）。
func (p *Plane) List(ctx context.Context) ([]View, error) {
	if p.store == nil {
		return nil, definition.ErrCapabilityUnavailable()
	}
	rows, err := p.store.ListExpertDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rows))
	for i := range rows {
		e, derr := decodeExpert(rows[i].ID, rows[i].Body)
		if derr != nil {
			// 坏行不阻断列表：以「空定义」形式仍出现，好让用户看到并修它（与装载期容错同向）。
			out = append(out, View{ID: rows[i].ID, Name: rows[i].ID})
			continue
		}
		out = append(out, viewOf(e))
	}
	return out, nil
}

// Get 读一条专家（ok=false = 不存在）。
func (p *Plane) Get(ctx context.Context, id string) (View, bool, error) {
	if p.store == nil {
		return View{}, false, definition.ErrCapabilityUnavailable()
	}
	row, ok, err := p.store.GetExpertDefinition(ctx, id)
	if err != nil || !ok {
		return View{}, false, err
	}
	e, derr := decodeExpert(row.ID, row.Body)
	if derr != nil {
		// 库里存在但正文坏了：报 40001 指路修它（不是 404——资源存在）。
		return View{}, true, failure.NewError(failure.ClassRequest, "definition_invalid",
			fmt.Sprintf("专家 %q 的定义无法解析：%v", id, derr), derr)
	}
	return viewOf(e), true, nil
}

// Write 新建或覆盖一条专家定义。create=true 时 id 取自 body（必填），否则取自 pathID，body id 若出现须一致。
//
// 语义与 skill/mcp 写面一致：PUT 只改 body 里出现的键（`null` 删键），未出现的保留原值；未知键原样保留。
func (p *Plane) Write(ctx context.Context, pathID string, req WriteRequest, create bool) (*WriteResult, error) {
	if p.store == nil {
		return nil, definition.ErrCapabilityUnavailable()
	}
	patch := definition.RawPatch(req) // 复用同一套键语义（null = 删键）

	id := pathID
	bodyID, hasBodyID, err := definition.RawString(req, "id")
	if err != nil {
		return nil, err
	}
	if create {
		if !hasBodyID || strings.TrimSpace(bodyID) == "" {
			return nil, definition.InvalidParamErr("新建 expert 必须带 id")
		}
		id = strings.TrimSpace(bodyID)
	} else {
		if hasBodyID && bodyID != pathID {
			return nil, definition.InvalidParamErr(fmt.Sprintf(
				"路径 id %q 与 body id %q 不一致（id 不可改：要换 id 就删掉重建）", pathID, bodyID))
		}
	}
	if !definition.ValidDefinitionID(id) {
		return nil, definition.InvalidDefinitionIDErr("expert", id)
	}

	var baseBody string
	var expectedRev int64
	if create {
		if _, ok, gerr := p.store.GetExpertDefinition(ctx, id); gerr != nil {
			return nil, gerr
		} else if ok {
			return nil, existsErr(id)
		}
	} else {
		row, ok, gerr := p.store.GetExpertDefinition(ctx, id)
		if gerr != nil {
			return nil, gerr
		}
		if !ok {
			return nil, notFoundErr(id)
		}
		baseBody, expectedRev = row.Body, row.Revision
	}

	merged, err := mergeDoc(baseBody, patch, id)
	if err != nil {
		return nil, err
	}
	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var e capability.Expert
	if err := json.Unmarshal(mergedBytes, &e); err != nil {
		return nil, definition.InvalidParamErr(fmt.Sprintf("expert 定义无法解析：%v", err))
	}
	e.ID = id
	if err := capability.ValidateExpert(&e); err != nil {
		return nil, failure.NewError(failure.ClassRequest, "definition_invalid",
			fmt.Sprintf("expert %q 的定义被拒绝：%v", id, err), err)
	}
	// 把规范化后的已知键写回文档（**未知键原样保留**——它们是用户/第三方字段，抹掉即丢数据）。
	if err := applyExpertToDoc(merged, &e); err != nil {
		return nil, err
	}
	canon, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	applied, err := p.store.UpsertExpertDefinition(ctx, id, string(canon), expectedRev, definition.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !applied {
		// CAGR 没中（并发写抢先 / 行已不存在）：翻成同一个 definition_changed(40901)，绝不 last-write-wins。
		return nil, definition.DefinitionChangedErr(Locator(id))
	}
	created := create
	// 提交成功后才入表（同一份定义刚过闸门，必然成功；真失败只记 WARN，不谎报写失败）。
	if rerr := p.caps.RegisterExpert(&e); rerr != nil {
		return nil, rerr
	}
	return &WriteResult{ID: e.ID, Path: Locator(id), Created: created}, nil
}

// Delete 删除一条专家定义（幂等）。会话快照不受影响（会话在创建时已物化技能/MCP）。
func (p *Plane) Delete(ctx context.Context, id string) (*DeleteResult, error) {
	if p.store == nil {
		return nil, definition.ErrCapabilityUnavailable()
	}
	if !definition.ValidDefinitionID(id) {
		return &DeleteResult{ID: id, Path: Locator(id)}, nil // 非法 id 不可能存在 → 幂等 no-op
	}
	row, ok, err := p.store.GetExpertDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		p.caps.RemoveExpert(id)
		return &DeleteResult{ID: id, Path: Locator(id)}, nil
	}
	deleted, err := p.store.DeleteExpertDefinition(ctx, id, row.Revision, definition.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, definition.DefinitionChangedErr(Locator(id))
	}
	p.caps.RemoveExpert(id)
	return &DeleteResult{ID: id, Deleted: deleted, Path: Locator(id)}, nil
}

// SetEnabled 启停一条专家定义（禁用只影响新选择，不影响已建会话）。
func (p *Plane) SetEnabled(ctx context.Context, id string, enabled bool) (*ToggleResult, error) {
	_, ok, err := p.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFoundErr(id)
	}
	patch := WriteRequest{"enabled": json.RawMessage(fmt.Sprintf("%t", enabled))}
	if _, err := p.Write(ctx, id, patch, false); err != nil {
		return nil, err
	}
	return &ToggleResult{ID: id, Enabled: enabled}, nil
}

// ——— 内部工具 ———

// WriteRequest 定义写面的原文对象（键语义由 definition.RawPatch 解释）。
type WriteRequest map[string]json.RawMessage

// mergeDoc 把 patch 合并进既有 body（create 时 base 为空），返回**保留未知键**的文档 map。
//
// 未知键保留：base 先解成 map[string]json.RawMessage，patch 只增删改目标键，未知键原样留在 map 里。
func mergeDoc(base string, patch map[string]interface{}, id string) (map[string]json.RawMessage, error) {
	doc := map[string]json.RawMessage{}
	if strings.TrimSpace(base) != "" {
		if err := json.Unmarshal([]byte(base), &doc); err != nil {
			return nil, failure.NewError(failure.ClassRequest, "definition_invalid",
				fmt.Sprintf("现有 expert %q 的定义无法解析：%v", id, err), err)
		}
	}
	for k, v := range patch {
		if v == nil {
			delete(doc, k)
			continue
		}
		raw, ok := v.(json.RawMessage)
		if !ok {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			raw = b
		}
		doc[k] = raw
	}
	return doc, nil
}

// applyExpertToDoc 把规范化后的已知键写回文档（空值删键），**不动未知键**。
func applyExpertToDoc(doc map[string]json.RawMessage, e *capability.Expert) error {
	set := func(key string, v interface{}) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		doc[key] = b
		return nil
	}
	del := func(key string) { delete(doc, key) }
	if err := set("id", e.ID); err != nil {
		return err
	}
	if err := set("name", e.Name); err != nil {
		return err
	}
	if e.Description != "" {
		if err := set("description", e.Description); err != nil {
			return err
		}
	} else {
		del("description")
	}
	if e.Avatar != "" {
		if err := set("avatar", e.Avatar); err != nil {
			return err
		}
	} else {
		del("avatar")
	}
	if e.Model != "" {
		if err := set("model", e.Model); err != nil {
			return err
		}
	} else {
		del("model")
	}
	if len(e.Skills) > 0 {
		if err := set("skills", e.Skills); err != nil {
			return err
		}
	} else {
		del("skills")
	}
	if len(e.Mcps) > 0 {
		if err := set("mcps", e.Mcps); err != nil {
			return err
		}
	} else {
		del("mcps")
	}
	if e.Enabled != nil {
		if err := set("enabled", *e.Enabled); err != nil {
			return err
		}
	} else {
		del("enabled")
	}
	if len(e.Tags) > 0 {
		if err := set("tags", e.Tags); err != nil {
			return err
		}
	} else {
		del("tags")
	}
	if e.Instructions != "" {
		if err := set("instructions", e.Instructions); err != nil {
			return err
		}
	} else {
		del("instructions")
	}
	return nil
}

// decodeExpert 把一行 body 解成强类型 Expert（id 以行主键为准）。
func decodeExpert(id, body string) (capability.Expert, error) {
	var e capability.Expert
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		return capability.Expert{}, err
	}
	e.ID = id
	return e, nil
}

// existsErr 新建时 id 已占用（40901：与当前状态冲突）。
func existsErr(id string) error {
	return failure.NewError(failure.ClassLimit, "expert_exists",
		fmt.Sprintf("expert %q 已存在：改它用 PUT /experts/%s，或换一个 id", id, id), nil)
}

// notFoundErr 目标专家不存在（40401，由 internal/core/code 的 notFoundCodes 提档）。
func notFoundErr(id string) error {
	return failure.NewError(failure.ClassRequest, "expert_not_found", fmt.Sprintf("未知 expert %q", id), nil)
}
