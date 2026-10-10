package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrmodel"
	"github.com/morehao/ark-mate/internal/controller/ctrprovider"
	"github.com/morehao/ark-mate/internal/controller/ctrruntime"
)

// registerRuntime 底座面：runtime 概要、provider 档案与模型目录。
//
// 供应商密钥只写不读回；`/models` 是**应用层清单**（不带底座维度），
// `/models/catalog` 是**运行时派生**的目录（ACP session config options），
// `?agent=<id>` 指定 agent（缺省取默认 agent）——平台不内置模型清单。
func registerRuntime(v1 *gin.RouterGroup, ports Ports) {
	rt := ctrruntime.NewRuntimeCtr(ports.Runtime)
	provider := ctrprovider.NewProviderCtr(ports.Runtime)
	model := ctrmodel.NewModelCtr(ports.Runtime, ports.Agent)

	v1.GET("/runtimes", rt.List)
	v1.GET("/providers", provider.List)
	v1.POST("/providers", provider.Create)
	v1.PATCH("/providers/:id", provider.Update)
	v1.DELETE("/providers/:id", provider.Delete)
	v1.POST("/providers/:id/probe", provider.Probe)
	v1.GET("/models", model.List)
	// 运行时**目录**（事实面）与会话级模型选择器共用：`/models` 是平台声明（意图），
	// 这里返回的是底座自报的会话配置项（不透明 value），两者不能互相替代
	// （dsh 的 value 是 JSON 数组，不是 `provider/model`）。
	v1.GET("/models/catalog", model.Catalog)
	v1.POST("/models", model.Create)
	v1.POST("/models/sync", model.Sync)
	v1.PATCH("/models/:id", model.Update)
	v1.DELETE("/models/:id", model.Delete)
}
