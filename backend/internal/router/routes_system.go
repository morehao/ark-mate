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
func registerSystem(v1 *gin.RouterGroup, meta func() ctrsystem.MetaInfo, assets AssetInfo) {
	ctr := ctrsystem.NewSystemCtr(func() ctrsystem.MetaInfo {
		m := meta()
		// 资产相关字段由本包按**已解析的资产**补全（M2-1 起来源是运行时解出来的，不是构建 tag）：
		// builtin → 只报 webSource；dir → 另报 webPath。webDigest / webBuildId 由 M2-3/M2-4 接。
		if assets.Has() {
			m.WebSource = string(assets.Source)
			if assets.Source == AssetSourceDir {
				p := assets.Path
				m.WebPath = &p
			}
			if assets.Digest != "" {
				d := assets.Digest
				m.WebDigest = &d
			}
			m.WebBuildID = assets.BuildID
		}
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
