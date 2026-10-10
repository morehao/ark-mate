package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// 资产层（M2-1，设计 §6.1 / §6.2）：**唯一来源 = 调用方显式给的目录**。
//
// 这里的历史有两层：最早是 `web_release.go` / `web_dev.go` 的构建 tag 分叉（"有没有资产"由 tag
// 决定，于是默认构建只能到一个"能调 API 但打不开界面"的服务）；M2-1 改成运行时解
// （显式目录 > 内嵌 > 拒绝启动）。**2026-10-10 起内嵌整条路删掉**（`//go:embed`、`-tags release`、
// `pkg/web` 一并消失）：核心**永远不自带前端资产**，资产是一份 `apps/web/dist`，由调用方
// （桌面壳 / 容器 / `make dev`）用 `--web-dir` 指过来。这样"资产在哪"只剩一个答案，而
// "只有一份 dist"（设计 §6.1 的红线）从"靠纪律"变成结构上不可能违反。

// 前端构建期写进产物的构建 id 文件（M2-4）与读不到时的口径。
//
// 这三个常量与前端契约（`@ark-mate/desktop-contract` 的 `UNKNOWN_BUILD_ID`）是**同一条契约的两侧**，
// 由 archtest 守卫逐字比对（`TestBuildIDUnknownMatchesContract`）——两边各写一个 'unknown'
// 但拼写不同的话，闸门会从"无法校验"变成"不一致"，把用户挡在整屏状态页外面。
const (
	// BuildIDFileName 前端构建写出的文件名（`apps/web/vite.config.ts` 的 writeBundle）。
	BuildIDFileName = "build-id.txt"
	// BuildIDUnknown 读不到构建 id 时的口径（前端契约里的同名字面量）。
	BuildIDUnknown = "unknown"
)

// AssetInfo 已解析的前端资产。零值（FS == nil）= 没有资产（只有测试会走到）。
type AssetInfo struct {
	// Path 资产目录的**绝对路径**（本仓从 2026-10-10 起只有目录这一种来源）。
	Path string
	// FS 资产根（内含 index.html 与 assets/）。
	FS fs.FS
	// BuildID 前端构建 id（M2-4 填；从 build-id.txt 读）。
	BuildID string
	// Digests 逐文件 sha256（rel path → hex），解析期算一次。
	// 两个用途：静态响应的**内容 ETag**（mtime 不可靠：解包/COPY 时间因部署方式而异），
	// 以及 /meta 的 webDigest 合成（M2-3）。
	Digests map[string]string
	// Digest 逐文件摘要的确定性合成（`sha256:<hex>`；排序后按 `rel\0hex\n` 拼接再哈希）。
	Digest string
}

// Has 报告是否真的拿到了资产。
func (a AssetInfo) Has() bool { return a.FS != nil }

// ResolveAssets 解析前端资产目录（设计 §6.1）。**没有目录即报错**（调用方按退出码 3 收尾）：
//
//   - `webDir` 必须绝对、存在、是目录、且含 index.html。
//
// 为什么"配错就拒启"而不是回落到别的来源：本仓从 2026-10-10 起**没有第二来源可回落**
// （内嵌已删）。显式目录配错时必须**拒绝启动**，而不是起一个"能调 API 但打不开界面"的服务——
// 后者表现为"用户看到旧界面，还以为是新版本没生效"，是最难排查的一类问题。
func ResolveAssets(webDir string) (AssetInfo, error) {
	dir := strings.TrimSpace(webDir)
	if dir == "" {
		return AssetInfo{}, fmt.Errorf(
			"没有前端资产：必须用 --web-dir 指向一份 dist（绝对路径）\n" +
				"  桌面形态：壳会传 --web-dir 指向包内那一份\n" +
				"  自建/容器：把 apps/web/dist 放到任意目录，用 --web-dir 或 MATE_WEB_DIR 指过去")
	}

	if !filepath.IsAbs(dir) {
		return AssetInfo{}, fmt.Errorf("--web-dir 必须是绝对路径，得到 %q", dir)
	}
	abs := filepath.Clean(dir)
	fi, err := os.Stat(abs)
	if err != nil {
		return AssetInfo{}, fmt.Errorf("--web-dir 指向的目录不可用：%s（%v）", abs, err)
	}
	if !fi.IsDir() {
		return AssetInfo{}, fmt.Errorf("--web-dir 不是目录：%s", abs)
	}
	index := filepath.Join(abs, "index.html")
	if _, err := os.Stat(index); err != nil {
		return AssetInfo{}, fmt.Errorf("--web-dir 目录里没有 index.html：%s（%v）", index, err)
	}
	return withDigests(AssetInfo{Path: abs, FS: os.DirFS(abs)})
}

