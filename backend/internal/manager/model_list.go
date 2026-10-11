package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/golib/glog"
)

// ——— 应用层模型清单的写面（v12）：CRUD + **单一**默认模型 + 同步触发 ———
//
// 与 v11 的两处根本差别：
//
//	① **没有 runtime 维度**：一份清单投给所有已注册底座（ADR-1/B3）。于是 v11 里那套
//	   "runtime 是否已声明 ConfigProjection"的前置校验、逐 runtime 的物化、逐 runtime 的默认
//	   全部消失；
//	② **默认模型是单一值**（`meta.MetaKeyDefaultModel`，ADR-21）：它是**用户的偏好**，
//	   不是模型行的属性。`is_default` 只是写面的**触发字段**，不落进资源、不出现在读面
//	   （A14 的结构断言就钉这一点）。
//
// 清单需要 sqlite 持久层：memory 模式没有 `models` 表，所以那里的写面明确报 40501
// 而不是"内存生效、盘上没留痕"。

// modelListAvailable 清单写面是否可用（sqlite 模式才有清单载体）。
func (s *Service) modelListAvailable() bool { return s.st != nil }

func modelListUnavailableErr() error {
	return acl.NewError(acl.ClassUnsupported, "model_list_unavailable",
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
	return acl.NewError(acl.ClassRequest, "invalid_param",
		fmt.Sprintf("`runtime` 不是模型资源的字段（收到 %q）：v12 起一份清单投给所有已注册底座。"+
			"要只为某个底座准备模型，请改用该底座自己的配置；平台侧不再有逐底座清单", *v), nil)
}

// CreateModel 新建一条模型条目（POST /models）。
func (s *Service) CreateModel(ctx context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error) {
	if !s.modelListAvailable() {
		return nil, modelListUnavailableErr()
	}
	if err := rejectRuntimeField(req.Runtime); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return nil, acl.NewError(acl.ClassRequest, "invalid_param", "model_id 必填", nil)
	}
	if err := s.requireProvider(ctx, req.ProviderID); err != nil {
		return nil, err
	}
	modalities, err := normalizeInputModalities(req.InputModalities)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	ok, err := s.st.CreateModelDefinition(ctx, store.ModelColumns{
		ID: id, ProviderID: req.ProviderID, ModelID: strings.TrimSpace(req.ModelID),
		Name: derefString(req.Name), Note: derefString(req.Note), InputModalities: modalities,
	}, definitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, modelExistsErr(req.ProviderID, req.ModelID)
	}
	if derefBool(req.IsDefault) {
		if err := s.st.SetDefaultModelID(ctx, id); err != nil {
			return nil, err
		}
	}
	resp, err := s.modelResp(ctx, id)
	if err != nil {
		return nil, err
	}
	resp.Effect = s.modelEffect(derefBool(req.IsDefault))
	// 清单已提交 → 立刻同步给各底座。失败不回滚清单（见 trySyncModels）。
	if note := s.trySyncModels(ctx); note != "" {
		resp.Effect.Note = note
	}
	return resp, nil
}

