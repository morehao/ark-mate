package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/pkg/redact"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— provider 连接自检（M4） ———
//
// 两段，边界是**有意的**：
//
//	本地段（无条件，零出网）：档案存在 → type 能否推出注入变量名 → 密钥能否解出来 →
//	  被哪些 agent 引用 → 注入变量名与是否并入代际基础键 → 引用它的 agent 的模型目录能不能读到。
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
	probeSkip    = "skip"
	probeUnknown = "unknown"
)

// probeUpstreamTimeout 上游段的超时：单次请求、不重试。
//
// 5s 是"用户盯着一个按钮"的量级；超时即 fail（而不是无限等）。
const probeUpstreamTimeout = 5 * time.Second

// providerProbe 的可注入面：测试用假 transport 断言"默认零请求"，并模拟四种响应。
type probeOptions struct {
	// transport 注入的 HTTP transport（nil = http.DefaultTransport）。生产路径不设置它。
	transport http.RoundTripper
}

// ProbeProvider 跑一次连接自检。
//
// 返回 `{ok, checks:[{name,status,detail,remedy}]}`。`ok` 是"没有 fail"（warn/unknown/skip 不算失败）
// ——与 doctor 的口径一致：**无法校验记警告、事实与声明不符才失败**。
func (s *Service) ProbeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq) (*dtoprovider.ProviderProbeResp, error) {
	return s.probeProvider(ctx, id, req, probeOptions{})
}

