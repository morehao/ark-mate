package archtest

import (
	"reflect"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// TestConfigOptionValueTypesMatchACP 会话配置项的平台值类型与协议类型**逐字段、逐 json tag** 相等。
//
// 背景（C2）：内核契约（`runtime.Runtime.ConfigOptions/ApplyModel`）、模型目录缓存、会话状态机
// 原先直接拿 `acp.ConfigOption` 当语言；现在它们是平台值（`runtime.ConfigOption`），协议类型只在
// 两个边界出现：`runtime.ConfigOptionsOfACP`（读）与 `adapters/outbound/dsh`（写面下发）。
//
// 为什么必须钉住：这两份结构会被**直接序列化**给前端（`session.config_options` 事件与 `/models`
// 目录的 `options`），所以 json tag 就是 wire 契约；而字段集不同会让某条取值在转换时被静默丢掉
// （比如协议侧新增 description 而平台侧没跟——事件里少一个键，前端永远读不到，两侧测试都绿）。
//
// 比较口径：字段**顺序与名字**相等、json tag 逐字相等、类型**形状**相等（string/bool/*string/
// slice<形状>）。类型名本身必然不同（一个在 runtime、一个在 acp）——那正是这次收口的目的。
func TestConfigOptionValueTypesMatchACP(t *testing.T) {
	assertSameShape(t, "ConfigOptionValue",
		reflect.TypeOf(runtime.ConfigOptionValue{}), reflect.TypeOf(acp.ConfigOptionValue{}))
	assertSameShape(t, "ConfigOption",
		reflect.TypeOf(runtime.ConfigOption{}), reflect.TypeOf(acp.ConfigOption{}))
}

// assertSameShape 逐字段比对两份结构的名字、json tag 与类型形状。
func assertSameShape(t *testing.T, name string, got, want reflect.Type) {
	t.Helper()
	if got.NumField() != want.NumField() {
		t.Fatalf("%s 字段数不等：平台 %d ≠ 协议 %d（少一个字段 = 转换时静默丢一个键）",
			name, got.NumField(), want.NumField())
	}
	for i := 0; i < got.NumField(); i++ {
		gf, wf := got.Field(i), want.Field(i)
		if gf.Name != wf.Name {
			t.Fatalf("%s 第 %d 个字段名不等：平台 %s ≠ 协议 %s（顺序也要一致：转换按字段搬）",
				name, i, gf.Name, wf.Name)
		}
		if g, w := gf.Tag.Get("json"), wf.Tag.Get("json"); g != w {
			t.Errorf("%s.%s 的 json tag 分叉：平台 %q ≠ 协议 %q（tag 就是 wire 契约）",
				name, gf.Name, g, w)
		}
		if shapeOf(gf.Type) != shapeOf(wf.Type) {
			t.Errorf("%s.%s 的类型形状分叉：平台 %s ≠ 协议 %s",
				name, gf.Name, gf.Type, wf.Type)
		}
	}
	// 长度相等的反向保险：字段集相同但名字换了位置也应在上面被抓到；这里兜住"两边都空"。
	if got.NumField() == 0 {
		t.Fatalf("%s 解析成 0 个字段（fail-closed）：结构被改名/挪走了？", name)
	}
}

// shapeOf 把类型归成一个与包名无关的形状串（slice 递归到元素形状）。
func shapeOf(t reflect.Type) string {
	if t.Kind() == reflect.Slice {
		return "[]" + shapeOf(t.Elem())
	}
	if t.Kind() == reflect.Ptr {
		return "*" + shapeOf(t.Elem())
	}
	return t.Kind().String()
}
