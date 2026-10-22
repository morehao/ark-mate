package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
)

// registerSystem 系统面（M1-7）：诊断 `/meta` 与能力声明 `/capabilities`。
//
// 两者都挂在 **rest 面凭据**之后（只有 `/health` 免鉴权）：`/meta` 会回数据目录与载荷版本，
// `/capabilities` 虽只描述能力，但"这份部署有没有托盘"这类信息不该对外网无凭据可读。
//
// v3.1 起值的拼装（版本 / 资产 / 底座身份 / 产品面 / 能力声明）全在装配根与 `svcsystem`，
// 路由层只做两条注册。
func registerSystem(v1 *gin.RouterGroup, ctr ctrsystem.SystemCtr) {
	v1.GET("/meta", ctr.GetMeta)
	v1.GET("/capabilities", ctr.GetCapabilities)
}