// UpdateModel 覆盖一条模型条目（PATCH /models/:id）。
func (s *Service) UpdateModel(ctx context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error) {
	if !s.modelListAvailable() {
		return nil, modelListUnavailableErr()
	}
	if err := rejectRuntimeField(patch.Runtime); err != nil {
		return nil, err
	}
	row, found, err := s.st.GetModelDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, modelNotFoundErr(id)
	}
	// 指针更新：只改 patch 里出现过的键。
	content := store.ModelContent{
		Name: row.Name, Note: row.Note,
		// 列值归一：空列（只可能来自手写行，列默认值是 `text`）也要写成明确值——否则"只改名字"
		// 这一步会把空串原样写回，让"默认 text"变成第三种落库形态。
		InputModalities: resource.ModalitiesToColumn(resource.ModalitiesFromColumn(row.InputModalities)),
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
	if patch.Name != nil || patch.Note != nil || patch.InputModalities != nil {
		ok, err := s.st.UpdateModelDefinition(ctx, id, content, row.Revision, definitionWriteActor)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, definitionChangedErr("db:model/" + id)
		}
	}
	// 置/清默认是**两个方向**，都必须被处理：只处理 `true` 会让 `is_default:false` 静默无效。
	if patch.IsDefault != nil {
		if *patch.IsDefault {
			if err := s.st.SetDefaultModelID(ctx, id); err != nil {
				return nil, err
			}
		} else if cleared, err := s.st.ClearDefaultModelID(ctx, id); err != nil {
			return nil, err
		} else if !cleared {
			// 它不是默认：幂等无操作（不是错误）。
			_ = cleared
		}
	}
	resp, err := s.modelResp(ctx, id)
	if err != nil {
		return nil, err
	}
	resp.Effect = s.modelEffect(patch.IsDefault != nil && *patch.IsDefault)
	if note := s.trySyncModels(ctx); note != "" {
		resp.Effect.Note = note
	}
	return resp, nil
}

// DeleteModel 删除一条模型条目（DELETE /models/:id）。
//
// 幂等。**行为变更（B4）**：v11 里"它是该 runtime 的默认模型"会被 40501 拒绝，v12 **允许删**
// 并**连带清空默认**——默认是**偏好**，偏好跟着资源消失比拦住用户更合理；清了就说出来
// （`DefaultCleared`），不静默。
func (s *Service) DeleteModel(ctx context.Context, id string) (*dtomodel.ModelDeleteResp, error) {
	if !s.modelListAvailable() {
		return nil, modelListUnavailableErr()
	}
	row, found, err := s.st.GetModelDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return &dtomodel.ModelDeleteResp{Deleted: false}, nil
	}
	// 删行 + 清默认**同一事务**（v12 是两次独立调用，中间有一个"默认指向已删行"的悬挂窗口）。
	deleted, cleared, err := s.st.DeleteModelDefinitionAndClearDefault(ctx, id, row.Revision, definitionWriteActor)
	if err != nil {
		return nil, err
	}
	resp := &dtomodel.ModelDeleteResp{Deleted: deleted}
	if cleared {
		resp.DefaultCleared = true
	}
	// 删掉一条也要重新同步：底座的配置里不能继续留着它。
	s.trySyncModels(ctx)
	return resp, nil
}

// ListModels 列出全部平台清单（**不分页**：清单上限 200 条，全量返回）。
func (s *Service) ListModels(ctx context.Context) ([]dtomodel.ModelResp, error) {
	if !s.modelListAvailable() {
		return []dtomodel.ModelResp{}, nil
	}
	rows, err := s.st.ListModelDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	// **不再在 Go 侧排序**：顺序由 SQL 的 `ORDER BY provider_id, model_id` 给出（ADR-6）。
	// 这里再排一次会让"库里的顺序"与"对外顺序"变成两份可能分叉的事实。
	out := make([]dtomodel.ModelResp, 0, len(rows))
	for i := range rows {
		out = append(out, modelRespFromRow(&rows[i]))
	}
	return out, nil
}

// DefaultModelValueFor 返回某 runtime 当前默认模型的**不透明值**（无默认 → 空串）。
//
// 这是 T2 快路径（会话创建时下发）与 T5 慢路径（物化进配置文件）**共用的取值面**：
// 两条路读同一个 `meta.default_model`，所以"底座自己认的默认"与"平台下发的默认"逐字同源。
//
// v12 起默认是**单一值**，所以这里没有"某 runtime 的默认"这回事——只是把同一个平台模型 id
// 按该 runtime 的生成规则折成它认得不透明值。
func (s *Service) DefaultModelValueFor(ctx context.Context, runtimeID string) (string, error) {
	row, ok, err := s.defaultModelRow(ctx)
	if err != nil || !ok {
		return "", err
	}
	v, verr := modelValueFor(runtimeID, row.ProviderID, row.ModelID)
	if verr != nil {
		// 该 runtime 没有声明模型值形态 = **它不消费平台默认模型**（不是失败）：
		// 回空串表示"不下发"，与会话侧"没有默认"逐字同一形态。
		return "", nil
	}
	return v, nil
}

