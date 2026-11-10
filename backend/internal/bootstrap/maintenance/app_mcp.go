package maintenance

// ——— `mate mcp app --apps=a,b`：运行期应用包工具面的 stdio 桥（WP-C6 接线）———
//
// 一期 WP5 的 app 工具面条目是 `mate mcp app --apps=a,b`（见 sessionruntime appendAppMcpServers）；
// 但那个子命令此前不存在（C4 只到「聚合器 + 注入器」层）。本文件把它补上，使**运行期包 app 的工具
// 真正能被模型调用**（二期 C 的「工具面闭环」）。
//
// 形态（与 `mate mcp platform` 同源）：一条 stdio MCP 服务端，stdout 只走协议帧、诊断走 stderr。
// 与 platform 的差别：本命令只服务**运行期包**——按 --apps 列出要暴露的 app id，从本地应用目录装载
// 这些包，为每个包**在进程内起一个隔离 host 子进程**（`mate app-host`，每包一进程、崩溃隔离），
// 然后用 `apptool.AggregateCompiledAndRuntime` 把它们聚合到一个 MCP 工具面（前缀分派复用
// platformmcp，不重写），最后 `mcp.Serve`。
//
// 为什么不复用内核进程的 host：本命令是**会话侧**拉起的独立子进程（底座经 ACP mcpServers spawn），
// 它与内核是两个进程；在此再 spawn `mate app-host` 是"桥里起 host"——隔离边界仍是每包一进程，
// 陌生代码始终不进本进程调用栈（本进程只做 RPC 转发）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/morehao/ark-mate/internal/core/apppkg"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/infra/apphost"
	"github.com/morehao/ark-mate/internal/infra/apptool"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
	"github.com/morehao/golib/glog"
)

const appMcpUsage = `app 用法：
  mate mcp app --apps=<id1,id2,...>
      运行期应用包的 MCP stdio 服务端：把 ` + "`--apps`" + ` 列出的包（从本地应用目录 <MATE_APPS_DIR 或 ~/.dsh/mate/apps>
      逐包加载）的工具面聚合到一条 stdio MCP server。每包一个隔离 host 子进程（崩溃隔离）。
      本命令由会话注入层按会话 app scope 拉起（见 sessionruntime appendAppMcpServers / RuntimeAppToolScope）。
      stdout 只走协议帧，诊断一律走 stderr。
`

// appMcp `mate mcp app` 进程入口。
func appMcp(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(os.Stderr, appMcpUsage)
		return
	}
	appIDs, err := parseAppsArg(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp app: %v\n\n%s", err, appMcpUsage)
		os.Exit(2)
	}
	if len(appIDs) == 0 {
		// 没有 app：起一条空工具面（不报错——零注入下基本不会走到这里，但握手要成功）。
		serveAppMcp(context.Background(), nil)
		return
	}

	IgnoreSIGPIPE()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go WatchParentExit(ctx, os.Getppid, ParentPollInterval, func() {
		glog.Warnw(context.Background(), "mcp app: 父进程（底座）已消失，开始停机")
		cancel()
	})

	serveAppMcp(ctx, appIDs)
}

// parseAppsArg 解析 `--apps=a,b`（也接受 `--apps a,b`）。空 → 空切片。
func parseAppsArg(args []string) ([]string, error) {
	var raw string
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--apps":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--apps 需要一个 id 列表参数")
			}
			raw = args[i+1]
			found = true
			i++
		case strings.HasPrefix(a, "--apps="):
			raw = strings.TrimPrefix(a, "--apps=")
			found = true
		default:
			return nil, fmt.Errorf("未知参数 %q", a)
		}
	}
	if !found {
		return nil, fmt.Errorf("必须指定 --apps=<id,...>")
	}
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// serveAppMcp 装载 --apps 指定的包、起 host、聚合工具面、Serve MCP 直到 ctx 结束。
func serveAppMcp(ctx context.Context, appIDs []string) {
	hosts := make([]*apphost.Host, 0, len(appIDs))
	// 退出时关所有 host（幂等；逆序）。
	defer func() {
		for i := len(hosts) - 1; i >= 0; i-- {
			_ = hosts[i].Close()
		}
	}()

	runtimeDomains := aggregateRuntimeDomains(ctx, appIDs, &hosts)
	// 无编译期 handler（桥只服务运行期包）；只有运行期域。
	agg, err := apptool.NewAggregator(runtimeDomains)
	if err != nil || agg == nil {
		// 没有域（包都没工具 / 都装不起来）→ 起一条空工具面，握手仍成功（避免会话因一条坏包整条挂）。
		glog.Warnw(ctx, "mcp app: 无可用运行期工具域，起空工具面", "apps", appIDs, "err", err)
		agg = emptyAggregator()
		if agg == nil {
			fmt.Fprintln(os.Stderr, "mcp app: 无法构造工具面")
			return
		}
	}

	err = mcp.Serve(ctx, os.Stdin, os.Stdout, mcp.ServerInfo{
		Name:    "mate-app",
		Version: "1",
		Instructions: "运行期应用包提供的工具（第三方包，经隔离子进程执行）。每个工具名前缀 = 其所属包的" +
			"工具域；调用失败会以 is_error 结果如实返回。",
	}, agg.Handler(), mcp.ServerOptions{})
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled):
		glog.Infow(context.Background(), "mcp app: 收到停机信号，退出")
		return
	default:
		fmt.Fprintf(os.Stderr, "mcp app: %v\n", err)
	}
}

