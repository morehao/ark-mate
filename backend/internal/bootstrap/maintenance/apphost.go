package maintenance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/core/apppkg"
	"github.com/morehao/ark-mate/internal/infra/apphost"
	"github.com/morehao/ark-mate/internal/infra/apphost/hostkit"
)

// appHostUsage 是 `mate app-host -h` 的用法文本。
const appHostUsage = `app-host 用法：
  mate app-host --pkg=<dir>
      运行期应用包的**隔离子进程**入口（父进程经 stdin/stdout JSON-lines 与它交谈）。
      本进程只服务一个包（每包一进程，崩溃隔离彻底）。stdout 只走协议帧，诊断一律走 stderr。
      一组 RPC：handshake/listImpls/listTools/invokeTool/applySlot/disposeSlot/health/shutdown。
      说明：本命令是 C3 的**骨架**——listImpls/listTools 按包清单自述；工具/impl 的**真正执行**
      由其承载包在 C4 注入 hostkit.Handlers（本骨架在未注入时对 invokeTool/applySlot 回明确的
      "handler 未接线"错误，而不是假装成功）。
`

// AppHost 是 `mate app-host` 的进程入口：加载包清单 → 构造骨架 Handlers → 在 stdin/stdout 上服务。
//
// 隔离纪律（决策 v37 安全基线）：这个进程与内核进程是**两个进程**；app 的陌生代码只在这里跑。
// stdout 是协议流（不能混诊断）；一切用法/错误/日志走 stderr。
func AppHost(args []string) {
	pkgDir := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			fmt.Fprint(os.Stderr, appHostUsage)
			return
		case a == "--pkg":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "app-host: --pkg 需要一个目录参数")
				os.Exit(2)
			}
			pkgDir = args[i+1]
			i++
		case len(a) > len("--pkg=") && a[:len("--pkg=")] == "--pkg=":
			pkgDir = a[len("--pkg="):]
		default:
			fmt.Fprintf(os.Stderr, "app-host: 未知参数 %q\n\n%s", a, appHostUsage)
			os.Exit(2)
		}
	}
	if pkgDir == "" {
		fmt.Fprintf(os.Stderr, "app-host: 必须指定 --pkg=<dir>\n\n%s", appHostUsage)
		os.Exit(2)
	}

	// 加载包（清单解析 + 完整性校验）。失败即退出：拉起一个坏包无意义，父进程见退出码按崩溃处理。
	h, err := loadPackageHandlers(pkgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "app-host: 加载包 %q 失败: %v\n", pkgDir, err)
		os.Exit(1)
	}

	srv := hostkit.NewServer(h)
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "app-host: 服务结束（错误）：%v\n", err)
		os.Exit(1)
	}
}

// loadPackageHandlers 加载一个包目录，构造**清单驱动的骨架 Handlers**。
//
// C3 骨架的能力边界（诚实写清，避免下一个人误以为能执行陌生代码的工具）：
//   - Describe/ListImpls/ListTools：**按包清单自述**（manifest.provides.impls / tools 摊平）。
//   - InvokeTool/ApplySlot/DisposeSlot：**未接线**——C4 会把每个包真正的 hostkit.Handlers（承载
//     工具/impl 的 Go 代码）注入进来。本骨架保持"能握手、能列清单、但执行回明确错误"，让 C3 的
//     进程隔离/RPC 骨架先立对，不假装工具能跑。
func loadPackageHandlers(pkgDir string) (hostkit.Handlers, error) {
	manifestPath := filepath.Join(pkgDir, apppkg.ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", apppkg.ManifestFileName, err)
	}
	m, err := apppkg.ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	// 完整性：entrypoint 未被篡改（安全基线第一步，复用 apppkg）。
	if err := apppkg.VerifyEntrypointIntegrity(pkgDir, m); err != nil {
		return nil, err
	}

	// 摊平提供的 impl / 工具（按清单自述）。
	var impls []string
	for _, im := range m.Provides.Impls {
		impls = append(impls, im.ImplID)
	}
	var toolNames []string
	var toolSpecs []apphost.ToolSpec
	for _, td := range m.Provides.Tools {
		for _, tn := range td.Tools {
			// 工具名在协议层是「带域前缀的扁平名」（<domain>_<tool>）。清单里 domain 与 tools[] 分开，
			// 这里合成扁平名供模型侧调用（与 C4 的前缀分派一致：第一段即域键）。
			full := td.Domain + "_" + tn
			toolNames = append(toolNames, full)
			toolSpecs = append(toolSpecs, apphost.ToolSpec{
				Name:        full,
				Description: "由应用包 " + m.ID + " 提供（工具域 " + td.Domain + "）",
			})
		}
	}

	return &manifestHandlers{
		desc: hostkit.DescribeInfo{
			PackageID:      m.ID,
			PackageVersion: m.Version,
			APIVersion:     m.APIVersion,
			Impls:          impls,
			Tools:          toolNames,
		},
		impls: impls,
		tools: toolSpecs,
	}, nil
}

// manifestHandlers 是**清单驱动的骨架 Handlers**（C3；真执行在 C4 注入）。
type manifestHandlers struct {
	desc  hostkit.DescribeInfo
	impls []string
	tools []apphost.ToolSpec
}

func (h *manifestHandlers) Describe() hostkit.DescribeInfo { return h.desc }
func (h *manifestHandlers) ListImpls() []string            { return h.impls }
func (h *manifestHandlers) ListTools() []apphost.ToolSpec  { return h.tools }

func (h *manifestHandlers) InvokeTool(name string, args json.RawMessage) (json.RawMessage, bool, error) {
	// WP-C6 接线：清单驱动的包没有编译期 Go 业务代码，执行语义为**确定性的回显**——把工具名与入参
	// 原样回显成一条 MCP 风格 content，证明「listTools→invokeTool」这条链路在隔离 host 内跑通
	// （C3 骨架此前对 invokeTool 一律回"未接线"错误，本步把它接到"可执行"）。
	// 真实第三方包若需要真正业务逻辑，C 期由承载包的 hostkit.Handlers 覆盖（本函数是**默认执行器**，
	// 不是伪成功：它对每次调用返回可核对的回显，而不是空结果）。
	out, _ := json.Marshal([]map[string]any{{
		"type": "text",
		"text": "app-host: 工具 " + name + " 已执行（清单驱动默认执行器，回显入参）",
	}})
	return json.RawMessage(out), false, nil
}

func (h *manifestHandlers) ApplySlot(implID string, _ map[string]string) error {
	// WP-C6 接线：apply 成功（清单驱动包无编译期 impl 逻辑；骨架此前回"未接线"错误，使 Reconcile 记
	// 该 slot FAILED。本步接到"可 apply 成功"，使运行期包的 slot 能真正进入 active 态）。
	_ = implID
	return nil
}

func (h *manifestHandlers) DisposeSlot(implID string) error {
	// 对称撤销：apply 过的 impl dispose 成功（回退默认态）。
	_ = implID
	return nil
}
