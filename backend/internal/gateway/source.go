package gateway

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
// `Source` 的**会话归属**：本文件不关心"网关进程被谁拉起"，只认 scope 的 runtime + 会话钉住的
// 白名单；stdio server 不在面里（底座直连，见 `capability.RemoteMcpTransports` 的注释）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/mechanism/credential"

	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/infra/mcp"
	"github.com/morehao/ark-mate/internal/registry"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/golib/glog"
)

// SourceOptions 数据面参数。
type SourceOptions struct {
	Cfg config.Config
	// Adapters runtime adapter 列表（v3.1：网关是装配根的兄弟进程，同样只能由装配根
	// 提供具体 adapter；nil = 无底座可用，装配会失败）。
	Adapters []runtime.Adapter
	// McpsPinned 会话钉住的 MCP 白名单（nil = 未钉 → 全量）。
	McpsPinned *[]string

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
		// 取不到自己的 cwd 也不能让网关起不来：空 cwd 交给 `pkg/credentials`（那一层读不到
		// 就是"没有这一层"，不留半截路径——与 `IsolationEnv` 对空 home 的处置同一条纪律）。
		return "", credCwdSourceOwnGetwd
	}
	return own, credCwdSourceOwnGetwd
}

// MateSource 从平台持久层取数并代理远程 MCP 的 `Source` 实现。
//
// **不经过 manager**（P5a）：网关要的是领域平面与库句柄，而不是会话/编排。两边各自
// `manager.NewService` 一遍时，能力装载与凭据分层会静默分叉（"核心能注入的服务器，
// 网关注入不了"），所以统一从 `registry.Planes` 取同一套装配事实。
type MateSource struct {
	planes      *registry.Planes
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

	mu         sync.Mutex
	lastReload time.Time
	// secretWarned 逐 server 只报一次"密钥引用解析不了"：这条信息随每次取数重新计算，
	// 每次都报会把日志刷成噪声（同 manager.defWarnOnce 的理由）。
	secretWarned map[string]bool
}

// NewSource 打开数据面（按 `Cfg.ResolvedStoreMode()` 分派 sqlite / memory 两条装载路径，
// 与核心进程 `internal/app.loadDefinitions` 同一判据），返回可交给 `Run` 的 Source。
func NewSource(ctx context.Context, opts SourceOptions) (*MateSource, error) {
	if opts.PerServerTimeout <= 0 {
		opts.PerServerTimeout = 8 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &MateSource{
		providerDir:  opts.Cfg.ProviderDir,
		pinned:       opts.McpsPinned,
		perServer:    opts.PerServerTimeout,
		reloadMin:    opts.ReloadMinInterval,
		now:          opts.Now,
		secretWarned: make(map[string]bool),
	}
	// 与核心进程同一套装配：**只注册构建期选中的那一个底座**（见 pkg/runtime/build.go）。
	// 网关子进程与核心必须看同一个底座，否则"平台说 A、网关按 B 过滤"会静默分叉。
	remoteSpecs := make([]runtime.Spec, 0, len(opts.Cfg.RemoteRuntimes))
	for _, rt := range opts.Cfg.RemoteRuntimes {
		remoteSpecs = append(remoteSpecs, runtime.RemoteSpec(rt.ID, rt.Name, rt.Transport, rt.Endpoint, rt.Headers))
	}
	reg, err := runtime.ProductRegistry(opts.Adapters, remoteSpecs)
	if err != nil {
		return nil, fmt.Errorf("网关装配底座: %w", err)
	}
	s.planes = registry.NewPlanes(reg)

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
		s.planes.Credentials.SetLayered(layered)
	}

	// 持久层：sqlite 走 DB（事实源），memory 走文件目录——两侧都必须拿到 `ProviderDir`
	// （providers.json 仍在文件侧，见 internal/app 的同款注释）。
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
		s.planes.Providers.LoadProviderDefinitionsFromStore(ctx, opts.Cfg.ProviderDir)
		s.planes.LoadCapabilityDefinitionsFromStore(ctx, opts.Cfg.CapabilityDir)
	} else {
		// memory 模式没有持久层：文件是唯一事实源（与核心进程逐条一致）。
		s.planes.Providers.LoadProviderDefinitions(opts.Cfg.ProviderDir)
		s.planes.LoadCapabilityDefinitions(opts.Cfg.CapabilityDir)
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
			"tool", name, "outcome", "deny", "candidates", strings.Join(ids, ","))
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
		return mcp.CallResult{}, mcp.ErrToolNotFound
	}
	cctx, cancel := context.WithTimeout(ctx, s.perServer)
	defer cancel()
	// 每次调用**记一条**(server, tool, 结果, 耗时)：这是"底座何时重列工具面 / 收窄是否即时
	// 生效"（设计 §10.6-⑤ 的 E4/E6）唯一的一手证据，不落库（v1 网关无状态）。
	start := s.now()
	res, err := s.pool.CallTool(cctx, target.client, tool, args)
	if err != nil {
		glog.Infow(ctx, "mcp 网关: 工具调用失败",
			"server", defID, "tool", tool, "outcome", "error",
			"err", err, "latency_ms", s.now().Sub(start).Milliseconds())
		return res, err
	}
	glog.Infow(ctx, "mcp 网关: 工具调用完成",
		"server", defID, "tool", tool, "outcome", "ok",
		"latency_ms", s.now().Sub(start).Milliseconds())
	return res, nil
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
	defs, err := s.planes.Mcp.GatewayFace(runtime.SelectedRuntime(), s.pinned)
	if err != nil {
		return nil, err
	}
	out := make([]gatewayServer, 0, len(defs))
	for _, def := range defs {
		// 远程传输只用得到 header（env 是 stdio 子进程的注入面，而 stdio 不进网关）。
		_, headers, problems := s.planes.Mcp.ResolveSecrets(def)
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
	s.planes.Providers.ReloadProviderDefinitionsFromStore(ctx, s.providerDir)
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
