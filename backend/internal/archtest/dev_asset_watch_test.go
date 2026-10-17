package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件钉住 P6-A（开发期资产重建通知，2026-10-11）的三条结构约束。
//
// 它解决什么：P1 取消"从源码现编译"的 dev server 之后（见 dev_parity_test.go），改 UI 要等
// `vite build --watch` 重建**再手动刷新**——功能没退化，开发体感退化了。P6-A 让核心盯着
// **它自己正在服务的那份 dist**，内容真变了就经 `GET /dev/assets/events`（SSE）推一帧，
// 页面收到即整页刷新。
//
// 这条机制的失效方式全部是**静默**的，所以各配一条机器判据：
//
//	① **开关忘了接**：配置解析出来了，路由却没注册（或反了）——页面永远不刷新，没有任何报错。
//	   判据取"注册点与开关判定必须同处 + 交付侧不得出现这个开关"，而不是"跑起来试试"。
//	② **通道悄悄进了交付形态**：桌面壳也传了这个开关，于是交付产物多一条设计与文档里都没有的
//	   端点，并且带一个永不停止的轮询循环。判据 = 桌面侧源码零命中（fail-closed：扫不到文件即红）。
//	③ **通知骑上业务事件流**：重建是**宿主内部事实**，`session.*` 事件 type 是冻结的对外契约
//	   （三处同步，见 AGENTS.md）。把重建塞进去会同时污染对外契约与语义。判据 = 页面半边不得
//	   引入事件流模块，且不得出现任何事件 type 字面量。
//
// 浏览器那半（EventSource → location.reload）由 `ui:accept` 与手工验收钉；Go 侧的行为判据在
// `internal/router/web_watch_test.go`（开关默认关闭 / 判据是内容摘要而不是 mtime / ctx 结束即停）。

// devAssetWatchFlag 开发期开关在 CLI/脚本侧的字面量（`--asset-watch`）。
const devAssetWatchFlag = "--asset-watch"

// devAssetWatchEnv 同一条开关在配置面的键名（`MATE_ASSET_WATCH` ↔ config 键 `asset_watch`）。
const devAssetWatchEnv = "MATE_ASSET_WATCH"

