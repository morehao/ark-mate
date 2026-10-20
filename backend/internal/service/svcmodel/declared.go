package svcmodel

import (
	"context"
	"fmt"
	"strings"

	defn "github.com/morehao/ark-mate/internal/core/definition"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/resource"
	"github.com/morehao/ark-mate/internal/domain/provider"
	provrow "github.com/morehao/ark-mate/internal/domain/provider/row"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ——— P4a 正文第五刀：平台模型清单的**写面**用例 ———
//
// 这一刀之前，三条写动作是 `manager/model_list.go` 上的 `*Service` 方法（与读面、目录面、
// 同步面同处一个 582 行的文件）。搬过来的是**不碰执行面**的那半边：校验、错误码出口、
// 单一默认、行 CAS、响应装配。留在 `manager` 的是读面（`ListModels` 的投影）、生效面回执
// （`ModelEffect`：要问"哪些会话会顺延"）与同步触发（`TrySyncModels`：要写各底座配置）——
// 后两件的判据都在**执行面**上，随 `Orchestrator` 在 P5 一起 dissolve。
//
// 于是这一层拿的是**依赖事实**加**一个过渡期端口**：
//
//	ModelStore        清单的窄端口（sqlite 模式才有；memory 模式为 nil ⇒ 写面 40501）
//	*provider.Plane   挂靠校验要问"这条供应商在不在"（域平面只有一份实例，见 `provider.NewPlane`）
//	Host              写面之外的模型面（读一条的投影 / 生效面回执 / 同步触发；见下）
//
// **关于 `Host` 这个名字**：它不是一个"只做一行转发的适配器层"，它是**还没搬完的那半边**的
// 收口——三条各自都是真实现（一套行投影、一套代际裁决、一套配置写面），只是今天还住在
// `manager`。搬进本包的那一天，`Host` 这个接口整个删掉，`Declared` 的构造参数少一个。
type Host interface {
	// ModelRow 读一条模型条目并投影成对外表示（`found=false` = 不在册）。
	//
	// 它是**读面**的一部分：搬过来之后 `Declared` 直接读 `models` 表，这个口子随之消失。
	// 形状是"查得到/查不到"：错误码（40401 `model_not_found`）由用例铸，两层不各铸一遍。
	ModelRow(ctx context.Context, id string) (*dtomodel.ModelResp, bool, error)
	// ModelEffect 写面提交后的**生效面回执**（哪些会话会顺延）。
	//
	// 判据与代际裁决同源（`planGenerationSwitch`），而那份裁决与连接/会话编排绑在一起——
	// 所以它在 P5 之前无法进这一层。**它不是装饰**：删掉它，用户就看不到"这不是「已生效」"。
	ModelEffect(defaultTouched bool) *dtomodel.ModelEffect
	// TrySyncModels 提交后立刻把清单同步给各底座，返回一句给用户看的话（空 = 无需说明）。
	//
	// 失败**不回滚**清单（清单是意图，配置是物化；见 `resource_sync.go` 的注释）。
	TrySyncModels(ctx context.Context) string
}

// ModelStore 模型清单的**窄端口**（v3.1 P4：用例层不 import store）。
//
// `*store.Store` 结构上满足它，装配根直接交下来；memory 模式传 nil（写面据此报 40501）。
// 六个方法就是本用例真正用到的全部：多一个都算"这层还能动别的东西"。
type ModelStore interface {
	CreateModelDefinition(ctx context.Context, cols model.ModelColumns, actor string) (bool, error)
	UpdateModelDefinition(ctx context.Context, id string, content model.ModelContent, expectedRev int64, actor string) (bool, error)
	DeleteModelDefinitionAndClearDefault(ctx context.Context, id string, expectedRev int64, actor string) (bool, bool, error)
	GetModelDefinition(ctx context.Context, id string) (*provrow.Model, bool, error)
	GetProviderDefinition(ctx context.Context, id string) (*provrow.Item, bool, error)
	SetDefaultModelID(ctx context.Context, id string) error
	ClearDefaultModelID(ctx context.Context, id string) (bool, error)
}

// Declared 平台模型清单的写面用例（见文件头）。
type Declared struct {
	st    ModelStore
	plane *provider.Plane
	host  Host
}

// NewDeclared 造写面用例。三个入参都是装配根交下来的**依赖事实/过渡端口**（实例在装配根构造，控制器只收接口）。
func NewDeclared(st ModelStore, plane *provider.Plane, host Host) DeclaredSvc {
	return &Declared{st: st, plane: plane, host: host}
}

var _ DeclaredSvc = (*Declared)(nil)

// CreateModel 新建一条模型条目（POST /models）。
func (m *Declared) CreateModel(ctx context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error) {
	if m.st == nil {
		return nil, modelListUnavailableErr()
	}
	if err := rejectRuntimeField(req.Runtime); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, acp.NewError(acp.ClassRequest, "invalid_param", "model 必填（发给上游的模型名）", nil)
	}
	if err := m.requireProvider(ctx, req.ProviderID); err != nil {
		return nil, err
	}
	modalities, err := normalizeInputModalities(req.InputModalities)
	if err != nil {
		return nil, err
	}
	maxIn, err := normalizeCapacity("max_input_tokens", req.MaxInputTokens)
	if err != nil {
		return nil, err
	}
	maxOut, err := normalizeCapacity("max_output_tokens", req.MaxOutputTokens)
	if err != nil {
		return nil, err
	}
	id := objid.New(objid.Model)
	ok, err := m.st.CreateModelDefinition(ctx, model.ModelColumns{
		ID: id, ProviderID: req.ProviderID, Model: strings.TrimSpace(req.Model),
		Name: derefString(req.Name), Note: derefString(req.Note), InputModalities: modalities,
		MaxInputTokens: maxIn, MaxOutputTokens: maxOut,
	}, defn.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, modelExistsErr(req.ProviderID, req.Model)
	}
	if derefBool(req.IsDefault) {
		if err := m.st.SetDefaultModelID(ctx, id); err != nil {
			return nil, err
		}
	}
	resp, found, err := m.host.ModelRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, modelNotFoundErr(id)
	}
	resp.Effect = m.host.ModelEffect(derefBool(req.IsDefault))
	// 清单已提交 → 立刻同步给各底座。失败不回滚清单。
	if note := m.host.TrySyncModels(ctx); note != "" {
		resp.Effect.Note = note
	}
	return resp, nil
}

