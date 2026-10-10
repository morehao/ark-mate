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
	path := filepath.Join(backendRoot(t), "pkg", "runtime", "build.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", path, err)
	}
	clean := stripTSComments(string(src))

	// 默认常量必须**指向 adapter 的 ID**（不是字面量 "dsh"，也不是别的 runtime）。
	defRE := regexp.MustCompile(`(?m)^\s*const\s+DefaultBuildRuntime\s*=\s*dsh\.ID\s*$`)
	if !defRE.MatchString(clean) {
		t.Fatalf("%s 里找不到 `const DefaultBuildRuntime = dsh.ID`："+
			"「不指定默认 dsh」这条需求的唯一落点被改了？", path)
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
// 为什么按文件给不同的形状集（而不是一套扫全部）：`ConversationRec.RuntimeHint` 是**执行事实**
// ——"这条会话上一次跑在哪个底座上"，恢复路径要靠它判 `sameRuntime`。它是读出来的账，不是用户
// 能填的选择面，因此 service.go 只 ban 前两条。
type selectionSurfaceTarget struct {
	rel      string
	patterns []*regexp.Regexp
}

// TestNoAgentRuntimeSelectionSurface 用户面不得再有"按 agent / 会话选底座"的字段声明。
//
// 扫描面是**定义与 DTO 的承载处**（agent 定义/请求/响应、automation 目标、会话请求）
// 加上前端 `Agent` 类型；每一条都是"用户能不能选"的直接落点。
// 刻意用**目录级清单**而不是全仓扫描：`capability.Skill.Runtimes` 等**投放声明**仍然合法，
// 全仓扫会误伤，而误伤的守卫迟早被人放宽到失效。
func TestNoAgentRuntimeSelectionSurface(t *testing.T) {
	root := backendRoot(t)
	targets := []selectionSurfaceTarget{
		{rel: filepath.Join("internal", "manager", "agents.go"),
			patterns: []*regexp.Regexp{reRuntimesList, reRoutingPolicy, reRuntimeHintStr}},
		{rel: filepath.Join("internal", "manager", "service.go"),
			patterns: []*regexp.Regexp{reRuntimesList, reRoutingPolicy}},
		{rel: filepath.Join("internal", "dto", "dtoagent", "request.go"),
			patterns: []*regexp.Regexp{reRuntimesList, reRoutingPolicy}},
		{rel: filepath.Join("internal", "dto", "dtoagent", "response.go"),
			patterns: []*regexp.Regexp{reRuntimesList, reRoutingPolicy}},
		{rel: filepath.Join("internal", "dto", "dtoconversation", "request.go"),
			patterns: []*regexp.Regexp{reRuntimeHintStr}},
		{rel: filepath.Join("pkg", "automation", "types.go"),
			patterns: []*regexp.Regexp{reRuntimeHintStr}},
	}
	for _, tgt := range targets {
		path := filepath.Join(root, tgt.rel)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", path, err)
		}
		clean := stripTSComments(string(src))
		for _, re := range tgt.patterns {
			if m := re.FindString(clean); m != "" {
				t.Errorf("%s 里出现了选择面字段声明 %q：底座由构建期固定，"+
					"用户面不得再有按 agent/会话选底座的字段（要拒绝请求请用 json.RawMessage，见 dtoagent）",
					tgt.rel, strings.TrimSpace(m))
			}
		}
	}

	// 前端消费侧：`Agent` 类型不得再声明 runtimes（后端已经不给这个字段）。
	front := filepath.Join(frontendRoot(t), "packages", "types", "src", "agent.ts")
	src, err := os.ReadFile(front)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", front, err)
	}
	if m := regexp.MustCompile(`(?m)^\s*runtimes\?:`).FindString(stripTSComments(string(src))); m != "" {
		t.Errorf("%s 的 Agent 类型又声明了 %q：后端已不再下发 runtimes，"+
			"前端声明它等于许诺一个永远为空的字段", front, strings.TrimSpace(m))
	}
}

// TestRuntimeSelectionRejectionsExist 写面的**显式拒绝**必须真的存在。
//
// 为什么单独立一条：拒绝逻辑是可以被"顺手删掉"的，而删掉之后请求照样返回 200——只是
// `runtimes` / `routing_policy` / `target.runtime_hint` 被 JSON 绑定悄悄丢掉，
// 用户以为"我改了底座"，实际什么都没发生。这类静默 no-op 是本仓的 B1 类风险。
func TestRuntimeSelectionRejectionsExist(t *testing.T) {
	root := backendRoot(t)

	// ① DTO 必须保留**只为拒绝**的原始 JSON 字段（类型是 json.RawMessage）。
	dto := filepath.Join(root, "internal", "dto", "dtoagent", "request.go")
	src, err := os.ReadFile(dto)
	if err != nil {
		t.Fatalf("读不到 %s: %v", dto, err)
	}
	clean := stripTSComments(string(src))
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`\bRuntimes\s+json\.RawMessage\b`),
		regexp.MustCompile(`\bRoutingPolicy\s+json\.RawMessage\b`),
	} {
		if !re.MatchString(clean) {
			t.Errorf("%s 里找不到 %q：写面必须能用 40001 显式拒绝这个键"+
				"（删掉它 = 请求静默成功、什么都不发生）", dto, re.String())
		}
	}

	// ② agent 写面必须调用拒绝函数（创建 + 更新两条路各一次）。
	agents := filepath.Join(root, "internal", "manager", "agents.go")
	src, err = os.ReadFile(agents)
	if err != nil {
		t.Fatalf("读不到 %s: %v", agents, err)
	}
	clean = stripTSComments(string(src))
	if !strings.Contains(clean, "func rejectRuntimeSelection(") {
		t.Errorf("%s 里找不到 `func rejectRuntimeSelection(`：agent 写面的显式拒绝实现没了", agents)
	}
	if n := strings.Count(clean, "rejectRuntimeSelection("); n < 3 { // 1 处定义 + 2 处调用
		t.Errorf("%s 里 rejectRuntimeSelection 只出现 %d 次（期望 1 处定义 + Create/Update 各 1 处调用）："+
			"少一处就意味着那条写路径会静默忽略", agents, n)
	}

	// ③ automation 写面必须有 runtime_hint 的拒绝 + 老键自愈。
	auto := filepath.Join(root, "internal", "manager", "automation.go")
	src, err = os.ReadFile(auto)
	if err != nil {
		t.Fatalf("读不到 %s: %v", auto, err)
	}
	clean = stripTSComments(string(src))
	for _, want := range []string{"func rejectRuntimeHintInPatch(", "func dropLegacyRuntimeHint("} {
		if !strings.Contains(clean, want) {
			t.Errorf("%s 里找不到 %q：automation 写面对 target.runtime_hint "+
				"既要显式拒绝（40001）也要对老定义的残留键自愈（否则存量定义永远存不回去）", auto, want)
		}
	}
}
