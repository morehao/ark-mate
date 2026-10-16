package svcprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/pkg/credentials"
	"github.com/morehao/ark-mate/pkg/redact"
	"github.com/morehao/ark-mate/pkg/resource"
)

// ——— P4a：供应商连接自检（M4）的**用例层实现** ———
//
// 与导出（`svcsystem`）同一模板：用例层拥有**判据与文案**（两段自检各自的检查项、结论口径、
// 出路写法、响应装配），**不拥有**事实的取法——档案快照来自 `Ports`（供应商域），
// 运行时目录来自 `Catalog`（模型目录用例）。于是这个包既不 import `manager`、也不 import
// 持久层，只认供应商域的 `Def` 这一个值对象。
//
// 两段，边界是**有意的**：
//
//	本地段（无条件，零出网）：档案存在 → 端点 → 密钥面（四层回落能不能读到、生效值来自哪一层）→
//	  被哪些模型条目挂靠 → 注入变量名与是否并入代际基础键 → 挂靠它的模型条目能不能读到。
//	上游段（**仅当请求体带 `{"upstream": true}`**）：对该 type 已登记的上游端点发**一次**最小请求，
//	  判断"能不能连上 / 鉴权是否通过 / 响应里有几条模型"——只计数，**不落清单、不写缓存、不进 /models**。
//
// 为什么上游段必须显式：它会把用户的密钥发到第三方。`GET` 或页面加载顺手触发它不是"方便"，
// 是把凭据泄漏变成一次浏览的副作用。这条与 MCP 面已有的 `POST /mcps/:id/probe` 同一取向。
//
// 为什么本地段也值得存在（它答不了"密钥对不对"）：它答的是**另外三类**问题，
// 而那三类同样是"开会话那一刻才发现"的（P2）：档案悬空、密钥解不开、type 推不出变量名。

// probeStatus 一条 check 的结论。取值是**对外契约**（前端按它上色，且"无法校验"必须是独立的
// 一档而不是 fail——与 `mate doctor` 的口径一致）。
const (
	probePass    = "pass"
	probeFail    = "fail"
	probeWarn    = "warn"
	probeUnknown = "unknown"
)

// probeUpstreamTimeout 上游段的超时：单次请求、不重试。
//
// 5s 是"用户盯着一个按钮"的量级；超时即 fail（而不是无限等）。
const probeUpstreamTimeout = 5 * time.Second

// probeOptions 自检的可注入面：测试用假 transport 断言"默认零请求"，并模拟四种响应。
type probeOptions struct {
	// transport 注入的 HTTP transport（nil = http.DefaultTransport）。生产路径不设置它。
	transport http.RoundTripper
}

// Ports 自检所需的**供应商域**读面（消费方声明的窄接口，ADR-6）。
//
// 刻意收成四个方法：比这再宽就等于把供应商域的内部形态搬到用例层。四个都只读，且都返回
// **值语义的副本**——域平面自己取锁，用例层不需要、也不该再包一层锁（那会把
// "用例锁 → 域锁"这条边固化下来，同族教训见 `internal/archtest/manager_lock_order_test.go`）。
type Ports interface {
	// Snapshot 取一份**值拷贝**的档案（nil = 不在册）。值语义是判据的一部分：探活要在快照上
	// 解析密钥并把结论写进回执，后续写入不得改变它。
	Snapshot(id string) (*provider.Def, bool)
	// ProviderRefName 该供应商的凭据引用名（逐供应商唯一，ADR-16）——诊断里报的是**键名**，
	// 绝不报值。
	ProviderRefName(providerID string) string
	// ProviderLocator 载体定位串（`db:provider/<id>` / `<dir>/providers.json` / 空）。
	// 它带资源层的代码词，所以只许经 `locatorKind` 折成用户词之后再进文案（ADR-22）。
	ProviderLocator(id string) string
	// ProviderModelRefsReadOnly 有哪些模型条目挂靠在这条档案上（只读，不改任何状态）。
	ProviderModelRefsReadOnly(ctx context.Context, providerID string) []string
}

