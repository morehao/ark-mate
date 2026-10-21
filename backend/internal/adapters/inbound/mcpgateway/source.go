package mcpgateway

// ——— 数据面：网关自己读平台的库与凭据文档（M7-2）———
//
// 网关是**独立进程**（底座按 stdio server 拉起），所以它必须自己拿到两样东西：
//  1. **定义与白名单**：走与核心进程同一个装载入口（`manager` 的 `Load…FromStore` / `Load…`），
//     读的是同一份库——故"停用/删除/改 URL"在网关侧下一次取数就生效，不需要换进程、不需要
//     换代际（设计 §10.6-③ 的回报）；
//  2. **密钥明文**：在网关进程内解析（`ResolveMcpSecrets`），底座配置里只有一条 stdio 命令
//     ——「MCP 明文 ⟂ 密钥」这条立场因此从"约定"变成结构事实。
//
// 三条纪律：
//
//   - **取数在每次 tools/list 与 tools/call 前重做**（P-4）。不是"启动读一次"：网关是个长命
//     进程，而定义在另一个进程（核心）里被改。节流开关默认 0（不节流）——正确性优先，实测
//     库只有几十行、一次重装是亚毫秒级；真成为成本再调 `ReloadMinInterval`。
//   - **一台坏 server 不能拖垮整个面**：密钥解析不了 → WARN 跳过（少几个工具是看得见的降级）；
//     远端超时/报错 → 该 server 这一轮没有工具（ERROR 留痕），其余照常。
//   - **绝不把密钥写进日志**：只记 server id、键名、引用名与失败原因（与 `resolveMcpSecrets`
//     同一纪律）。
//
// **调用审计（v31 / T2 第一刀）**：每一次 `tools/call` 除日志之外还落一行 `mcp_call_audits`
// （会话归属、server、工具、三态结果、耗时）。日志回答"刚才发生了什么"，审计表回答"**这个会话**
// 调了什么"——后者按会话聚合，日志做不到（按日期轮转、进程一退就散）。三条取舍：
//   - **写失败只 WARN、不阻断调用**（与事件持久化 C32 同一取舍）：结果已经拿回来了，因为审计
//     没落地把它变成失败，比少一行审计更糟；
//   - **不记工具入参**（入参里常带路径与令牌）；
//   - **会话归属靠核心下发**（`mcp.GatewaySessionEnv`）：网关只带 scope，自己不知道谁拉起了它。
//     memory 模式没有持久层 ⇒ 没有审计面，启动时 INFO 一次（不静默，也不假装写过）。
//
// `Source` 的**会话归属**：本文件不关心"网关进程被谁拉起"，只认 scope 的 runtime + 会话钉住的
// 白名单；stdio server 不在面里（底座直连，见 `capability.RemoteMcpTransports` 的注释）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/mechanism/credential"

	"github.com/morehao/ark-mate/internal/adapters/outbound/mcp"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/config"
	dommcp "github.com/morehao/ark-mate/internal/domain/mcp"
	domprovider "github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// Planes 网关进程需要的**那几个装配面**（由装配根注入，网关不构造领域平面）。
//
// 为什么是接口而不是 `*planes.Planes`：网关是 L6 入站适配，而平面容器住在装配根之下
// （`bootstrap/assembly/planes`，L9）——反过来 import 就是把"适配器认识装配根"钉进依赖图，
// 而这条边正是 C3 要拆的那条。这里只声明它真正用的七个动作，装配根传容器进来即可
// （结构上直接满足，无需适配体）。
//
// 边界的判据是"谁构造"：容器的唯一构造点仍是装配根（核心、网关子进程、doctor 三处各自
// 进程内各造一次，但用的是**同一份** `New`），网关只消费。
type Planes interface {
	// EnableStore 把库句柄接到全部平面（与核心进程同一个入口）。
	EnableStore(st *store.Store)
	// CredentialStore 凭据机制（网关要接分层凭据源）。
	CredentialStore() *credential.Store
	// ProviderPlane 供应商平面（装载 / 重装供应商定义）。
	ProviderPlane() *domprovider.Plane
	// McpPlane MCP 平面（网关面与密钥解析）。
	McpPlane() *dommcp.Plane
	// LoadCapabilityDefinitions 装载定义目录（memory 模式的事实源）。
	LoadCapabilityDefinitions(dir string) int
	// LoadCapabilityDefinitionsFromStore 从库装载定义（sqlite 模式的事实源）。
	LoadCapabilityDefinitionsFromStore(ctx context.Context, importDir string) int
	// ReloadCapabilityDefinitions 每次取数前重装（M7-2）。
	ReloadCapabilityDefinitions(ctx context.Context) (int, error)
}

