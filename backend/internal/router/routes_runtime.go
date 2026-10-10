package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrmodel"
	"github.com/morehao/ark-mate/internal/controller/ctrprovider"
	"github.com/morehao/ark-mate/internal/controller/ctrruntime"
)

// registerRuntime 底座面：runtime 概要、provider 档案与模型目录。
//
// provider 注入面密钥只写不读回；模型目录是**运行时派生**（ACP session config options），
// `?agent=<id>` 指定 agent（缺省取默认 agent）——平台不内置模型清单，
// provider 档案只影响启动 env 注入。
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
	v1.POST("/models", model.Create)
	v1.POST("/models/apply", model.Apply)
	v1.PATCH("/models/:id", model.Update)
	v1.DELETE("/models/:id", model.Delete)
}