// UpdateModel 覆盖一条模型条目（PATCH /models/:id）。
func (m *Declared) UpdateModel(ctx context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error) {
	if m.st == nil {
		return nil, modelListUnavailableErr()
	}
	if err := rejectRuntimeField(patch.Runtime); err != nil {
		return nil, err
	}
	row, found, err := m.st.GetModelDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, modelNotFoundErr(id)
	}
	// 指针更新：只改 patch 里出现过的键。
	content := model.ModelContent{
		Name: row.Name, Note: row.Note,
		// 列值归一：空列（只可能来自手写行，列默认值是 `text`）也要写成明确值——否则"只改名字"
		// 这一步会把空串原样写回，让"默认 text"变成第三种落库形态。
		InputModalities: resource.ModalitiesToColumn(resource.ModalitiesFromColumn(row.InputModalities)),
		// 容量是**解析后的最终值**：省略 = 保持原值；0 = 清除（nil）；≥1 = 声明。
		MaxInputTokens: row.MaxInputTokens, MaxOutputTokens: row.MaxOutputTokens,
	}
	if patch.Name != nil {
		content.Name = *patch.Name
	}
	if patch.Note != nil {
		content.Note = *patch.Note
	}
	if patch.InputModalities != nil {
		modalities, err := normalizeInputModalities(patch.InputModalities)
		if err != nil {
			return nil, err
		}
		content.InputModalities = modalities
	}
	if patch.MaxInputTokens != nil {
		v, err := normalizeCapacity("max_input_tokens", patch.MaxInputTokens)
		if err != nil {
			return nil, err
		}
		content.MaxInputTokens = v
	}
	if patch.MaxOutputTokens != nil {
		v, err := normalizeCapacity("max_output_tokens", patch.MaxOutputTokens)
		if err != nil {
			return nil, err
		}
		content.MaxOutputTokens = v
	}
	if patch.Name != nil || patch.Note != nil || patch.InputModalities != nil ||
		patch.MaxInputTokens != nil || patch.MaxOutputTokens != nil {
		ok, err := m.st.UpdateModelDefinition(ctx, id, content, row.Revision, defn.DefinitionWriteActor)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, defn.DefinitionChangedErr("db:model/" + id)
		}
	}
	// 置/清默认是**两个方向**，都必须被处理：只处理 `true` 会让 `is_default:false` 静默无效。
	if patch.IsDefault != nil {
		if *patch.IsDefault {
			if err := m.st.SetDefaultModelID(ctx, id); err != nil {
				return nil, err
			}
		} else if cleared, err := m.st.ClearDefaultModelID(ctx, id); err != nil {
			return nil, err
		} else if !cleared {
			// 它不是默认：幂等无操作（不是错误）。
			_ = cleared
		}
	}
	resp, found, err := m.host.ModelRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, modelNotFoundErr(id)
	}
	resp.Effect = m.host.ModelEffect(patch.IsDefault != nil && *patch.IsDefault)
	if note := m.host.TrySyncModels(ctx); note != "" {
		resp.Effect.Note = note
	}
	return resp, nil
}

