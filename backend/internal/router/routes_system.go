package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
	"github.com/morehao/ark-mate/internal/version"
)

// registerSystem 系统面（M1-7）：诊断 `/meta` 与能力声明 `/capabilities`。
//
// 两者都挂在 **rest 面凭据**之后（只有 `/health` 免鉴权）：`/meta` 会回数据目录与载荷版本，
// `/capabilities` 虽只描述能力，但"这份部署有没有托盘"这类信息不该对外网无凭据可读。
func registerSystem(v1 *gin.RouterGroup, meta func() ctrsystem.MetaSource, assets AssetInfo,
	hostFacts CapabilityHostFacts, runtimeIdentity func() dtoruntime.IdentityResp) {
	ctr := ctrsystem.NewSystemCtr(func() ctrsystem.MetaSource {
		src := meta()
		// runtime 段（构建期固定的底座身份）：由本包从 Runtime 端口补一次。
		//
		// 为什么不让每个装配点各填一次：`/meta` 是**这个二进制的属性**出口，而"这台二进制认哪个
		// 底座"就是 Runtime 端口的第一手事实——装配层没显式给（最小装配/测试）时也要正确，
		// 否则页面上的底座名会随装配方式静默变空。装配层显式给了就以它为准（非空即不覆盖）。
		if src.Runtime.ID == "" && runtimeIdentity != nil {
			if ri := runtimeIdentity(); ri.ID != "" {
				src.Runtime = ctrsystem.RuntimeInfo{ID: ri.ID, Name: ri.Name, Version: ri.Version}
			}
		}
		// product 段（产品面事实）：装配层通常已经填好；最小装配/测试没填时在这里补齐——
		// 它是**恒有值**字段（前端「关于」面板与 `/meta` 的消费者都按"总是出现"写）。
		// 不读 runtime：产品面刻意不回答"跑在哪个底座上"。
		if src.Product.Name == "" {
			src.Product = ctrsystem.ProductInfoOf(version.Version)
		}
		// 资产相关字段由本包按**已解析的资产**补全：webPath 是那份目录的绝对路径，再由 `/meta`
		// 投影出 webDigest（内容摘要，解析期算）。build id **现场读**——它是闸门用来发现
		// "资产被换过"的字段，缓存住就失去意义（M2-4）。
		if assets.Has() {
			src.Assets = ctrsystem.MetaAssets{
				Path:    assets.Path,
				Digest:  assets.Digest,
				BuildID: assets.CurrentBuildID(),
			}
		}
		return src
	}, func() bool {
		if hostFacts == nil {
			return false // 装配没给事实：报"没有"，让前端走应用内浏览（不撒谎）
		}
		return hostFacts()
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
		Product:     ctrsystem.ProductInfoOf(version.Version),
	}
}
