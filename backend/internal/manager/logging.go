package manager

import (
	"context"

	"github.com/morehao/golib/glog"
)

// logAt 按档位分派到 glog。
//
// glog 没有"级别作参数"的入口（它是 Debugw/Infow/Warnw/Errorw 四个函数），而装载内核
// （`loadAgentDefinitionsFromStore` / `reloadCapabilityDefinitions`）只有一个 level 形参：
// 核心进程用 Info、网关的长命进程用 Debug（每次 tools/list 都重装，Info 会把日志刷成噪声）。
func logAt(ctx context.Context, level glog.Level, msg string, kvs ...any) {
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
