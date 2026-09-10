package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrusage"
	"github.com/morehao/ark-forge/internal/controller/ctrws"
	"github.com/morehao/ark-forge/internal/eventbus"
)

// registerObservability 观测与实时通道：用量统计（M3 首项）与 WS 事件流。
//
// 用量统计只读：runs 计数/时长 + 会话上下文/费用快照聚合（memory 模式返回 40501）。
// WS 是实时事件通道（M0 基本版）：订阅 eventbus → 按 conversation 聚合推送
// event/snapshot 帧，契约见 docs/design-and-roadmap.md §4.5。
func registerObservability(v1 *gin.RouterGroup, ports Ports, bus *eventbus.Bus) {
	usage := ctrusage.NewUsageCtr(ports.Usage)
	ws := ctrws.NewWSCtr(bus)

	v1.GET("/usage/summary", usage.Summary)
	v1.GET("/ws/events", ws.Handle)
}