// SourceOptions 数据面参数。
type SourceOptions struct {
	Cfg config.Config
	// Adapters runtime adapter 列表（v3.1：网关是装配根的兄弟进程，同样只能由装配根
	// 提供具体 adapter；nil = 无底座可用，装配会失败）。
	Adapters []runtime.Adapter
	// McpsPinned 会话钉住的 MCP 白名单（nil = 未钉 → 全量）。
	McpsPinned *[]string
	// NewPlanes 领域平面工厂（**装配根注入**，nil → 装配失败）。
	//
	// 传工厂而不是实例：网关要先用 `opts.Adapters` 造注册表（"只注册构建期选中的那一个底座"），
	// 平面必须建在同一份注册表上；否则"平台说 A、网关按 B 过滤"会静默分叉。
	NewPlanes func(reg *runtime.Registry) Planes

	// Store 注入已打开的持久层（测试用；nil → 按 Cfg 打开）。非 nil 时由调用方负责关闭。
	Store *store.Store
	// Pool 注入连接池（测试用；nil → 自建并在 Close 时关闭）。
	Pool *mcp.Pool

	// PerServerTimeout 单台远端 server 一次操作（list/call）的超时；0 → 8s。
	//
	// 为什么要有它：`tools/list` 会把所有远端 server 的面拼起来，一台挂住的 server 不能把
	// 整次列举拖到天荒地老（底座那边看到的是"网关不回话"，无从分辨是谁的错）。
	PerServerTimeout time.Duration
	// ReloadMinInterval 两次重装定义之间的最小间隔；0 → 每次都重装（默认，见文件头注释）。
	ReloadMinInterval time.Duration
	// Now 时钟注入（测试用）。
	Now func() time.Time

	// CredCwd **凭据 cwd**（v20）：`<cwd>/.env` 那一层的 cwd，即"用户启动 `mate serve` 的目录"。
	//
	// 常规路径下留空——核心会把它经 `mcp.GatewayCredCwdEnv` 放在网关条目的 `env` 里（见
	// `resolveCredCwd` 的优先级：显式入参 > 环境变量 > 网关自己的 cwd）。留出这个字段是为了让
	// 调用方与测试能**显式**给一个值，而不必去摆布进程环境。
	CredCwd string

	// SessionID 本网关进程服务的**平台会话 id**（审计归属）。
	//
	// 常规路径下留空——核心会把它经 `mcp.GatewaySessionEnv` 放在网关条目的 `env` 里（见
	// `resolveSessionID` 的优先级：显式入参 > 环境变量 > 空）。与 `CredCwd` 同理，留出这个
	// 字段是让调用方与测试不必摆布进程环境。
	SessionID string
	// Audit 审计落点（测试用；nil → 用注入/自建的持久层，memory 模式下没有落点）。
	//
	// 为什么留一个注入点：本包要验的两条判据（"一次调用恰好一行"、"写失败不阻断调用"）里，
	// 后者的**唯一**观测方式就是让写必然失败——用一个失败的 sink 比"关掉库句柄"干净得多
	// （关库会让定义重读先失败，于是根本不是审计那条路在出错）。
	Audit AuditSink
}

// AuditSink 审计落点：把一次网关工具调用写成一行审计。
//
// `*store.Store` 结构上满足它（方法名与参数类型相同），装配根与 CLI 不需要适配体。
type AuditSink interface {
	AppendMcpCallAudit(ctx context.Context, rec store.McpCallRecord) error
}

// 凭据 cwd 的来源（日志与测试共用同一组取值；`source` 进日志，判据落在"回落到自己那份"）。
const (
	credCwdSourceOption   = "core-option" // 调用方显式传入
	credCwdSourceEnv      = "core-env"    // 核心经网关条目的 env 传入
	credCwdSourceOwnGetwd = "own-getwd"   // 谁都没给 → 网关自己的 cwd（**必须 WARN**）
)