// Catalog 运行时模型目录的读面（消费方声明的窄接口，ADR-6）。
//
// 为什么是端口而不是 import `svcmodel`：`service/svc*` 之间不许互相 import
// （`layer_isolation_test.go` 的 `TestServicePortsMustNotImportEachOther`）——两个用例包互相
// 认识会让"这份数据归谁"没有答案。自检要的是目录**结论**（source/options/note），
// 至于那份结论怎么来（活会话 / 缓存 / resume 取样）是模型面的不变量。
type Catalog interface {
	ModelCatalog(ctx context.Context) (*dtomodel.ModelCatalogResp, error)
}

// Probe 供应商自检用例（实现 `Prober.ProbeProvider`）。
type Probe struct {
	ports   Ports
	catalog Catalog
}

// NewProbe 造一个自检用例（两个端口由装配侧交出；`catalog` 可以为 nil——那表示本进程没有
// 执行面，`catalog` 那一条会如实记 unknown 而不是崩）。
func NewProbe(ports Ports, catalog Catalog) *Probe {
	return &Probe{ports: ports, catalog: catalog}
}

// ProbeProvider 跑一次连接自检。
//
// 返回 `{ok, checks:[{name,status,detail,remedy}]}`。`ok` 是"没有 fail"（warn/unknown/skip 不算失败）
// ——与 doctor 的口径一致：**无法校验记警告、事实与声明不符才失败**。
func (p *Probe) ProbeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq) (*dtoprovider.ProviderProbeResp, error) {
	return p.probeProvider(ctx, id, req, probeOptions{})
}

// probeProvider 带可注入面（测试专用入口；生产只走 ProbeProvider）。
func (p *Probe) probeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq, opts probeOptions) (*dtoprovider.ProviderProbeResp, error) {
	if err := provider.ValidateProviderID(id); err != nil {
		return nil, err
	}
	// 快照是**值拷贝**（域平面自己取锁）：随后的读取本身就是竞态安全的，用例层不再包锁。
	d, _ := p.ports.Snapshot(id)
	var snap provider.Def
	if d != nil {
		snap = *d
	}

	checks := make([]dtoprovider.ProviderCheckResp, 0, 8)
	checks = append(checks, p.probeLocal(ctx, id, d, &snap)...)
	if req.Upstream {
		checks = append(checks, p.probeUpstream(ctx, &snap, opts))
	}
	resp := &dtoprovider.ProviderProbeResp{ID: id, Upstream: req.Upstream, Checks: checks}
	resp.OK = true
	for _, c := range checks {
		if c.Status == probeFail {
			resp.OK = false
			break
		}
	}
	return resp, nil
}

