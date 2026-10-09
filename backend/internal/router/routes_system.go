package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/version"
)

// registerSystem 系统面（M1-7）：诊断 `/meta` 与能力声明 `/capabilities`。
//
// 两者都挂在 **rest 面凭据**之后（只有 `/health` 免鉴权）：`/meta` 会回数据目录与载荷版本，
// `/capabilities` 虽只描述能力，但"这份部署有没有托盘"这类信息不该对外网无凭据可读。
func registerSystem(v1 *gin.RouterGroup, meta func() ctrsystem.MetaSource, assets AssetInfo) {
	ctr := ctrsystem.NewSystemCtr(func() ctrsystem.MetaSource {
		src := meta()
		// 资产相关字段由本包按**已解析的资产**补全：builtin → 只报 webSource；dir → 另报 webPath；
		// 两者都报 webDigest（内容摘要，解析期算）。build id **现场读**——它是闸门用来发现
		// "资产被换过"的字段，缓存住就失去意义（M2-4）。
		if assets.Has() {
			src.Assets = ctrsystem.MetaAssets{
				Source:  string(assets.Source),
				Path:    assets.Path,
				Digest:  assets.Digest,
				BuildID: assets.CurrentBuildID(),
			}
		}
		return src
	})
	v1.GET("/meta", ctr.GetMeta)
	v1.GET("/capabilities", ctr.GetCapabilities)
}

// defaultMeta 未显式注入时的诊断面默认值（测试与最小装配用）。
func defaultMeta() ctrsystem.MetaSource {
	return ctrsystem.MetaSource{
		CoreVersion: version.Version,
		APIContract: version.APIContract,
		Payloads:    []ctrsystem.PayloadInfo{},
	}
}
