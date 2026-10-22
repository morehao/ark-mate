package runtimeconn

import (
	"github.com/morehao/ark-mate/internal/core/protocol"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— P4a 正文第五十四刀：连接池落执行面 ———
//
// 连接池（`conns`）连同它的**连接对象**从 `manager.Orchestrator` 搬到这里。搬的理由不是
// "文件行数好看"，而是这份状态此前由 `Orchestrator.mu` 一把锁同时保护**三样东西**：
// 连接表、活跃会话表、原生 id 映射。三样东西的归属不同（连接=执行面，会话=会话域/编排），
// 挤在一把锁下就必然出现"持锁请求会话快照"那类锁序反转（§3.1.49 踩过一次，表现是
// `go test ./...` **挂死**而不是红）。分开之后：
//
//	Pool.mu   —— 只保护连接表、代际上限与空闲回收的停止句柄；
//	o.mu      —— 只保护活跃会话表与 byProvider 映射（仍在 `Orchestrator`）。
//
// **两条边都只朝一个方向**：池可以在自己的锁**外**回调 `DropSink`（问"还有人用吗"、通知
// "这条连接没了"），所以 `pool.mu → 会话锁` 这条边不存在；反过来编排侧也不许持 `o.mu` 调池。

// Conn 一条 runtime 连接（本域的对象；字段全导出——搬过来时不再需要一批访问器）。
//
// **一次只为一条子进程**：`Key` 是内容寻址的（基础键 + 能力指纹 + 配置指纹），所以
// "同配置共享暖进程"这件事在键上就已经决定了。
type Conn struct {
	// Client ACP 客户端句柄（拨号成功后才有；测试桩可为 nil）。
	Client *acp.Client
	// Spec 这条连接面向的 runtime 声明（投影、日志与回收都从它取事实）。
	Spec runtime.Spec
	// Key 连接缓存键（B5：runtime ID + 原生权限 env 签名 + 代际维度）。同一 runtime 但原生权限
	// 配置不同的会话必须各自独立进程（权限开关是进程级），配置相同则共享暖进程。
	Key string
	// BaseKey 该连接的基础键（= 不含代际维度的 Key）。停机/回收用它一次性找到**该配置的
	// 全部代际**——只按 Key 找的话，代际机制一旦真的产生多代际就会漏关。
	BaseKey string
	// ConfigDigest 该连接建立时的平台模型配置指纹（T5）。代际裁决用它判断"清单变了没有"。
	ConfigDigest string
	// Started 连接建立时刻（空闲回收计时起点）。
	Started time.Time
	// PID 子进程 pid（进程注册表登记用；0 = 未登记）。
	PID int
	// Intentional 该连接是**我方主动关闭**的（daemon 停机 / 空闲回收 / 代际淘汰），
	// 而非对端或子进程意外断开。拆除方在关闭之前置位，摘除时据此判定拆除原因；
	// 主动拆除不得把会话写成失败态（见 `DisconnectSession` 的状态语义）。
	Intentional atomic.Bool
}

// Pool 连接池：键派生、准入、回收、拆除通知。自持一把只保护连接表的锁。
type Pool struct {
	// bind 执行面的绑定（进程状态 running/stopped 唯一写入方就是本池的建连与拆连）。
	bind *Plane

	// 三个装配期登记的依赖（nil = 该事实不可得：键少一维 / 不做回收 / 不应用拆除）。
	env    ProviderEnvSource
	digest ConfigDigestSource
	sink   DropSink
	// native 原生权限/沙箱 env 的来源（nil = 不注入、基础键无这一维）。
	native NativeEnvSource

	mu              sync.Mutex
	conns           map[string]*Conn
	generationLimit int
	reapStop        func() // 空闲回收 goroutine 的停止函数（CloseAll 调用）
}

// NewPool 造一个连接池（绑定执行面：进程状态与子进程注册表都在那边）。
func NewPool(bind *Plane) *Pool {
	return &Pool{bind: bind, conns: make(map[string]*Conn)}
}

// SetProviderEnvSource 装配期登记子进程 env 的来源（nil = 不注入、基础键无 env 维度）。
func (pl *Pool) SetProviderEnvSource(src ProviderEnvSource) { pl.env = src }

// SetNativeEnvSource 装配期登记原生权限/沙箱 env 的来源（nil = 不注入、基础键无这一维）。
func (pl *Pool) SetNativeEnvSource(src NativeEnvSource) { pl.native = src }

// SetConfigDigestSource 装配期登记模型清单指纹的来源（nil = 代际键不含该维度）。
func (pl *Pool) SetConfigDigestSource(src ConfigDigestSource) { pl.digest = src }

// SetDropSink 装配期登记拆除的应用面（编排器回挂时调——它在此之前还不存在）。
func (pl *Pool) SetDropSink(s DropSink) { pl.sink = s }

// KeyInputs 一次"取该用的连接"所需的全部键输入。
//
// 四个字段同源是这一刀最想要的性质：**算键用的 env 与实际注入的 env 是同一次取值**。
// 分开取会得到"键说共享、env 却是另一套"的静默分叉（`ProviderEnvSource` 的注释）。
type KeyInputs struct {
	BaseKey      string
	Env          []string
	ConfigDigest string
	Key          string
}

// KeyFor 按当前配置算出某会话该用的连接键（`fingerprint` = 会话钉住的能力指纹，空 = 缺省代际）。
//
// 这是**取键点的唯一实现**：基础键的 env 维度、代际维度的两个指纹都在这里拼进去。
// 键怎么构造、怎么匹配只在这个文件里说话（`TestConnectionKeysLiveInRuntimeconn` 钉着）。
func (pl *Pool) KeyFor(spec runtime.Spec, fingerprint, nativeMode string) KeyInputs {
	var env []string
	if pl.env != nil {
		env = pl.env.ProviderEnv(spec.ID)
	}
	var digest string
	if pl.digest != nil {
		digest = pl.digest.ConfigDigest(spec.ID)
	}
	// **第三个 env 组：原生权限/沙箱**（P1-f 下半）。
	//
	// 为什么它是**基础键**的一维而不是代际维：模式和代际是两件不同的事——代际回答"能力集是不是
	// 同一套"，模式回答"这个进程的沙箱边界在哪"。两者混进同一个维度会让"改了能力选择"看起来
	// 也换了模式（诊断时读不出来到底是哪一维变了）。
	//
	// 为什么它必须进键：模式只能以**进程 env**（`DSH_PERMISSION_MODE`）下发（探针实测
	// `/permission` 命令面在 ACP 上不存在），所以一个进程只能有一个模式。不进键的表现不是报错，
	// 是**后建的会话悄悄跑在先进那个进程的模式里**——用户以为选了只读，实际是完全访问。
	nativeEnv, _ := pl.nativeEnvFor(spec.ID, nativeMode)
	base := ConnKey(spec.ID, env, nativeEnv)
	return KeyInputs{
		BaseKey: base,
		// **注入的就是算键用的那一份**（同一次取值）：分开取会得到"键说共享、env 却是另一套"
		// 的静默分叉——那正是 `ProviderEnvSource` 注释里那条纪律，这里逐字适用。
		Env:          append(append([]string{}, env...), nativeEnv...),
		ConfigDigest: digest,
		Key:          GenerationKey(base, fingerprint, digest),
	}
}

// Get 按缓存键取连接（无 → nil）。**不判断存活**：调用方必须自己验活——这里给的是登记事实。
func (pl *Pool) Get(key string) *Conn {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.conns[key]
}

// Len 连接池条数（**持池锁**读）。诊断与测试用；裸读连接表在 `-race` 下就是数据竞争。
func (pl *Pool) Len() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return len(pl.conns)
}