// probeLocal 本地段（零出网）。
func (p *Probe) probeLocal(ctx context.Context, id string, d *provider.Def, snap *provider.Def) []dtoprovider.ProviderCheckResp {
	out := make([]dtoprovider.ProviderCheckResp, 0, 7)
	// 该供应商的凭据引用名（逐供应商唯一，ADR-16）——诊断里报的是**键名**，绝不报值。
	refName := p.ports.ProviderRefName(id)

	// ① exists：档案存在。
	if d == nil {
		return append(out, dtoprovider.ProviderCheckResp{
			Name: "exists", Status: probeFail,
			Detail: fmt.Sprintf("这条供应商 %q 不存在", id),
			Remedy: "刷新列表；若刚在别处删过它，请同步引用它的 agent 的凭据选择",
		})
	}
	out = append(out, dtoprovider.ProviderCheckResp{
		Name: "exists", Status: probePass, Detail: "这条供应商在册（载体：" + locatorKind(p.ports.ProviderLocator(id)) + "）",
	})

	// ② protocol：本期只支持 OpenAI 兼容端点。
	switch snap.Protocol {
	case string(resource.ProtocolOpenAI):
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "protocol", Status: probePass, Detail: "protocol=openai（OpenAI 兼容端点）",
		})
	case "":
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "protocol", Status: probeWarn, Detail: "protocol 为空：按 openai 处理（写面写入时总是显式落这一个值）",
		})
	default:
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "protocol", Status: probeFail, Detail: "不支持的协议 " + snap.Protocol,
			Remedy: "本期只支持 OpenAI 兼容端点；原生 anthropic/gemini 协议需要各自的 api 映射与鉴权形态",
		})
	}

	// ②′ base_url：缺端点 = 这条供应商的模型不会被交付给任何底座（fail-loud 而不是"照发"）。
	switch {
	case strings.TrimSpace(snap.BaseURL) == "":
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "base_url", Status: probeFail, Detail: "缺端点（base_url 为空）：它的模型不会被交付给任何会话进程",
			Remedy: "在「设置 · 模型」的供应商块补一个 OpenAI 兼容端点（例如 https://api.openai.com/v1）",
		})
	case !strings.HasPrefix(snap.BaseURL, "http://") && !strings.HasPrefix(snap.BaseURL, "https://"):
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "base_url", Status: probeWarn, Detail: "端点不是 http(s) URL：" + snap.BaseURL,
			Remedy: "确认这是一个能被解析的绝对 URL",
		})
	default:
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "base_url", Status: probePass, Detail: "端点 " + snap.BaseURL,
		})
	}

	// ③ secret：密钥面（v20：生效值来自四层回落；权威是库那一列）。
	//
	// 三种情形分开报，因为出路完全不同：库里没有（去设一条）／分层读不了（去修那一层）／
	// 生效值来自 `env`（**不是错误**，但必须说出来——用户会以为面板里那条在生效）。
	switch {
	case snap.Key != "" && snap.KeySource == credentials.SourceEnv:
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probePass,
			Detail: "密钥生效值来自进程环境变量 " + snap.KeyEnvKey + "（层序：继承环境优先于托管文档），" +
				"本应注入 " + refName,
			Remedy: "若面板里保存的那条不是你想用的值，请清掉这个环境变量（或改用它）——" +
				"环境变量在层序中优先，面板里保存的那条会被它遮住",
		})
	case snap.Key != "":
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probePass,
			Detail: "密钥可用（来源 " + string(snap.KeySource) + "），本应注入 " + refName,
		})
	case len(snap.KeyProblems) > 0:
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probeFail,
			Detail: "密钥读不出来：" + strings.Join(snap.KeyProblems, "；"),
			Remedy: "修掉上面那条（层序是 继承环境 > 托管文档 > <cwd>/.env > <MATE_HOME>/.env；" +
				"某一层存在但读不动时解析会停下），或在面板里重设一次密钥",
		})
	default:
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probeFail, Detail: "没有密钥（本应注入 " + refName + "）",
			Remedy: "在「设置 · 模型」的供应商块补一条密钥，或清空引用它的 agent 的凭据选择",
		})
	}

	// ④ refs：被哪些模型条目挂靠；以及这份凭据有没有真的在环上（v19 起引用者只有模型条目）。
	modelIDs := p.ports.ProviderModelRefsReadOnly(ctx, id)
	sort.Strings(modelIDs)
	if len(modelIDs) == 0 {
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "refs", Status: probeWarn, Detail: "当前没有模型条目挂靠在这条档案上（事实，不是错误）",
			Remedy: "在「设置 · 模型」里让它承载模型条目；没有被挂靠的档案不会下发到会话进程",
		})
	} else {
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "refs", Status: probePass,
			Detail: fmt.Sprintf("承载 %d 个模型条目：%v", len(modelIDs), modelIDs),
		})
	}

	// ⑤ delivery：凭据交付面（纯提示，永远 pass）。
	//
	// 报的是**两条通道各是什么**，并给逐供应商的引用名——用户能拿去核对子进程 env 或凭据文档。
	out = append(out, dtoprovider.ProviderCheckResp{
		Name: "delivery", Status: probePass,
		Detail: "凭据引用名 " + refName + "；密钥并入连接代际键（改它会让引用它的会话在下一个轮次边界重建连接）。" +
			"原生凭据文档由平台落盘（0600）；env 通道由平台注入子进程",
	})

	// ⑥ catalog：引用它的 agent 的模型目录能不能读到、有没有被运行时标成不可用的取值。
	// 只读现有取样（会话/缓存/resume），**不新建会话**——与 `GET /models` 同一条路径。
	out = append(out, p.probeLocalCatalog(ctx, refName)...)
	return out
}

