package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P2b：「不许持 manager 的锁去调会话域」守卫 ———
//
// 背景（2026-10-16 实测）：`manager` 的 `s.mu` 一度**同时**保护三样东西——workspace 策略字段、
// memory 模式的默认工作空间、以及**会话快照的读顺序**。后者的形态是"持 `s.mu` 调
// `sessionPlane.<动作>()`"。
//
// 那是**锁序反转**的雏形，而它的表现是 `go test ./...` **挂死**（不是红——见计划 §10.5.3 教训 1
// 的同一个家族）：会话域会回调 workspace 侧（`SetWorkspaceDisplay` 端口），一旦被调的域在自己
// 的锁下再请求 manager 侧，就得到互指的两条边。
//
// 判据（静态、可解析）：`internal/manager/**` 的生产文件里，任何**同一函数体内**既出现
// `s.mu.Lock()` 又出现 `sessionPlane.<动作>(` 的地方即红。
//
// 两条实现上的讲究，都是**实测踩出来的**：
//
//   - **函数边界只认顶格的 `}`**：第一版用 `strings.TrimSpace(line) == "}"`，于是函数里任何一个
//     提前 return 的 `if` 块（它的 `}` 也是单独一行）都会被当成函数结束——`providerEffect`
//     那种"前面有个 early return、后面才持锁取快照"的函数因此**静默漏过**（假绿）。
//     gofmt 过的代码里顶层函数的收尾 `}` 一定在第 0 列，所以按列判是可靠的。
//   - **判据放宽到 `sessionPlane.` 全部动作**（不只是 `Snapshot()`）：同族漏网的还有
//     `Lookup(`（`sessionRunning` / `sessionMeta` 就是）——只钉 `Snapshot()` 等于把"同一个错
//     的另一种写法"放过去。
//
// fail-closed：扫描面为空、或这个包里一处 `s.mu.Lock()` 都没有 → 失败（锁被搬走/改名 = 判据失锚）。
//
// 反证（实跑）：把 `s.mu.Lock()` / `defer Unlock` 加回 `workspaceUsage` → 红；
// 把 `sessionRunning` 的 `s.mu` 加回去 → 红。

// mutexSessionCallFunctions 扫出 manager 生产文件里"持 s.mu 且调会话域"的函数名（供报错用）。
func mutexSessionCallFunctions(t *testing.T, src string) []string {
	t.Helper()
	var out []string
	var cur string
	var locked bool
	var calls []string
	flush := func() {
		if cur != "" && locked && len(calls) > 0 {
			out = append(out, cur+" → sessionPlane."+strings.Join(dedupeStrings(calls), "/"))
		}
	}
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") {
			flush()
			cur, locked, calls = funcNameOf(trimmed), false, nil
			continue
		}
		// **顶格** `}` = 函数体结束；缩进的 `}` 只是某个块的结束（第一版在这里假绿）。
		if line == "}" {
			flush()
			cur, locked, calls = "", false, nil
			continue
		}
		if strings.Contains(line, "s.mu.Lock()") {
			locked = true
		}
		for _, m := range sessionPlaneCallRe.FindAllStringSubmatch(line, -1) {
			calls = append(calls, m[1])
		}
	}
	flush()
	return out
}

// sessionPlaneCallRe 抓 `sessionPlane.<动作>(` 的动作名。
var sessionPlaneCallRe = regexp.MustCompile(`sessionPlane\.([A-Za-z_][A-Za-z0-9_]*)\(`)

// dedupeStrings 保序去重（小切片，线性扫足够）。
func dedupeStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// funcNameOf 从 `func (s *Service) name(...)` / `func name(...)` 里取名字（取不到就返回整行）。
var funcNameRe = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)

func funcNameOf(decl string) string {
	if m := funcNameRe.FindStringSubmatch(decl); len(m) == 2 {
		return m[1]
	}
	return decl
}

// TestManagerDoesNotHoldMutexWhileCallingSessionPlane 见文件头：manager 不许持 `s.mu`
// 去调会话域。
func TestManagerDoesNotHoldMutexWhileCallingSessionPlane(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "manager")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：这条守卫失去了检查对象（目录改名/搬走了？）", dir)
	}
	locked := 0
	for _, path := range files {
		src := stripLineComments(readFileOrFatal(t, path))
		if strings.Contains(src, "s.mu.Lock()") {
			locked++
		}
		for _, fn := range mutexSessionCallFunctions(t, src) {
			t.Errorf("internal/manager/%s 的 `%s` 持 `s.mu` 调会话域：这是**锁序反转**的雏形。\n"+
				"会话域会回调 workspace / provider 侧（`SetWorkspaceDisplay` 这类端口），于是"+
				"\"manager 锁 → 会话锁\" 与某条反向回调合成互指的边——表现是 `go test ./...`"+
				"**挂死**而不是红。\n"+
				"处置：把对会话域的调用提到锁**外面**（会话域的方法自己取锁并返回副本，本来就不需要这把锁）。",
				filepath.Base(path), fn)
		}
	}
	// 判据本身不许空转：这个包里必须还有人在用 s.mu（否则说明 mutex 字段被搬走了，
	// 这条守卫的判据对象也就不存在了）。
	if locked == 0 {
		t.Fatal("internal/manager 里没有任何 `s.mu.Lock()`：锁本身被搬走/改名了？" +
			"这条守卫的判据挂在它身上，失锚等于空转。")
	}
}
