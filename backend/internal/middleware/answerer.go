package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// AnswererTokenHeader 是 answerer 桥接插件随每次提问请求携带的令牌头名（与插件侧逐字对齐，
// 见 frontend/apps/desktop/assets/ark-mate-answerer/lib/index.js 的 `X-Mate-Answerer-Token`）。
const AnswererTokenHeader = "X-Mate-Answerer-Token"

// AnswererToken 是 `/sessions/:id/elicitations/ask` 这条**外部 source 提问**入口的令牌强校验。
//
// 为什么需要它（设计 2026-11-11-ask-user-answerer-bridge-design.md §8.1 / §10.5）：
// 该入口把 dsh 内 answerer 插件的提问桥接到前端卡片通道，是**本机回环**上的一个 POST 面。
// 回环 + OriginPolicy 已经挡掉了大部分浏览器跨源/DNS-rebinding 攻击，但那是与**凭据无关**的
// 边界——任何能在本机发 HTTP 的进程（含被投毒的脚本）都能打到它。令牌是平台注入给**本进程
// 拉起的 dsh 子进程**的一次性秘密（`dsh.AnswererEnv` 生成、经 Launch.Env 注入），服务端保存
// 同一份值比对，缺头/错值一律拒绝：这是 OriginPolicy 之外的一层 defense-in-depth。
//
// `expected == ""`（未配置）时 **no-op 放行**：与 OriginPolicy 的「nil/未启用 = 不判定」同口径
// ——测试路径与未注入令牌的部署（answerer 本就 disabled）不该因缺一层校验而整体不可用。生产里
// 只要 answerer 被启用（End/插件路径都在），令牌必然非空（`genAnswererToken` 绝不返回空串）。
// 比较用 `subtle.ConstantTimeCompare`，避免时序侧信道。
func AnswererToken(expected string) gin.HandlerFunc {
	if expected == "" {
		return func(c *gin.Context) { c.Next() }
	}
	want := []byte(expected)
	return func(c *gin.Context) {
		got := []byte(c.GetHeader(AnswererTokenHeader))
		// 长度先比（ConstantTimeCompare 对不等长直接返 0，但显式判空更清晰：缺头就是缺头）。
		if len(got) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
			c.Abort()
			gincontext.FailWithStatus(c, http.StatusUnauthorized, code.Error(code.InvalidRequest,
				"answerer 令牌缺失或不匹配（"+AnswererTokenHeader+"）"))
			return
		}
		c.Next()
	}
}