// defaultModelRow 读出默认模型那一行（未设 / 指向已删模型 → (nil,false,nil)）。
func (s *Service) defaultModelRow(ctx context.Context) (*store.ModelDefinition, bool, error) {
	if s.st == nil {
		return nil, false, nil
	}
	id, ok, err := s.st.DefaultModelID(ctx)
	if err != nil || !ok {
		return nil, false, err
	}
	row, found, err := s.st.GetModelDefinition(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	return row, true, nil
}

// modelValueFor 平台清单 → 运行时认得的不透明值（**两条算法必须共用它**）。
//
//	dsh（dsh）：`JSON.stringify([provider, model])`（ACP 的 modelValue()）
//	opencode：`<provider>/<model>`
//
// 为什么不解析运行时的值反推：那是把"事实"当"意图"。平台只存生成 value 的输入，
// 生成规则在这里唯一一份——下发与物化因此不可能分叉。
func modelValueFor(runtimeID, providerID, modelID string) (string, error) {
	switch runtimeID {
	case "dsh":
		b, err := json.Marshal([]string{providerID, modelID})
		if err != nil {
			return "", err
		}
		return string(b), nil
	case "opencode":
		return providerID + "/" + modelID, nil
	default:
		return "", acl.NewError(acl.ClassUnsupported, "model_value_unsupported",
			fmt.Sprintf("runtime %q 的模型值形态未声明：平台不为未知底座猜模型值的形状", runtimeID), nil)
	}
}

// ——— 内部工具 ———

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
			return "", acl.NewError(acl.ClassUnsupported, "modality_unsupported",
				fmt.Sprintf("不支持的输入模态 %q：合法取值是 `text`（默认）与 `image`。"+
					"图片输入是在文本之上追加的能力，所以 `image` 会落成 `text,image`", v), nil)
		}
	}
	return resource.ModalitiesToColumn(*in), nil
}

// modelRespFromRow 把一行模型条目投影成对外表示（**不含任何 runtime 维度**）。
//
// v13 起是**纯列投影，不可能失败**：没有 JSON 可比坏，于是"一行坏 body 打不开整页"
// （设计 P2 / 现象：整页 500）在结构上消失了。
func modelRespFromRow(row *store.ModelDefinition) dtomodel.ModelResp {
	out := dtomodel.ModelResp{
		ID: row.ID, ProviderID: row.ProviderID, ModelID: row.ModelID,
		Name: row.Name, Note: row.Note,
		InputModalities: resource.ModalitiesFromColumn(row.InputModalities),
		Revision:        row.Revision,
	}
	if !row.UpdatedAt.IsZero() {
		out.UpdatedAt = model.FormatTime(row.UpdatedAt)
	}
	return out
}

// modelResp 读一条并投影（不存在 → model_not_found）。
func (s *Service) modelResp(ctx context.Context, id string) (*dtomodel.ModelResp, error) {
	row, found, err := s.st.GetModelDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, modelNotFoundErr(id)
	}
	out := modelRespFromRow(row)
	return &out, nil
}

// requireProvider 校验挂靠的供应商存在（不存在 → 40401 provider_not_found）。
func (s *Service) requireProvider(ctx context.Context, providerID string) error {
	if strings.TrimSpace(providerID) == "" {
		return acl.NewError(acl.ClassRequest, "invalid_param", "provider_id 必填（模型必须挂靠在一条供应商下）", nil)
	}
	if s.st != nil {
		if _, found, err := s.st.GetProviderDefinition(ctx, providerID); err != nil {
			return err
		} else if found {
			return nil
		}
	}
	s.mu.Lock()
	_, ok := s.providers[providerID]
	s.mu.Unlock()
	if !ok {
		return acl.NewError(acl.ClassRequest, "provider_not_found",
			fmt.Sprintf("供应商 %q 不存在：模型必须挂靠在一条已登记的供应商下", providerID), nil)
	}
	return nil
}

