package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/core/protocol"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 会话的启动 / 恢复 / 换句柄：编排侧入口（P4a 正文第五十六刀）———
//
// 实现已整族进 `domain/session.Lifecycle`（`Start` / `Ensure` / `Connect` / `Attach` /
// `ApplyModel`）。本文件只剩薄入口：调用点（`Service.CreateSession`、`RunOnce`、代际迁移与
// 既有测试）不跟着改名。**这里不许再长出判断**——校验、错误码、投影与模型应用都在域里。

// StartSession 在**构建期选定的** runtime 上建立持久 ACP 会话（session/new，绝对 cwd），
// 并发布 session.created。不发起 prompt；轮次由 `Runner` 负责。
func (o *Orchestrator) StartSession(ctx context.Context, opt session.StartOptions) (*session.CreateResult, error) {
	return o.life.StartSession(ctx, opt)
}

// startSessionOn 与 StartSession 同义（历史入口名；`Service.CreateSession` 与 `RunOnce` 都汇聚于此）。
func (o *Orchestrator) startSessionOn(ctx context.Context, opt session.StartOptions) (*session.CreateResult, error) {
	return o.life.StartSession(ctx, opt)
}

// ApplySessionModel 把模型变更应用到**已存在的会话**（会话内改模型）。
//
//   - 会话不在活跃集合（未启动/已断连）→ (false, nil)：调用方只更新元数据，等下次恢复会话时生效；
//   - 活跃 → 经 session/set_config_option 应用（运行时是唯一判据），失败原样上抛（调用方不得吞）。
func (o *Orchestrator) ApplySessionModel(ctx context.Context, sessionID string, model runtime.ModelConfig) (bool, error) {
	return o.life.ApplyModel(ctx, sessionID, model)
}

// ensureSession 返回活跃会话；不在活跃集合但平台有记录时按需重建。
func (o *Orchestrator) ensureSession(ctx context.Context, sessionID string) (*session.ActiveSession, error) {
	return o.life.Ensure(ctx, sessionID)
}

// connectSession 在指定代际上重开一条会话（原生 resume 优先、handoff memo 兜底）。
func (o *Orchestrator) connectSession(ctx context.Context, rec *session.SessionRec,
	spec runtime.Spec, cwd string, sel *session.CapabilitySelection, fingerprint string) (*session.ActiveSession, string, error) {
	return o.life.ConnectSession(ctx, rec, spec, cwd, sel, fingerprint)
}

// attachSession 把句柄装进活跃集合（替换同 id 的旧句柄），发布 session.resumed。
func (o *Orchestrator) attachSession(as *session.ActiveSession, reason string) {
	o.life.Attach(as, reason)
}

// liveConfigOptions 返回任一活跃会话的配置项（模型目录的**首选来源**：运行时此刻的真实状态）。
func (o *Orchestrator) liveConfigOptions() ([]protocol.ConfigOption, bool) {
	return o.runner.LiveConfigOptions()
}