// resolveCredCwd 决定凭据 cwd：**显式入参 > 环境变量 > 网关自己的 cwd**。
//
// 为什么这一层这么较真：`<cwd>/.env` 是四层回落里的第三层，而网关是**底座 spawn 的子进程**——
// 它的 cwd（通常是会话工作区）是用户没选过的目录。核心与网关各自 `os.Getwd()` 时，同一个引用
// 会在两个进程里解析出不同的值（面板说"取不到"而网关注解得出来，或反过来），而两边各自的测试
// 都是绿的。所以：核心**显式交下来**（`mcp.GatewayCredCwdEnv`），网关只在确实没拿到时才回落，
// 并把回落这件事交给调用方 WARN。
//
// 纯函数（`getenv` 注入）：判据可测，不必摆布进程环境。
func resolveCredCwd(explicit string, getenv func(string) string) (cwd, source string) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, credCwdSourceOption
	}
	if getenv != nil {
		if v := strings.TrimSpace(getenv(mcp.GatewayCredCwdEnv)); v != "" {
			return v, credCwdSourceEnv
		}
	}
	own, err := os.Getwd()
	if err != nil {
		// 取不到自己的 cwd 也不能让网关起不来：空 cwd 交给 `internal/adapters/outbound/credentials`（那一层读不到
		// 就是"没有这一层"，不留半截路径——与 `IsolationEnv` 对空 home 的处置同一条纪律）。
		return "", credCwdSourceOwnGetwd
	}
	return own, credCwdSourceOwnGetwd
}

// resolveSessionID 决定审计归属：**显式入参 > 环境变量 > 空**。
//
// 与 `resolveCredCwd` 同一个形状（纯函数 + getenv 注入，判据可测），但**没有回落兜底**：
// cwd 至少还有个"自己的"可回落，而"哪个会话拉起了我"只存在于核心的记忆里——猜不出来，
// 也不该猜（猜错的审计行比空 session_id 更糟：它会把一次调用记到别的会话上）。
func resolveSessionID(explicit string, getenv func(string) string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	if getenv != nil {
		return strings.TrimSpace(getenv(mcp.GatewaySessionEnv))
	}
	return ""
}

// MateSource 从平台持久层取数并代理远程 MCP 的 `Source` 实现。
//
// **不经过执行面**（P5a）：网关要的是领域平面与库句柄，而不是会话/编排。两边各自
// 构造一遍服务时，能力装载与凭据分层会静默分叉（"核心能注入的服务器，
// 网关注入不了"），所以统一从装配根构造的领域平面取同一套装配事实（C3 4.5 后半：容器在
// `bootstrap/assembly/planes`，网关经 `Planes` 端口注入，不 import 它）。
type MateSource struct {
	planes      Planes
	pool        *mcp.Pool
	ownPool     bool
	st          *store.Store
	ownStore    bool
	providerDir string
	pinned      *[]string
	perServer   time.Duration
	reloadMin   time.Duration
	now         func() time.Time
	// credCwd / credCwdSource 本次装配实际用的凭据 cwd 与它的来源（见 resolveCredCwd；
	// 留在结构体上是为了让日志与测试看的是**同一个已决定的值**，不是各算一遍）。
	credCwd       string
	credCwdSource string
	// sessionID 本次装配认下的平台会话 id（审计归属；空 = 没拿到，审计行照写但 session_id 留空）。
	sessionID string
	// audit 审计落点（nil = memory 模式没有持久层，不写审计表）。
	audit AuditSink
	// auditPrunedAt 上次裁剪审计表的时刻（按 `auditPruneInterval` 节流；启动时先裁一次）。
	auditPrunedAt time.Time

	mu         sync.Mutex
	lastReload time.Time
	// secretWarned 逐 server 只报一次"密钥引用解析不了"：这条信息随每次取数重新计算，
	// 每次都报会把日志刷成噪声（同 `definitions` 面 `warnOnce` 的理由）。
	secretWarned map[string]bool
}

