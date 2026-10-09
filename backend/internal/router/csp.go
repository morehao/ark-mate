package router

import "github.com/gin-gonic/gin"

// ContentSecurityPolicy 控制面下发**文档**的 CSP（设计 §9.2）。
//
// **两处下发同一策略**：核心服务浏览器入口时以响应头下发；桌面壳在 `mate-app://app`
// 的文档响应上下同一个头。策略值的唯一真源在前端契约 `@ark/desktop-contract` 的
// `CSP_POLICY`，这里是与它**逐字相同**的 Go 常量——由 `archtest` 的
// `TestRendererHardeningBaseline` 断言两处字符串一字不差（改一处忘另一处即红）。
//
// 纪律：不列任何外域（无在线字体 / CDN / 遥测）、`script-src` 不含 `unsafe-eval`、
// `connect-src` 只放行自身与回环 WS（事件流直连核心，设计 §7.2）。
const ContentSecurityPolicy = "default-src 'self'; connect-src 'self' ws://127.0.0.1:*; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// setDocumentCSP 给文档类响应加 CSP 头。
//
// 只给"会被浏览器当文档/脚本加载"的响应加：JSON 信封（/v1/*）不需要，
// 而且给它们加会让 curl/脚本调试时多一个无意义的头。
func setDocumentCSP(c *gin.Context) {
	c.Header("Content-Security-Policy", ContentSecurityPolicy)
}
