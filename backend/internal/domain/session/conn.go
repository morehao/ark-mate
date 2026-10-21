package session

import (
	"context"
	"github.com/morehao/ark-mate/internal/platform/code"
	"time"

	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 执行面的连接端口（阶段 0.4）———
//
// 连接池与运行时平面的**实现**在 `drivers/runtimeconn`（出站适配器），但端口由域声明：
// 会话与编排只认"一条连接能给我什么"（句柄、键、配置指纹），不认池的具体类型。
//
// 为什么端口落在 `domain/session` 而不是执行面 `app/run`：`runtimeconn`（L6）必须**实现**
// 这些端口，而它只能向下 import（L6 → L2 合法，L6 → L7 是"向上"）。端口放执行面会让适配器
// 无法落地——那正是层表在提醒的边界方向。
//
// 与 `Runner` / `Lifecycle` 的既有窄端口同一口径：这里的方法**只交事实**（键、指纹、句柄），
// 不做判断；"要不要迁代际 / 要不要回收"都在编排侧。

// Conn 一条 runtime 连接的读面（执行面实现、会话/编排消费）。
//
// 只暴露**判定需要的那几样**：谁在位（Client）、面向哪个底座（Spec）、挂在哪个键上
// （Key / BaseKey）、按哪份配置建起来（ConfigDigest）。进程记账（pid / 建立时刻 / 拆除原因）
// 留在执行面内部——端口一旦能表达那些，就又变成"编排自己管连接"。
type Conn interface {
	// Agent 这条连接的能力面（建/恢复会话、能力事实、stderr、会话列举）。
	//
	// nil = 未拨号/测试桩：**连接端口只交能力，不交协议句柄**——协议类型归适配器内部。
	Agent() AgentConn
	// Spec 这条连接面向的 runtime 声明。
	Spec() runtime.Spec
	// Key 连接缓存键（基础键 + 能力指纹 + 配置指纹）。
	Key() string
	// BaseKey 该连接的基础键（不含代际维度）。
	BaseKey() string
	// ConfigDigest 该连接建立时的平台模型配置指纹。
	ConfigDigest() string
}

// DialOptions 一次拨号所需的编排侧事实。
type DialOptions struct {
	// Fingerprint 该会话钉住的能力指纹（空 = 缺省代际）。
	Fingerprint string
	// NativeMode 该会话钉住的原生权限模式（空 = 不注入）。
	NativeMode string
	// CheckCredentials 凭据面校验（nil = 跳过；离线/测试路径）。
	CheckCredentials func(ctx context.Context) error
	// Hooks 拨号时挂给执行面的回调集合（事件 / 权限 / 提问 / 提问完成）。
	//
	// **不透明载荷**：这四个回调的载荷类型分属 `event` 与 `interaction` 两个值面，而领域之间
	// 不许互相 import（`TestDomainPackagesMustNotImportEachOther`）。域对它一个字节都不读，
	// 只负责把装配侧交下来的那份原样转给适配器——适配器按自己的约定断言（类型不符即报
	// `dial_hooks_invalid`，不静默忽略：静默忽略的表现是"事件不推进、权限永远超时"）。
	//
	// 这是 C2 收尾刻意留下的一处 `any`：换成具体类型要么让会话域 import 另一个域的值包，
	// 要么把两个值面下沉到共享内核（C3 的 `platform`）——两条都比"一次不透明转发"贵。
	Hooks any
}

// KeyInputs 一次"取该用的连接"所需的全部键输入（四样同源：算键与注入用同一次取值）。
type KeyInputs struct {
	BaseKey      string
	Env          []string
	ConfigDigest string
	Key          string
}

// ConnDrop 一条连接被摘除时交给编排侧的事实。
//
// 带 `Client` 而不是会话 id 列表：谁挂在一条连接上是**会话侧**的登记，执行面没有、也不该有。
type ConnDrop struct {
	// Conn 刚摘下的那条连接（编排侧据它的键找出此刻挂在它上面的会话）。
	Conn Conn
	// Runtime 该连接的 runtime id（日志与诊断用）。
	Runtime string
	// WasCurrent 被摘的是该 runtime 的**当前**连接。
	WasCurrent bool
	// Intentional 主动拆除（停机 / 空闲回收）而不是连接故障。
	Intentional bool
	// Cause 拆除原因标签（`intentional` / `unexpected`）。
	Cause string
}

// DropSink 连接拆除的**应用**面（编排侧实现）。
//
// 实现约束：由池在**释放池锁之后**调用（实现里可以随便取别的锁）。
type DropSink interface {
	// OnConnDropped 一条连接刚被摘除。
	OnConnDropped(d ConnDrop)
	// InUseOn 这条连接此刻还有人在用吗——在途轮次、或挂着的会话（含已完成态）。
	InUseOn(conn Conn) bool
}

// ConnPool 连接池端口（`drivers/runtimeconn.Pool` 实现）。
//
// 方法分两类：取连接（Dial / KeyFor / ConnOfClient / ConnForRuntime）与生命周期
// （ReclaimGeneration / CloseAll / SetDropSink / SetGenerationLimit / StartIdleReaper）。
// env / 凭据 / 配置指纹这些**来源**的登记不在这里：那是装配根对具体适配器的接线
// （它持有池与平面，见 `bootstrap/assembly`），不是编排要用的能力。
type ConnPool interface {
	// Dial 取（或拨）一条 runtime 连接：活着复用，断开则作废重拨。
	Dial(ctx context.Context, spec runtime.Spec, opt DialOptions) (Conn, error)
	// KeyFor 按当前配置算出某会话该用的连接键。
	KeyFor(spec runtime.Spec, fingerprint, nativeMode string) KeyInputs
	// ConnForRuntime 返回该 runtime 已建立的连接（无 → nil；runtimeID 空 = 任一条）。
	ConnForRuntime(runtimeID string) Conn
	// ReclaimGeneration 回收指定的那一条代际（若它已无人引用且空闲）。
	ReclaimGeneration(c Conn) bool
	// CloseAll 关掉全部连接并停止空闲回收。
	CloseAll()
	// SetDropSink 装配期登记拆除的应用面。
	SetDropSink(s DropSink)
	// SetGenerationLimit 设置代际上限（0 = 不限制）。
	SetGenerationLimit(n int)
	// StartIdleReaper 启动空闲回收（interval 扫描，idle 阈值）。
	StartIdleReaper(interval, idle time.Duration)
}

// RuntimePlane 运行时平面端口（`drivers/runtimeconn.Plane` 实现）。
//
// 只交出编排真正要用的四件事：诊断留痕两条、底座绑定两条。凭据面与连接池的登记
// （`SetCredentialSource` / `SetLiveClientSource`）属于装配接线，不进端口。
type RuntimePlane interface {
	// RecordRuntimeError 记录底座最近一次失败（含 stderr 尾巴）。
	RecordRuntimeError(sessionID string, ae *code.RuntimeError, tail []string)
	// NoteScopeAmbiguous 记一条「归属歧义」留痕。
	NoteScopeAmbiguous(detail string)
	// SelectedSpec 取构建期选定的底座 spec（未装配 → 明确错误）。
	SelectedSpec() (runtime.Spec, error)
	// SetState 更新底座进程状态（建/拆连接时调用）。
	SetState(state string)
	// SampleConfigOptions 恢复/装载一条已有会话，读它的配置项，然后关闭（只读取样）。
	// **不新建会话**：那是执行面的责任，调用方只管候选枚举与退避。
	SampleConfigOptions(ctx context.Context, conn Conn, rawSessionID, cwd string) ([]runtime.ConfigOption, error)
}

// MaxDiagStderrLines 诊断留痕里 stderr 尾巴的保留行数（执行面与编排侧共用同一份上限）。
//
// 放在域而不是执行面：编排侧只拿到连接端口，不 import 具体池，
// 而两边必须用**同一个数字**（两处各写一份的表现是诊断里尾巴长短不一致）。
const MaxDiagStderrLines = 20
