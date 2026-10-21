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
// 判据（静态、可解析）：`internal/app/run/**` 的生产文件里，任何**同一函数体内**既出现
// `s.mu.Lock()` 又出现**任一域平面**的 `<动作>(` 的地方即红。
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
//   - **再放宽到「所有域平面」**（`sessionPlane`/`providerPlane`/`automationPlane`/`planner`/
//     `runtimePlane`）：P4a 实测又抓到三处只钉会话域时看不见的同族写法，见 `domainPlaneNames`。
//
// fail-closed：扫描面为空、或这个包里一处 `s.mu.Lock()` 都没有 → 失败（锁被搬走/改名 = 判据失锚）。
//
// 反证（实跑）：把 `s.mu.Lock()` / `defer Unlock` 加回 `workspaceUsage` → 红；
// 把 `sessionRunning` 的 `s.mu` 加回去 → 红。

// mutexSessionCallFunctions 扫出 manager 生产文件里"持 s.mu 且调域平面"的函数名（供报错用）。
func mutexSessionCallFunctions(t *testing.T, src string) []string {
	t.Helper()
	var out []string
	var cur string
	var locked bool
	var calls []string
	flush := func() {
		if cur != "" && locked && len(calls) > 0 {
			out = append(out, cur+" → "+strings.Join(dedupeStrings(calls), "/"))
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
		if serviceMutexLockRe.MatchString(line) {
			locked = true
		}
		for _, m := range domainPlaneCallRe.FindAllStringSubmatch(line, -1) {
			calls = append(calls, m[1]+"."+m[2])
		}
	}
	flush()
	return out
}

// domainPlaneNames 域平面的字段名——**判据对"所有域平面"成立，不只会话域**。
//
// 这条扩展本身是实测驱动的（2026-10-16，P4a）：原来只钉 `sessionPlane.`，而**同一个错的另一种
// 写法**在另外三个地方活得好好的——
//
//	provider_probe.go  probeProvider    持 s.mu 调 providerPlane.Snapshot（P4a 起该文件已搬进
//	                                    `app/model`，这条是**当时**的证据，留档）
//	model_list.go      requireProvider   持 s.mu 调 providerPlane.Exists
//	resource_sync.go   providerForSync   持 s.mu 调 providerPlane.Snapshot
//
// 三处**都只读域平面自己取锁后返回的副本**，manager 锁在语义上多余。判据只钉 `sessionPlane`
// 等于把"同一个错的另外三种写法"放过去——与 §3.1.53 把 `Snapshot()` 放宽到 `sessionPlane.*`
// 是同一条推理，这次再放宽到**所有域平面**。
var domainPlaneNames = []string{"sessionPlane", "providerPlane", "automationPlane", "planner", "runtimePlane", "providers"}

// serviceMutexLockRe 抓 **Service 自己那把锁**的加锁点。
//
// 必须是正则而不是 `strings.Contains(line, "s.mu.Lock()")`——后者会把 `as.mu.Lock()`
// （`*ActiveSession` 的锁）**当成** `s.mu.Lock()`：`a` 后面紧跟着 `s.mu.Lock()` 这个子串。
// 于是"函数里既持 as.mu 又调域平面"会被误报成锁序反转，而两者的锁根本不是同一把、也不构成
// 任何互指的边。实测抓到（2026-10-16，P0-2）：`executeRun` / `CancelRun` 只是在 `as.mu` 解锁
// **之后**发过程状态，判据却喊"持 s.mu 调域平面"。
//
// 与本节另外两条讲究同一条推理：**判据要对「名字长什么样」免疫**——写成子串匹配就等于假设
// 这个名字不会被别人当后缀。
var serviceMutexLockRe = regexp.MustCompile(`\b[sr]\.mu\.Lock\(\)`)

// domainPlaneCallRe 抓 `?.<域平面>.<动作>(` 的动作名（报错用）。
var domainPlaneCallRe = regexp.MustCompile(`(?:\.|\b)(` + strings.Join(domainPlaneNames, "|") + `)\.([A-Za-z_][A-Za-z0-9_]*)\(`)

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

// TestManagerDoesNotHoldMutexWhileCallingDomainPlane 见文件头：manager 不许持 `s.mu`
// 去调**任一域平面**（会话域 / 供应商域 / 自动化域 / 工作空间域 / 执行面）。
func TestManagerDoesNotHoldMutexWhileCallingDomainPlane(t *testing.T) {
	root := backendRoot(t)
	// P5a 起同步面搬出 manager，最后一个 `s.mu` 随之消失；判据的检查对象是**两个装配/同步面**
	// （任一处在锁内调域平面都算红色）：执行面 `app/run` 与资源同步面 `adapters/outbound/models`
	// （C3 4.5 后半：原 `registry.Resources` 按域拆到 models）。
	dirs := []string{
		filepath.Join(root, "internal", "app", "run"),
		filepath.Join(root, "internal", "adapters", "outbound", "models"),
	}
	var files []string
	for _, dir := range dirs {
		fs := productionGoFiles(t, dir)
		if len(fs) == 0 {
			t.Fatalf("%s 下没有生产 .go 文件：这条守卫失去了检查对象（目录改名/搬走了？）", dir)
		}
		files = append(files, fs...)
	}
	locked := 0
	for _, path := range files {
		src := stripLineComments(readFileOrFatal(t, path))
		if serviceMutexLockRe.MatchString(src) {
			locked++
		}
		for _, fn := range mutexSessionCallFunctions(t, src) {
			t.Errorf("%s 的 `%s` 持本结构的锁调**域平面**：这是**锁序反转**的雏形。\n"+
				"域平面会回调别的域（`SetWorkspaceDisplay` 这类端口），于是"+
				"\"manager 锁 → 域锁\" 与某条反向回调合成互指的边——表现是 `go test ./...`"+
				"**挂死**而不是红。\n"+
				"处置：把对域平面的调用提到锁**外面**（域平面的方法自己取锁并返回副本，"+
				"本来就不需要这把锁）。判据覆盖**所有域平面**（`domainPlaneNames`），不只会话域——"+
				"只钉一处等于把同一个错的其它写法放过去。",
				filepath.Base(path), fn)
		}
	}
	// 判据本身不许空转：两个装配/同步面里必须还有人在用本结构的锁（`models.Resources`
	// 保着同步状态）。一处都没有 = 锁被搬走/改名了，判据失锚。
	if locked == 0 {
		t.Fatal("internal/{app/run, adapters/outbound/models} 里没有任何 `s.mu.Lock()` / `r.mu.Lock()`：锁本身被搬走/改名了？" +
			"这条守卫的判据挂在它身上，失锚等于空转。")
	}
}
