package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// frozenEventTypes 是控制面**对外契约**的事件 type 全集（`/v1/forge/ws/events` 帧的
// `type` 字段 + sqlite `events.type` 列 + transcript 回放）。前端 @ark/types 的
// events.ts 按这些字面量做联合类型，历史数据里也存着它们。
//
// 因此：新增事件是**兼容变更**（改这个列表 + 前端），改名/删除是**破坏性变更**
// （会让历史 events 行与前端联合类型同时失配）。本快照让后者无法静默发生。
//
// 权威定义在 internal/manager/events.go（P2/P4 拆包后随之迁移，本测试同步更新）。
var frozenEventTypes = []string{
	"session.created",
	"session.resumed",
	"session.user_message",
	"session.message",
	"session.thought",
	"session.tool_call",
	"session.tool_update",
	"session.usage",
	"session.permission",
	"session.config_options",
	"session.permission_request",
	"session.permission_policy",
	"session.permission_resolved",
	"session.elicitation_request",
	"session.elicitation_resolved",
	"session.input_queued",
	"session.input_delivered",
	"session.input_cancelled",
	"session.runtime",
	"turn.ended",
	"turn.error",
	"run.cancelled",
	"automation.run",
}

// TestEventTypeContractFrozen 冻结域事件 type 字面量。
func TestEventTypeContractFrozen(t *testing.T) {
	// 从 events.go **源码**里扫出全部 `Bus* = "..."`，而不是在守卫里再手抄一份清单：
	// 手抄那份有个洞——新增常量时若忘了往守卫里加一行，守卫照样绿（它根本没看见新常量），
	// 而"新增事件 type 忘了同步契约"正是这个守卫存在的唯一理由。
	raw, err := os.ReadFile(filepath.Join(backendRoot(t), "internal", "manager", "events.go"))
	if err != nil {
		t.Fatalf("读 events.go 失败: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*(Bus[A-Za-z0-9_]*)\s*=\s*"([^"]+)"`)
	got := make([]string, 0, len(frozenEventTypes))
	seen := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		if prev, dup := seen[m[2]]; dup {
			t.Fatalf("事件 type %q 被两个常量重复声明（%s 与 %s）", m[2], prev, m[1])
		}
		seen[m[2]] = m[1]
		got = append(got, m[2])
	}
	if len(got) == 0 {
		t.Fatal("从 events.go 没扫到任何 `Bus* = \"...\"`（正则失效，守卫形同虚设）")
	}
	sort.Strings(got)
	want := append([]string(nil), frozenEventTypes...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("事件 type 数量变化：契约 %d 个，代码 %d 个", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个事件 type 不一致：契约 %q，代码 %q", i, want[i], got[i])
		}
	}
}

// TestFrontendEventTypesMatchRegistry 钉住事件契约的**第三条边**：后端 events.go ↔ 前端 Ev。
//
// 上面 TestEventTypeContractFrozen 钉的是「真实常量 ↔ frozenEventTypes」，而它自己的注释
// 就写着「前端 @ark/types 的 events.ts 按这些字面量做联合类型」——但**此前没有任何测试
// 兜住前端那一份**：后端改一个事件名（或前端手滑打错一个字母），前端 Ev 会静默失配，
// 直到某个 WS 帧在浏览器里没人认领才暴露。这与错误码的 BizCode 第三条边是同一个缺口
// （见 pkg/code 的 TestFrontendBizCodesMatchRegistry），这里用同样办法补上：
// **Go 读 TS 源码**，不引前端测试框架、不依赖 node 工具链。
func TestFrontendEventTypesMatchRegistry(t *testing.T) {
	path := filepath.Join(backendRoot(t), "..", "frontend", "packages", "types", "src", "events.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读前端事件常量 %s 失败: %v（前端目录结构变了？）", path, err)
	}

	// 只取 `export const Ev = {` … `} as const` 这一段：文件里还有 interface 定义，
	// 全局扫引号会把别的字面量卷进来。
	src := string(raw)
	start := strings.Index(src, "export const Ev = {")
	if start < 0 {
		t.Fatalf("%s 里找不到 `export const Ev = {`（常量改名了？守卫已失效）", path)
	}
	end := strings.Index(src[start:], "} as const")
	if end < 0 {
		t.Fatalf("%s 里找不到 Ev 块的收尾 `} as const`（守卫已失效）", path)
	}
	block := src[start : start+end]

	re := regexp.MustCompile(`(?m)^\s*([A-Za-z][A-Za-z0-9_]*)\s*:\s*'([^']*)'\s*,?`)
	got := map[string]string{} // 事件字面量 → 前端键名（报错时好定位）
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		got[m[2]] = m[1]
	}
	if len(got) == 0 {
		t.Fatalf("从 %s 的 Ev 块里没解析出任何 `Key: 'value'`（解析失效，守卫形同虚设）", path)
	}

	want := map[string]bool{}
	for _, ev := range frozenEventTypes {
		want[ev] = true
		if _, ok := got[ev]; !ok {
			t.Errorf("事件 type %q 在契约里、但前端 events.ts 的 Ev 缺失", ev)
		}
	}
	for ev, key := range got {
		if !want[ev] {
			t.Errorf("前端 Ev.%s = %q，但后端 events.go / 事件契约里没有这个 type", key, ev)
		}
	}
}
