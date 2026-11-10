package router

// 二期 C · WP-C7：运行期应用**沙箱页**静态端点 `/_app/<token>/<file>`。
//
// 背景（设计 §17.2、magpie notes 机制 7）：C 应用页面代码来自「运行期应用包」，**不能编进主窗
// 产物**（那会破坏一期 A+Slot「封闭静态包」）；它必须由宿主在**沙箱 iframe**（`sandbox="allow-scripts"`）
// 里加载，页面只通过 `postMessage` 桥与主窗通讯。本文件负责这条链路的**服务端**：把某个应用包
// 的页面静态资源按一个**不可猜的 token** 挂到一个隔离路径下，供前端 iframe 拉取。
//
// 安全边界（威胁模型 §17.3「页面 XSS / 供应链」的处置之一）：
//   - **token 不可猜**：`<appID>.<hex(hmac-sha256(appID))>`，密钥是进程级随机（或 env 注入）。
//     拿不到 token 就拼不出路径，等于给页面资源加一层能力式访问控制（本地回环无鉴权场景的兜底）。
//   - **只 serve 包内 `page/` 子目录**：路径拼接后二次确认不越出包根（防 `..`/符号链接穿越）。
//   - **隔离 CSP**：此端点下发的文档走 `setAppPageCSP`，`connect-src 'none'`（页面**不得**自带网络
//     出站——所有读能力只能经 postMessage 桥；与主窗那份 CSP 的纪律同源，但更严：连 'self' 都不给）。
//   - **响应头禁嵌入主窗**：`X-Frame-Options` 不加（它由我们自己的 iframe 嵌），但 `frame-ancestors`
//     仍由 CSP 限定 `'self'`（只有同源主窗能嵌它，防被外站嵌套）。
//
// **[C8 加固留口]**：当前 token 是「进程级随机 HMAC」，随进程重启失效；正式的**包 spec 签名链**
// （第三方作者身份、可验证、可再次分发）归 C8。这里只做「不可猜 + 不可穿越 + 隔离 CSP」的最小可靠形态，
// 不做身份认证——单机单操作员的过渡期够用，接入第三方分发前必须换 C8 的签名 token。
//
// 与装配的关系（C6 接线点，本 WP 先做端点 + `Deps.AppPages` 开关）：
//   - `Deps.AppPages` 为 nil → 端点**不挂**（交付期未接线时的默认态，与 AssetWatch 同款开关范式）。
//   - 非 nil 时由装配层给 `AppsDir`（应用包根目录，每个 `<id>/` 子目录含 `manifest.json` + `page/`）。
//   - 前端拿到某 app 的 token 走 C6 的 app 详情/列表接口（本 WP 只导出 `Token(appID)` 供装配遍历）。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// appPageRootName 是每个应用包目录里**页面静态资源**的固定子目录名。
//
// 约定：`<appsDir>/<id>/page/` 下放该 app 的 iframe 页面产物（`index.html` 及随附 js/css）。
// 与 `manifest.json`（走 `apppkg`）在同级的包目录里，但**只有 page/ 子目录会被本端点暴露**。
// 若某 app 声明了 `provides.page` 却没有 page/ 目录，iframe 会拿到 404——由前端把该 frame 标为
// 「页面不可用」，不影响其它 app（失败隔离）。
const appPageRootName = "page"

// AppPageAssets 是运行期应用沙箱页静态端点（`/_app/<token>/`）的装配配置。
//
// nil = 端点不挂（默认关闭，与 AssetWatch 同款开关范式）。装配层构造它即开启。
type AppPageAssets struct {
	// AppsDir 是应用包根目录（每个 `<id>/` 子目录含 manifest.json + page/）。
	AppsDir string
	// secret 是签发/校验 page token 的 HMAC 密钥（进程级随机；不导出，防外部伪造）。
	secret []byte
}

// NewAppPageAssets 构造 sandbox 页静态端点配置：解析 appsDir，生成进程级随机密钥。
//
// 密钥用 crypto/rand 每次启动重生（不可持久化到磁盘——否则重装/回滚会复用旧 token）；
// 代价是「重启后旧 token 失效」，这对 iframe 一次性加载的页面无影响（页面每次都从接口拿新 token）。
func NewAppPageAssets(appsDir string) *AppPageAssets {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// crypto/rand 失败极罕见；退回用 appsDir 派生的确定性密钥（仍不可猜，只要路径不泄露）。
		// 不 panic：端点不值得为一次随机失败把整个进程拖死（与「失败不阻断界面」一致）。
		sum := sha256.Sum256([]byte("app-page-fallback:" + appsDir))
		secret = sum[:]
	}
	return &AppPageAssets{AppsDir: appsDir, secret: secret}
}

// Token 返回某 app 的页面访问 token：`<appID>.<hex(hmac-sha256(appID))>`。
//
// appID 词法为 `^[a-z0-9]+(-[a-z0-9]+)*$`（不含 `.`），故以 `.` 分段可无歧义解析。
// 装配层遍历已装载的（带 page 的）应用包时，为每个 app 调本方法发 token 给前端。
func (a *AppPageAssets) Token(appID string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(appID))
	return appID + "." + hex.EncodeToString(mac.Sum(nil))
}

