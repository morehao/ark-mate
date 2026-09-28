package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrsystem"
	"github.com/morehao/ark-forge/internal/version"
)

// registerSystem 系统面（M1-7）：诊断 `/meta` 与能力声明 `/capabilities`。
//
// 两者都挂在 **rest 面凭据**之后（只有 `/health` 免鉴权）：`/meta` 会回数据目录与载荷版本，
// `/capabilities` 虽只描述能力，但"这份部署有没有托盘"这类信息不该对外网无凭据可读。
func registerSystem(v1 *gin.RouterGroup, meta func() ctrsystem.MetaInfo) {
	ctr := ctrsystem.NewSystemCtr(func() ctrsystem.MetaInfo {
		m := meta()
		// 资产来源由本包按**构建形态**补全：核心自己最清楚"内嵌了没有"，
		// 而装配层不该为了一个诊断字段去反向依赖路由的资产细节。
		m.WebSource = currentWebSource()
		return m
	})
	v1.GET("/meta", ctr.GetMeta)
	v1.GET("/capabilities", ctr.GetCapabilities)
}

// defaultMeta 未显式注入时的诊断面默认值（测试与最小装配用）。
func defaultMeta() ctrsystem.MetaInfo {
	return ctrsystem.MetaInfo{
		CoreVersion: version.Version,
		APIContract: version.APIContract,
		Payloads:    []ctrsystem.PayloadInfo{},
	}
}