// Add 把刚拨起来的连接落位：登记子进程 + 进程状态置 running + 入表 + 起断连监听。
//
// 四件事收在一个入口，是因为它们**必须一起发生**：只入表不登记，孤儿回收看不见这个子进程；
// 只入表不起监听，连接断了它永远留在表里（`Get` 会给出一条死连接，而调用方以为还能用）。
// `Client` 为 nil 的桩（测试）不会起监听——没有 Done 通道可等。
func (pl *Pool) Add(c *Conn) {
	if c == nil {
		return
	}
	pl.registerProc(c)
	pl.mu.Lock()
	pl.conns[c.Key] = c
	pl.mu.Unlock()
	if pl.bind != nil {
		pl.bind.SetState(session.AgentStateRunning)
	}
	if c.Client != nil {
		go pl.watch(c)
	}
}

// watch 监听连接断开（对端或子进程结束）——摘除并通知编排侧。
func (pl *Pool) watch(c *Conn) {
	<-c.Client.Done()
	pl.Drop(c)
}

// Drop 摘除一条连接（幂等）：登记摘除 → 状态回落 → 通知编排侧应用。
//
// **顺序有意义**：
//   - 先摘进程登记：子进程已经不在（或即将不在）时，孤儿回收不该再把它当成"我的"；
//   - 进程状态只在**摘的是当前连接**时回落：旧连接收尾不得覆盖新连接的 running
//     （空闲回收后立刻按需重拨的场景）；
//   - 会话收尾放在最后、且在**池锁之外**：那是编排侧的事，它会去取会话锁。
func (pl *Pool) Drop(c *Conn) {
	if c == nil {
		return
	}
	pl.unregisterProc(c)
	intentional := c.Intentional.Load()

	pl.mu.Lock()
	wasCurrent := pl.conns[c.Key] == c
	if wasCurrent {
		delete(pl.conns, c.Key)
	}
	pl.mu.Unlock()

	if pl.bind != nil && wasCurrent {
		pl.bind.SetState(session.AgentStateStopped)
	}
	if pl.sink != nil {
		var droppedClient protocol.Conn
		if c.Client != nil { // nil 归一：typed nil 装进接口不等于 nil
			droppedClient = c.Client
		}
		pl.sink.OnConnDropped(ConnDrop{
			Client:      droppedClient,
			Runtime:     c.Spec.ID,
			WasCurrent:  wasCurrent,
			Intentional: intentional,
			Cause:       ConnCauseLabel(intentional),
		})
	}
}

