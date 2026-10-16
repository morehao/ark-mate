package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
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
	if strings.TrimSpace(req.Model) == "" {
		return nil, acl.NewError(acl.ClassRequest, "invalid_param", "model 必填（发给上游的模型名）", nil)
	}
	if err := s.requireProvider(ctx, req.ProviderID); err != nil {
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
	ok, err := s.st.CreateModelDefinition(ctx, store.ModelColumns{
		ID: id, ProviderID: req.ProviderID, Model: strings.TrimSpace(req.Model),
		Name: derefString(req.Name), Note: derefString(req.Note), InputModalities: modalities,
		MaxInputTokens: maxIn, MaxOutputTokens: maxOut,
	}, definition.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, modelExistsErr(req.ProviderID, req.Model)
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
		ok, err := s.st.UpdateModelDefinition(ctx, id, content, row.Revision, definition.DefinitionWriteActor)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, definition.DefinitionChangedErr("db:model/" + id)
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
	deleted, cleared, err := s.st.DeleteModelDefinitionAndClearDefault(ctx, id, row.Revision, definition.DefinitionWriteActor)
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
// v12 起默认是**单一值**（`meta.default_model`），所以这里没有"某 runtime 的默认"这回事——只是把
// 同一个平台模型 id 按该 runtime 的生成规则折成它认得不透明值。
//
// v26 起会话创建路径改走 `resolveSessionModel`（它一次给出"平台行 id + 运行时值"），本函数因此
// **委托**给同一条实现而不是自己再走一遍 `defaultModelRow` + `modelValueFor`：两份实现会在
// "某个底座不声明模型值形态"这类边界上分叉，而那种分叉只在一条路径上显现。保留它是为了这个
// 语义本身（"某 runtime 的默认模型值"）与既有诊断/测试判据。
func (s *Service) DefaultModelValueFor(ctx context.Context, runtimeID string) (string, error) {
	_, cfg, err := s.resolveSessionModel(ctx, runtimeID, "")
	return cfg.Model, err
}

// defaultModelRow 读出默认模型那一行（未设 / 指向已删模型 → (nil,false,nil)）。
func (s *Service) defaultModelRow(ctx context.Context) (*model.ModelEntity, bool, error) {
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

// modelValueFor 平台清单 → 运行时认得的不透明值（**下发与物化必须共用它**）。
//
// 生成规则**不属于内核**：它挂在注册表的第四张表上（`runtime.ModelValueAdapter`，见
// `pkg/runtime/model_value.go`）——dsh 是 `["<provider>","<model>"]` 这个 JSON 数组，别的底座
// 可能是 `<provider>/<model>` 这一串。内核只做分发，一个内建底座名都不认识。
//
// 这里曾经是一段 `switch runtimeID { case "dsh": …; case "opencode": …; default: 报错 }`：
// 接第三个底座的人会在 `default` 撞墙，然后**回内核加一个 case**。收口后由 archtest 的
// `TestInternalHasNoRuntimeIDSpecialCase` 钉住这一类特判。
//
// `ok=false`（该 id 没有登记实现）= 这个底座不消费平台的模型清单，**不是失败**；调用方按语义
// 选"回空串（不下发）"或"跳过这条"，所以这里返回的是可判别的错误而不是一个空值。
//
// 入参名 v24 从 `modelID` 改成 `model`：它接的是**上游模型名**（`models.model`），不是任何一行的
// id——改名之前这个名字让"这一列到底是不是 id"要靠读注释才知道。
func modelValueFor(reg *runtime.Registry, runtimeID, providerID, model string) (string, error) {
	a, ok := reg.ModelValue(runtimeID)
	if !ok {
		return "", acl.NewError(acl.ClassUnsupported, "model_value_unsupported",
			fmt.Sprintf("runtime %q 的模型值形态未声明：平台不为未知底座猜模型值的形状", runtimeID), nil)
	}
	return a.Value(providerID, model)
}

// ——— 内部工具 ———

// resolveSessionModel 把"会话请求里的**平台模型行 id**"解析成 (平台行 id, 运行时模型配置)。
//
// 语义（v26 定案「辰」，设计 §5.4）：
//
//	入参为空          → 用 `meta.default_model` 那一行；没有默认行 → ("", {}, nil) = **不下发**
//	                    模型（跟随底座自己的默认，与 v26 之前的"零回归那一支"逐字相同）；
//	入参非空但形状不对 → 40401 `model_not_found`；
//	入参非空但查不到行 → 40401 `model_not_found`（**复用既有码**，E9：不新增错误码——它此前的用途是
//	                    "PATCH/DELETE 打到不存在的条目"，这里扩到"会话引用了一个查不到的模型行"，
//	                    同一个码、同一份引导（刷新列表））。
//
// 返回值的第一项是**平台行 id**（落库的那一个）；第二项是**运行时的不透明值**，由 adapter 现算
// （`modelValueFor`）——它**不落库、不过 wire**（这正是「辰」的核心：库里只留身份）。
//
// 纪律：解析失败一律**上抛**，绝不静默回落到底座默认、也绝不静默换成别的模型——"界面显示 A、
// 实际用 B"正是本仓要消灭的静默偏差（这也是有意偏离 QwenWork 四级回退的那一处）。
func (s *Service) resolveSessionModel(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error) {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" {
		row, ok, err := s.defaultModelRow(ctx)
		if err != nil || !ok {
			// 读不出来 = 无法校验：不下发模型（不是把上一次的选择偷偷用上）。
			return "", runtime.ModelConfig{}, err
		}
		id, cfg, verr := s.modelValueForRow(runtimeID, row.ID, row.ProviderID, row.Model)
		if verr != nil {
			if ae, ok := acl.As(verr); ok && ae.Code == "model_value_unsupported" {
				// 该底座**不消费**平台模型清单 = 它没有模型语义：这里"不下发"是正确结论而不是失败
				// （与 v26 之前 `DefaultModelValueFor` 回空串的行为逐字相同——零回归的那一支）。
				return "", runtime.ModelConfig{}, nil
			}
			return "", runtime.ModelConfig{}, verr
		}
		return id, cfg, nil
	}
	if !strings.HasPrefix(trimmed, objid.Model) || !objid.IsPlatformID(trimmed) {
		// 形状就不对：它不可能是平台行 id（旧形态的不透明值 / 手抄错的串都会落到这里）。
		return "", runtime.ModelConfig{}, sessionModelNotFoundErr(trimmed)
	}
	row, found, err := s.st.GetModelDefinition(ctx, trimmed)
	if err != nil {
		return "", runtime.ModelConfig{}, err
	}
	if !found {
		return "", runtime.ModelConfig{}, sessionModelNotFoundErr(trimmed)
	}
	return s.modelValueForRow(runtimeID, row.ID, row.ProviderID, row.Model)
}

// modelValueForRow 把一条**平台模型行**折成 (行 id, 运行时配置)。
//
// 独立成函数而不是塞进 `resolveSessionModel` 的两个分支里：两条来源（显式 id / 默认行）必须走
// **同一段**翻译，否则"显式选的那条"与"默认那条"的运行时值会各长一套（而那种分叉只在某一条路径上显现）。
func (s *Service) modelValueForRow(runtimeID, rowID, providerID, model string) (string, runtime.ModelConfig, error) {
	v, err := modelValueFor(s.reg, runtimeID, providerID, model)
	if err != nil {
		return "", runtime.ModelConfig{}, err
	}
	// `Provider` 是**留痕语义**（仅展示/诊断，不进任何契约）：v26 之前它填的是恒空的
	// `SessionRec.ProviderID`，现在由**模型行**派生——值终于有来源，含义一个字没变。
	return rowID, runtime.ModelConfig{Provider: providerID, Model: v}, nil
}

// sessionModelNotFoundErr 会话引用了平台清单里查不到的模型。
//
// 与 `modelNotFoundErr`（"PATCH/DELETE 打到不存在的条目"）**同一个码**（40401），但文案分开：
// 这里的处境是"这条会话声明的模型已不在清单里"，出路是**重新选一个**（或先把模型声明回来），
// 而不是"你打错了 id"。码相同是刻意的（E9）——新增一个码要动 `pkg/code`↔README↔前端三条边。
func sessionModelNotFoundErr(modelID string) error {
	return acl.NewError(acl.ClassRequest, "model_not_found",
		fmt.Sprintf("会话声明的模型 %q 不在平台清单里（或它还不是平台模型 id）："+
			"请到「设置 · 模型」重新选一个——平台只接受自己声明过的模型", modelID), nil)
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
		return nil, acl.NewError(acl.ClassRequest, "invalid_param",
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
func modelRespFromRow(row *model.ModelEntity) dtomodel.ModelResp {
	out := dtomodel.ModelResp{
		ID: row.ID, ProviderID: row.ProviderID, Model: row.Model,
		Name: row.Name, Note: row.Note,
		InputModalities: resource.ModalitiesFromColumn(row.InputModalities),
		// 容量原样透出（nil = null = 未声明）：界面据此如实标"容量由环境兜底（不是平台声明）"。
		MaxInputTokens: row.MaxInputTokens, MaxOutputTokens: row.MaxOutputTokens,
		Revision: row.Revision,
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
	ok := s.providerPlane.Exists(providerID)
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
	recs := s.sessionPlane.Snapshot()
	s.mu.Unlock()
	for i := range recs {
		eff.AffectedSessions++
		if o == nil {
			continue
		}
		as := o.session(recs[i].ID)
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
		out[spec.ID] = row.Model
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
