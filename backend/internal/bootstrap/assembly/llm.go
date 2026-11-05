package assembly

import (
	"context"
	"fmt"

	corellm "github.com/morehao/ark-mate/internal/core/llm"
	"github.com/morehao/ark-mate/internal/domain/provider"
	infrallm "github.com/morehao/ark-mate/internal/infra/llm"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/sessionruntime"
)

// ——— 内部模型网关的装配（v37）———
//
// 网关本身（选执行器/记账编排）在 core/llm；这里只做**三件接线**：把两个执行器造出来、
// 把平台 provider 数据面适配成 `ProviderSource`、把流水落库适配成 `Recorder`。全部依赖都在
// 装配根可见——这正是"接口内、实现在外、装配根注入"的落点（与 runtime.Adapter 同构）。

// providerSource 把平台 provider 域适配成 `corellm.ProviderSource`（窄端口，不外泄 Def）。
type providerSource struct{ plane *provider.Plane }

// Resolve 按档案 id 取解析后的拨号信息（协议/端点/明文密钥）。
func (p providerSource) Resolve(ctx context.Context, providerID string) (corellm.ProviderInfo, error) {
	if p.plane == nil {
		return corellm.ProviderInfo{}, fmt.Errorf("llm: provider 域未装配")
	}
	def, ok := p.plane.Snapshot(providerID)
	if !ok {
		return corellm.ProviderInfo{}, fmt.Errorf("llm: 供应商 %q 不存在", providerID)
	}
	// 密钥是"读时现解析"的（库/托管/.env/进程环境都可能变）：用 KeyFor 取生效明文，不缓存。
	key := p.plane.KeyFor(ctx, def)
	return corellm.ProviderInfo{Protocol: def.Protocol, BaseURL: def.BaseURL, Key: key.Value}, nil
}

// storeRecorder 把调用流水落到 `llm_calls`（`llm.LLMCall` → `model.LLMCallEntity` → store）。
//
// 为什么在这个薄适配层而不让 store 直接 import core/llm：store 只该认识 `model.LLMCallEntity`
// 这一种行形状；把 L1 契约塞进 L5 的 store 会让 store 多背一条不该它的依赖边。
type storeRecorder struct{ st *store.Store }

// Record 落一条流水。store 为 nil（memory 模式无持久载体）→ 静默跳过（调用本身仍可用）。
func (r storeRecorder) Record(ctx context.Context, c corellm.LLMCall) error {
	if r.st == nil {
		return nil
	}
	return r.st.InsertLLMCall(ctx, model.LLMCallEntity{
		ID:                c.ID,
		At:                c.At,
		Purpose:           string(c.Purpose),
		SessionID:         c.SessionID,
		RunID:             c.RunID,
		Provider:          c.Provider,
		Model:             c.Model,
		InputTokens:       c.Usage.InputTokens,
		OutputTokens:      c.Usage.OutputTokens,
		CachedReadTokens:  c.Usage.CachedReadTokens,
		CachedWriteTokens: c.Usage.CachedWriteTokens,
		ThoughtTokens:     c.Usage.ThoughtTokens,
		TotalTokens:       c.Usage.TotalTokens,
		CostAmount:        c.CostAmount,
		CostCurrency:      c.CostCurrency,
		LatencyMS:         c.LatencyMS,
		Status:            c.Status,
	})
}

// buildLLMGateway 装配内部模型网关：会话执行器（Orchestrator）+ 无状态执行器（HTTP 直连）+
// 流水落库。任一必要件缺失（如 memory 模式无 Orchestrator/无 store）时**降级**——
// 缺会话执行器则该类调用在网关里报"未装配执行器"；缺 store 则流水丢弃（记 warn 语义由 Record 返回 nil）。
func buildLLMGateway(f Facts) corellm.Gateway {
	executors := map[corellm.ExecutorKind]corellm.Executor{
		corellm.ExecutorStateless: infrallm.NewStatelessExecutor(infrallm.StatelessConfig{
			Providers: providerSource{plane: providersOf(f)},
		}),
	}
	if f.Orchestrator != nil {
		executors[corellm.ExecutorStateful] = sessionruntime.NewStatefulExecutor(f.Orchestrator, "", "")
	}
	return corellm.NewGateway(corellm.GatewayConfig{
		Executors: executors,
		Recorder:  storeRecorder{st: f.Store},
	})
}

// providersOf 取 provider 域（Planes 可能为 nil——最小装配/测试）。
func providersOf(f Facts) *provider.Plane {
	if f.Planes == nil {
		return nil
	}
	return f.Planes.Providers
}
