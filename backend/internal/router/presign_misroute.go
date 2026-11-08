package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// presignMisrouteGuard 兜住「前端把取字节的预签名请求打错了后端」这一误路由。
//
// 背景：控制面（mate 核心）的签名字节通路只挂在**核心 origin**（`/v1/mate/files/:id/presign-url`，
// 由 golib ginupload 挂上）。前端预览取字节时若把请求打到了别的宿主（例如 DSH Web 的 62005 端口），
// 路径会变成 `/v1/files/<id>/presign-url`（丢了 `/mate` 段）或 `/files/<id>/presign-url`——
// 这些路径在本引擎里**根本没有对应路由**，于是落进 NoRoute，被当成一句
// "接口不存在: POST /v1/files/..." 的 404。那句 404 本身没错，但对排查的人是**误导**：真正的问题
// 不是"接口不存在"，而是"打错了后端"。
//
// 本守卫把这类请求改成 **HTTP 405 + 标准信封**，msg 明确指向"改用核心 origin"，让排查一步到位。
//
// # 为什么挂在引擎级中间件（r.Use）上，而不是 v1 组的普通中间件
//
// `gin` 对**未命中**任何路由的请求只跑 `engine.allNoRoute`（其组成 = 引擎根 `Use` 挂的中间件 +
// `NoRoute` 处理器），**不跑** v1 组 `v1.Use` 挂的中间件（实测：v1 组中间件在
// `/v1/files/x/presign-url` 上根本不执行，见 presign_misroute_test.go 的反证）。被误路由的路径
// 恰恰就是"未命中"的那一类——所以守卫必须挂在**引擎级** `r.Use`，才能进入 NoRoute 链、在请求变成
// 404 之前先截下来。
//
// 时序要点：gin 在**注册路由那一刻**就把当次 `group.Handlers` 快照进该路由的处理器链。因此本守卫
// 在全部路由注册完（含 ginupload.Register）之后才 `r.Use`——这样既有**已注册路由的链里没有它**
// （真实请求原样直达 handler，天然"放行"，不会平白多一跳），而 `rebuild404Handlers` 在 `Use` 时
// 重算 NoRoute 链，把守卫带进去。一句话：守卫只出现在"未命中"的链上，命中请求零改动。
//
// # 为什么不用 `RouterGroup.Match` 判"真实是否命中"
//
// `gin` 的 `RouterGroup.Match` **不是匹配器**，它是"注册一组方法路由"的别名（内部就是 `group.handle`，
// 见 gin v1.12 routergroup.go）。拿它去"判命中"会真的往路由表里插一条路由，重复注册直接 panic。
// 真正的"这条路有没有被真实路由接住"由 `engine.Routes()` 给出（注册期快照：方法 + 含 `:param`/
// `*wildcard` 的完整路径），本文件用一个轻量段匹配器对它逐条比对。
//
// # 放行口径（fail-closed 的反面：该放行的一个都不能拦）
//
//   - 请求不是 `POST` → 不归本守卫管，`c.Next()` 交回后续链。
//   - 路径不以 `/presign-url` 结尾 → 不归本守卫管，`c.Next()`。
//   - 路径以 `/v1/mate/` 开头 → **核心 origin 形态**，即便此刻没命中真实路由也不是"打错后端"，
//     `c.Next()` 交回原 NoRoute（保持既有 404 口径，不误报成 405）。
//   - 路径能被 `engine.Routes()` 里某条真实路由以同方法接住 → 真实存在，`c.Next()`（防御性分支：
//     命中的请求本就不进 NoRoute；也是本守卫的双向反证点）。
//   - 其余（如 `/v1/files/<id>/presign-url`、`/files/<id>/presign-url`）→ **405 + 信封**并 abort。
//
// # 为什么用 code.NotFound(40401)
//
// 语义上这仍是"路由没命中"（HTTP 404 类），不是业务拒绝；用 40401 与 notfound.go 的 writeNotFound
// 口径一致，比 40501（NotSupported，业务语义"能力不支持"）更贴切。不新造码。
//
// `rest` 参数保留是为了让调用点读起来和注册顺序对齐（守卫挂在 ginupload.Register 之后），
// 但真正的判据来自引擎路由表——见上。未挂 `/files`（memory 模式）时它同样照常守（rest 为 nil 也不影响）。
func guardPresignMisroute(r *gin.Engine, rest *gin.RouterGroup) {
	_ = rest // 判据来自引擎路由表；保留入参只为对齐调用点语义（见函数注释）。

	// 引擎级中间件：只在 NoRoute 链上真正"接住"未命中的请求；已注册路由的链在注册时已快照，
	// 不含本处理器，命中请求零改动（见函数注释「时序要点」）。
	r.Use(func(c *gin.Context) {
		if isPresignMisroute(c, r.Routes()) {
			gincontext.FailWithStatus(c, http.StatusMethodNotAllowed,
				code.Errorf(code.NotFound, "接口不存在: %s %s（文件预览签名接口只挂在核心 origin，"+
					"请改用核心 origin；当前请求打到了其它宿主）", c.Request.Method, c.Request.URL.Path))
			c.Abort()
			return
		}
		c.Next()
	})
}

// isPresignMisroute 判定一条请求是否属于"打错后端的 presign 误路由"。
func isPresignMisroute(c *gin.Context, realRoutes gin.RoutesInfo) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	p := c.Request.URL.Path
	if !strings.HasSuffix(p, "/presign-url") {
		return false
	}
	// 核心 origin 形态：一段 `/v1/mate/` 前缀就认定是"该由核心处理"，不拦（保持既有 404 口径）。
	if strings.HasPrefix(p, "/v1/mate/") {
		return false
	}
	// 防御性：若某条真实路由（同方法）确实能接住这条路径，则不是误路由。
	if matchRealRoute(http.MethodPost, p, realRoutes) {
		return false
	}
	return true
}

// matchRealRoute 判断 (method, path) 是否能被已注册的真实路由接住。
//
// gin 的 RoutesInfo 路径里 `:name` 是单段参数、`*name` 是"其余全吃"的通配。这里按段比对：
// 段数一致（或末尾 `*` 通配）且每个静态段逐字相等、参数段匹配任意非空段。
func matchRealRoute(method, path string, realRoutes gin.RoutesInfo) bool {
	reqSegs := splitPath(path)
	for _, ri := range realRoutes {
		if ri.Method != method {
			continue
		}
		if matchRouteSegments(splitPath(ri.Path), reqSegs) {
			return true
		}
	}
	return false
}

// matchRouteSegments 比对路由模板段与请求段。
func matchRouteSegments(tmpl, req []string) bool {
	for i, t := range tmpl {
		if strings.HasPrefix(t, "*") {
			// 通配段吃掉剩余全部（允许 0 段）。
			return true
		}
		if i >= len(req) {
			return false
		}
		if strings.HasPrefix(t, ":") {
			// 参数段：匹配任意非空段。
			if req[i] == "" {
				return false
			}
			continue
		}
		if t != req[i] {
			return false
		}
	}
	return len(tmpl) == len(req)
}

// splitPath 把路径切成非空段（`/a/b/` → ["a","b"]）。
func splitPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
