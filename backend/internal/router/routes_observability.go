package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrusage"
	"github.com/morehao/ark-mate/internal/controller/ctrws"
	"github.com/morehao/ark-mate/internal/middleware"
)

// registerUsage 用量统计（M3 首项）：runs 计数/时长 + 会话上下文/费用快照聚合
// （memory 模式返回 40501）。只读，挂在 rest 面凭据中间件之后。
func registerUsage(v1 *gin.RouterGroup, usage ctrusage.UsageCtr) {
	v1.GET("/usage/summary", usage.Summary)
}

// registerStream 实时事件通道（`/ws/events`）：订阅 eventbus → 按会话聚合推送
// event/snapshot 帧，契约见 docs/architecture.md §6.2。
//
// 首帧 `attach` 必须在 AttachDeadline 内送达（协议形状）——未按时 attach 由服务端主动关闭（1008）。
//
// **来源判定（M3-5）**：与 REST 共用同一个 OriginPolicy（Host 白名单 + Origin 矩阵）。
// 握手也可能带错 Host（DNS rebinding 的请求同样能发起 WS），所以控制器拿到的是完整判定，
// 不是只比 Origin 的一小段逻辑。
func registerStream(v1 *gin.RouterGroup, ws ctrws.WSCtr, origin *middleware.OriginPolicy) {
	v1.GET("/ws/events", ws.Handle)
}