// TestDevAssetWatchIsOptIn 断言开关**只在开发形态打开**，且"开关"与"注册点"绑在一起。
//
// 五段判据（缺一段就留下一种静默失效）：
//
//	① `internal/router/router.go` 里注册 `/dev/assets/events` 的那段必须**被开关判定包着**
//	   （`o.AssetWatch != nil`）——分开写就会出现"配置说开着、路由没挂"；
//	② `frontend/scripts/dev-loop.mjs` 必须真的传这个开关（不传 = P6-A 白做，而且没有任何报错）；
//	③ `apps/desktop/src/**` 与壳的构建脚本里**不得**出现这个开关（交付形态不该有这条端点）；
//	④ 页面半边的 `EventSource` 只许出现在一个文件里，且 `bootstrap.tsx` 只调用一次
//	   （多一处就会有两套"收到帧就刷新"的逻辑，互相打架或反复刷新）；
//	⑤ 端点路径两侧逐字一致（Go 常量 ↔ TS 常量）——不一致的表现是"探测永远说通道不存在"。
//
// 反证：把 ① 的 `if o.AssetWatch != nil` 删掉（无条件注册）→ 红；把 ② 的 flag 删掉 → 红；
// 给桌面壳加这个开关 → 红；在别处再写一个 `new EventSource(` → 红。
func TestDevAssetWatchIsOptIn(t *testing.T) {
	router := readFrontendFile(t, filepath.Join(backendRoot(t), "internal", "router", "router.go"))

	// ① 注册点与开关同处：从注册语句往上找最近的一段，必须能看到开关判定。
	registerAt := strings.Index(router, "o.AssetWatch.Mount(r)")
	if registerAt < 0 {
		t.Fatalf("router.New 里找不到 `o.AssetWatch.Mount(r)`——注册点改名/搬迁会让这条守卫静默空转，" +
			"也意味着 P6-A 的端点没有挂上（页面永不刷新且无人报错）")
	}
	// 取注册点往前 400 字节：足够覆盖紧邻的 if 判定与该段注释，又不至于把别处的条件算进来。
	window := router[max(0, registerAt-400):registerAt]
	if !strings.Contains(window, "o.AssetWatch != nil") {
		t.Error("注册 `/dev/assets/events` 的那句没有被 `o.AssetWatch != nil` 包着——" +
			"开关与注册点必须同处：分开写就会出现「配置说开着、路由没挂」这种无声的 no-op")
	}

	// ② 开发循环必须**真的把它交给核心**（不传 = 白做）。
	//
	// 判据要看"有没有这句参数"，不是"文件里有没有这个字符串"：文件头注释里就写着这个开关名，
	// 拿裸 `strings.Contains` 判会被注释喂绿（本守卫第一版正是这么假绿的，反证才露出来）。
	// 所以剥注释、并且要求它出现在 `coreArgs.push(` 里。
	devLoop := stripTSComments(readFrontendFile(t, filepath.Join(frontendRoot(t), "scripts", "dev-loop.mjs")))
	push := regexp.MustCompile(`coreArgs\.push\(\s*['"]` + regexp.QuoteMeta(devAssetWatchFlag) + `['"]`).FindString(devLoop)
	if push == "" {
		t.Errorf("scripts/dev-loop.mjs 没有把 %s 交给核心（期望 `coreArgs.push('%s', …)`）——"+
			"P6-A 的全部价值都在这个开关上，不传等于白做，而且没有任何报错", devAssetWatchFlag, devAssetWatchFlag)
	}

	// ③ 交付侧零命中（桌面壳 + 它的构建脚本）。
	desktopRoot := filepath.Join(frontendRoot(t), "apps", "desktop")
	desktopFiles := listFrontendSources(t, filepath.Join(desktopRoot, "src"))
	desktopFiles = append(desktopFiles, listFrontendSources(t, filepath.Join(desktopRoot, "scripts"))...)
	if len(desktopFiles) == 0 {
		t.Fatal("apps/desktop 下扫不到任何源码——路径变了，这条守卫会静默变绿")
	}
	for _, f := range desktopFiles {
		body := stripTSComments(readFrontendFile(t, f))
		if strings.Contains(body, devAssetWatchFlag) || strings.Contains(body, devAssetWatchEnv) {
			rel, _ := filepath.Rel(frontendRoot(t), f)
			t.Errorf("交付侧 %s 出现了 %s/%s——桌面壳与打包脚本都不该开这条通道："+
				"交付形态没有 watcher 在重写资产，开着只是白跑一个轮询循环 + 多一条端点",
				rel, devAssetWatchFlag, devAssetWatchEnv)
		}
	}

	// ④ 页面半边只有一个订阅点，且从 bootstrap 调一次。
	eventSourceUse := regexp.MustCompile(`new\s+EventSource\s*\(`)
	var owners []string
	for _, f := range listUISources(t) {
		if eventSourceUse.MatchString(stripTSComments(readFrontendFile(t, f))) {
			rel, _ := filepath.Rel(frontendRoot(t), f)
			owners = append(owners, filepath.ToSlash(rel))
		}
	}
	if len(owners) != 1 || !strings.HasSuffix(owners[0], "packages/console/src/transport/assetWatch.ts") {
		t.Errorf("`new EventSource(` 应当只出现在 packages/console/src/transport/assetWatch.ts，实际出现在 %v——"+
			"多一处就会有两套「收到帧就刷新」的逻辑", owners)
	}
	bootstrap := stripTSComments(readFrontendFile(t,
		filepath.Join(frontendRoot(t), "packages", "console", "src", "bootstrap.tsx")))
	if n := strings.Count(bootstrap, "watchDevAssets()"); n != 1 {
		t.Errorf("bootstrap.tsx 里 `watchDevAssets()` 出现 %d 次，期望恰好 1 次（订阅点必须唯一）", n)
	}

	// ⑤ 端点路径两侧逐字一致。
	goPath := regexp.MustCompile(`AssetWatchPath\s*=\s*"([^"]+)"`).
		FindStringSubmatch(readFrontendFile(t, filepath.Join(backendRoot(t), "internal", "router", "web_watch.go")))
	tsPath := regexp.MustCompile(`ASSET_WATCH_PATH\s*=\s*'([^']+)'`).
		FindStringSubmatch(readFrontendFile(t,
			filepath.Join(frontendRoot(t), "packages", "console", "src", "transport", "assetWatch.ts")))
	if goPath == nil || tsPath == nil {
		t.Fatalf("解析不到端点常量（Go: %v / TS: %v）——改名会让这条判据静默失效", goPath, tsPath)
	}
	if goPath[1] != tsPath[1] {
		t.Errorf("端点路径两侧不一致：Go %q / TS %q——不一致的表现是「探测永远说通道不存在」", goPath[1], tsPath[1])
	}
}

