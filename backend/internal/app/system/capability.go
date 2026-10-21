package system

import "github.com/morehao/ark-mate/internal/contract/dtosystem"

// 能力声明面（`/capabilities`）的用例实现。
//
// 为什么不在控制器里：声明是**契约**（id 闭集、顺序冻结、与前端联合类型一一对应），
// 而入口判定（桌面壳 / 宿主事实）是请求上下文；把契约留在用例层，控制器只负责读请求头。
// 声明与执行的分工见设计 §7.3。

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
//
// **2026-10-11 收窄（P3/D5）**：原来还有 `open-path` / `diagnostics-export` / `deep-link` / `update`
// 四条，连同它们各自的 `browserFallback` 一并删除——**四条在两个入口里都没有任何消费者**。
// 判据由 `internal/archtest` 的守卫钉住：**声明的每条能力必须在界面源码被真实引用**，
// 确需保留的（如 `tray`）必须显式登记 + 写理由。
var capabilityDecls = []capabilityDecl{
	// 纯增强：没有浏览器等价物，也不阻塞任何流程（壳真的装了托盘，见 `apps/desktop/src/tray.ts`）。
	// 它是**唯一**允许 `browserFallback: none` 的能力，也是唯一"声明了但界面不读"的登记项。
	{"tray", "none", false},
	// 宿主原生目录选择器：**可用性看宿主**（绑回环 + 非 SSH + 有显示会话），不看入口形态；
	// 宿主抬不起来时走服务端目录浏览（`browse` 那半永远可用）。
	{"native-dialog", "server:directory-browse", true},
}

// CapabilityFacts 能力声明要用的宿主事实（装配层注入，避免用例去猜）。
type CapabilityFacts struct {
	// Desktop 本次请求来自桌面壳（`X-Mate-Carrier: desktop`）。
	Desktop bool
	// NativeDialog 宿主能不能抬起系统目录面板（picker 的判定结果）。
	NativeDialog bool
}

// CapabilitySvc 能力声明面用例。
type CapabilitySvc interface {
	Capabilities(facts CapabilityFacts) dtosystem.Capabilities
}

type capabilitySvc struct{}

// NewCapabilities 构造能力声明面用例。
func NewCapabilities() CapabilitySvc { return capabilitySvc{} }

// Capabilities 按事实给出声明。`available` 是唯一的差异项：
// 宿主判定的能力看宿主，其余看"客户端是不是桌面壳"。
func (capabilitySvc) Capabilities(f CapabilityFacts) dtosystem.Capabilities {
	entries := dtosystem.Entries{Browser: true, Desktop: f.Desktop}
	out := make([]dtosystem.CapabilityEntry, 0, len(capabilityDecls))
	for _, d := range capabilityDecls {
		available := f.Desktop
		if d.hostResolved {
			available = f.NativeDialog
		}
		out = append(out, dtosystem.CapabilityEntry{ID: d.id, Available: available, BrowserFallback: d.browserFallback})
	}
	return dtosystem.Capabilities{Entries: entries, Capabilities: out}
}
