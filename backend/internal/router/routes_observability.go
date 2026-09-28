package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrusage"
	"github.com/morehao/ark-forge/internal/controller/ctrws"
	"github.com/morehao/ark-forge/internal/credential"
	"github.com/morehao/ark-forge/internal/eventbus"
)

// registerUsage 用量统计（M3 首项）：runs 计数/时长 + 会话上下文/费用快照聚合
// （memory 模式返回 40501）。只读，挂在 rest 面凭据中间件之后。
func registerUsage(v1 *gin.RouterGroup, ports Ports) {
	usage := ctrusage.NewUsageCtr(ports.Usage)
	v1.GET("/usage/summary", usage.Summary)
}

// registerStream 实时事件通道（`/ws/events`）：订阅 eventbus → 按会话聚合推送
// event/snapshot 帧，契约见 docs/design-and-roadmap.md §4.5。
//
// **鉴权在带内**：这条路由不挂 REST 凭据中间件，因为浏览器无法为 WS 握手设置请求头，
// 而令牌又不允许进 URL（M1-1）。取而代之的是首帧 `attach` 携带 stream 面凭据，
// 且必须在 AttachDeadline 内送达——未按时 attach 或凭据不符由服务端主动关闭（1008）。
func registerStream(v1 *gin.RouterGroup, bus *eventbus.Bus, creds *credential.Set) {
	ws := ctrws.NewWSCtr(bus, ctrws.Options{Credentials: creds})
	v1.GET("/ws/events", ws.Handle)
}
