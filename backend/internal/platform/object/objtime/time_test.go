package objtime

import (
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// TestLayoutMatchesSchemaLayout 钉住「平台时间文本」与「列落库布局」是同一个串。
//
// 两份声明必然并存（L0 不能 import L1，领域也不能 import L0）：分叉的表现是接口里的时间
// 带小数秒而库里不带（或反过来），逐字比较的游标/区间过滤会静默漏行，而两边各自的测试都绿。
func TestLayoutMatchesSchemaLayout(t *testing.T) {
	if Layout != model.LayoutTS {
		t.Fatalf("时间布局分叉：objtime.Layout = %q，model.LayoutTS = %q", Layout, model.LayoutTS)
	}
	if got := Format(time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)); got != "2026-10-20T01:02:03Z" {
		t.Fatalf("整秒格式变了：%q", got)
	}
	if got := Format(time.Time{}); got != "" {
		t.Fatalf("零值应为空串，实际 %q", got)
	}
	if got := Parse("2026-10-20T01:02:03Z"); !got.Equal(time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("解析结果不对：%v", got)
	}
	if got := Parse("garbage"); !got.IsZero() {
		t.Fatalf("不可解析应退到零值，实际 %v", got)
	}
}
