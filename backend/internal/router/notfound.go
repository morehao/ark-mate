package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// apiPrefix 控制面 API 的路径前缀。未命中路由落到 NoRoute 时靠它区分
// 「调错了控制面接口」与「在找一个前端页面」（后者在 release 构建里回落 SPA）。
const apiPrefix = "/v1/"

// isAPIPath 判定请求是否落在控制面 API 前缀下。
func isAPIPath(p string) bool { return strings.HasPrefix(p, apiPrefix) }

// writeNotFound 未命中任何路由时的统一响应（两个构建形态共用）。
//
// 三处有意为之的取舍：
//
//   - **HTTP 状态保持 404**，不改成一律 200。README 说「语义错误一律 HTTP 200 + code != 0」，
//     那说的是**业务**错误——路由命中了、领域层判定失败。这里是路由层根本没命中，属于
//     HTTP 语义问题，代理、客户端与抓包工具都靠状态码识别，混为一谈会让"接口不存在"
//     看起来像"接口存在但拒绝了"。
//
//   - **响应体仍是标准信封**，且 code 取自 pkg/code 注册表。此前这里手写的是
//     `{"code":404,"msg":"not found"}`：404 既不在注册表里、也不在任何对外码表里
//     （契约里"资源不存在"是 40401），客户端拿不到可判定的取值；而且缺 requestID 与 data，
//     是全部错误出口里唯一形状不完整的一处。
//
//     现在改用 `gincontext.FailWithStatus`（golib v1.32.16 起提供）：它把「按 HTTP 语义
//     换状态码」与「构造信封」解耦，信封字段、msg 取值与 `code: -1` 兜底规则都由 golib
//     单点给出。本仓不再自己拼 `gcontext.ResponseRender`——那样等于把信封约定复制一份，
//     golib 改了我们不会跟着变，形成静默分歧。
//
//   - **requestID 如实留空**而不是现造一个：设置它的 ginmiddleware.AccessLog 挂在版本分组上
//     （group.Use），而 NoRoute 挂在引擎上，分组中间件对未命中路由不会执行。既然没有对应的
//     服务端访问日志可关联，编一个 id 只会误导排查。
func writeNotFound(c *gin.Context) {
	gincontext.FailWithStatus(c, http.StatusNotFound,
		code.Errorf(code.NotFound, "接口不存在: %s %s", c.Request.Method, c.Request.URL.Path))
}