// DeleteModel 删除一条模型条目（DELETE /models/:id）。
//
// 幂等。**行为变更（B4）**：v11 里"它是该 runtime 的默认模型"会被 40501 拒绝，v12 **允许删**
// 并**连带清空默认**——默认是**偏好**，偏好跟着资源消失比拦住用户更合理；清了就说出来
// （`DefaultCleared`），不静默。
func (m *Declared) DeleteModel(ctx context.Context, id string) (*dtomodel.ModelDeleteResp, error) {
	if m.st == nil {
		return nil, modelListUnavailableErr()
	}
	row, found, err := m.st.GetModelDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return &dtomodel.ModelDeleteResp{Deleted: false}, nil
	}
	// 删行 + 清默认**同一事务**（v12 是两次独立调用，中间有一个"默认指向已删行"的悬挂窗口）。
	deleted, cleared, err := m.st.DeleteModelDefinitionAndClearDefault(ctx, id, row.Revision, defn.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	resp := &dtomodel.ModelDeleteResp{Deleted: deleted}
	if cleared {
		resp.DefaultCleared = true
	}
	// 删掉一条也要重新同步：底座的配置里不能继续留着它。
	m.host.TrySyncModels(ctx)
	return resp, nil
}

// modelListUnavailableErr memory 模式没有 `models` 表：写面明确报 40501，
// 而不是"内存生效、盘上没留痕"。
func modelListUnavailableErr() error {
	return acp.NewError(acp.ClassUnsupported, "model_list_unavailable",
		"模型清单需要 sqlite 持久层（MATE_STORE=sqlite）：memory 模式没有平台侧清单载体，"+
			"模型面仍由运行时派生", nil)
}

// rejectRuntimeField 显式拒收请求体里的 `runtime`（A2）。
//
// 为什么宁可报错也不静默忽略：模型不再分底座之后，`runtime` 是一个会让人以为"这条模型只给
// 某个底座"的陷阱。静默收下就等于承认它有意义——而它什么都不做。
func rejectRuntimeField(v *string) error {
	if v == nil {
		return nil
	}
	return acp.NewError(acp.ClassRequest, "invalid_param",
		fmt.Sprintf("`runtime` 不是模型资源的字段（收到 %q）：v12 起一份清单投给所有已注册底座。"+
			"要只为某个底座准备模型，请改用该底座自己的配置；平台侧不再有逐底座清单", *v), nil)
}

// requireProvider 校验挂靠的供应商存在（不存在 → 40401 provider_not_found）。
func (m *Declared) requireProvider(ctx context.Context, providerID string) error {
	if strings.TrimSpace(providerID) == "" {
		return acp.NewError(acp.ClassRequest, "invalid_param", "provider_id 必填（模型必须挂靠在一条供应商下）", nil)
	}
	if m.st != nil {
		if _, found, err := m.st.GetProviderDefinition(ctx, providerID); err != nil {
			return err
		} else if found {
			return nil
		}
	}
	// **不持任何上层锁**：`Exists` 自己取供应商域的锁（见 `manager_lock_order_test.go`：
	// 上层持锁去请求域平面就是锁序反转的雏形）。
	if m.plane == nil || !m.plane.Exists(providerID) {
		return acp.NewError(acp.ClassRequest, "provider_not_found",
			fmt.Sprintf("供应商 %q 不存在：模型必须挂靠在一条已登记的供应商下", providerID), nil)
	}
	return nil
}

// normalizeCapacity 校验并归一一个容量声明（v25）。
//
// 三态见 `dtomodel.ModelPatchReq` 的注释：nil = 不改 / 0 = 清除 / ≥1 = 声明。**负数即拒绝**——
// 枚举与数值型键的既有纪律是"不认识的取值当场报错，不静默回落"，因为容量的静默回落会变成
// "用户以为设了 8k，实际用的是兜底的 256k"。
func normalizeCapacity(field string, v *int64) (*int64, error) {
	if v == nil || *v == 0 {
		return nil, nil
	}
	if *v < 0 {
		return nil, acp.NewError(acp.ClassRequest, "invalid_param",
			fmt.Sprintf("%s 必须是 ≥ 1 的整数（0 = 清除这条声明）：收到 %d", field, *v), nil)
	}
	return v, nil
}

// normalizeInputModalities 校验并归一请求里的输入模态（nil = 默认 `["text"]`）。
//
// 枚举型键的既有纪律：不认识的取值 → 40501 并**列出合法取值**（不静默回落）。
// 返回落库形态（逗号分隔；两态 `text` | `text,image`）。
func normalizeInputModalities(in *[]string) (string, error) {
	if in == nil {
		return resource.ModalityText, nil
	}
	for _, v := range *in {
		if !resource.ValidModalities[strings.TrimSpace(v)] {
			return "", acp.NewError(acp.ClassUnsupported, "modality_unsupported",
				fmt.Sprintf("不支持的输入模态 %q：合法取值是 `text`（默认）与 `image`。"+
					"图片输入是在文本之上追加的能力，所以 `image` 会落成 `text,image`", v), nil)
		}
	}
	return resource.ModalitiesToColumn(*in), nil
}

func modelExistsErr(providerID, modelID string) error {
	return acp.NewError(acp.ClassLimit, "model_exists",
		fmt.Sprintf("供应商 %q 下已经声明过模型 %q：同一条模型在同一个供应商下只能有一行",
			providerID, modelID), nil)
}

func modelNotFoundErr(id string) error {
	return acp.NewError(acp.ClassRequest, "model_not_found", fmt.Sprintf("未知模型条目 %q", id), nil)
}

// derefString 解引用可选字符串（nil → 空串）。
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}
