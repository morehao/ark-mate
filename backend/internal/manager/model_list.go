package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— 平台模型清单的写面（v11 M5）：CRUD + 每 runtime 一个默认 + 两条通道的生效回执 ———
//
// 清单的存储形态由 `store` 保证两条不变量（见 store/model.go）：`(runtime, provider_id, model_id)`
// 唯一、每个 runtime 至多一条 `is_default`。本文件负责**业务判据**：runtime 是否承载清单、
// provider 是否存在、id 生成、以及删除默认模型时的拒绝与出路。
//
// 清单需要 sqlite 持久层：memory 模式没有 `models` 表（也没有任何平台侧清单载体），
// 所以那里的写面明确报 40501 而不是"内存生效、盘上没留痕"。

// modelListAvailable 清单写面是否可用（sqlite 模式才有清单载体）。
func (s *Service) modelListAvailable() bool { return s.st != nil }

func modelListUnavailableErr() error {
	return acl.NewError(acl.ClassUnsupported, "model_list_unavailable",
		"模型清单需要 sqlite 持久层（MATE_STORE=sqlite）：memory 模式没有平台侧清单载体，"+
			"模型面仍由运行时派生", nil)
}

// configProjectionOf 取某 runtime 已解析的模型配置落点（未注册/未声明 → declared=false）。
func (s *Service) configProjectionOf(runtimeID string) (runtime.ConfigProjectionPath, bool, error) {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return runtime.ConfigProjectionPath{}, false, acl.NewError(acl.ClassRequest, "runtime_not_found",
			fmt.Sprintf("runtime %q 未注册", runtimeID), nil)
	}
	p, declared, err := runtime.ResolveConfigProjection(spec)
	if err != nil {
		return runtime.ConfigProjectionPath{}, false, acl.NewError(acl.ClassUnsupported, "config_projection_invalid",
			fmt.Sprintf("runtime %q 的模型配置落点声明非法：%v", runtimeID, err), nil)
	}
	return p, declared, nil
}

// CreateModel 新建一条模型条目（POST /models）。
func (s *Service) CreateModel(ctx context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error) {
	if !s.modelListAvailable() {
		return nil, modelListUnavailableErr()
	}
	_, declared, err := s.configProjectionOf(req.Runtime)
	if err != nil {
		return nil, err
	}
	// E3：runtime 必须已声明 ConfigProjection（否则清单没有兑现通道，"写进去也不会生效"）。
	if !declared {
		return nil, configProjectionMissingErr(req.Runtime)
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return nil, acl.NewError(acl.ClassRequest, "invalid_param", "model_id 必填", nil)
	}
	if err := s.requireProvider(ctx, req.ProviderID); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	body, err := modelBody(dtomodel.ModelResp{
		ModelID: req.ModelID, Name: derefString(req.Name), Extra: req.Extra,
		Hidden: derefBool(req.Hidden), Pinned: derefBool(req.Pinned),
	})
	if err != nil {
		return nil, err
	}
	ok, err := s.st.CreateModelDefinition(ctx, id, req.Runtime, req.ProviderID, req.ModelID, body, definitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, modelExistsErr(req.Runtime, req.ProviderID, req.ModelID)
	}
	// 置默认（同事务语义由 store 的 SetDefaultModel 保证：先清同 runtime 再置新）。
	if derefBool(req.IsDefault) {
		if ok, err := s.st.SetDefaultModel(ctx, id, definitionWriteActor); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			return nil, modelNotFoundErr(id)
		}
	}
	resp, err := s.modelResp(ctx, id)
	if err != nil {
		return nil, err
	}
	resp.Effect = s.modelEffect(req.Runtime, derefBool(req.IsDefault))
	// ② 清单已提交 → 立刻重新生成该 runtime 的配置（T5）。失败不回滚清单（见 tryMaterialize）。
	if note := s.tryMaterialize(ctx, req.Runtime); note != "" {
		resp.Effect.Note = note
	}
	return resp, nil
}

