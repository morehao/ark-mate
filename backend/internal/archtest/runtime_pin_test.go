package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是「底座由构建期固定」的三条静态守卫（2026-10-10 起的产品约束）。
//
// 需求原话是"用户不能选择 agent runtime，只能在构建时指定，不指定默认 dsh"。这条约束的三个
// 危险失效方式都**不会**让任何测试变红，所以必须各有一条机器判据：
//
//	① 默认值被顺手改掉（或注入变量被初始化成别的值）—— 默认 dsh 是"不指定"的全部含义；
//	② 选择面字段被顺手加回来 —— 加了就编译得过，用户面悄悄多出一个能写、平台却不读的字段；
//	③ 写面的**显式拒绝**被删掉 —— 删掉之后请求照样 200，只是什么都不发生（本仓明令禁止的
//	   骗人字段：agent 的 `default_model` 就是为这条立的先例）。
//
// 判据一律"解析不到就失败"（fail-closed）：改名/搬迁会让守卫静默空转，那比没有守卫更糟。

// TestBuildRuntimeDefaultIsDsh 不注入 `-X` 时，构建期底座必须是 dsh（dsh）。
func TestBuildRuntimeDefaultIsDsh(t *testing.T) {
	path := filepath.Join(backendRoot(t), "internal", "runtime", "build.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", path, err)
	}
	clean := stripTSComments(string(src))

	// v3.1：`internal/runtime` 不得 import adapter（§3.2.1 规则 5），所以默认值是**字面量**；
	// 它与 adapter 的 `ID` 相等由 `internal/infra/dsh` 的守卫钉住（双向：任一侧改名都会红）。
	defRE := regexp.MustCompile(`(?m)^\s*const\s+DefaultBuildRuntime\s*=\s*"dsh"\s*$`)
	if !defRE.MatchString(clean) {
		t.Fatalf("%s 里找不到 `const DefaultBuildRuntime = \"dsh\"`："+
			"「不指定默认 dsh」这条需求的唯一落点被改了？", path)
	}
	// adapter 侧必须同时声明 `ID = "dsh"`，且有一个测试断言两侧相等。
	adapterPath := filepath.Join(backendRoot(t), "internal", "infra", "dsh", "adapter.go")
	adapterSrc := stripTSComments(readFileOrFatal(t, adapterPath))
	if !strings.Contains(adapterSrc, "runtime.Adapter") {
		t.Fatalf("internal/infra/dsh/adapter.go 里找不到 `runtime.Adapter`：adapter 契约被改名/搬走了？")
	}
	// 注入变量必须是**空串初值**：初值一旦是别的 runtime id，`-X` 没注入时默认就不再是 dsh。
	// 允许显式类型声明（`var BuildRuntime string`）或带初值（`var BuildRuntime = ""`）两种写法。
	varRE := regexp.MustCompile(`(?m)^\s*var\s+BuildRuntime\s*(string\s*)?=\s*""\s*$`)
	if !varRE.MatchString(clean) {
		t.Fatalf("%s 里找不到空的 `var BuildRuntime = \"\"`：注入变量的初值必须为空"+
			"（否则「不指定」的语义被初值顶掉）", path)
	}
}

// agentRuntimeSelectionFields 选择面字段的声明形状（**只剩拒绝用的 json.RawMessage 是可以的**）。
//
// 判据刻意只看"Go 字段声明"这一种形状：`Runtimes json.RawMessage`（拒绝出口）、
// `a.Runtimes` 之类的读法、以及注释里的说明都不该命中。所以模式带上类型前缀。
var (
	reRuntimesList   = regexp.MustCompile(`\bRuntimes\s+\[\]string\b`)
	reRoutingPolicy  = regexp.MustCompile(`\bRoutingPolicy\s+string\b`)
	reRuntimeHintStr = regexp.MustCompile(`\bRuntimeHint\s+string\b`)
)

// selectionSurfaceTarget 一处扫描面：ban 哪些形状。
//
// 为什么按承载处给不同的形状集（而不是一套扫全部）：`SessionRec` 上"这条会话上一次跑在哪个
// 底座上"是**执行事实**（该字段 v17 前叫 `RuntimeHint`，现在叫 `RuntimeID`），恢复路径要靠它
// 判 `sameRuntime`。它是读出来的账，不是用户能填的选择面，因此会话记录只 ban 前两条。
// 禁词形状本身仍是 `RuntimeHint string`——那是**已删除**的选择面字段名，改名之后（v17）
// 它不再是一条"放行豁免"，而只是一句历史说明：这个名字出现即红。
//
// `path` 一律是**绝对路径**：会话记录那一处按**符号**定位（`uniqueSourceFile`，见下面
// `TestNoAgentRuntimeSelectionSurface` 的注释），另两处是仍然稳定的精确路径（`dto` 与
// `internal/platform/automation` 不在本次「manager 解散」的重划范围内）。
type selectionSurfaceTarget struct {
	path     string
	patterns []*regexp.Regexp
}

