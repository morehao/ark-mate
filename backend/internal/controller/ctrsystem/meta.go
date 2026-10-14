package ctrsystem

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
//
// 2026-10-10 起没有 `webSource` 字段：核心只有"调用方给的目录"这一种资产来源，一个恒为 `dir`
// 的字段不携带信息（改看 `webPath` 就知道核心读的是哪一份）。
//
// 为什么把"来源"做成一个结构体而不是继续用一堆闭包参数：这份响应是**版本闸门与诊断**的
// 唯一入口（设计 §6.7 / V5/V6），字段来源散在各层会让"某字段为什么是空的"无从追查；
// 集中在一处后，`Snapshot()` 就是这段契约的可读实现。
// PayloadInfo 随包运行时载荷的一条（M3-4 起填）。
//
// 值来自**壳的**三级解析结果（设计 §6.5：显式配置 → 随包 → PATH）：只有壳做了那次解析，
// 核心自己不探测本机安装，只把壳交来的清单原样透出。壳的清单里还有 `entry`（包内入口路径），
// 那是壳的内部布局，**刻意不进本结构**——`/meta` 的契约只回答"命中了哪一份、来自哪一级"。
type PayloadInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	// Source 命中级别：bundled（随包）| runtime-env（显式覆盖）| path（PATH 上找到）。
	// 取值由壳给出并在启动期被核心校验（非法值直接拒绝启动），因为"来源"是诊断面最会骗人的字段。
	Source string `json:"source"`
}

// RuntimeInfo 构建期固定的底座身份（v19）。
//
// 与前端 `packages/types/src/runtime.ts` 的 `RuntimeInfo` **逐字段一致**（两侧手工一致，
// 由界面验收脚本端到端覆盖）。
//
// 为什么放在 `/meta` 而不是一条独立读面：它是**这个二进制的属性**（`-X
// pkg/runtime.BuildRuntime=<id>`，不注入 = dsh），与 coreVersion/apiContract 同类——
// 客户端拿到一次就够，且它必须与版本号来自同一次握手（两份读面各自失效时会互相矛盾）。
type RuntimeInfo struct {
	// ID 底座 id（= `GET /runtime/diagnostics` 的 `runtime`，也 = 会话的 `runtime_id`）。
	ID string `json:"id"`
	// Name 底座的展示名（界面标题用它；空 = 取不到，回落用 ID）。
	Name string `json:"name"`
	// Version 底座版本（取不到为空串：核心**不探测**本机安装）。
	Version string `json:"version"`
}

// MetaInfo `/v1/mate/meta` 的响应（字段契约见计划 §5 M1-7/M2-3）。
type MetaInfo struct {
	CoreVersion string `json:"coreVersion"`
	WebBuildID  string `json:"webBuildId"`
	APIContract string `json:"apiContract"`
	// WebPath 资产目录的**绝对路径**；未接资产层时为 null（**显式 null**，客户端按可空字段处理）。
	WebPath *string `json:"webPath"`
	// WebDigest 逐文件 sha256 的确定性合成；未接资产层时为 null。
	WebDigest *string       `json:"webDigest"`
	Home      string        `json:"home"`
	Payloads  []PayloadInfo `json:"payloads"`
	// Runtime 底座身份（构建期固定）。它**总是出现**（不是可空字段）：装配层一定知道它是谁。
	Runtime RuntimeInfo `json:"runtime"`
}

// MetaAssets 资产层交给 `/meta` 的那部分（由 internal/router 从 AssetInfo 投影过来）。
type MetaAssets struct {
	// Path 资产目录的绝对路径；空串 = 本次进程没有资产（未给 --web-dir 时会拒绝启动，
	// 因此这只在"仅供 API"的装配与测试里出现）。
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
	// Runtime 底座身份（装配层或路由层从 `svcruntime.RuntimeSvc.RuntimeIdentity` 取；
	// 两条路都没给 = 未装配，如实发空串而不是编一个）。
	Runtime RuntimeInfo
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
		Runtime:     s.Runtime,
	}
	if m.Payloads == nil {
		m.Payloads = []PayloadInfo{}
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