// UpdateModel 覆盖一条模型条目（PATCH /models/:id）。
func (s *Service) UpdateModel(ctx context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error) {
	if !s.modelListAvailable() {
		return nil, modelListUnavailableErr()
	}
	row, found, err := s.st.GetModelDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, modelNotFoundErr(id)
	}
	cur, err := modelRespFromRow(row)
	if err != nil {
		return nil, err
	}
	// 指针更新：只改 patch 里出现过的键。
	if patch.Name != nil {
		cur.Name = *patch.Name
	}
	if patch.Extra != nil {
		cur.Extra = patch.Extra
	}
	if patch.Hidden != nil {
		cur.Hidden = *patch.Hidden
	}
	if patch.Pinned != nil {
		cur.Pinned = *patch.Pinned
	}
	body, err := modelBody(cur)
	if err != nil {
		return nil, err
	}
	ok, err := s.st.UpsertModelDefinition(ctx, id, body, row.Revision, definitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, definitionChangedErr("db:model/" + id)
	}
	// 置/清默认是**两个方向**，都必须被处理：只处理 `true` 会让 `is_default:false` 静默无效
	// （2026-10-10 实测：取消默认后平台仍在下发那个取值，用户只能删掉整条模型才停）。
	if patch.IsDefault != nil {
		var ok bool
		var err error
		if *patch.IsDefault {
			ok, err = s.st.SetDefaultModel(ctx, id, definitionWriteActor)
		} else {
			ok, err = s.st.ClearDefaultModel(ctx, id, definitionWriteActor)
		}
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, modelNotFoundErr(id)
		}
	}
	resp, err := s.modelResp(ctx, id)
	if err != nil {
		return nil, err
	}
	resp.Effect = s.modelEffect(row.Runtime, patch.IsDefault != nil && *patch.IsDefault)
	if note := s.tryMaterialize(ctx, row.Runtime); note != "" {
		resp.Effect.Note = note
	}
	return resp, nil
}

// DeleteModel 删除一条模型条目（DELETE /models/:id）。
//
// 幂等；但**若它是该 runtime 的默认模型则拒绝**（40501 `model_is_default` + 可执行出路）：
// 不静默把默认置空——那会让"平台的默认模型"在一个用户看不见的时刻消失。
// 删掉一条模型**不会弄坏既有会话**（dsh 逐字：「a saved selection can still submit requests
// after its catalog entry disappears」），所以这里不需要扫描会话改绑，UI 文案也不该吓唬用户。
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
	if row.IsDefault {
		return nil, acl.NewError(acl.ClassUnsupported, "model_is_default",
			fmt.Sprintf("模型条目 %q 是 runtime %q 的默认模型，不能直接删除：那会让平台的默认模型在你看不见的时刻消失。"+
				"先把它设为默认的那个换成另一条（PATCH /models/<其它条目> {\\\"is_default\\\":true}），再删这一条",
				modelIDOf(row.Body), row.Runtime), nil)
	}
	ok, err := s.st.DeleteModelDefinition(ctx, id, row.Revision, definitionWriteActor)
	if err != nil {
		return nil, err
	}
	// 删掉一条也要重新物化：底座的配置里不能继续留着它。
	s.tryMaterialize(ctx, row.Runtime)
	return &dtomodel.ModelDeleteResp{Deleted: ok}, nil
}