// NewSource 打开数据面（按 `Cfg.ResolvedStoreMode()` 分派 sqlite / memory 两条装载路径，
// 与核心进程 `internal/bootstrap/app.loadDefinitions` 同一判据），返回可交给 `Run` 的 Source。
func NewSource(ctx context.Context, opts SourceOptions) (*MateSource, error) {
	if opts.PerServerTimeout <= 0 {
		opts.PerServerTimeout = 8 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewPlanes == nil {
		// 静默兜底成"没有平面"会让工具面恒空而日志干净——那是最难查的一类。
		return nil, fmt.Errorf("网关装配底座: 装配根未注入领域平面工厂（NewPlanes）")
	}
	s := &MateSource{
		providerDir:  opts.Cfg.ProviderDir,
		pinned:       opts.McpsPinned,
		perServer:    opts.PerServerTimeout,
		reloadMin:    opts.ReloadMinInterval,
		now:          opts.Now,
		secretWarned: make(map[string]bool),
	}
	// 与核心进程同一套装配：**只注册构建期选中的那一个底座**（见 internal/runtime/build.go）。
	// 网关子进程与核心必须看同一个底座，否则"平台说 A、网关按 B 过滤"会静默分叉。
	remoteSpecs := make([]runtime.Spec, 0, len(opts.Cfg.RemoteRuntimes))
	for _, rt := range opts.Cfg.RemoteRuntimes {
		remoteSpecs = append(remoteSpecs, runtime.RemoteSpec(rt.ID, rt.Name, rt.Transport, rt.Endpoint, rt.Headers))
	}
	reg, err := runtime.ProductRegistry(opts.Adapters, remoteSpecs)
	if err != nil {
		return nil, fmt.Errorf("网关装配底座: %w", err)
	}
	s.planes = opts.NewPlanes(reg)

	if opts.Pool != nil {
		s.pool = opts.Pool
	} else {
		s.pool = mcp.NewPool(mcp.PoolOptions{})
		s.ownPool = true
	}

	// 凭据面**先行装配**（v20）：下面 `EnableStore`/装载之后才会解析密钥（`resolveMcpSecrets`
	// 在网关每次取工具面时都会走），而"密钥从哪来"由这一层回答（四层回落的唯一实现）。
	// 网关与核心进程**同一套装配**——两处各写一遍必然分叉，而分叉的表现是
	// "核心能注入的服务器，网关注入不了"（用户只会看到工具少了一半）。
	//
	// **cwd 由核心传入**（`mcp.GatewayCredCwdEnv`，见 `resolveCredCwd`）：`<cwd>/.env` 那一层的
	// 语义是"用户启动 `mate serve` 的目录"，而网关的 cwd 是**底座给的**（会话工作区）——
	// 各自 `os.Getwd()` 会让同一个引用在这里和核心解析出不同的值。
	credCwd, credCwdSource := resolveCredCwd(opts.CredCwd, os.Getenv)
	s.credCwd, s.credCwdSource = credCwd, credCwdSource
	if credCwdSource == credCwdSourceOwnGetwd {
		// 这一条 WARN 就是本裁决的判据：核心没把 cwd 交下来时，"两个进程用了不同的 cwd"
		// 必须看得见，而不是静默地各解析各的。
		glog.Warnw(ctx, "mcp 网关: 核心没有传凭据 cwd，本次用网关自己的 cwd（与核心可能不同）",
			"cwd", credCwd, "env", mcp.GatewayCredCwdEnv)
	} else {
		glog.Infow(ctx, "mcp 网关: 凭据 cwd 已确定", "cwd", credCwd, "source", credCwdSource)
	}
	if layered, _, cErr := credential.OpenCredentialSource(opts.Cfg.HomeDir, credCwd); cErr != nil {
		glog.Warnw(ctx, "mcp 网关: 凭据分层未装配（只有库里的那一份可用）", "err", cErr)
	} else {
		s.planes.CredentialStore().SetLayered(layered)
	}

	// 持久层：sqlite 走 DB（事实源），memory 走文件目录——两侧都必须拿到 `ProviderDir`
	// （providers.json 仍在文件侧，见 internal/bootstrap/app 的同款注释）。
	if st := opts.Store; st != nil {
		s.st, s.ownStore = st, false
	} else if opts.Cfg.ResolvedStoreMode() == config.StoreModeSQLite {
		st, err := store.Open(opts.Cfg.DBPath)
		if err != nil {
			return nil, fmt.Errorf("网关打开持久层 %s: %w", opts.Cfg.DBPath, err)
		}
		s.st, s.ownStore = st, true
	}
	if s.st != nil {
		s.planes.EnableStore(s.st)
		s.planes.ProviderPlane().LoadProviderDefinitionsFromStore(ctx, opts.Cfg.ProviderDir)
		s.planes.LoadCapabilityDefinitionsFromStore(ctx, opts.Cfg.CapabilityDir)
	} else {
		// memory 模式没有持久层：文件是唯一事实源（与核心进程逐条一致）。
		s.planes.ProviderPlane().LoadProviderDefinitions(opts.Cfg.ProviderDir)
		s.planes.LoadCapabilityDefinitions(opts.Cfg.CapabilityDir)
	}
	// 调用审计（v31）：sqlite 模式写 `mcp_call_audits`；memory 模式没有持久层，没有审计面。
	//
	// 归属由**核心**经条目 env 下发（`mcp.GatewaySessionEnv`）：网关只带 scope，自己无从知道
	// "我被哪个会话拉起来了"。判据落在这里的三个分支上——三种形态都必须**说出来**，
	// 因为"审计静默不写"与"审计写了但归属为空"在复盘时看起来一样（都是查不到）。
	s.sessionID = resolveSessionID(opts.SessionID, os.Getenv)
	if opts.Audit != nil {
		s.audit = opts.Audit // 注入的落点优先（测试形态；它在 memory 模式下也成立）
	} else if s.st != nil {
		s.audit = AuditSink(s.st)
	}
	if s.st != nil {
		// 启动时先裁一次：节流只按进程内的时钟走，长命网关进程之间不会互相"记账"。
		s.auditPrunedAt = s.now()
		if removed, pErr := s.st.PruneMcpCallAudits(ctx, s.now()); pErr != nil {
			glog.Warnw(ctx, "mcp 网关: 启动时裁剪审计表失败（不影响调用）", "err", pErr)
		} else if removed > 0 {
			glog.Infow(ctx, "mcp 网关: 启动时裁剪审计表", "deleted", removed)
		}
	}
	switch {
	case s.audit == nil:
		glog.Infow(ctx, "mcp 网关: 没有审计落点（memory 模式没有持久层），本次不写调用审计")
	case s.sessionID == "":
		// 审计行照写，但 session_id 留空："不知道属于谁"也是事实，比装作没有这次调用好。
		glog.Warnw(ctx, "mcp 网关: 没有拿到会话归属，审计行将无法按会话复盘", "env", mcp.GatewaySessionEnv)
	default:
		glog.Infow(ctx, "mcp 网关: 调用审计就绪", "session", s.sessionID)
	}

	s.lastReload = s.now()
	glog.Infow(ctx, "mcp 网关数据面就绪",
		"store", opts.Cfg.ResolvedStoreMode(), "db", opts.Cfg.DBPath,
		"pinned", s.pinned != nil)
	return s, nil
}

// Close 释放网关自己开的东西（注入进来的不关：那是调用方的）。
func (s *MateSource) Close() error {
	if s.ownPool {
		s.pool.Close()
	}
	if s.ownStore && s.st != nil {
		return s.st.Close()
	}
	return nil
}

// Tools 取当下该 scope 的远程工具面（命名空间化后的名字）。
func (s *MateSource) Tools(ctx context.Context) ([]mcp.Tool, error) {
	servers, err := s.servers(ctx)
	if err != nil {
		return nil, err
	}
	type outcome struct {
		idx   int
		tools []mcp.Tool
		err   error
	}
	out := make([]outcome, len(servers))
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			srv := servers[i]
			cctx, cancel := context.WithTimeout(ctx, s.perServer)
			defer cancel()
			res, err := s.pool.ListTools(cctx, srv.client)
			if err != nil {
				out[i] = outcome{idx: i, err: err}
				return
			}
			out[i] = outcome{idx: i, tools: res.Tools}
		}(i)
	}
	wg.Wait()

	// 先按 server 收齐，再**统一命名**：跨 server 的重名只有在这一层才看得见（见 NamespaceTools）。
	payload := make([]mcp.Tool, 0, 16)
	names := make([]capability.NamespacedTool, 0, 16)
	for i, o := range out {
		srv := servers[i]
		if o.err != nil {
			// 一台远端不通/超时：这一轮它没有工具，其余照常（网关是单点，不能因为一台挂住就
			// 让整个工具面消失）。错误进日志——降级必须留痕。
			glog.Errorw(ctx, "mcp 网关: 列举远端工具失败（本轮该 server 无工具）",
				"server", srv.client.ID, "transport", srv.client.Transport, "err", o.err)
			continue
		}
		for _, t := range o.tools {
			if t.Name == "" {
				continue // 无名工具无法被调用，不进面（与探测面的口径一致）
			}
			payload = append(payload, t)
			names = append(names, capability.NamespacedTool{ServerID: srv.def.ID, Raw: t.Name})
		}
	}
	kept, conflicts := capability.NamespaceTools(names)
	for _, c := range conflicts {
		// fail-closed（P-3）：同一个暴露名来自多台 server 时**整组丢弃**，不保留任何一个——
		// 保留"第一个"会让调用静默落到一台不是调用方以为的 server 上。这里必须响亮：用户只会
		// 看到"有些工具没了"，而原因（哪两台 server 撞了名）只有日志说得清。
		owners := make([]string, 0, len(c.Claims))
		for _, cl := range c.Claims {
			owners = append(owners, cl.ServerID+"→"+cl.Raw)
		}
		glog.Errorw(ctx, "mcp 网关: 工具名跨 server 冲突，整组丢弃（fail-closed）",
			"name", c.Name, "claims", strings.Join(owners, ", "))
	}
	tools := make([]mcp.Tool, 0, len(kept))
	for _, i := range kept {
		t := payload[i]
		t.Name = names[i].Name
		tools = append(tools, t)
	}
	glog.Debugw(ctx, "mcp 网关: 工具面已取数",
		"servers", len(servers), "tools", len(tools), "conflicts", len(conflicts))
	return tools, nil
}

