package run

import (
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 领域信号的出口（C2 事件载荷那一刀收口后）———
//
// ACP → 领域信号的翻译已在适配器（`adapters/outbound/runtimeconn/events.go`）：本层只收
// `event.Signal`，一个协议类型都不认识（`session.DialOptions.OnEvent` 就是按这个形状声明的）。
//
// 这里只剩一件**应用**的事：配置项目录缓存。它是"底座推送 → 平台目录"的副作用，不属于领域
// 状态机；事件本身照原样转给 `Runner.EventSink`——事件 type 与载荷键由
// `TestEventTypeContractFrozen` / `TestToolPayloadKeysAreClosed` 冻结，这里只转发、不重写。

// eventSink 造一个**平台信号形状**的事件出口：先按需刷新配置项目录缓存，再交给领域出口。
func (o *Orchestrator) eventSink(runtimeID string) func(event.Signal) {
	sink := o.runner.EventSink(runtimeID)
	catalog := runnerCatalog{svc: o.svc}
	return func(sig event.Signal) {
		if sig.Kind == event.KindConfigOptions {
			if p, ok := sig.Payload.(event.ConfigOptions); ok && p.SessionID != "" {
				// 载荷里的 `Options` 是不透明形状；折成平台值的断言留在 `runtime`
				// （那是协议形状的合法落点，见 `runtime.ConfigOptionsOfPayload`）。
				if opts, ok := runtime.ConfigOptionsOfPayload(p.Options); ok {
					catalog.PutConfigOptions(runtimeID, opts)
				}
			}
		}
		sink(sig)
	}
}
