package dtoapp

// ——— 应用市场（App Market）DTO（M-A2）———
//
// 这两个形状是「清单条目」与「清单条目 + 派生装态」的跨层契约：
//   - appmarket.Listing 是 infra 层的清单结构；svcapp 不能 import infra（分层），所以由装配根/控制器
//     把它转成 MarketListing 再传给 svcapp.MarketApps；
//   - MarketAppResp 是 svcapp.MarketApps 的出参（= GET /apps/market 的 data 项），对齐执行计划 §2.2
//     与前端 MarketApp 类型。
//
// 派生装态（installed/enabled/updatable/installable/state）由 svcapp 依 store（定义表）+ runtime.HasPack
// （本地包目录）算出，不落库（与 C6「state 是派生的」同一纪律）。

// MarketListing 是一条市场清单条目（appmarket.Listing 的跨层 DTO 形态）。
type MarketListing struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Icon           string        `json:"icon,omitempty"`
	Summary        MarketSummary `json:"summary"`
	Version        string        `json:"version"`
	Homepage       string        `json:"homepage,omitempty"`
	Source         MarketSource  `json:"source"`
	MinCoreVersion string        `json:"min_core_version,omitempty"`
	Tags           []string      `json:"tags,omitempty"`
	Tagline        string        `json:"tagline,omitempty"`
	Examples       []string      `json:"examples,omitempty"`
	UsageGuide     string        `json:"usage_guide,omitempty"`
}

// MarketSummary 是一句话摘要（中/英）。
type MarketSummary struct {
	ZH string `json:"zh"`
	EN string `json:"en,omitempty"`
}

// MarketSource 是应用包的来源声明（type=dir 为本期唯一可装类型；url 置灰属 C8）。
type MarketSource struct {
	Type string `json:"type"`
	Dir  string `json:"dir,omitempty"`
}

// MarketAppResp 是「清单条目 + 派生装态」（GET /apps/market 的 data 项，对齐前端 MarketApp）。
//
// 派生字段语义（svcapp 侧算）：
//   - installed   本地有包（runtime.HasPack）或 apps 表有定义 → true；
//   - enabled     app 定义 enabled（无定义 = false）；
//   - updatable   已装包版本 < 清单 version（无包 = false；C8 更新判定）；
//   - installable type==dir && 来源根下目录存在 && 非「已装且版本相同」；
//   - state       已装时的生命周期态（active/pending/off/failed/disposed；未装为空）。
type MarketAppResp struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Icon     string        `json:"icon,omitempty"`
	Summary  MarketSummary `json:"summary"`
	Version  string        `json:"version"`
	Homepage string        `json:"homepage,omitempty"`
	Tags     []string      `json:"tags,omitempty"`
	Source   MarketSource  `json:"source"`
	Tagline  string        `json:"tagline,omitempty"`
	Examples []string      `json:"examples,omitempty"`
	UsageGuide string      `json:"usage_guide,omitempty"`

	Installed   bool   `json:"installed"`
	Enabled     bool   `json:"enabled"`
	Updatable   bool   `json:"updatable"`
	Installable bool   `json:"installable"`
	State       string `json:"state,omitempty"`
}