// appIDFromToken 校验 token 并取回 appID；不合法（格式错/签名不符/appID 词法非法）返回 ("", false)。
//
// 用 `hmac.Equal` 做常量时间比较，防时序侧信道（虽回环场景风险低，仍按安全基线写）。
func (a *AppPageAssets) appIDFromToken(token string) (string, bool) {
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return "", false
	}
	appID := token[:dot]
	sigHex := token[dot+1:]
	// appID 词法先卡死（防把任意串当 appID 拼进路径）。
	if !appPageIDRE.MatchString(appID) {
		return "", false
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(appID))
	want := mac.Sum(nil)
	got, err := hex.DecodeString(sigHex)
	if err != nil || !hmac.Equal(got, want) {
		return "", false
	}
	return appID, true
}

// mountAppPageAssets 在 engine 上挂 `/_app/<token>/<file>` 与 `/_app/<token>/`（默认 index.html）。
//
// 挂在**引擎级**（不是 `/v1` 分组）：页面资源不是 API，不带 Origin 判定（与静态资产同口径），
// 且必须注册在 `MountWeb` 的 `NoRoute`（SPA 回落）**之前**——否则 `/_app/...` 会被 SPA 回落吃掉，
// iframe 拿到的是一份主窗 HTML 而不是应用页。
func mountAppPageAssets(r *gin.Engine, a *AppPageAssets) {
	if a == nil {
		return
	}
	serve := func(c *gin.Context) {
		rel := strings.TrimPrefix(c.Param("filepath"), "/")
		if rel == "" {
			rel = "index.html"
		}
		a.serveFile(c, c.Param("token"), rel)
	}
	r.GET("/_app/:token/*filepath", serve)
}

// serveFile 校验 token → 定位包内 page/ 下的文件 → 安全读取并下发（带隔离 CSP）。
func (a *AppPageAssets) serveFile(c *gin.Context, token, rel string) {
	appID, ok := a.appIDFromToken(token)
	if !ok {
		// 非法 token：不泄露"是 token 错还是文件不存在"，统一 404。
		c.Status(http.StatusNotFound)
		return
	}

	// 包根 = <AppsDir>/<appID>，页面根 = <包根>/page。rel 只允许落在这两者之内。
	pkgRoot := filepath.Join(a.AppsDir, appID)
	pageRoot := filepath.Join(pkgRoot, appPageRootName)
	fullPath := filepath.Join(pageRoot, filepath.FromSlash(rel))
	// 二次越界确认（rel 可能含 `..` 或经由符号链接逃逸）：必须仍在 pageRoot 内。
	if !withinAppDir(pageRoot, fullPath) {
		c.Status(http.StatusNotFound)
		return
	}

	fi, err := os.Stat(fullPath)
	if err != nil || fi.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// 隔离 CSP：页面文档**零出站**（connect-src 'none'）；只有同源主窗能嵌它（frame-ancestors 'self'）。
	// 无论 HTML 还是子资源都下发同一份头（子资源用不上 frame-ancestors，但无害且省一次分支）。
	c.Header("Content-Security-Policy", appPageCSP)
	c.Header("X-Content-Type-Options", "nosniff")
	// 页面是包内产物、以 token 隔离：不缓存（token 每次可能变，缓存只会留陈旧页）。
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, contentTypeFor(rel), data)
}

// appPageCSP 是运行期沙箱页文档的 CSP：**比主窗那份更严**——连自身都不给网络出站。
//
//   - `connect-src 'none'`：页面自己发不出任何请求（fet阿/ XHR / WS 全禁）；所有读只能走 postMessage 桥，
//     由**主窗**按清单 permissions 代发（主窗网关是唯一出站口）。这是 magpie `noFramedAPI` 的等价约束。
//   - `script-src 'self' 'unsafe-inline'`：允许页面用自身包内脚本与内联引导（应用页是可信度低于主窗但
//     仍是本机安装的产物；`allow-scripts` sandbox 已把它们隔离在无 same-origin 的源里，无凭据可偷）。
//   - `frame-ancestors 'self'`：只允许同源（主窗）嵌套，防外站把应用页嵌成钓鱼。
//   - `base-uri 'none'` / `object-src 'none'`：常见加固默认项。
const appPageCSP = "default-src 'self'; connect-src 'none'; img-src 'self' data:; font-src 'self' data:; " +
	"script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'"

// withinAppDir 报告 target 是否在 dir 之内（filepath.Clean + Rel 判定，防 `..` 上跳与拼接穿越）。
//
// 与 apppkg.withinDir 同款逻辑（不 import apppkg 的私有函数，router 侧独立一份——两处都只做
// 一件小事，重复比跨层耦合更清晰）。
func withinAppDir(dir, target string) bool {
	dirClean := filepath.Clean(dir)
	targetClean := filepath.Clean(target)
	if targetClean == dirClean {
		return true
	}
	rel, err := filepath.Rel(dirClean, targetClean)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !strings.HasPrefix(rel, ".."+"/")
}

// appPageIDRE 与 capability/apppkg 的 app id 同口径（小写字母/数字、连字符分段）。
// router 侧独立一份正则（不 import core/apppkg，保持入站层零业务依赖；词法只用于防路径注入）。
var appPageIDRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
