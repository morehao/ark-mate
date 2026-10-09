package gateway

// ——— 数据面：网关自己读平台的库与 vault（M7-2）———
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
// `Source` 的**会话归属**：本文件不关心"网关进程被谁拉起"，只认 `AgentID` + 会话钉住的
// 白名单；stdio server 不在面里（底座直连，见 `capability.RemoteMcpTransports` 的注释）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/config"
	"github.com/morehao/ark-forge/pkg/mcp"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// SourceOptions 数据面参数（`AgentID` 必填）。
type SourceOptions struct {
	Cfg config.Config
	// AgentID 网关条目的 scope（注入时由平台拼出）。
	AgentID string
	// McpsPinned 会话钉住的 MCP 白名单（nil = 未钉 → 回落 agent 声明）。
	McpsPinned *[]string
	Logger     *slog.Logger

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
}

// ForgeSource 从平台持久层取数并代理远程 MCP 的 `Source` 实现。
type ForgeSource struct {
	log       *slog.Logger
	svc       *manager.Service
	pool      *mcp.Pool
	ownPool   bool
	st        *store.Store
	ownStore  bool
	agentDir  string
	agentID   string
	pinned    *[]string
	perServer time.Duration
	reloadMin time.Duration
	now       func() time.Time

	mu         sync.Mutex
	lastReload time.Time
	// secretWarned 逐 server 只报一次"密钥引用解析不了"：这条信息随每次取数重新计算，
	// 每次都报会把日志刷成噪声（同 manager.defWarnOnce 的理由）。
	secretWarned map[string]bool
}

// NewSource 打开数据面（按 `Cfg.ResolvedStoreMode()` 分派 sqlite / memory 两条装载路径，
// 与核心进程 `internal/app.loadDefinitions` 同一判据），返回可交给 `Run` 的 Source。
func NewSource(ctx context.Context, opts SourceOptions) (*ForgeSource, error) {
	if opts.AgentID == "" {
		return nil, fmt.Errorf("网关数据面缺少 agent id")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.PerServerTimeout <= 0 {
		opts.PerServerTimeout = 8 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &ForgeSource{
		log:          opts.Logger,
		agentDir:     opts.Cfg.AgentDir,
		agentID:      opts.AgentID,
		pinned:       opts.McpsPinned,
		perServer:    opts.PerServerTimeout,
		reloadMin:    opts.ReloadMinInterval,
		now:          opts.Now,
		secretWarned: make(map[string]bool),
	}
	s.svc = manager.NewService(runtime.DefaultRegistry(), nil)

	if opts.Pool != nil {
		s.pool = opts.Pool
	} else {
		s.pool = mcp.NewPool(mcp.PoolOptions{Logger: opts.Logger})
		s.ownPool = true
	}

	// 持久层：sqlite 走 DB（事实源），memory 走文件目录——两侧都必须拿到 `AgentDir`
	// （providers.json 与 vault 锚点仍在文件侧，见 internal/app 的同款注释）。
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
		s.svc.EnableStore(s.st)
		s.svc.LoadAgentDefinitionsFromStore(ctx, opts.Cfg.AgentDir)
		s.svc.LoadCapabilityDefinitionsFromStore(ctx, opts.Cfg.CapabilityDir)
	} else {
		// memory 模式没有持久层：文件是唯一事实源（与核心进程逐条一致）。
		s.svc.LoadAgentDefinitions(opts.Cfg.AgentDir)
		s.svc.LoadCapabilityDefinitions(opts.Cfg.CapabilityDir)
	}
	s.lastReload = s.now()
	s.log.Info("mcp 网关数据面就绪",
		"agent", s.agentID, "store", opts.Cfg.ResolvedStoreMode(), "db", opts.Cfg.DBPath,
		"pinned", s.pinned != nil)
	return s, nil
}

// Close 释放网关自己开的东西（注入进来的不关：那是调用方的）。
func (s *ForgeSource) Close() error {
	if s.ownPool {
		s.pool.Close()
	}
	if s.ownStore && s.st != nil {
		return s.st.Close()
	}
	return nil
}

// Tools 取当下该 scope 的远程工具面（命名空间化后的名字）。
func (s *ForgeSource) Tools(ctx context.Context) ([]mcp.Tool, error) {
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

	tools := make([]mcp.Tool, 0, 16)
	for i, o := range out {
		srv := servers[i]
		if o.err != nil {
			// 一台远端不通/超时：这一轮它没有工具，其余照常（网关是单点，不能因为一台挂住就
			// 让整个工具面消失）。错误进日志——降级必须留痕。
			s.log.Error("mcp 网关: 列举远端工具失败（本轮该 server 无工具）",
				"server", srv.client.ID, "transport", srv.client.Transport, "err", o.err)
			continue
		}
		for _, t := range o.tools {
			if t.Name == "" {
				continue // 无名工具无法被调用，不进面（与探测面的口径一致）
			}
			t.Name = capability.GatewayToolName(srv.def.ID, t.Name)
			tools = append(tools, t)
		}
	}
	s.log.Debug("mcp 网关: 工具面已取数", "servers", len(servers), "tools", len(tools))
	return tools, nil
}

// Call 代理一次远端工具调用。**每次都重取定义**（P-4）：这样"停用一台 server"或"轮换密钥"
// 在下一次调用就生效，而不是等底座换代际。
func (s *ForgeSource) Call(ctx context.Context, name string, args json.RawMessage) (mcp.CallResult, error) {
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
	return s.pool.CallTool(cctx, target.client, tool, args)
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
func (s *ForgeSource) servers(ctx context.Context) ([]gatewayServer, error) {
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	defs, err := s.svc.GatewayMcpFace(s.agentID, s.pinned)
	if err != nil {
		return nil, err
	}
	out := make([]gatewayServer, 0, len(defs))
	for _, def := range defs {
		// 远程传输只用得到 header（env 是 stdio 子进程的注入面，而 stdio 不进网关）。
		_, headers, problems := s.svc.ResolveMcpSecrets(def)
		if len(problems) > 0 {
			if !s.warnOnce("secret:" + def.ID) {
				s.log.Warn("mcp 网关: 密钥引用解析失败，跳过该 server", "server", def.ID, "problems", problems)
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
func (s *ForgeSource) reload(ctx context.Context) error {
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

	if _, err := s.svc.ReloadCapabilityDefinitions(ctx); err != nil {
		return fmt.Errorf("网关重读能力定义失败: %w", err)
	}
	// agent 面也要重读：白名单（agent.Mcps）与"agent 是否还在"都在它上面。
	// `agentDir` 必须原样传回：它是 vault 锚点（`providers.json` 在文件侧），传空会静默
	// 把密钥解析面砍掉（症状是"所有引用都解析不了"，而不是报错）。
	s.svc.ReloadAgentDefinitionsFromStore(ctx, s.agentDir)
	return nil
}

// warnOnce 逐 key 只报一次（返回 true = 已经报过）。
func (s *ForgeSource) warnOnce(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.secretWarned[key]
	s.secretWarned[key] = true
	return seen
}