// TestDevAssetWatchKeepsEventStreamsSeparate 断言重建通知**不骑业务事件流**。
//
// 为什么单列一条：这是本仓最容易"顺手做错"的地方——业务事件流（WS `/ws/events` + `session.*`
// 事件 type）是冻结的对外契约，三处手工同步（内核 `events.go` 的 `Bus*` 常量、README/文档、
// 前端 `@ark-mate/types` 的 `Ev`），且它的凭据走带内首帧 attach。重建是**宿主内部事实**，
// 不是会话事件：塞进去会同时污染对外契约与语义（参照实现 DSH 也把它放在独立的 `/plugins/events`）。
//
// 判据（页面半边）：
//
//	① 不得引入事件流模块（`transport/ws`）或事件类型包（`@ark-mate/types` 的 events）；
//	② 不得出现任何 `session.` 事件 type 字面量。
//
// 反证：在 assetWatch.ts 里 `import { ws } from './ws'` → 红；写一个 `'session.created'` → 红。
func TestDevAssetWatchKeepsEventStreamsSeparate(t *testing.T) {
	path := filepath.Join(frontendRoot(t), "packages", "console", "src", "transport", "assetWatch.ts")
	body := stripTSComments(readFrontendFile(t, path))

	for _, forbidden := range []string{"./ws'", `"./ws"`, "@ark-mate/types"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("页面半边引入了 %s——重建通知必须与业务事件流分线："+
				"事件 type 是冻结的对外契约（内部事实不该塞进去），凭据与语义也都不搭", forbidden)
		}
	}
	if m := regexp.MustCompile(`['"]session\.[a-z_]+['"]`).FindString(body); m != "" {
		t.Errorf("页面半边出现事件 type 字面量 %s——重建帧不是会话事件", m)
	}
	// 正向：必须真的是 SSE（EventSource），而不是被"顺手"改成复用一个已有连接。
	if !strings.Contains(body, "new EventSource(") {
		t.Error("页面半边没有用 EventSource——若改成别的方式取重建事实，请同步这条守卫与设计文档（§4.5）")
	}
	// 反向：Go 侧那条路由也不该挂在 v1 契约组上（它不进 README 路由表、不受 REST 凭据约束）。
	watchGo := readFrontendFile(t, filepath.Join(backendRoot(t), "internal", "router", "web_watch.go"))
	if !strings.Contains(watchGo, `AssetWatchPath = "/dev/`) {
		t.Errorf("端点路径不在 /dev/ 面下——请确认它没有被挂进 /v1/mate 契约组（那会让它变成一条要写进 README 的 API）")
	}
}
