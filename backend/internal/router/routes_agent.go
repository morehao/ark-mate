package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctragent"
	"github.com/morehao/ark-mate/internal/controller/ctrcapability"
)

// registerAgent Agent 管理面：注册向导（definitions 持久化）+ 启停 + 热更新 + 删除，
// 以及某 Agent 可见能力的聚合读口。
func registerAgent(v1 *gin.RouterGroup, ports Ports) {
	ctr := ctragent.NewAgentCtr(ports.Agent)
	caps := ctrcapability.NewCapabilityCtr(ports.Capability)

	v1.GET("/agents", ctr.List)
	v1.POST("/agents", ctr.Create)
	v1.PATCH("/agents/:id", ctr.Update)
	// 切换「当前 Agent」（全局默认单例：新建会话/模型目录缺省取它；幂等，选择落 default.json）。
	v1.POST("/agents/:id/default", ctr.SetDefault)
	v1.DELETE("/agents/:id", ctr.Delete)
	v1.POST("/agents/:id/start", ctr.Start)
	v1.POST("/agents/:id/stop", ctr.Stop)
	// A3：agent 诊断快照（连接态 + stderr 诊断尾巴 + 最近失败）。
	v1.GET("/agents/:id/diagnostics", ctr.Diagnostics)
	// P2 §8.1 统一能力目录：某 Agent 可见的 skill/MCP/tool（按候选 runtime 聚合，只读）。
	v1.GET("/agents/:id/capabilities", caps.AgentCapabilities)
}