// modelEffect 计算模型清单改动的生效面回执（只读、无副作用；判据与代际裁决同源）。
func (s *Service) modelEffect(defaultTouched bool) *dtomodel.ModelEffect {
	eff := &dtomodel.ModelEffect{Effect: "next_session"}
	o := s.orch
	s.mu.Lock()
	recs := make([]ConversationRec, 0, len(s.conversations))
	for _, rec := range s.conversations {
		if rec != nil {
			recs = append(recs, *rec)
		}
	}
	s.mu.Unlock()
	for i := range recs {
		if _, ok := s.AgentByID(recs[i].AgentID); !ok {
			continue
		}
		eff.AffectedConversations++
		if o == nil {
			continue
		}
		as := o.conversation(recs[i].ID)
		if as == nil {
			continue
		}
		if plan := o.planGenerationSwitch(as, &recs[i]); plan.Action == genActionDefer {
			eff.Deferred++
		}
	}
	if defaultTouched {
		eff.Note = "已保存：新会话创建时由平台下发这个默认模型（立即）；既有会话在下一个会话进程读到配置。**不是「已生效」**"
	} else {
		eff.Note = "已保存，将在下一个会话进程生效（配置在进程启动时读）；此刻底座的模型目录仍是旧的那份"
	}
	return eff
}

func modelExistsErr(providerID, modelID string) error {
	return acl.NewError(acl.ClassLimit, "model_exists",
		fmt.Sprintf("供应商 %q 下已经声明过模型 %q：同一条模型在同一个供应商下只能有一行",
			providerID, modelID), nil)
}

func modelNotFoundErr(id string) error {
	return acl.NewError(acl.ClassRequest, "model_not_found", fmt.Sprintf("未知模型条目 %q", id), nil)
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

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ——— 派生只读：`GET /agents` 的 `default_model` ———

// defaultModelByRuntime 读一次库里的默认模型（v12 是**单一值**，这里为保持既有 wire 形状
// 把它摊到每个已注册 runtime 上）。
//
// 为什么摊：`GET /agents` 的 `default_model` 是逐 runtime 的 map（那是 agent 读面，不是
// 「设置 · 模型」的用户面，A14 的断言只针对后者）。读一次就够：`models` 是几十行，
// 而 `GET /agents` 要对每个 agent 求交——逐 agent 查库会让这个只读接口的 IO 次数随 agent 数增长。
func (s *Service) defaultModelByRuntime(ctx context.Context) map[string]string {
	if s.st == nil {
		return nil
	}
	row, ok, err := s.defaultModelRow(ctx)
	if err != nil {
		glog.Warnw(ctx, "models: 读默认模型失败（本次 /agents 的 default_model 为空）", "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, spec := range s.reg.List() {
		out[spec.ID] = row.ModelID
	}
	return out
}

// derivedDefaultModels 按 agent 的 `runtimes[]` 与默认模型求交。
//
// 返回 nil 而不是空 map：`omitempty` 让"这个 agent 没有任何默认"在 wire 上是一个**缺席的键**
// 而不是 `{}`。
func derivedDefaultModels(runtimes []string, defaults map[string]string) map[string]string {
	if len(defaults) == 0 {
		return nil
	}
	var out map[string]string
	for _, rt := range runtimes {
		if id, ok := defaults[rt]; ok && id != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[rt] = id
		}
	}
	return out
}

// rejectDerivedDefaultModel 显式拒绝 agent 写面里的 `default_model`。
//
// 为什么宁可报错也不静默忽略：agent 的 body 会**原样保留未知键**（落盘纪律），所以静默收下
// 会让它变成一个「写了、存了、什么都不发生」的字段。`null` 视为"没带"。
func rejectDerivedDefaultModel(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return acl.NewError(acl.ClassRequest, "default_model_is_derived",
		"`default_model` 是平台清单的**派生只读**投影，不能写在 agent 上："+
			"默认模型归属于平台清单，改它请到「设置 · 模型」里点星号（改的是 meta 的单一 default_model）", nil)
}
