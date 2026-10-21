package archtest

// 本文件是 **注入面预算** 的守卫（T6）：平台侧真正进入模型上下文的正文只有一处——handoff memo
// （`domain/session/handoff.go`）。skill 正文是**文件投影**（底座自己读）、系统提示是部署级常量，
// 两者都没有平台侧的拼装点，所以**刻意不给它们造预算**（造出来的是一条空转判据）。
//
// 四条判据，每条挡一种"不会让别的测试变红"的错法：
//
//	A1 **预算走参数**（`HandoffMemo(sessionID string, maxChars int)`）：预算一旦退回全局常量，
//	   "这一次注入用了多少预算"就变成读代码才知道的事，而且测试只能靠改全局来造边界。
//	A2 **预算只有一个来源**：memo 实现体里不得写死 `DefaultHandoffMaxChars` 或字面量 `6000`
//	   ——那会让"配置改了不起作用"（配置项看起来在，实际被内部常量盖掉）。
//	A3 **唯一注入入口**：生产代码里 `HandoffMemo(` 只许有一处调用，且必须传平面上的预算字段
//	   （`handoffMaxChars`）——多一处调用或传字面量，都会让某个入口悄悄用另一套预算。
//	A4 **截断必须可回读**：截断提示里必须带位次锚（`seq=`）与读面路径（`GET /sessions/…/history?since_seq=`）。
//	   只说"（已截断）"等于信息丢失——模型不知道少了什么、人不知道去哪儿补。
//
// 反证（实跑 4 条，均红）：把 memo 的 maxChars 参数删掉改为读常量；把 `text, _ = truncate(...)`
// 那行的提示整段删掉；把提示里的 `since_seq=0` 去掉；在别处再加一个 `HandoffMemo(x, 3000)` 调用。

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestHandoffMemoBudgetIsParametric 见文件头 A1/A2。
func TestHandoffMemoBudgetIsParametric(t *testing.T) {
	root := backendRoot(t)
	path := filepath.Join(root, "internal", "domain", "session", "handoff.go")
	code := stripLineComments(readFileOrFatal(t, path))

	if !strings.Contains(code, "func (pl *Plane) HandoffMemo(sessionID string, maxChars int) string") {
		t.Fatalf("%s 里找不到 `func (pl *Plane) HandoffMemo(sessionID string, maxChars int) string`："+
			"预算必须走参数（否则「这一次注入用了多少预算」只能靠读代码）", path)
	}
	for _, banned := range []string{"DefaultHandoffMaxChars", "6000"} {
		if strings.Contains(code, banned) {
			t.Errorf("memo 实现体里出现 %q：预算只许由调用方给（内部再兜一个默认值会让配置改了不起作用）", banned)
		}
	}
}

// TestHandoffMemoHasSingleInjectionEntry 见文件头 A3。
func TestHandoffMemoHasSingleInjectionEntry(t *testing.T) {
	root := backendRoot(t)
	var callSites []string
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		base := filepath.Base(p)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		code := stripLineComments(readFileOrFatal(t, p))
		if !strings.Contains(code, "HandoffMemo(") {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		callSites = append(callSites, rel)
	}
	if len(callSites) == 0 {
		t.Fatalf("全仓找不到 `HandoffMemo(`：注入入口被改名/删掉了？（本守卫 fail-closed）")
	}
	// 定义一处 + 调用一处 = 恰好 2 个文件；多出来就是"第二个注入入口"。
	if len(callSites) != 2 {
		t.Fatalf("`HandoffMemo(` 应只出现在定义与其唯一调用点，实际 %d 处：%v", len(callSites), callSites)
	}
	lifecycle := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "lifecycle.go")))
	if !strings.Contains(lifecycle, "HandoffMemo(rec.ID, l.handoffMaxChars)") {
		t.Fatalf("唯一调用点必须传平面上的预算字段（`HandoffMemo(rec.ID, l.handoffMaxChars)`）：" +
			"传字面量就等于让 `MATE_HANDOFF_MAX_CHARS` 失效")
	}
}

// TestHandoffTruncationIsLocatable 见文件头 A4（源码侧；运行时行为由
// `domain/session` 的 `TestHandoffMemoTruncationIsLocatable` 钉住）。
func TestHandoffTruncationIsLocatable(t *testing.T) {
	root := backendRoot(t)
	path := filepath.Join(root, "internal", "domain", "session", "handoff.go")
	code := stripLineComments(readFileOrFatal(t, path))
	for _, want := range []struct{ text, why string }{
		{"已截断", "截断必须说出来（沉默的有损压缩等于信息丢失）"},
		{"seq=", "必须带位次锚：模型/人据此知道保留的内容从哪开始"},
		{"GET /sessions/", "必须给可回读的读面路径"},
		{"history?since_seq=", "必须给读面的锚点参数（`since_seq`），只给路径等于让人自己猜"},
	} {
		if !strings.Contains(code, want.text) {
			t.Errorf("%s 里找不到 %q：%s", path, want.text, want.why)
		}
	}
}
