package dtolibrary

import (
	"encoding/json"
	"testing"

	"github.com/morehao/ark-mate/pkg/object/objdelivery"
)

// TestApplicabilityWireShapeUnchanged 钉住 M6 迁移（类型从本包搬到 pkg/object/objdelivery）**逐字不变**的
// JSON 形状。
//
// 为什么值得一条测试：前端 `packages/types/src/capability.ts` 的 `LibraryApplicability`
// （`runtime`/`ok`/`status`/`reason?`）是按这份 wire 形状写的，迁移只要动了字段名、大小写或
// `omitempty`，前端就会静默读不到字段（TS 不做运行时校验，界面只是"少了原因那句话"）。
// 所以这里**逐字**比对序列化结果，而不是比对结构体字段。
func TestApplicabilityWireShapeUnchanged(t *testing.T) {
	got, err := json.Marshal(Applicability{
		Runtime: "rtx", OK: true, Status: StatusProjected, Reason: "投放到 /tmp/skills（skill）",
	})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	want := `{"runtime":"rtx","ok":true,"status":"projected","reason":"投放到 /tmp/skills（skill）"}`
	if string(got) != want {
		t.Fatalf("Applicability 的 wire 形状变了（前端零改的前提被破坏）:\n got %s\nwant %s", got, want)
	}
	// reason 缺席（omitempty）时不得出现空字符串键。
	got, err = json.Marshal(Applicability{Runtime: "dsh", OK: false, Status: StatusNotApplicable})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	want = `{"runtime":"dsh","ok":false,"status":"not_applicable"}`
	if string(got) != want {
		t.Fatalf("reason 缺席时的 wire 形状变了:\n got %s\nwant %s", got, want)
	}
}

// TestApplicabilityIsAliasOfObjdelivery 别名必须在**类型身份**上就是同一个（迁移期间两处并存
// 时最容易出的错是"看起来同名、其实是两个结构体"，那会让 /library 与 /providers 的 JSON 悄悄分叉）。
func TestApplicabilityIsAliasOfObjdelivery(t *testing.T) {
	var a Applicability = objdelivery.Applicability{Runtime: "rtx", OK: true, Status: StatusProjected}
	var b objdelivery.Applicability = a
	if b.Runtime != "rtx" || b.Status != objdelivery.StatusProjected {
		t.Fatalf("别名不是同一类型: %+v", b)
	}
}

// TestStatusConstantsForwarded Status 常量转发到 objdelivery（既有调用点 `dtolibrary.StatusXxx` 零改）。
func TestStatusConstantsForwarded(t *testing.T) {
	pairs := map[string]string{
		StatusProjected:     objdelivery.StatusProjected,
		StatusWritten:       objdelivery.StatusWritten,
		StatusUntested:      objdelivery.StatusUntested,
		StatusNotConfigured: objdelivery.StatusNotConfigured,
		StatusNotApplicable: objdelivery.StatusNotApplicable,
		StatusDisabled:      objdelivery.StatusDisabled,
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("常量转发不一致: %q != %q", got, want)
		}
	}
}
