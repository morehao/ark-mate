package ctrsystem

// `/meta` 的内容定义（M2-3）：**谁填什么**在这里一次说清，装配点只负责把值凑齐。
//
//	字段            来源
//	coreVersion     internal/version（发布构建由 -ldflags 注入）
//	apiContract     internal/version（契约代数，与核心版本解耦）
//	webSource       资产层（builtin | dir | 空=没有资产）
//	webPath         资产层（dir 时是**绝对路径**；builtin 为 null）
//	webDigest       资产层（逐文件 sha256 的确定性合成；两种来源同值）
//	webBuildId      前端产物里的构建 id（M2-4）
//	home            数据目录（默认 ~/.forge）
//	payloads        随包运行时载荷（M3-1 起有值；现在恒为空数组）
//
// 为什么把"来源"做成一个结构体而不是继续用一堆闭包参数：这份响应是**版本闸门与诊断**的
// 唯一入口（设计 §6.7 / V5/V6），字段来源散在各层会让"某字段为什么是空的"无从追查；
// 集中在一处后，`Snapshot()` 就是这段契约的可读实现。

// PayloadInfo 随包运行时载荷的一条（M3-1 起填；当前恒为空）。
type PayloadInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	// Source 命中级别：bundled（随包）| runtime-env（显式覆盖）| path（PATH 上找到）。
	Source string `json:"source"`
}

// MetaInfo `/v1/forge/meta` 的响应（字段契约见计划 §5 M1-7/M2-3）。
type MetaInfo struct {
	CoreVersion string `json:"coreVersion"`
	WebBuildID  string `json:"webBuildId"`
	APIContract string `json:"apiContract"`
	// WebSource builtin | dir（未接资产层时为空 → 字段省略）。
	WebSource string `json:"webSource,omitempty"`
	// WebPath dir 来源的绝对路径；其余为 null（**显式 null**，客户端按可空字段处理）。
	WebPath *string `json:"webPath"`
	// WebDigest 逐文件 sha256 的确定性合成；未接资产层时为 null。
	WebDigest *string       `json:"webDigest"`
	Home      string        `json:"home"`
	Payloads  []PayloadInfo `json:"payloads"`
}

// MetaAssets 资产层交给 `/meta` 的那部分（由 internal/router 从 AssetInfo 投影过来）。
type MetaAssets struct {
	// Source builtin | dir；空串 = 本次进程没有资产（默认构建且未给 --web-dir 时会拒绝启动，
	// 因此这只在"仅供 API"的装配里出现）。
	Source string
	// Path dir 来源的绝对路径。
	Path string
	// Digest 合成摘要（`sha256:<hex>`）。
	Digest string
	// BuildID 前端构建 id（M2-4）。
	BuildID string
}

// MetaSource `/meta` 的值来源。
type MetaSource struct {
	CoreVersion string
	APIContract string
	Home        string
	Payloads    []PayloadInfo
	Assets      MetaAssets
}

// Snapshot 组装一份响应值。
//
// 两个刻意的形状约定（客户端解析成本最低）：
//   - `payloads` 恒为**数组**（空也发 `[]`，不是 null）：让前端只写一种分支；
//   - `webPath` / `webDigest` 恒**出现**（无值时是 null）：它们是"可空字段"，
//     缺字段会让 `'webPath' in data` 与 `data.webPath === null` 两种判断各说各话。
func (s MetaSource) Snapshot() MetaInfo {
	m := MetaInfo{
		CoreVersion: s.CoreVersion,
		APIContract: s.APIContract,
		Home:        s.Home,
		Payloads:    s.Payloads,
		WebBuildID:  s.Assets.BuildID,
	}
	if m.Payloads == nil {
		m.Payloads = []PayloadInfo{}
	}
	if s.Assets.Source != "" {
		m.WebSource = s.Assets.Source
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