// probeLocalCatalog 读一次运行时模型目录（`GET /models` 的同一实现）。
//
// 只有**一个**底座有执行面（构建期固定），所以这里没有"逐个 agent 查一次"这回事。
//
// "本进程没有执行面"与"有执行面但目录读不动"是两件事：前者在装配期就定了（`catalog == nil`），
// 后者是运行期事实——两种都记 unknown（**无法校验**不是失败），但出路不同。
func (p *Probe) probeLocalCatalog(ctx context.Context, refName string) []dtoprovider.ProviderCheckResp {
	if p.catalog == nil {
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probeUnknown,
			Detail: "本进程未启用执行面（无 Orchestrator）：无法读运行时模型目录",
			Remedy: "在 serve 形态下重跑自检",
		}}
	}
	resp, err := p.catalog.ModelCatalog(ctx)
	if err != nil {
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probeUnknown,
			Detail: fmt.Sprintf("运行时模型目录不可读：%v", err),
			Remedy: "确认本构建的运行时已注册且可启动；仍不行就导出诊断包",
		}}
	}
	if resp == nil {
		// "回 nil 且 nil 错误"是**没有结论**的回执：它是装配缺陷（不是用户事实），所以走与
		// "读不到"同一条出路，但文案说清这是进程内部的问题（不把它讲成目录事实）。
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probeUnknown,
			Detail: "运行时模型目录读面没有返回结论（内部装配问题）",
			Remedy: "这属于程序缺陷：导出诊断包并报告（自检本身无法校验目录）",
		}}
	}
	unavailable := make([]string, 0, 2)
	for _, opt := range resp.Options {
		for _, v := range opt.Values {
			if v.Unavailable {
				unavailable = append(unavailable, v.Name+"/"+opt.Name)
			}
		}
	}
	switch {
	case resp.Source == "none" || len(resp.Options) == 0:
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probeUnknown,
			Detail: fmt.Sprintf("模型目录暂不可读（source=%s）：%s", resp.Source, resp.Note),
			Remedy: "先在任一会话上发一条消息，让会话进程报出目录后重跑自检",
		}}
	case len(unavailable) > 0:
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probeWarn,
			Detail: fmt.Sprintf("目录可读，但有条目被运行时标为不可用：%v", unavailable),
			Remedy: "在会话进程内完成登录（按下面返回的 auth_guidance 执行），或检查 " + refName + " 是否正确",
		}}
	default:
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probePass,
			Detail: fmt.Sprintf("模型目录可读（source=%s，%d 个配置项）", resp.Source, len(resp.Options)),
		}}
	}
}