// ConnOfClient 反查某客户端所属的连接（无 → nil）。连接数是代际上限量级（个位数），线性扫足够；
// 代际迁移要拿它读"这条句柄此刻挂在哪个代际键上"（那是判"要不要迁"的唯一权威来源）。
func (pl *Pool) ConnOfClient(client protocol.Conn) *Conn {
	if client == nil {
		return nil
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for _, c := range pl.conns {
		// 按身份比较：`*acp.Client` 与 `protocol.Conn` 是同一对象，接口比较落到指针相等。
		if any(c.Client) == any(client) {
			return c
		}
	}
	return nil
}

// ConnForRuntime 返回该 runtime 已建立的连接（无 → nil；runtimeID 空 = 任一条）。
// 用于读取握手上冻结的能力断言（如鉴权引导）：不主动拨号，避免「查一次目录就起一个进程」。
func (pl *Pool) ConnForRuntime(runtimeID string) *Conn {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for _, c := range pl.conns {
		if runtimeID != "" && c.Spec.ID != runtimeID {
			continue
		}
		if c.Client == nil {
			continue
		}
		select {
		case <-c.Client.Done():
			continue // 已断开：视为无连接
		default:
			return c
		}
	}
	return nil
}

// LiveClient 实现 `LiveClientSource`：某 runtime 此刻的活连接（未断开时）；
// pid 为子进程号（0 = 未登记）。**不持池锁做 Done 判断**（那会把收尾路径一起等着）。
func (pl *Pool) LiveClient(runtimeID string) (*acp.Client, int, bool) {
	pl.mu.Lock()
	var c *Conn
	for _, cand := range pl.conns {
		if runtimeID != "" && cand.Spec.ID != runtimeID {
			continue
		}
		if cand.Client != nil {
			c = cand
			break
		}
	}
	pl.mu.Unlock()
	if c == nil {
		return nil, 0, false
	}
	select {
	case <-c.Client.Done():
		return nil, 0, false
	default:
		return c.Client, c.PID, true
	}
}

// GenerationKeysFor 某**基础键**对应的全部代际缓存键（含基础键本身，可能为空）。
//
// 停机/回收要关掉该配置的**所有**代际，而不是只关"当前算出来的那一个键"——代际是内容
// 寻址的，同一配置的旧代际可能仍然活着。匹配用 `BaseKey`（连接自己记着它由哪个基础键
// 派生），不做键字符串的切分：键怎么构造、怎么匹配只在一个地方说话。
func (pl *Pool) GenerationKeysFor(base string) []string {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]string, 0, 2)
	for k, c := range pl.conns {
		if c.BaseKey == base {
			out = append(out, k)
		}
	}
	sort.Strings(out) // 遍历顺序稳定（关闭顺序与日志可复现）
	return out
}

// CloseAll 关掉全部连接并停止空闲回收（daemon 退出/测试收尾）。
//
// 逐条走 `Drop`（而不是先清表再逐条关）：这样"谁才是当前连接"的判定与其它拆除路径**同源**，
// 并发重拨进来的新连接不会被一条晚到的旧收尾置成 stopped。
func (pl *Pool) CloseAll() {
	pl.mu.Lock()
	stop := pl.reapStop
	pl.reapStop = nil
	conns := make([]*Conn, 0, len(pl.conns))
	for _, c := range pl.conns {
		conns = append(conns, c)
	}
	pl.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, c := range conns {
		// 主动停机：先置原因再拆除 —— 停机不是连接故障，不能把会话写成失败态。
		c.Intentional.Store(true)
		pl.Drop(c)
		if c.Client != nil {
			_ = c.Client.Close() // watch 收尾（Drop 幂等）
		}
	}
}

// NativeEnvSource 原生权限/沙箱 env 的来源（adapter 拥有措辞，这里只要结果）。
//
// 签名只用基础类型：实现在 `internal/manager`，这一层不该认识 runtime 根包的类型细节
// （`NativePermissionAdapter.Env` 的两个额外参数由实现侧补齐）。
type NativeEnvSource interface {
	// NativePermissionEnv 该 runtime 在给定原生模式下的进程 env（空模式/未知 → nil）。
	// desc 是人类可读说明（日志与诊断用；**不注入也要给**，否则"我设了策略却没反应"只能靠猜）。
	NativePermissionEnv(runtimeID, mode string) (env []string, desc string)
}

// nativeEnvFor 取该模式的 env（无来源 → 空）。**只做转发**，判据都在实现侧。
func (pl *Pool) nativeEnvFor(runtimeID, mode string) ([]string, string) {
	if pl.native == nil {
		return nil, ""
	}
	return pl.native.NativePermissionEnv(runtimeID, mode)
}
