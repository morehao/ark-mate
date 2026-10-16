// Package logging 是「第三方库只认 log/slog」这一处边界的桥。
//
// 本仓自己的日志一律走 golib/glog（zap driver，装配根见 internal/app/command/logging.go）。
// 但外部依赖改不了：当前是 ACP SDK——`acpsdk.ClientSideConnection.SetLogger(*slog.Logger)`
// 的签名就是 `*slog.Logger`，且没注入时它自己回落到 `slog.Default()`。
// 没有这层桥，协议层诊断会绕过 glog 直接走 std log 的 stderr：级别、格式、轮转、
// 目标全都不一样（桌面态还会落到壳的 stderr，而不是平台的日志出口）。
//
// 因此**全仓只有本包允许 import "log/slog"**（由 internal/archtest 守卫）。这里不是
// "还在用 slog"，而是把外部世界唯一会说的一种日志语言翻译进来。
//
// 另外它也是「按档位分派到 glog」这一件事的唯一实现（见 `At`）：glog 没有"级别作参数"的入口，
// 而装载内核（定义装载、档案装载）只有一个 level 形参——核心进程用 Info、网关的长命进程用
// Debug（每次 tools/list 都重装，Info 会把日志刷成噪声）。
package logging

import (
	"context"
	"log/slog"

	"github.com/morehao/golib/glog"
)

// SDKLogger 返回一个把记录转发进 glog 的 *slog.Logger，供只认 slog 的第三方库使用。
func SDKLogger() *slog.Logger { return slog.New(handler{}) }

// handler 把 slog 记录按级别分派到 glog 的 *w 入口。
type handler struct{}

func (handler) Enabled(context.Context, slog.Level) bool { return true }

func (handler) Handle(ctx context.Context, r slog.Record) error {
	kvs := make([]any, 0, r.NumAttrs()*2)
	r.Attrs(func(a slog.Attr) bool {
		kvs = append(kvs, a.Key, a.Value.Any())
		return true
	})
	switch {
	case r.Level >= slog.LevelError:
		glog.Errorw(ctx, r.Message, kvs...)
	case r.Level >= slog.LevelWarn:
		glog.Warnw(ctx, r.Message, kvs...)
	case r.Level >= slog.LevelInfo:
		glog.Infow(ctx, r.Message, kvs...)
	default:
		glog.Debugw(ctx, r.Message, kvs...)
	}
	return nil
}

// WithAttrs / WithGroup 目前直接丢弃附加维度：调用方（ACP SDK）不使用它们，
// 实现完整语义需要一套属性累积，收益为零。真出现调用方要它，这里再补。
func (h handler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h handler) WithGroup(string) slog.Handler { return h }

// At 按档位分派到 glog。
//
// glog 没有"级别作参数"的入口（它是 Debugw/Infow/Warnw/Errorw 四个函数），而装载内核
// （`loadAgentDefinitionsFromStore` / `reloadCapabilityDefinitions` / `loadProviders`）只有一个
// level 形参：核心进程用 Info、网关的长命进程用 Debug（每次 tools/list 都重装，Info 会把日志刷成噪声）。
func At(ctx context.Context, level glog.Level, msg string, kvs ...any) {
	switch level {
	case glog.DebugLevel:
		glog.Debugw(ctx, msg, kvs...)
	case glog.WarnLevel:
		glog.Warnw(ctx, msg, kvs...)
	case glog.ErrorLevel:
		glog.Errorw(ctx, msg, kvs...)
	default:
		glog.Infow(ctx, msg, kvs...)
	}
}
