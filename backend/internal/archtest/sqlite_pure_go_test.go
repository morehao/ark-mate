package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是「SQLite 驱动保持纯 Go，构建面保持 CGO_ENABLED=0」的静态守卫（2026-10-12，
// golib v1.32.22 的去 CGO 迁移）。
//
// 为什么要机器判据：这条链的失效方式是**编译期无感、运行期致命**，而且历史上真发生过两次
// 方向相反的事故——
//
//	① 2026-10-10：构建面写 `CGO_ENABLED=0`，而当时的 golib dialect 是 CGO 的
//	   mattn/go-sqlite3。0 之下编译照样过，注册的却是 stub，**连库时**才报
//	   `go-sqlite3 requires cgo to work. This is a stub`——桌面核一启动就撞墙；
//	② 2026-10-11：四处构建面改成 1（并接受动态链接 libc/glibc 的代价）。
//
// v1.32.22 的 dialect 换成纯 Go 的 ncruces/go-sqlite3（SQLite 编成 wasm 再翻译回 Go），
// 于是 0 重新是正确取值：不需要 C 工具链、产物全静态、交叉编译不必先有 C 交叉工具链。
// 但"0 安全"**完全依赖**于依赖图里没有 CGO 驱动——谁把 golib 降级回 v1.32.21、或直接
// 空引入 `gorm.io/driver/sqlite`，就重新落回事故 ①，而 `go build` 不会报任何错。
//
// 两侧一起钉：依赖侧不得出现 CGO 的 SQLite 驱动（并正向断言纯 Go 那套真的在），
// 构建侧四个面一律 `CGO_ENABLED=0`。任一侧单独看都不足以判断（0 + CGO 驱动会死；
// 1 + 纯 Go 驱动只是丢掉静态产物，但会静默回到"每个目标平台都要有 C 工具链"那条老路）。

// cgoSQLiteDrivers 是**禁止出现在主模块依赖图里**的 CGO SQLite 驱动模块路径。
//
// 为什么用"模块路径 + 后面跟空格"匹配而不是子串：`github.com/mattn/go-isatty` 与
// `github.com/mattn/go-sqlite3` 前缀相同，子串匹配会把前者误判成后者。
var cgoSQLiteDrivers = []string{
	"github.com/mattn/go-sqlite3",
	"gorm.io/driver/sqlite",
}

// pureGoSQLiteMarkers 是"纯 Go dialect 仍在"的正向证据（缺即说明依赖被换掉了，fail-closed）。
var pureGoSQLiteMarkers = []string{
	"github.com/morehao/golib",
	"github.com/ncruces/go-sqlite3",
}

// cgoPinnedSurfaces 是必须把 CGO_ENABLED 钉成 0 的四个构建面（相对仓库根）。
//
// 这四处就是 2026-10-11 那次"四处一律 1"的原班人马：桌面核、容器构建阶段、两份会自己
// 重建桌面核的验收脚本。漏掉任何一处都不会让别的测试变红——那正是要钉的原因。
var cgoPinnedSurfaces = []struct {
	rel  string
	want *regexp.Regexp
}{
	{"Makefile", regexp.MustCompile(`CGO_ENABLED=0\s+go build`)},
	{"Dockerfile", regexp.MustCompile(`(?m)^\s*CGO_ENABLED=0\s*$`)},
	{"frontend/scripts/desktop-no-host-acceptance.mjs", regexp.MustCompile(`CGO_ENABLED:\s*'0'`)},
	{"frontend/scripts/desktop-skill-projection-acceptance.mjs", regexp.MustCompile(`CGO_ENABLED:\s*'0'`)},
}

// reCGOEnabledOne 命中"把 CGO_ENABLED 钉成 1"的取值（`CGO_ENABLED=1` / `CGO_ENABLED: '1'`）。
//
// `1\b` 而不是裸 `1`：RE2 没有前瞻，裸 1 会把 `CGO_ENABLED=10` 也判成 1。
var reCGOEnabledOne = regexp.MustCompile(`CGO_ENABLED\s*[:=]\s*['"]?1\b`)