// withDigests 解析期算一遍逐文件 sha256 与合成摘要。
//
// 为什么在解析期而不是每次请求：目录资产的 mtime 不可靠（部署方式决定），**内容摘要**才是可靠
// 的条件请求依据；而逐请求哈希 1.3MB 的 chunk 纯属浪费（单操作员回环服务也不该这么花）。
// 走查失败（理论上不会：ResolveAssets 已校验 index.html）时返回 Info 但不带摘要——
// 让"没有 ETag"退化成普通的可缓存响应，而不是让服务起不来。
func withDigests(info AssetInfo) (AssetInfo, error) {
	digests := map[string]string{}
	err := fs.WalkDir(info.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := fs.ReadFile(info.FS, path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		digests[path] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return info, nil
	}
	info.Digests = digests
	info.Digest = CompositeDigest(digests)
	info.BuildID = readBuildID(info.FS)
	return info, nil
}

// CurrentBuildID 现场读一次这份资产里的构建 id（`/meta` 用）。
//
// 为什么不直接用解析期那个快照：这个字段的**职责就是发现"资产被换了"**（版本闸门的全部意义）。
// 把它缓存成启动瞬间的值，等于把判据变成"启动那一刻两边一样吗"——部署者换掉 dist 之后
// 页面会立刻发现（页面 bundle 是新的），而核心还在报旧值，表现成"永远无法复现的一次报错"。
// 摘要（Digest）仍是解析期算的：重算整棵树的代价大，而"换文件"这一场景由本字段兜住。
func (a AssetInfo) CurrentBuildID() string {
	if !a.Has() {
		return ""
	}
	b, err := fs.ReadFile(a.FS, BuildIDFileName)
	if err != nil {
		return a.BuildID // 文件被删了：退回启动时的快照，不让 /meta 忽然没有这个字段
	}
	if id := strings.TrimSpace(string(b)); id != "" {
		return id
	}
	return a.BuildID
}

// readBuildID 从**这份**资产里读构建 id（解析期，兼作启动告警；M2-4，设计 §6.7）。
//
// 读不到不是错误：手工放的 dist、旧构建、第三方部署都可能没有这个文件。这时报 `unknown`
// 并记一条警告——版本闸门据此判"无法校验"（放行），而不是判"不一致"（拦下）。
// 这也正是闸门能抓混搭的原理：页面里的常量来自编译期，这个值来自**核心此刻实际服务的那份目录**。
func readBuildID(fsys fs.FS) string {
	b, err := fs.ReadFile(fsys, BuildIDFileName)
	if err != nil {
		slog.Warn("资产里没有 build-id.txt，前端与核心的版本一致性将无法校验（M2-4）"+
			"：若是自己拼的 dist，请由前端构建产出该文件", "file", BuildIDFileName, "err", err)
		return BuildIDUnknown
	}
	if id := strings.TrimSpace(string(b)); id != "" {
		return id
	}
	slog.Warn("build-id.txt 是空的，按 unknown 处理", "file", BuildIDFileName)
	return BuildIDUnknown
}

// CompositeDigest 逐文件摘要的确定性合成（C9 的"可复现"要求）：
// 按路径排序，逐行 `rel\0hex\n` 拼接后再 sha256。不含时间戳、不含绝对路径，
// 因此同一份产物在**不同部署位置**下得到同一个值（M2-5 的资产一致性断言比它：
// 壳侧与核心侧读同一个目录，但走的实现不同）。
//
// 导出（M4-5）是因为它不再只服务资产：`mate doctor --verify-payload` 要用**同一个**函数
// 复算随包载荷树的合成摘要。载荷清单是 JS 侧（`lib/payload.mjs` 的 `compositeDigest`）算的，
// 三处（Go 资产 / Go 载荷 / JS 载荷）必须是同一套算法——不一致的典型后果是"门禁永远红"
// 或更坏"永远绿"（两边各自算了个自洽的错值）。
func CompositeDigest(digests map[string]string) string {
	if len(digests) == 0 {
		return ""
	}
	paths := make([]string, 0, len(digests))
	for p := range digests {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%s\n", p, digests[p])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// MountWeb 把已解析的资产挂到引擎上（设计 §6.2）。
//
// 路由分工：
//   - `GET /` → index.html；
//   - 命中存在的静态文件 → 原样返回（**带未来缓存头**，M2-2 统一）；
//   - 其余非 API 路径 → 回落 index.html（SPA 深链）；
//   - `/v1/*` 未注册路径 → 标准信封 404（**不回落**：把 API 404 变成 HTML 会让前端把
//     HTML 当 JSON 解析，报出与真实原因无关的错误）。
//
// 无资产时只注册信封 404 的 NoRoute（测试与"仅供 API"的进程会走到这里）。
func MountWeb(r *gin.Engine, assets AssetInfo) {
	if !assets.Has() {
		r.NoRoute(writeNotFound)
		return
	}
	index, err := fs.ReadFile(assets.FS, "index.html")
	if err != nil {
		// ResolveAssets 已经校验过 index.html 存在；这里再兜一次是因为 FS 也可能被换掉。
		panic(fmt.Sprintf("资产里读不到 index.html（目录 %s）：%v", assets.Path, err))
	}
	indexResp := func(c *gin.Context) {
		// 浏览器入口的文档也下发 CSP（与桌面壳同一个头，M1-5）：前端产物不得依赖 eval，
		// 也不允许任何外域连接——这条与"R3 零出站"是同一条纪律的两面。
		setDocumentCSP(c)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", cacheControlFor("/index.html"))
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write(index)
	}
	r.GET("/", indexResp)
	r.NoRoute(func(c *gin.Context) {
		if isAPIPath(c.Request.URL.Path) {
			writeNotFound(c)
			return
		}
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if p == "index.html" {
			indexResp(c)
			return
		}
		if p != "" && !strings.HasSuffix(p, "/") {
			if fi, err := fs.Stat(assets.FS, p); err == nil && !fi.IsDir() {
				serveStaticFile(c, assets, p)
				return
			}
		}
		// SPA 深链：前端路由（/settings、/conversations/<id>…）由页面自己解析。
		indexResp(c)
	})
}

// serveStaticFile 服务一个**存在的**静态文件：内容 ETag + 条件请求 304 + 三档缓存头。
//
// 为什么不用 `c.FileFromFS`：它背后是 http.ServeContent，条件请求只认 mtime——而目录资产的
// mtime 因部署方式而异（解包、COPY、rsync 各不同），同一份内容能给出不同 Last-Modified，
// 正是设计 §6.2 要避免的"两侧行为不一致"。改用**内容摘要**做 ETag：位置无关，且强校验
// （内容变了 ETag 必变）。
//
// 不设 Last-Modified：位置相关的 mtime 只会误导缓存；有强 ETag 就够了。
func serveStaticFile(c *gin.Context, assets AssetInfo, rel string) {
	data, err := fs.ReadFile(assets.FS, rel)
	if err != nil {
		writeNotFound(c)
		return
	}
	etag := assets.etagFor(rel, data)
	c.Header("ETag", etag)
	c.Header("Cache-Control", cacheControlFor("/"+rel))
	if ifNoneMatch(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, contentTypeFor(rel), data)
}

// contentTypeFor 静态响应的 Content-Type。
//
// Go 的 mime 表按系统与版本而异（`.webmanifest` 在 darwin 上返回空串），而壳侧用
// `net.fetch(file://…)` 走的是 Chromium 的映射（`application/manifest+json`）——
// 两侧不一致会被 asset-parity（M2-5）抓到。这里把**产物里真实出现**的扩展显式钉死，
// 其余仍交给 mime 表 + octet-stream 兜底。
// 导出给 archtest 与前端契约逐字比对（TestAssetContentTypesMatchContract）。
var MimeOverrides = map[string]string{
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".js":          "text/javascript",
	".mjs":         "text/javascript",
	".css":         "text/css",
	".html":        "text/html; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
}

func contentTypeFor(rel string) string {
	ext := strings.ToLower(path.Ext(rel))
	if ct, ok := MimeOverrides[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// etagFor 取该文件的内容 ETag（解析期已算过；没有摘要时按需现算，保证行为不变）。
func (a AssetInfo) etagFor(rel string, data []byte) string {
	sum, ok := a.Digests[rel]
	if !ok {
		s := sha256.Sum256(data)
		sum = hex.EncodeToString(s[:])
	}
	return `"` + sum + `"`
}

// ifNoneMatch 判定 If-None-Match 是否命中该 ETag（支持 `*` 与逗号分隔的多值、弱比较前缀 W/）。
func ifNoneMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	if strings.TrimSpace(header) == "*" {
		return true
	}
	for _, part := range strings.Split(header, ",") {
		v := strings.TrimSpace(part)
		v = strings.TrimPrefix(v, "W/")
		if v == etag {
			return true
		}
	}
	return false
}

// cacheControlFor 缓存语义（设计 §6.2；M2-2 统一两侧）：
//
//   - `index.html`（含 `/`）→ `no-store`：它引用带内容 hash 的 assets，必须每次拿新的，
//     否则发版后页面还在引用旧 chunk；
//   - `/assets/*` → `immutable` 一年：文件名含内容 hash，内容变了文件名就变；
//   - 其余（favicon / manifest / 其它）→ `no-cache`（可缓存但每次revalidate）。
func cacheControlFor(path string) string {
	switch {
	case path == "/" || path == "/index.html":
		return "no-store"
	case strings.HasPrefix(path, "/assets/"):
		return "public, immutable, max-age=31536000"
	default:
		return "no-cache"
	}
}