// Call 代理一次远端工具调用。**每次都重取定义**（P-4）：这样"停用一台 server"或"轮换密钥"
// 在下一次调用就生效，而不是等底座换代际。
func (s *MateSource) Call(ctx context.Context, name string, args json.RawMessage) (mcp.CallResult, error) {
	servers, err := s.servers(ctx)
	if err != nil {
		return mcp.CallResult{}, err
	}
	ids := make([]string, 0, len(servers))
	for _, srv := range servers {
		ids = append(ids, srv.def.ID)
	}
	defID, tool, ok := capability.SplitGatewayToolName(name, ids)
	if !ok {
		// 未知/已停用的工具：按 MCP 的"未知工具"分线（协议层 -32602），不是内部错误。
		// 否决同样留痕：模型调一个已被收窄掉的工具时，日志里必须能看出"平台拒绝了它"，
		// 而不是"远端没回话"——同样的 (tool, allow/deny) 证据面。
		glog.Infow(ctx, "mcp 网关: 工具调用被拒（不在当下工具面）",
			"tool", name, "outcome", store.McpCallOutcomeDeny, "candidates", strings.Join(ids, ","))
		// 否决也要落审计：复盘时"模型要调一个已被收窄掉的工具"与"远端没回话"是两件事，
		// 而两者在结果里都只是"工具调用失败"。
		s.auditCall(ctx, store.McpCallRecord{Tool: name, Outcome: store.McpCallOutcomeDeny})
		return mcp.CallResult{}, mcp.ErrToolNotFound
	}
	var target *gatewayServer
	for i := range servers {
		if servers[i].def.ID == defID {
			target = &servers[i]
			break
		}
	}
	if target == nil {
		// 选中的 server 在这一轮里消失了（定义刚被改）：同样是 deny，但知道它本该是谁。
		s.auditCall(ctx, store.McpCallRecord{ServerID: defID, Tool: tool, Outcome: store.McpCallOutcomeDeny})
		return mcp.CallResult{}, mcp.ErrToolNotFound
	}
	cctx, cancel := context.WithTimeout(ctx, s.perServer)
	defer cancel()
	// 每次调用**记一条**(server, tool, 结果, 耗时)：这是"底座何时重列工具面 / 收窄是否即时
	// 生效"（设计 §10.6-⑤ 的 E4/E6）唯一的一手证据。当日志之外还要落一行审计（v31）：
	// 日志只回答"刚才发生了什么"，按会话复盘要靠 `mcp_call_audits`。
	start := s.now()
	res, err := s.pool.CallTool(cctx, target.client, tool, args)
	latency := s.now().Sub(start).Milliseconds()
	if err != nil {
		glog.Infow(ctx, "mcp 网关: 工具调用失败",
			"server", defID, "tool", tool, "outcome", store.McpCallOutcomeError,
			"err", err, "latency_ms", latency)
		s.auditCall(ctx, store.McpCallRecord{
			ServerID: defID, Tool: tool, Outcome: store.McpCallOutcomeError,
			ErrorKind: classifyCallError(err), LatencyMS: latency,
		})
		return res, err
	}
	glog.Infow(ctx, "mcp 网关: 工具调用完成",
		"server", defID, "tool", tool, "outcome", store.McpCallOutcomeOK,
		"latency_ms", latency)
	s.auditCall(ctx, store.McpCallRecord{
		ServerID: defID, Tool: tool, Outcome: store.McpCallOutcomeOK, LatencyMS: latency,
	})
	return res, nil
}