// ListModels 列出平台清单（runtimeID 为空 = 全部）。
func (s *Service) ListModels(ctx context.Context, runtimeID string) ([]dtomodel.ModelResp, error) {
	if !s.modelListAvailable() {
		return []dtomodel.ModelResp{}, nil
	}
	rows, err := s.st.ListModelDefinitions(ctx, runtimeID)
	if err != nil {
		return nil, err
	}
	out := make([]dtomodel.ModelResp, 0, len(rows))
	for i := range rows {
		r, err := modelRespFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runtime != out[j].Runtime {
			return out[i].Runtime < out[j].Runtime
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// DefaultModelValueFor 返回某 runtime 当前默认模型的**不透明值**（无默认 → 空串）。
//
// 这是 T2 快路径（会话创建时下发）与 T5 慢路径（物化进配置文件）**共用的取值面**：
// 两条路读同一个 `is_default`，所以"底座自己认的默认"与"平台下发的默认"逐字同源。
func (s *Service) DefaultModelValueFor(ctx context.Context, runtimeID string) (string, error) {
	if !s.modelListAvailable() {
		return "", nil
	}
	row, found, err := s.st.DefaultModelDefinition(ctx, runtimeID)
	if err != nil || !found {
		return "", err
	}
	return modelValueFor(runtimeID, row.ProviderID, modelIDOf(row.Body))
}

// modelValueFor 平台清单 → 运行时认得的不透明值（**两条算法必须共用它**，见 E6）。
//
//	dsh（deepseekharness）：`JSON.stringify([provider, model])`（ACP 的 modelValue()）
//	opencode：`<provider>/<model>`
//
// 为什么不解析运行时的值反推：那是把"事实"当"意图"。平台只存生成 value 的输入，
// 生成规则在这里唯一一份——T5 写进配置文件的值与 T2 下发给会话的值因此不可能分叉。
func modelValueFor(runtimeID, providerID, modelID string) (string, error) {
	switch runtimeID {
	case "deepseekharness":
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

// modelBody 把一条清单条目编成正文 JSON（platform 拥有 `model_id`/`name`/`extra`/`hidden`/`pinned`；
// `is_default` 是**列**，不进正文，避免双写漂移）。
func modelBody(m dtomodel.ModelResp) (string, error) {
	body := map[string]any{"model_id": m.ModelID}
	if m.Name != "" {
		body["name"] = m.Name
	}
	if len(m.Extra) > 0 {
		body["extra"] = m.Extra
	}
	if m.Hidden {
		body["hidden"] = true
	}
	if m.Pinned {
		body["pinned"] = true
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// modelRespFromRow 把一行模型条目投影成对外表示（含生成的 `value`）。
func modelRespFromRow(row *store.ModelDefinition) (dtomodel.ModelResp, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(row.Body), &body); err != nil {
		return dtomodel.ModelResp{}, fmt.Errorf("manager: 模型条目 %s 的正文非法: %w", row.ID, err)
	}
	out := dtomodel.ModelResp{
		ID: row.ID, Runtime: row.Runtime, ProviderID: row.ProviderID,
		ModelID:   bodyString(body, "model_id"),
		Name:      bodyString(body, "name"),
		Hidden:    bodyBool(body, "hidden"),
		Pinned:    bodyBool(body, "pinned"),
		IsDefault: row.IsDefault,
		Revision:  row.Revision,
	}
	if extra, ok := body["extra"].(map[string]any); ok {
		out.Extra = extra
	}
	if !row.UpdatedAt.IsZero() {
		out.UpdatedAt = model.FormatTime(row.UpdatedAt)
	}
	// 生成的 value 失败不是错误：某些 runtime 的形态未声明时它为空（前端据此不提供"复制 value"）。
	if v, err := modelValueFor(row.Runtime, row.ProviderID, out.ModelID); err == nil {
		out.Value = v
	}
	return out, nil
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
	out, err := modelRespFromRow(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// requireProvider 校验挂靠的凭据存在（不存在 → 40401 provider_not_found）。
func (s *Service) requireProvider(ctx context.Context, providerID string) error {
	if strings.TrimSpace(providerID) == "" {
		return acl.NewError(acl.ClassRequest, "invalid_param", "provider_id 必填（模型条目必须挂靠在一条凭据下）", nil)
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
			fmt.Sprintf("provider 档案 %q 不存在：模型条目必须挂靠在一条已登记的凭据下", providerID), nil)
	}
	return nil
}

// modelEffect 计算模型清单改动的生效面回执（只读、无副作用；判据与代际裁决同源）。
func (s *Service) modelEffect(runtimeID string, defaultTouched bool) *dtomodel.ModelEffect {
	channels := []string{"config_write"}
	effect := "next_process"
	if defaultTouched {
		// 默认模型还有 T2 那条快路径：新会话创建时平台立刻下发——**这是 dsh 首版（T5 只读）
		// 唯一能兑现默认的通道**，所以它必须在回执里出现，不能被 T5 的 next_process 盖住。
		channels = append(channels, "session_config")
		effect = "next_session"
	}
	eff := &dtomodel.ModelEffect{
		Channels:          channels,
		Effect:            effect,
		AppliesToRuntimes: []string{runtimeID},
	}
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
		agent, ok := s.AgentByID(recs[i].AgentID)
		if !ok || !containsString(agent.Runtimes, runtimeID) {
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
	switch {
	case defaultTouched && eff.AffectedConversations == 0:
		eff.Note = "已保存：新会话创建时由平台下发这个默认模型；引用该 runtime 的会话会在下一个会话进程读到它"
	case defaultTouched:
		eff.Note = "已保存：新会话创建时由平台下发这个默认模型（立即）；既有会话在下一个会话进程读到配置。**不是「已生效」**"
	default:
		eff.Note = "已保存，将在下一个会话进程生效（配置在进程启动时读）；此刻底座的模型目录仍是旧的那份"
	}
	return eff
}

// configProjectionMissingErr runtime 未声明模型配置落点（清单没有兑现通道）。
func configProjectionMissingErr(runtimeID string) error {
	return acl.NewError(acl.ClassUnsupported, "config_projection_unsupported",
		fmt.Sprintf("runtime %q 未声明模型配置落点（ConfigProjection）：平台清单没有兑现通道，"+
			"写进去也不会被这个底座认领", runtimeID), nil)
}

func modelExistsErr(runtimeID, providerID, modelID string) error {
	return acl.NewError(acl.ClassLimit, "model_exists",
		fmt.Sprintf("runtime %q 下已经声明过模型 %q（挂靠在 %q）：同一条模型在同一个底座上只能有一行",
			runtimeID, modelID, providerID), nil)
}

func modelNotFoundErr(id string) error {
	return acl.NewError(acl.ClassRequest, "model_not_found", fmt.Sprintf("未知模型条目 %q", id), nil)
}

// modelIDOf 从正文里取 model_id（清单的唯一性判据之一）。
func modelIDOf(body string) string { return store.ModelIDOfBody(body) }

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

func bodyBool(body map[string]any, key string) bool {
	v, ok := body[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
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

// defaultModelByRuntime 读一次库里各 runtime 的默认模型（`models.is_default`）。
//
// 读一次就够：`models` 是几十行，而 `GET /agents` 要对每个 agent 求交——逐 agent 查库会让
// 这个只读接口的 IO 次数随 agent 数增长。返回 nil = 没有清单载体（memory 模式）或读失败
// （失败只告警：`default_model` 是**呈现**，不该因为它读不到而让 agent 列表打不开）。
func (s *Service) defaultModelByRuntime(ctx context.Context) map[string]string {
	if s.st == nil {
		return nil
	}
	rows, err := s.st.ListDefaultModelDefinitions(ctx)
	if err != nil {
		slog.Warn("models: 读默认模型失败（本次 /agents 的 default_model 为空）", "err", err)
		return nil
	}
	out := make(map[string]string, len(rows))
	for i := range rows {
		out[rows[i].Runtime] = store.ModelIDOfBody(rows[i].Body)
	}
	return out
}

// derivedDefaultModels 按 agent 的 `runtimes[]` 与各 runtime 的默认模型求交。
//
// 返回 nil 而不是空 map：`omitempty` 让"这个 agent 没有任何默认"在 wire 上是一个**缺席的键**
// 而不是 `{}`（两者前端都要能处理，但少一种形状少一类边界）。
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

// rejectDerivedDefaultModel 显式拒绝 agent 写面里的 `default_model`（E8）。
//
// 为什么宁可报错也不静默忽略：agent 的 body 会**原样保留未知键**（落盘纪律），所以静默收下
// 会让它变成一个「写了、存了、什么都不发生」的字段——正是业务风险 B1 要防的那种骗人。
// `null` 视为"没带"（客户端把可选键显式置空是常见写法，不该因此报错）。
func rejectDerivedDefaultModel(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return acl.NewError(acl.ClassRequest, "default_model_is_derived",
		"`default_model` 是平台清单的**派生只读**投影，不能写在 agent 上："+
			"默认模型归属于 (platform, runtime)，改它请到「设置 · 模型」里点星号（改的是该 runtime 的 models.is_default）", nil)
}