// TestSqliteDriverStaysPureGo SQLite 驱动必须是纯 Go 的，且四个构建面一律 CGO_ENABLED=0。
func TestSqliteDriverStaysPureGo(t *testing.T) {
	backend := backendRoot(t)
	repo := filepath.Dir(backend)

	// —— 依赖侧 ——
	gomod := readFileOrFatal(t, filepath.Join(backend, "go.mod"))
	for _, mod := range cgoSQLiteDrivers {
		// 匹配"模块路径作为独立记号出现"：既覆盖 require 块里的独占一行
		// （`\tgithub.com/mattn/go-sqlite3 v1.14.22 // indirect`），也覆盖单行
		// `require gorm.io/driver/sqlite v1.6.0`。所以前缀是"行首或空白"而不是 `^\s*`。
		re := regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(mod) + `\s`)
		if re.MatchString(gomod) {
			t.Fatalf("backend/go.mod 里出现了 CGO 的 SQLite 驱动 %q：\n\n"+
				"golib v1.32.22 起 SQLite dialect 是纯 Go 的 ncruces/go-sqlite3，本仓的四个构建面\n"+
				"都写 CGO_ENABLED=0；CGO 驱动在 0 之下**编译通过、连库即死**\n"+
				"（`go-sqlite3 requires cgo to work. This is a stub`）。\n"+
				"要么把 golib 升回 >= v1.32.22，要么把四个构建面一起改成 1（并接受动态链接 libc）。",
				mod)
		}
	}
	for _, marker := range pureGoSQLiteMarkers {
		if !strings.Contains(gomod, marker) {
			t.Fatalf("backend/go.mod 里找不到 %q：本守卫的正向前提没了（依赖被换成了别的\n"+
				"SQLite 方案？）。换驱动是可评估的事，但必须同时改这条守卫与\n"+
				"internal/infra/dbclient 的行为守卫，不能让它静默空转。", marker)
		}
	}
	// 驱动确实是从 internal/infra/dbclient 空引入的（守卫的对象没被搬走）。
	gormSrc := readFileOrFatal(t, filepath.Join(backend, "internal", "infra", "dbclient", "gorm.go"))
	if !strings.Contains(gormSrc, `_ "github.com/morehao/golib/dbaccess/dbgorm/driver/sqlite"`) {
		t.Fatalf("internal/infra/dbclient/gorm.go 里没有空引入 golib 的 SQLite driver：" +
			"SQLite dialector 的落点变了，本守卫的对象就没了（fail-closed）")
	}

	// —— 构建侧 ——
	for _, surface := range cgoPinnedSurfaces {
		path := filepath.Join(repo, filepath.FromSlash(surface.rel))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到构建面 %s：%v（路径改了请同步 cgoPinnedSurfaces）", surface.rel, err)
		}
		src := stripSurfaceComments(string(raw))
		if reCGOEnabledOne.MatchString(src) {
			t.Errorf("%s 把 CGO_ENABLED 钉成了 1：纯 Go 的 SQLite dialect 不需要 CGO，"+
				"1 只会让产物动态链接 libc（并且要求构建机有 C 工具链）", surface.rel)
		}
		if !surface.want.MatchString(src) {
			t.Errorf("%s 里找不到 %s：四个构建面必须一律 CGO_ENABLED=0（漏一处就是\n"+
				"「桌面核与容器核跑在不同的链接形态上」，而这件事没有任何别的测试会红）",
				surface.rel, surface.want.String())
		}
	}
}

// stripSurfaceComments 剥掉注释后再做判据：TS 注释（`//` 与 `/* */`，两份 .mjs 走这条）
// 与整行 `#`/`@#` 注释（Makefile、Dockerfile 走这条）。
//
// 不剥的话，两种假红都会出现：Makefile 里 `@# CGO_ENABLED=1 会...` 这样的历史说明，
// 以及 Dockerfile 里同样写着历史取值的注释行。
func stripSurfaceComments(src string) string {
	lines := strings.Split(stripTSComments(src), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
