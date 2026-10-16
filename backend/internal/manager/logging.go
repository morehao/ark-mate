package manager

import (
	"context"

	"github.com/morehao/ark-mate/pkg/logging"
	"github.com/morehao/golib/glog"
)

// logAt 按档位分派到 glog（唯一实现已落 `pkg/logging.At`——机制/领域层也要用它）。
//
// TODO(P4b): 删转发（调用点直接依赖 logging.At）
func logAt(ctx context.Context, level glog.Level, msg string, kvs ...any) {
	logging.At(ctx, level, msg, kvs...)
}