// probeUpstream 上游段：对已登记的端点发**一次**最小请求（5s 超时，不重试，不落库不写缓存）。
//
// 三种结论：
//
//	pass    —— 2xx（鉴权通过、响应可解析）
//	fail    —— 401/403（密钥不对）或网络/超时（连不上）
//	unknown —— 没有端点（**无法校验**，不是失败；与 `mate doctor` 的口径一致）
func (p *Probe) probeUpstream(ctx context.Context, snap *provider.Def, opts probeOptions) dtoprovider.ProviderCheckResp {
	// v12 起上游段就是一次 `GET <base_url>/models` 带 `Authorization: Bearer <key>`——
	// 按**资源字段**探，而不是 v11 那张"type → 厂商 URL"硬编码表（自建网关因此也能自检）。
	if strings.TrimSpace(snap.BaseURL) == "" {
		return dtoprovider.ProviderCheckResp{
			Name: "upstream", Status: probeUnknown,
			Detail: "这条供应商没有端点（base_url 为空）：**无法校验**（不是失败）",
			Remedy: "先在「设置 · 模型」里补一个 OpenAI 兼容端点，再做上游自检",
		}
	}
	if snap.Key == "" {
		return dtoprovider.ProviderCheckResp{
			Name: "upstream", Status: probeFail, Detail: "没有可用密钥，无法发起上游请求",
			Remedy: "先补一条密钥（密钥解不开时上游段必然失败）",
		}
	}
	url := strings.TrimRight(snap.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return dtoprovider.ProviderCheckResp{
			Name: "upstream", Status: probeFail, Detail: "构造上游请求失败：" + err.Error(),
		}
	}
	req.Header.Set("Authorization", "Bearer "+snap.Key)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: probeUpstreamTimeout, Transport: opts.transport}
	resp, err := client.Do(req)
	if err != nil {
		// 错误文本先过 redact：有些库会把 URL（含 query 里的 key）拼进错误。
		detail := redact.String(err.Error())
		status := probeFail
		remedy := "检查网络/代理，以及该 type 的上游端点是否可达"
		if strings.Contains(detail, "timeout") || strings.Contains(detail, "deadline") {
			remedy = "上游在 " + probeUpstreamTimeout.String() + " 内没有响应：检查网络/代理后重试（平台不重试）"
		}
		return dtoprovider.ProviderCheckResp{Name: "upstream", Status: status, Detail: "上游请求失败：" + detail, Remedy: remedy}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		_, detail := countModels(resp, "data")
		return dtoprovider.ProviderCheckResp{
			Name: "upstream", Status: probePass,
			Detail: fmt.Sprintf("上游可达且鉴权通过（HTTP %d，%s）；只计数、不落清单", resp.StatusCode, detail),
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return dtoprovider.ProviderCheckResp{
			Name: "upstream", Status: probeFail,
			Detail: fmt.Sprintf("上游拒绝了这个密钥（HTTP %d）", resp.StatusCode),
			Remedy: "密钥无效/过期/权限不足：到「设置 · 模型」重设密钥后重试",
		}
	default:
		return dtoprovider.ProviderCheckResp{
			Name: "upstream", Status: probeFail,
			Detail: fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode),
			Remedy: "先确认该 type 的上游端点没变；再检查账号状态与网络",
		}
	}
}

// countModels 只读响应体，数出模型条数（**不落库、不写缓存、不进 /models**）。
//
// 解析失败不是失败：拿得到 2xx 就说明"连得上、鉴权过"，条数只是附带信息。
func countModels(resp *http.Response, path string) (int, string) {
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	total := 0
	for total < 1<<20 { // 上限 1 MiB：只为了数个数，不需要读完整份目录
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
			total += n
		}
		if err != nil {
			break
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, "响应不是 JSON（只判可达性）"
	}
	arr, ok := parsed[path].([]any)
	if !ok {
		return 0, "响应里没有模型列表（只判可达性）"
	}
	return len(arr), fmt.Sprintf("响应里有 %d 条模型", len(arr))
}

// locatorKind 把载体定位串折成**用户词**（`db` / `file` / `未知`）。
//
// 为什么要折：原始定位串（`db:provider/<id>` / `<AgentDir>/providers.json`）是**排障标识**，
// 里面带着资源层的代码词（`provider`）。设置 · 模型 那一节的用户面不许出现这类词（ADR-22），
// 而自检结果是要渲染在那一节里的。需要完整定位串的场合是 `mate doctor` 与诊断接口。
func locatorKind(locator string) string {
	switch {
	case strings.HasPrefix(locator, "db:"):
		return "db"
	case locator == "":
		return "未知"
	default:
		return "file"
	}
}