// TestNoAgentRuntimeSelectionSurface 用户面不得再有"按 agent / 会话选底座"的字段声明。
//
// 扫描面是**还活着的那些承载处**（v19 起 agent 定义整个没了，所以当时 agent 定义与
// `dto/dtoagent/*` 那几处随之删除——把它们留在清单里会让这条守卫一上来就"读不到文件"而红，
// 而不是静默空转）：会话请求、automation 目标、会话记录。
// 刻意用**逐个承载处**而不是全仓扫描：`capability.Skill.Runtimes` 等**投放声明**仍然合法
// （`runtimes` 在 v19 之后依然是 skill 的正经字段），全仓扫会误伤，而误伤的守卫迟早被人放宽到失效。
//
// P0：会话记录那一处原先钉着精确文件路径（`service.go`）。该文件是 §3.4「改判」的
// 解散对象（45 个字段按归属拆到领域包），钉路径必然在搬家后烂掉。判据改锚在**符号**上——
// `type SessionRec struct` 是"会话记录"这条承载处的声明行，它随会话域搬进 `domain/session`；
// `uniqueSourceFile` 已经 fail-closed（0 处 = 判据失去对象；≥2 处 = 有人又抄了一份），
// 而旧写法只读钉死的那一个文件、看不见第二处。
func TestNoAgentRuntimeSelectionSurface(t *testing.T) {
	root := backendRoot(t)
	internalDir := filepath.Join(root, "internal")
	targets := []selectionSurfaceTarget{
		{path: uniqueSourceFile(t, internalDir, nil,
			`(?m)^type\s+SessionRec\s+struct`, "会话记录 `SessionRec` 的类型声明"),
			patterns: []*regexp.Regexp{reRuntimesList, reRoutingPolicy}},
		{path: filepath.Join(root, "internal", "contract", "dtosession", "request.go"),
			patterns: []*regexp.Regexp{reRuntimeHintStr}},
		{path: filepath.Join(root, "internal", "platform", "automation", "types.go"),
			patterns: []*regexp.Regexp{reRuntimeHintStr}},
	}
	for _, tgt := range targets {
		path := tgt.path
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", path, err)
		}
		clean := stripTSComments(string(src))
		for _, re := range tgt.patterns {
			if m := re.FindString(clean); m != "" {
				t.Errorf("%s 里出现了选择面字段声明 %q：底座由构建期固定，"+
					"用户面不得再有按 agent/会话选底座的字段（要拒绝请求请用 json.RawMessage，"+
					"见 `dtosession.Request.AgentID` 与被撤键的显式拒绝实现 `rejectRemovedAgentSelection`）",
					path, strings.TrimSpace(m))
			}
		}
	}

	// 前端消费侧：这三个类型文件是"用户能不能选"的唯一落点，不得再声明被撤掉的字段。
	// v19 之前这里扫的是 `packages/types/src/agent.ts`（该文件已随 agent 面删除）——
	// 名字换了而扫描面没跟，最常见的结果是**扫一个不存在的路径**（红）或**扫一个空目录**（静默绿），
	// 所以下面每个文件读不到都当场判失败（fail-closed）。
	frontFaces := []string{"automation.ts", "session.ts", "usage.ts"}
	for _, name := range frontFaces {
		front := filepath.Join(frontendRoot(t), "packages", "types", "src", name)
		src, err := os.ReadFile(front)
		if err != nil {
			t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", front, err)
		}
		clean := stripTSComments(string(src))
		for _, banned := range []string{`runtimes?:`, "routing_policy", "runtime_hint", "agent_id"} {
			if idx := strings.Index(clean, banned); idx >= 0 {
				line := 1 + strings.Count(clean[:idx], "\n")
				t.Errorf("%s:%d 仍声明/使用了被撤掉的字段 %q：后端已经不给这些字段，"+
					"前端声明它等于许诺一个永远为空的东西（注释里解释历史是允许的——本判据已剥注释）",
					name, line, banned)
			}
		}
	}
}

