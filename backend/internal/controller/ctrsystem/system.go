// Package ctrsystem 承载控制面的**系统面**：诊断（`/meta`）与能力声明（`/capabilities`）。
//
// 两者都是设计 §7.3 的"声明面"：
//   - `/meta` 回答"这份部署是什么"（核心版本、前端构建 id、资产来源、数据目录、随包载荷）；
//   - `/capabilities` 回答"这个入口有哪些能力"，**两个入口同构**（id 集合与顺序完全一致），
//     只有 `available` 的值不同——浏览器入口如实回报"无托盘 / 无原生对话框"。
//
// 能力**执行面**不在这里：那是 `__MATE_DESKTOP__`（壳的 preload）。声明与执行分工见设计 §7.3。
package ctrsystem

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// CarrierHeader 壳的反代在转发 `/v1/*` 时打上的入口标识。
//
// 为什么用请求头而不是"核心自己的启动形态"：桌面安装里浏览器入口与桌面窗口打的是**同一个核心**，
// 只有"这次请求是从哪条通道来的"才能决定该回报哪一套能力。壳是唯一会把 REST 请求带凭据转进来的
// 组件（桌面态 restToken 只在壳手里），因此这个头是可信的入口声明。
const CarrierHeader = "X-Mate-Carrier"

// CarrierDesktop 桌面载体的标识值。
const CarrierDesktop = "desktop"

// Entries 这份部署对外提供哪些入口。
type Entries struct {
	Browser bool `json:"browser"`
	Desktop bool `json:"desktop"`
}

// CapabilityEntry 一条能力的声明。
type CapabilityEntry struct {
	// ID 能力 id（闭集，与前端 @ark-mate/desktop-contract 的 CapabilityId 一一对应）。
	ID string `json:"id"`
	// Available 对**本次请求的入口**是否可用。
	Available bool `json:"available"`
	// BrowserFallback 浏览器入口的等价路径（V3 单向一致性的机器判据）：
	// 任何 available=true 且被某个流程依赖的能力，这里都不得是 none（tray 例外：纯增强、不阻塞流程）。
	BrowserFallback string `json:"browserFallback"`
}

// Capabilities `/v1/mate/capabilities` 的响应。
type Capabilities struct {
	Entries      Entries           `json:"entries"`
	Capabilities []CapabilityEntry `json:"capabilities"`
}

// SystemCtr 系统面控制器。
type SystemCtr interface {
	GetMeta(c *gin.Context)
	GetCapabilities(c *gin.Context)
}

var _ SystemCtr = (*systemCtr)(nil)

type systemCtr struct {
	meta func() MetaSource
	// hostFacts 报告"宿主能不能抬起系统目录面板"；nil = 未知（当 false，能力声明退化）。
	hostFacts func() bool
}

// NewSystemCtr 构造系统面控制器。`meta` 由装配层凑齐（版本、数据目录、载荷、资产，
// 见 meta.go 的"谁填什么"表）。
//
// 参数是**函数**而不是值：`/meta` 里的资产字段（尤其 build id）要反映"核心此刻服务的那份资产"，
// 而不是进程启动瞬间的快照——版本闸门据此发现"dist 被换过"（M2-4）。
func NewSystemCtr(meta func() MetaSource, hostFacts func() bool) SystemCtr {
	return &systemCtr{meta: meta, hostFacts: hostFacts}
}

// GetMeta 诊断面：这份部署的版本、资产来源与数据目录。
func (h *systemCtr) GetMeta(c *gin.Context) {
	gincontext.Success(c, h.meta().Snapshot())
}

// GetCapabilities 声明面：两个入口**同构**（id 集合与顺序完全一致），只有 available 随入口不同。
//
// v14.5 起多了一类：`native-dialog` 的可用性由**宿主事实**给出（`hostFacts` 由装配层注入），
// 因为那个对话框是核心进程抬的——本机浏览器也能用，远程/SSH 才不行。
func (h *systemCtr) GetCapabilities(c *gin.Context) {
	desktop := strings.EqualFold(strings.TrimSpace(c.GetHeader(CarrierHeader)), CarrierDesktop)
	facts := CapabilityFacts{Desktop: desktop}
	if h.hostFacts != nil {
		facts.NativeDialog = h.hostFacts()
	}
	gincontext.Success(c, Capabilities{
		Entries:      Entries{Browser: true, Desktop: desktop},
		Capabilities: capabilityList(facts),
	})
}

// capabilityDecl 一条能力的**声明**（与入口无关的部分）。
type capabilityDecl struct {
	id              string
	browserFallback string
	// hostResolved 该能力的可用性由**宿主事实**决定，而不是"客户端是不是桌面壳"。
	//
	// v14.5 起 `native-dialog` 属于这一类：目录面板是**核心进程**抬起来的（macOS osascript /
	// Linux zenity·kdialog），所以本机浏览器打开控制台也能用到它——这与参照实现把判定放在宿主侧
	// 是同一条口径（远程/SSH/无显示会话才退化成应用内浏览）。
	hostResolved bool
}

// capabilityDecls 能力声明的**冻结顺序**（设计 §7.3 的表按此顺序；id 与前端契约的联合类型一一对应）。
//
// 顺序也冻结：前端可以按下标做稳定渲染，V18 的"两个入口同构"断言比的就是 id 序列。
var capabilityDecls = []capabilityDecl{
	{"tray", "none", false}, // 纯增强：没有浏览器等价物，也不阻塞任何流程
	// 宿主原生目录选择器：**可用性看宿主**（绑回环 + 非 SSH + 有显示会话），不看入口形态；
	// 宿主抬不起来时走服务端目录浏览（`browse` 那半永远可用）。
	{"native-dialog", "server:directory-browse", true},
	{"open-path", "ui:show-path-copy", false},      // 浏览器端显示路径 + 复制
	{"diagnostics-export", "http:download", false}, // 浏览器端直接下载导出包
	{"deep-link", "url:direct", false},             // 浏览器端 URL 直达
	{"update", "deployer:replace-binary", false},   // 浏览器端由部署者替换二进制
}

// CapabilityFacts 能力声明要用的宿主事实（装配层注入，避免控制器去猜）。
type CapabilityFacts struct {
	// Desktop 本次请求来自桌面壳（`X-Mate-Carrier: desktop`）。
	Desktop bool
	// NativeDialog 宿主能不能抬起系统目录面板（`manager.NativePickerMode() == native`）。
	NativeDialog bool
}

// capabilityList 按事实给出能力声明。`available` 是唯一的差异项：
// 宿主判定的能力看宿主，其余看"客户端是不是桌面壳"。
func capabilityList(f CapabilityFacts) []CapabilityEntry {
	out := make([]CapabilityEntry, 0, len(capabilityDecls))
	for _, d := range capabilityDecls {
		available := f.Desktop
		if d.hostResolved {
			available = f.NativeDialog
		}
		out = append(out, CapabilityEntry{ID: d.id, Available: available, BrowserFallback: d.browserFallback})
	}
	return out
}
