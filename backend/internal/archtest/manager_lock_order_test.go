package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P2b 前置：「不许持 manager 的锁去请求会话快照」守卫 ———
//
// 背景（2026-10-16 实测）：`manager` 的 `s.mu` 一度**同时**保护三样东西——workspace 策略字段、
// memory 模式的默认工作空间、以及**会话快照的读顺序**。后者的形态是"持 `s.mu` 调
// `sessionPlane.Snapshot()`"（`workspace_registry.go` 的 `workspaceRespForMemDefault`、
// `workspace_view.go` 的 `workspaceUsage` 两处）。
//
// 那是**锁序反转**的雏形，而它的表现是 `go test ./...` **挂死**（不是红——见计划 §10.5.3 教训 1
// 的同一个家族）：会话域已经会回调 workspace 侧（`SetWorkspaceDisplay` 端口），一旦 workspace
// 的状态搬进域平面、域锁下再请求会话快照，就得到 `workspace.mu → session.mu`，与那条回调合成
// 互指的边。P2b 把 workspace 搬进 `domain/workspace` 之前必须先解开它，否则每一次搬迁都在把一条
// 潜死锁往深处埋。
//
// 判据（静态、可解析）：`internal/manager/**` 里任何**同一函数体内**既出现 `s.mu.Lock()`
// 又出现 `sessionPlane.Snapshot()` 的地方即红。写成"同一函数体"而不是"同一文件"是因为
// 同一个文件里两个不相干的函数各做各的并不构成锁序反转。
//
// fail-closed：扫描面为空即失败（目录改名/搬走会让守卫静默空转）。
//
// 反证（实跑）：把 `workspaceUsage` 里的 `s.mu.Lock()` 加回去 → 红。

// mutexSnapshotFunctions 扫出 manager 生产文件里"持 s.mu 且请求会话快照"的函数名（供报错用）。
//
// 实现取的是**够用即止**的形状：Go 的按行扫描 + 函数边界识别（`^func ` 起、`^}` 止）。
// 不去解析 AST 是因为 `internal/archtest` 一直用文本判据（`stripLineComments` 那一族），
// 引 AST 会给这条守卫带来它不需要的复杂度；而"持锁 + 快照同处一个函数体"这件事在文本上
// 没有歧义（两行之间不会有别的函数定义）。
func mutexSnapshotFunctions(t *testing.T, src string) []string {
	t.Helper()
	var out []string
	var cur string
	var locked, snap bool
	flush := func() {
		if cur != "" && locked && snap {
			out = append(out, cur)
		}
	}
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") {
			flush()
			cur, locked, snap = funcNameOf(trimmed), false, false
			continue
		}
		if trimmed == "}" {
			flush()
			cur, locked, snap = "", false, false
			continue
		}
		if strings.Contains(line, "s.mu.Lock()") {
			locked = true
		}
		if strings.Contains(line, "sessionPlane.Snapshot()") {
			snap = true
		}
	}
	flush()
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

// TestManagerDoesNotHoldMutexWhileReadingSessionSnapshot 见文件头：manager 不许持 `s.mu`
// 去请求会话快照。
func TestManagerDoesNotHoldMutexWhileReadingSessionSnapshot(t *testing.T) {
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
		for _, fn := range mutexSnapshotFunctions(t, src) {
			t.Errorf("internal/manager/%s 的 `%s` 持 `s.mu` 请求会话快照（`sessionPlane.Snapshot()`）："+
				"这是**锁序反转**的雏形。\n"+
				"会话域会回调 workspace 侧（`SetWorkspaceDisplay`），于是 workspace 状态搬进域平面后"+
				"就得到 `workspace.mu → session.mu` 与一条反向回调互指——表现是 `go test ./...`"+
				"**挂死**而不是红。\n"+
				"处置：把快照读提到锁**外面**（循环体只读快照与入参的，本来就不需要这把锁）。",
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