// TestRuntimeSelectionRejectionsExist 写面的**显式拒绝**必须真的存在。
//
// 为什么单独立一条：拒绝逻辑是可以被"顺手删掉"的，而删掉之后请求照样返回 200——只是
// 那个键被 JSON 绑定悄悄丢掉，用户以为"我改了底座/我指定了 agent"，实际什么都没发生。
// 这类静默 no-op 是本仓的 B1 类风险，而它**不会让任何别的测试变红**。
//
// v19 的面：会话写面（`agent_id`）与用量读面（`?agent_id=`）走会话与用量两个写面上对
// `rejectRemovedAgentSelection` / `rejectRemovedUsageDimension` 的调用，automation 补丁里的
// `target.runtime_hint` 走 automation 写面的那一对（拒绝 + 老键自愈）。三处各自的"定义"与
// "调用"都要在，缺一处就是一条写路径静默忽略。
//
// P0：判据一律锚在**符号**上。原先它钉着四个精确文件路径（`reject_removed_keys.go` /
// `sessions.go` / `usage.go` / `automation.go`）——它们在重构里分别归 `mechanism/definition`
// 与 `domain/session`、`domain/automation`
// （设计 §3.4），钉路径在搬家后只会"读不到文件"（假红）或静默空转。锚在符号上之后：
// 0 处 = 判据失去对象（fail-closed），≥2 处 = 有人又抄了一份实现/调用点（旧写法只读钉死的那一个文件，看不见第二处）。
func TestRuntimeSelectionRejectionsExist(t *testing.T) {
	root := backendRoot(t)
	internalDir := filepath.Join(root, "internal")

	// ① DTO 必须保留**只为拒绝**的原始 JSON 字段（类型是 json.RawMessage）。
	dto := filepath.Join(root, "internal", "contract", "dtosession", "request.go")
	src, err := os.ReadFile(dto)
	if err != nil {
		t.Fatalf("读不到 %s: %v", dto, err)
	}
	clean := stripTSComments(string(src))
	if !regexp.MustCompile(`\bAgentID\s+json\.RawMessage\b`).MatchString(clean) {
		t.Errorf("%s 里找不到 `AgentID json.RawMessage`：会话写面必须能接住这个已下线的键"+
			"才能 40001 显式拒绝（删掉它 = 请求静默成功、什么都不发生）", dto)
	}

	// ② 拒绝的**实现**必须存在，且**恰好一处**（按符号定位，不钉文件路径）。
	for _, fn := range []string{"rejectRemovedAgentSelection", "rejectRemovedUsageDimension"} {
		impl := uniqueSourceFile(t, internalDir, nil, declREFor(fn).String(),
			"被撤键的显式拒绝实现 `"+fn+"`")
		if !strings.Contains(stripTSComments(readSourceFor(t, impl)), "func "+fn+"(") {
			t.Errorf("%s 里找不到 `func %s(`：被撤掉的键的显式拒绝实现没了（写面会静默成功）", impl, fn)
		}
	}

	// ②' 拒绝必须被**调用**——只有定义没有调用等于一条没人执行的规矩。
	// 锚 = 那次交接本身（把接住的 raw 值交给拒绝函数）：调用点随会话/用量写面搬走，判据不跟着搬。
	for _, site := range []struct {
		expr string
		call string
	}{
		{"rejectRemovedAgentSelection(req.AgentID)", "rejectRemovedAgentSelection("},
		{"rejectRemovedUsageDimension(q)", "rejectRemovedUsageDimension("},
	} {
		path := uniqueSourceFile(t, internalDir, nil, regexp.QuoteMeta(site.expr),
			"被撤键的拒绝调用点 `"+site.expr+"`")
		if !strings.Contains(stripTSComments(readSourceFor(t, path)), site.call) {
			t.Errorf("%s 里找不到 %q：这条写/读路径会静默忽略被撤掉的键", path, site.call)
		}
	}

	// ③ automation 写面必须有 runtime_hint 的拒绝 + 老键自愈。锚同样是符号。
	for _, fn := range []string{"rejectRuntimeHintInPatch", "dropLegacyRuntimeHint"} {
		impl := uniqueSourceFile(t, internalDir, nil, declREFor(fn).String(),
			"automation 写面的 `"+fn+"`")
		if !strings.Contains(stripTSComments(readSourceFor(t, impl)), "func "+fn+"(") {
			t.Errorf("%s 里找不到 `func %s(`：automation 写面对 target.runtime_hint "+
				"既要显式拒绝（40001）也要对老定义的残留键自愈（否则存量定义永远存不回去）", impl, fn)
		}
	}
}