// probeProvider 带可注入面（测试专用入口；生产只走 ProbeProvider）。
func (s *Service) probeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq, opts probeOptions) (*dtoprovider.ProviderProbeResp, error) {
	if err := validateProviderID(id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	d := s.providers[id]
	var snap providerDef
	if d != nil {
		snap = *d
	}
	s.mu.Unlock()

	checks := make([]dtoprovider.ProviderCheckResp, 0, 8)
	checks = append(checks, s.probeLocal(ctx, id, d, &snap)...)
	if req.Upstream {
		checks = append(checks, s.probeUpstream(ctx, &snap, opts))
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
func (s *Service) probeLocal(ctx context.Context, id string, d *providerDef, snap *providerDef) []dtoprovider.ProviderCheckResp {
	out := make([]dtoprovider.ProviderCheckResp, 0, 7)
	// 该供应商的凭据引用名（逐供应商唯一，ADR-16）——诊断里报的是**键名**，绝不报值。
	refName := s.providerRefName(id)

	// ① exists：档案存在。
	if d == nil {
		return append(out, dtoprovider.ProviderCheckResp{
			Name: "exists", Status: probeFail,
			Detail: fmt.Sprintf("这条供应商 %q 不存在", id),
			Remedy: "刷新列表；若刚在别处删过它，请同步引用它的 agent 的凭据选择",
		})
	}
	out = append(out, dtoprovider.ProviderCheckResp{
		Name: "exists", Status: probePass, Detail: "这条供应商在册（载体：" + locatorKind(s.providerLocator(id)) + "）",
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

	// ③ secret：有 key_ref 且能解出来。
	switch {
	case snap.Key != "" && snap.KeyRef != "":
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probePass, Detail: "密钥可解密（密文在 vault 里）",
		})
	case snap.Key != "":
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probeWarn, Detail: "密钥只在内存（vault 不可用）：进程重启后会丢失",
			Remedy: "确认 mate home 下能创建 vault.key（或设 MATE_VAULT_KEY），然后重设一次密钥",
		})
	case snap.KeyRef != "" && snap.keyErr != nil:
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probeFail,
			Detail: "密钥密文无法解密（" + snap.KeyRef + "）：" + snap.keyErr.Error(),
			Remedy: "确认 vault.key 未丢失/未被更换（备份清单见 docs/deployment.md），或重设密钥",
		})
	default:
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "secret", Status: probeFail, Detail: "没有密钥（本应注入 " + refName + "）",
			Remedy: "在「设置 · 模型」的供应商块补一条密钥，或清空引用它的 agent 的凭据选择",
		})
	}

	// ④ refs：被哪些 agent 引用 + 这些 agent 的底座（构建期常量，所有 agent 同一个）。
	s.mu.Lock()
	agentIDs := make([]string, 0, 4)
	rtSet := map[string]bool{}
	for agentID, a := range s.agents {
		if a == nil || a.Provider != id {
			continue
		}
		agentIDs = append(agentIDs, agentID)
	}
	if len(agentIDs) > 0 {
		rtSet[runtime.SelectedRuntime()] = true
	}
	s.mu.Unlock()
	sort.Strings(agentIDs)
	runtimes := make([]string, 0, len(rtSet))
	for rt := range rtSet {
		runtimes = append(runtimes, rt)
	}
	sort.Strings(runtimes)
	if len(agentIDs) == 0 {
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "refs", Status: probeWarn, Detail: "当前没有 Agent 使用这条档案（事实，不是错误）",
			Remedy: "在「设置 · Agent 管理」里把某个 Agent 的凭据指向它，或在「设置 · 模型」里让它承载模型条目",
		})
	} else {
		out = append(out, dtoprovider.ProviderCheckResp{
			Name: "refs", Status: probePass,
			Detail: fmt.Sprintf("被 %d 个 Agent 引用：%v（runtime %v）", len(agentIDs), agentIDs, runtimes),
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
	out = append(out, s.probeLocalCatalog(ctx, agentIDs, refName)...)
	return out
}

// probeLocalCatalog 对每个引用它的 agent 读一次模型目录（`GET /models` 的同一实现）。
func (s *Service) probeLocalCatalog(ctx context.Context, agentIDs []string, refName string) []dtoprovider.ProviderCheckResp {
	if len(agentIDs) == 0 {
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probeSkip, Detail: "没有引用它的 Agent：没有目录可查",
		}}
	}
	if s.orch == nil {
		return []dtoprovider.ProviderCheckResp{{
			Name: "catalog", Status: probeUnknown,
			Detail: "本进程未启用执行面（无 Orchestrator）：无法读运行时模型目录",
			Remedy: "在 serve 形态下重跑自检",
		}}
	}
	out := make([]dtoprovider.ProviderCheckResp, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		resp, err := s.ModelCatalog(ctx, agentID)
		if err != nil {
			out = append(out, dtoprovider.ProviderCheckResp{
				Name: "catalog", Status: probeUnknown,
				Detail: fmt.Sprintf("agent %s 的模型目录不可读：%v", agentID, err),
				Remedy: "确认该 Agent 的 runtime 已注册且可启动（见该 Agent 的诊断快照）",
			})
			continue
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
			out = append(out, dtoprovider.ProviderCheckResp{
				Name: "catalog", Status: probeUnknown,
				Detail: fmt.Sprintf("agent %s 的模型目录暂不可读（source=%s）：%s", agentID, resp.Source, resp.Note),
				Remedy: "先在该 Agent 上发一条消息，让会话进程报出目录后重跑自检",
			})
		case len(unavailable) > 0:
			out = append(out, dtoprovider.ProviderCheckResp{
				Name: "catalog", Status: probeWarn,
				Detail: fmt.Sprintf("agent %s 的目录可读，但有条目被运行时标为不可用：%v", agentID, unavailable),
				Remedy: "在会话进程内完成登录（按下面返回的 auth_guidance 执行），或检查 " + refName + " 是否正确",
			})
		default:
			out = append(out, dtoprovider.ProviderCheckResp{
				Name: "catalog", Status: probePass,
				Detail: fmt.Sprintf("agent %s 的模型目录可读（source=%s，%d 个配置项）", agentID, resp.Source, len(resp.Options)),
			})
		}
	}
	return out
}

// probeUpstream 上游段：对已登记的端点发**一次**最小请求（5s 超时，不重试，不落库不写缓存）。
//
// 三种结论：
//
//	pass    —— 2xx（鉴权通过、响应可解析）
//	fail    —— 401/403（密钥不对）或网络/超时（连不上）
//	unknown —— 该 type 未登记端点（**无法校验**，不是失败；与 `mate doctor` 的口径一致）
func (s *Service) probeUpstream(ctx context.Context, snap *providerDef, opts probeOptions) dtoprovider.ProviderCheckResp {
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

// knownProviderType 该 type 是否在已知上游类型表里（决定 type 那条 check 是 pass 还是 warn）。
func knownProviderType(typ string) bool {
	switch typ {
	case "openai", "anthropic", "deepseek", "openrouter", "groq":
		return true
	}
	return false
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