// ——— 调用审计的写入面（v31 / T2 第一刀）———

// auditPruneInterval 两次裁剪之间的最小间隔。
//
// 裁剪要全扫一遍 `created_at`（`julianday()` 用不上索引，见 `store.PruneMcpCallAudits` 的理由），
// 所以不能每次调用都裁。一小时一次：表最多两万行，扫它一次是毫秒级，而"晚一小时才删掉过期行"
// 对审计表没有任何影响。
const auditPruneInterval = time.Hour

// auditCall 写一行审计。**永不返回错误、永不改变调用结果**（C32 同一取舍）：
//
// 写失败只 WARN。这是刻意的——结果已经拿回来了，因为"审计行没落地"把一次成功的调用变成失败，
// 比少一行审计糟得多。反过来说，这条 WARN 也**必须**在测试里被钉住（把这里改成 return err，
// 那条"上游成功但审计失败"的用例必须红）。
func (s *MateSource) auditCall(ctx context.Context, rec store.McpCallRecord) {
	if s.audit == nil {
		return // memory 模式：没有持久层，也没有审计面（NewSource 已 INFO 过一次）
	}
	rec.SessionID = s.sessionID
	if rec.RunID == "" {
		rec.RunID = s.openRunID(ctx)
	}
	rec.At = s.now()
	if err := s.audit.AppendMcpCallAudit(ctx, rec); err != nil {
		glog.Warnw(ctx, "mcp 网关: 审计行写入失败（调用不受影响）",
			"server", rec.ServerID, "tool", rec.Tool, "outcome", rec.Outcome, "err", err)
		return
	}
	s.pruneAuditIfDue(ctx)
}

