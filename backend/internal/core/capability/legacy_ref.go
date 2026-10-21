package capability

import (
	"context"

	"github.com/morehao/golib/glog"
)

// WarnLegacySecretRef 就地警告一条"档案里还留着明文/旧引用"的键。
//
// 归在这一层（而不是机制层）：警告的**文案与判据**都由 `LegacySecretRefWarning` 定，
// 这里只是把那条文案发出去。留在机制层会让领域为了打一条日志反向依赖它。
func WarnLegacySecretRef(ctx context.Context, ref, where string) {
	glog.Warnw(ctx, LegacySecretRefWarning(ref), "ref", ref, "where", where)
}