// aggregateRuntimeDomains 逐包装载 + 起 host + 解析运行期工具域，合并成一个 domainKey→handler 映射。
//
// 失败隔离：单包装载/host 起不来 → 跳过该包（日志告警），其它包照常；不因一条坏包让整条工具面起不来。
func aggregateRuntimeDomains(ctx context.Context, appIDs []string, hosts *[]*apphost.Host) map[string]mcp.Handler {
	appsRoot, err := apppkg.DefaultAppsDir()
	if err != nil {
		glog.Warnw(ctx, "mcp app: 解析应用根目录失败", "err", err)
		return nil
	}
	spawn := apphost.ExecSpawn()
	merged := map[string]mcp.Handler{}
	for _, id := range appIDs {
		pkgDir := filepath.Join(appsRoot, id)
		scan, serr := apppkg.Scan(filepath.Dir(pkgDir))
		if serr != nil {
			glog.Warnw(ctx, "mcp app: 扫描包目录失败", "app", id, "err", serr)
			continue
		}
		var pkg *apppkg.Package
		for _, p := range scan.Loaded {
			if p.Manifest != nil && p.Manifest.ID == id {
				pkg = p
				break
			}
		}
		if pkg == nil {
			glog.Warnw(ctx, "mcp app: 包不在本地应用目录或校验未过，跳过", "app", id)
			continue
		}
		h := apphost.NewHost(apphost.HostConfig{
			PackageID:  id,
			PkgDir:     pkg.Dir,
			APIVersion: pkg.Manifest.APIVersion,
			Spawn:      spawn,
		})
		if herr := h.Start(ctx); herr != nil {
			glog.Warnw(ctx, "mcp app: host 启动失败，跳过该包工具", "app", id, "err", herr)
			_ = h.Close()
			continue
		}
		*hosts = append(*hosts, h)
		// 解析该包自述的工具域（按清单声明收窄，取交集，防自述扩张）。
		runner := apphost.NewImplRunner(h)
		invoker := hostInvokerAdapter{runner: runner}
		hs, rerr := apptool.ResolveRuntimeDomains(id, invoker)
		if rerr != nil {
			glog.Warnw(ctx, "mcp app: 读取运行期工具自述失败，跳过该包工具", "app", id, "err", rerr)
			continue
		}
		declared := declaredDomains(pkg.Manifest)
		domains, derr := apptool.SelectRuntimeDomains(hs, declared)
		if derr != nil {
			glog.Warnw(ctx, "mcp app: 声明域与自述不一致，跳过该包工具", "app", id, "err", derr)
			continue
		}
		for dom, hd := range domains {
			if _, dup := merged[dom]; dup {
				glog.Warnw(ctx, "mcp app: 域前缀冲突，后者让位", "domain", dom, "app", id)
				continue
			}
			merged[dom] = hd
		}
	}
	return merged
}

// hostInvokerAdapter 把 apphost.ImplRunner 适配成 apptool.ToolInvoker（ToolSpec→ToolDesc）。
type hostInvokerAdapter struct{ runner *apphost.ImplRunner }

func (a hostInvokerAdapter) ListTools() ([]apptool.ToolDesc, error) {
	specs, err := a.runner.ListTools()
	if err != nil {
		return nil, err
	}
	out := make([]apptool.ToolDesc, 0, len(specs))
	for _, s := range specs {
		out = append(out, apptool.ToolDesc{Name: s.Name, Description: s.Description, InputSchema: []byte(s.InputSchema)})
	}
	return out, nil
}

func (a hostInvokerAdapter) InvokeTool(name string, args []byte) ([]byte, bool, error) {
	return a.runner.InvokeTool(name, args)
}

// declaredDomains 取清单声明的工具域（apppkg.Manifest.Provides.Tools[].Domain）。
func declaredDomains(m *apppkg.Manifest) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m.Provides.Tools))
	for _, td := range m.Provides.Tools {
		out = append(out, td.Domain)
	}
	return out
}

// emptyAggregator 造一个"空工具面"的聚合器（一个稳定占位域，工具集为空），使握手成功。
// 不直接用 NewAggregator(nil)（其拒空）；用一个恒空 handler 的域满足其"至少一个域"约束。
//
// 注意：占位域名**不能含下划线**——platformmcp.New 把域名当工具名第一段，拒绝含 `_` 的前缀
// （历史 bug：这里曾用 "_empty"，New 直接报错返回 nil，导致空工具面构造失败、`mate mcp app` 在
// 无包/包起不来时**直接退出而非空握手**，进而让 dsh 建会话报 "mcp-client initial connection failed"）。
func emptyAggregator() *apptool.Aggregator {
	agg, _ := apptool.NewAggregator(map[string]mcp.Handler{"noop": emptyDomainHandler{}})
	return agg
}

// emptyDomainHandler 是一个永不返回工具的域 handler（占位空工具面用）。
type emptyDomainHandler struct{}

func (emptyDomainHandler) ListTools(context.Context) ([]protocol.McpTool, error) { return nil, nil }
func (emptyDomainHandler) CallTool(context.Context, string, json.RawMessage) (protocol.McpCallResult, error) {
	return protocol.McpCallResult{}, mcp.ErrToolNotFound
}