// openRunID 调用发生时该会话**正在进行**的那一轮；空 = 不在轮内/查不到/不唯一。
//
// 为什么不缓存：一轮的长短从几秒到几十分钟，缓存只会让"归属"过期；而这条查询走
// `runs(session_id, …)` 的索引前缀（最多两条行），比一次远端工具调用便宜几个数量级。
// 多条开轮行 = 崩溃残轮，这时"最近一条"只是猜测，宁可留空（同 `store.RunningRuns` 的口径）。
func (s *MateSource) openRunID(ctx context.Context) string {
	if s.st == nil || s.sessionID == "" {
		return ""
	}
	runs, err := s.st.RunningRuns(ctx, s.sessionID)
	if err != nil {
		glog.Debugw(ctx, "mcp 网关: 读进行中的轮失败（审计行的 run_id 留空）", "err", err)
		return ""
	}
	if len(runs) != 1 {
		return ""
	}
	return runs[0].ID
}

// pruneAuditIfDue 按 `auditPruneInterval` 节流裁剪；失败只 WARN（裁剪失败不影响审计继续写）。
func (s *MateSource) pruneAuditIfDue(ctx context.Context) {
	if s.st == nil {
		return // 注入的审计落点没有裁剪面（测试形态）
	}
	now := s.now()
	s.mu.Lock()
	due := now.Sub(s.auditPrunedAt) >= auditPruneInterval
	if due {
		s.auditPrunedAt = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	removed, err := s.st.PruneMcpCallAudits(ctx, now)
	if err != nil {
		glog.Warnw(ctx, "mcp 网关: 裁剪审计表失败", "err", err)
		return
	}
	if removed > 0 {
		glog.Infow(ctx, "mcp 网关: 审计表已裁剪", "deleted", removed)
	}
}

// classifyCallError 把一次调用的错误折成 `error_kind` 的三个取值之一。
//
// 只做"是哪一类"的粗分类：细信息（原始错误串）留在日志里，不进库——它可能带 URL 与
// 上游客体（T2 的风险段：审计表只记元数据）。
func classifyCallError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "upstream"
	}
}

