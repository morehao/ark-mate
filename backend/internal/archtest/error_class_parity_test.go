package archtest

import (
	"testing"

	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/infra/acp"
)

// TestErrorClassVocabularyMatchesACP 平台分类词与协议分类词必须逐条同值。
//
// 背景（C2）：`domain` / `service` 不再 import 协议库，错误分类改由 `platform/code` 声明
// （`code.ClassRequest` 等），由 `code.NewError` 在**唯一一处**翻成协议载体。
//
// 失效方式：两份词汇分叉不会让任何东西变红——错误照常构造、`acp.As` 也照常命中，只是
// `class` 字段落成另一个串：前端按 class 渲染的动作（重试 / 去登录 / 改参数）与控制器
// 出口的码映射给出不同答案，而两边自己的测试都是绿的。更隐蔽的一半是**新增一个 acp 分类**
// 而平台词汇没跟：内圈从此拿不到它，写代码的人只能回去 import 协议库。
//
// fail-closed：这张对照表是唯一入口，新增分类必须同时更新它（少一条 = 少一条判据，
// 但下面的"反向覆盖"会先红）。
func TestErrorClassVocabularyMatchesACP(t *testing.T) {
	pairs := []struct {
		name     string
		platform code.Class
		wire     acp.Class
	}{
		{"ClassConnection", code.ClassConnection, acp.ClassConnection},
		{"ClassAccess", code.ClassAccess, acp.ClassAccess},
		{"ClassLimit", code.ClassLimit, acp.ClassLimit},
		{"ClassRequest", code.ClassRequest, acp.ClassRequest},
		{"ClassService", code.ClassService, acp.ClassService},
		{"ClassUnsupported", code.ClassUnsupported, acp.ClassUnsupported},
		{"ClassUnknown", code.ClassUnknown, acp.ClassUnknown},
	}
	platform := map[string]bool{}
	for _, p := range pairs {
		if string(p.platform) != string(p.wire) {
			t.Errorf("%s 分叉：code=%q acp=%q（改一侧等于改掉前端按 class 渲染的动作）",
				p.name, p.platform, p.wire)
		}
		platform[string(p.platform)] = true
	}
	// 反向覆盖：协议侧的每个分类都必须在平台词汇里（否则内圈迟早要 import 协议库）。
	wireClasses := map[string]acp.Class{
		"ClassConnection":  acp.ClassConnection,
		"ClassAccess":      acp.ClassAccess,
		"ClassLimit":       acp.ClassLimit,
		"ClassRequest":     acp.ClassRequest,
		"ClassService":     acp.ClassService,
		"ClassUnsupported": acp.ClassUnsupported,
		"ClassUnknown":     acp.ClassUnknown,
	}
	if len(platform) != len(wireClasses) {
		t.Fatalf("分类数量不等：platform=%d wire=%d（新增一个分类必须在本表里登记）",
			len(platform), len(wireClasses))
	}
	for name, w := range wireClasses {
		if !platform[string(w)] {
			t.Errorf("协议侧分类 %s=%q 在平台词汇里没有对应项：内圈将只能 import 协议库",
				name, string(w))
		}
	}

	// 构造面真的带上了平台分类与 code（不是丢字段）：B4 起 `code.NewError` 造的是**平台载体**
	// （`*code.Coded`），协议错误只在适配器出口被折成平台视图，内圈不再产出协议错误。
	err := code.NewError(code.ClassRequest, "invalid_param", "参数不对", nil)
	re, ok := code.RuntimeErrorOf(err)
	if !ok || re.Class != code.ClassRequest || re.Code != "invalid_param" {
		t.Fatalf("code.NewError 未把分类/code 带到平台错误视图：%+v (ok=%v)", re, ok)
	}
	if ce, ok := code.CodedOf(err); !ok || ce.Class != code.ClassRequest {
		t.Fatalf("code.NewError 的载体应是 *code.Coded：%+v (ok=%v)", ce, ok)
	}
}
