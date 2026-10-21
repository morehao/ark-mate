package system

import (
	productidentity "github.com/morehao/ark-mate/internal/domain/identity"
	"github.com/morehao/ark-mate/internal/contract/dtosystem"
)

// `/meta` 的内容定义（M2-3）：**谁填什么**在这里一次说清，装配点只负责把值凑齐。
//
//	字段            来源
//	coreVersion     internal/version（发布构建由 -ldflags 注入）
//	apiContract     internal/version（契约代数，与核心版本解耦）
//	webPath         资产层（资产目录的**绝对路径**；未接资产层时为 null）
//	webDigest       资产层（逐文件 sha256 的确定性合成）
//	webBuildId      前端产物里的构建 id（M2-4）
//	home            数据目录（默认 ~/.ark-mate）
//	payloads        随包运行时载荷（M3-4 起由壳经 --payloads 交来的已解析清单；非桌面态为空数组）
//	runtime         构建期固定的底座身份（id/name/version，v19）
//	product         产品面事实（internal/domain/identity：产品名/版本/身份版本/定位）
//
// 2026-10-10 起没有 `webSource` 字段：核心只有"调用方给的目录"这一种资产来源，一个恒为 `dir`
// 的字段不携带信息（改看 `webPath` 就知道核心读的是哪一份）。
//
// 为什么把"来源"做成一个结构体而不是继续用一堆闭包参数：这份响应是**版本闸门与诊断**的
// 唯一入口（设计 §6.7 / V5/V6），字段来源散在各层会让"某字段为什么是空的"无从追查；
// 集中在一处后，`Snapshot()` 就是这段契约的可读实现。
type MetaSource struct {
	CoreVersion string
	APIContract string
	Home        string
	Payloads    []dtosystem.PayloadInfo
	Assets      MetaAssets
	// Runtime 底座身份（装配层从执行面取；两条路都没给 = 未装配，如实发空串而不是编一个）。
	Runtime dtosystem.RuntimeInfo
	// Product 产品面事实（`ProductInfoOf(version.Version)` 的产物；空 = 由装配层补）。
	Product dtosystem.ProductInfo
}

// MetaAssets 资产层交给 `/meta` 的那部分。
type MetaAssets struct {
	// Path 资产目录的绝对路径；空串 = 本次进程没有资产（未给 --web-dir 时会拒绝启动，
	// 因此这只在"仅供 API"的装配与测试里出现）。
	Path string
	// Digest 合成摘要（`sha256:<hex>`）。
	Digest string
	// BuildID 前端构建 id（M2-4）。
	BuildID string
}

// Snapshot 组装一份响应值。
//
// 两个刻意的形状约定（客户端解析成本最低）：
//   - `payloads` 恒为**数组**（空也发 `[]`，不是 null）：让前端只写一种分支；
//   - `webPath` / `webDigest` 恒**出现**（无值时是 null）：它们是"可空字段"，
//     缺字段会让 `'webPath' in data` 与 `data.webPath === null` 两种判断各说各话。
func (s MetaSource) Snapshot() dtosystem.MetaInfo {
	m := dtosystem.MetaInfo{
		CoreVersion: s.CoreVersion,
		APIContract: s.APIContract,
		Home:        s.Home,
		Payloads:    s.Payloads,
		WebBuildID:  s.Assets.BuildID,
		Runtime:     s.Runtime,
		Product:     s.Product,
	}
	if m.Payloads == nil {
		m.Payloads = []dtosystem.PayloadInfo{}
	}
	if s.Assets.Path != "" {
		p := s.Assets.Path
		m.WebPath = &p
	}
	if s.Assets.Digest != "" {
		d := s.Assets.Digest
		m.WebDigest = &d
	}
	return m
}

// ProductInfoOf 组装产品面事实（`/meta.product` 与「关于」面板的唯一来源）。
//
// 为什么收成一个构造函数：装配层与路由默认值两处都要填它，各写一份字段列表的结果是
// "`/meta` 有 tagline、面板没有"——而两者都不报错。核心版本由调用方给（它不在产品域的
// 事实范围里），身份版本/产品名/定位词一律从 `internal/domain/identity` 取。
func ProductInfoOf(coreVersion string) dtosystem.ProductInfo {
	return dtosystem.ProductInfo{
		Name:            productidentity.Name,
		DisplayName:     productidentity.DisplayName,
		Version:         coreVersion,
		IdentityVersion: productidentity.Version,
		Tagline:         productidentity.Tagline,
	}
}

// MetaSvc 诊断面用例：把**值来源**投影成对外契约（`/meta`）。
//
// 参数是函数而不是值：资产字段（尤其 build id）要反映"核心此刻服务的那份资产"，
// 而不是进程启动瞬间的快照——版本闸门据此发现"dist 被换过"（M2-4）。
type MetaSvc interface {
	Meta() dtosystem.MetaInfo
}

type metaSvc struct {
	source func() MetaSource
}

// NewMeta 构造诊断面用例。
func NewMeta(source func() MetaSource) MetaSvc { return &metaSvc{source: source} }

func (s *metaSvc) Meta() dtosystem.MetaInfo { return s.source().Snapshot() }