// gatewayServer 一台远程 server 的"定义 + 注入形态"。
type gatewayServer struct {
	def    capability.McpServer
	client mcp.Server
}

// servers 重取定义 → 选址 → 解析密钥，得到当下可用的远程 server 列表（顺序稳定）。
//
// 三处"降级而不是失败"都在这条路上：定义读不出来 → 返回错误（调用方降级成空面 + ERROR）；
// 某台 server 的引用解析不了 → WARN + 跳过它；agent 查不到 → 返回错误（**不**回落成全量，
// 见 `GatewayMcpFace` 的注释）。
func (s *MateSource) servers(ctx context.Context) ([]gatewayServer, error) {
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	defs, err := s.planes.McpPlane().GatewayFace(runtime.SelectedRuntime(), s.pinned)
	if err != nil {
		return nil, err
	}
	out := make([]gatewayServer, 0, len(defs))
	for _, def := range defs {
		// 远程传输只用得到 header（env 是 stdio 子进程的注入面，而 stdio 不进网关）。
		_, headers, problems := s.planes.McpPlane().ResolveSecrets(def)
		if len(problems) > 0 {
			if !s.warnOnce("secret:" + def.ID) {
				glog.Warnw(ctx, "mcp 网关: 密钥引用解析失败，跳过该 server", "server", def.ID, "problems", problems)
			}
			continue
		}
		name := def.Name
		if name == "" {
			name = def.ID
		}
		out = append(out, gatewayServer{
			def: def,
			client: mcp.Server{
				ID: def.ID, Transport: def.Transport, URL: def.URL,
				Headers: headers,
				// 远程传输不用 Command/Args/Env：Env 只在 stdio 子进程有意义，而 stdio 不进网关
				// （底座直连）。这里**刻意不填**：填了会让人以为它生效。
			},
		})
	}
	return out, nil
}

// reload 重新装载定义（受 `ReloadMinInterval` 节流；0 = 不节流）。
func (s *MateSource) reload(ctx context.Context) error {
	if s.st == nil {
		return nil // memory 模式：定义来自文件，由核心进程改写；网关不做文件轮询
	}
	s.mu.Lock()
	if s.reloadMin > 0 && s.now().Sub(s.lastReload) < s.reloadMin {
		s.mu.Unlock()
		return nil
	}
	s.lastReload = s.now()
	s.mu.Unlock()

	if _, err := s.planes.ReloadCapabilityDefinitions(ctx); err != nil {
		return fmt.Errorf("网关重读能力定义失败: %w", err)
	}
	// provider 面也要重读：密钥引用解析与"这条档案还在不在"都在它上面。
	// `providerDir` 必须原样传回：`providers.json` 在文件侧（locator 的取值来源），传空会静默
	// 把密钥解析面砍掉（症状是"所有引用都解析不了"，而不是报错）。
	s.planes.ProviderPlane().ReloadProviderDefinitionsFromStore(ctx, s.providerDir)
	return nil
}

// warnOnce 逐 key 只报一次（返回 true = 已经报过）。
func (s *MateSource) warnOnce(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.secretWarned[key]
	s.secretWarned[key] = true
	return seen
}
