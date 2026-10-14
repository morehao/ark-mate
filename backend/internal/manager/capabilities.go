package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/redact"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// defWarnOnce 同一条定义问题在一个进程内只 WARN 一次（key 由调用方给，须含 id 与 revision）。
//
// 为什么要有它：`reloadCapabilityDefinitions` 的调用频率不再只是"启动一次"——导入会重装，
// 平台侧网关**每次取数**都会重装（M7-2）。一条坏定义每次都报的后果不是"更显眼"，而是日志
// 被刷成噪声、真问题被埋掉。key 里带 revision 是刻意的：**同一条定义改坏了两次要报两次**。
func (s *Service) defWarnOnce(key, msg string, args ...any) {
	s.mu.Lock()
	if s.defWarned == nil {
		s.defWarned = make(map[string]bool)
	}
	seen := s.defWarned[key]
	s.defWarned[key] = true
	s.mu.Unlock()
	if seen {
		return
	}
	glog.Warnw(context.Background(), msg, args...)
}

// LoadCapabilityDefinitions 加载能力数据面目录并合并（dir 为空 = 仅代码注入/空表）。
// 返回三类合计加载数（坏文件跳过，对齐 LoadAgentDefinitions 容错）。
//
// **语义降级（M6-2，必须写在注释里，避免后人误当事实源）**：自 v8 起，sqlite 模式下本方法
// **不再是装载源**——能力定义的事实源是 DB（`skills`/`mcp_servers`），本方法只剩两个用途：
//  1. **memory 模式**（`MATE_STORE=memory`，或无 DB 文件时的 auto）的装载路径：memory 模式
//     没有持久层，文件是它唯一的持久形态，不给它造第二套存储、也不让能力静默变空表；
//  2. **导入/导出路径**（M6-4 的 `mate capability import|export`）：`LoadDir` 降级为
//     "把存量文件读成定义"的编解码入口。
//
// 登记 `capabilityDir` 的语义也随之降级为"导入/导出目录"（见 service.capabilityDir）。
func (s *Service) LoadCapabilityDefinitions(dir string) int {
	if s.caps == nil {
		return 0
	}
	// 登记目录：导入/导出与（M6-3 之前的）启停写回都按它定位（没有它就只能内存生效，报错给出路）。
	s.mu.Lock()
	s.capabilityDir = dir
	s.mu.Unlock()
	return s.caps.LoadDir(dir, normalizeSkillRuntimeDecls).Total()
}

// LoadCapabilityDefinitionsFromStore 从持久层装载能力定义（sqlite 模式的事实源，M6-2）。
//
// 三步：**清空 → 逐行解码 → 过注册闸门**。顺序不能换：
//   - 先 Reset 是"整体替换"语义的前提（否则库里删掉的行会以幽灵定义留下，见 Registry.Reset）；
//   - 过 `RegisterSkill`/`RegisterMcpServer` 闸门是必须的：定义进 DB 的**写面**要走同一道闸门，
//     但 DB 是可达的（`mate capability import`、将来的 SQL 手工修库），装载是最后一道防线——
//     一手坏定义不该让整个能力面起不来。
//
// 坏行/非法定义**逐条 WARN 跳过、不阻断启动**，与 `LoadDir` 的容错口径逐字一致（用户目录里
// 一个坏文件从来不阻挡服务启动，DB 化不该把这条变严）。返回成功装载的条数。
//
// `importDir` 只用于登记"导入/导出目录"（能力定义仍在文件里的那份存量）；空串 = 不登记。
func (s *Service) LoadCapabilityDefinitionsFromStore(ctx context.Context, importDir string) int {
	n, _ := s.reloadCapabilityDefinitions(ctx, importDir, glog.InfoLevel)
	return n
}

// ReloadCapabilityDefinitions 重新装载定义（**网关每次取数前**调用；M7-2）。
//
// 与 `LoadCapabilityDefinitionsFromStore` 是同一份实现，只有日志档位不同：网关是个长命进程，
// 底座的**每次** tools/list / tools/call 都会触发一次装载，用 Info 档会把日志刷成噪声——而噪声
// 的代价是"真出问题时没人看日志"。逐行的坏定义问题仍走 WARN（但**同一条只报一次**，见 defWarnOnce）。
func (s *Service) ReloadCapabilityDefinitions(ctx context.Context) (int, error) {
	return s.reloadCapabilityDefinitions(ctx, "", glog.DebugLevel)
}

// normalizeSkillRuntimeDecls 把 skill 的投放声明归一到当前 runtime 标识（历史别名，
// 见 runtime.NormalizeID）。它是 `capability.LoadDir` 的归一钩子，也用在 DB 装载与写面。
//
// **必须做，不是美化**：老定义写着 `runtimes: ["deepseekharness"]`，而平台现在拿 `dsh` 去比对
// ——不归一，这条技能**再也不投放**，而界面上它仍是"已启用"（静默失效，正是本仓反复被咬的那类）。
// 归一后，写面/重校会把新值落回定义文件，别名自然会收敛掉。
func normalizeSkillRuntimeDecls(sk *capability.Skill) {
	sk.Runtimes = runtime.NormalizeIDs(sk.Runtimes)
}

// reloadCapabilityDefinitions 装载内核（公开入口与网关共用；level 只作用于汇总行）。
func (s *Service) reloadCapabilityDefinitions(ctx context.Context, importDir string, level glog.Level) (int, error) {
	if s.caps == nil || s.st == nil {
		return 0, fmt.Errorf("能力注册表或持久层未初始化")
	}
	if importDir != "" {
		s.mu.Lock()
		s.capabilityDir = importDir
		s.mu.Unlock()
	}
	// 装进**独立的**新注册表再整体换入（不是"先 Reset 再逐条注册"）：后者会在共享面上留下
	// 一个空的/半装的可观测中间态，而网关每次取数前都重装定义 ⇒ 并发调用必然撞上它
	// （`Registry.ReplaceAll` 的注释记了全过程）。坏定义照样逐行跳过，不影响换面。
	fresh := capability.NewRegistry()
	total := 0

	skills, err := s.st.ListSkillDefinitions(ctx)
	if err != nil {
		s.defWarnOnce("store:skills", "capability: 从持久层读 skill 定义失败（能力面将为空）", "err", err)
		return total, err
	}
	for i := range skills {
		row := skills[i]
		var sk capability.Skill
		if err := json.Unmarshal([]byte(row.Body), &sk); err != nil {
			s.defWarnOnce("skill:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		// id 以**行主键**为准：body 里的 id 与行主键不一致时，行主键是事实（它是 CAS 与审计的键）。
		// 这与文件路径的"文件名兜底"同一思路——文件名/主键是可寻址的那一个。
		if sk.ID != row.ID {
			if sk.ID != "" {
				s.defWarnOnce("skill-id:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", sk.ID)
			}
			sk.ID = row.ID
		}
		normalizeSkillRuntimeDecls(&sk)
		// SourceFile 刻意**不填**：DB 化之后"文件来源"不再成立（计划 §9 的风险项），
		// 库视图的来源口径在 M6-5/M6-6 统一重定义为 db / builtin / imported。
		if err := fresh.RegisterSkill(&sk); err != nil {
			s.defWarnOnce("skill-rej:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	mcps, err := s.st.ListMcpServerDefinitions(ctx)
	if err != nil {
		s.defWarnOnce("store:mcps", "capability: 从持久层读 mcp server 定义失败", "err", err)
		return total, err
	}
	for i := range mcps {
		row := mcps[i]
		var m capability.McpServer
		if err := json.Unmarshal([]byte(row.Body), &m); err != nil {
			s.defWarnOnce("mcp:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp server 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		if m.ID != row.ID {
			if m.ID != "" {
				s.defWarnOnce("mcp-id:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", m.ID)
			}
			m.ID = row.ID
		}
		if err := fresh.RegisterMcpServer(&m); err != nil {
			s.defWarnOnce("mcp-rej:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	// 一次换面：此后所有读者看到的是完整的新目录。
	s.caps.ReplaceAll(fresh)
	logAt(ctx, level, "capability: 定义已从持久层装载", "skills", len(skills), "mcps", len(mcps), "adopted", total)
	return total, nil
}

// RegisterTool 注册/覆盖一个工具目录条目（平台 builtin / 观察兜底 / P2 MCP live）。
func (s *Service) RegisterTool(t capability.Tool) error {
	return s.caps.RegisterTool(&t)
}

// ListSkills 返回 Skill 目录（按 ID 排序）。
func (s *Service) ListSkills() ([]dtocapability.SkillResp, error) {
	items := s.caps.ListSkills()
	out := make([]dtocapability.SkillResp, 0, len(items))
	for _, sk := range items {
		out = append(out, dtocapability.SkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body,
			Runtimes: append([]string(nil), sk.Runtimes...), Scope: sk.EffectiveScope(),
			Enabled: sk.IsEnabled(), Warnings: skillWarnings(sk),
		})
	}
	return out, nil
}

// ListMcpServers 返回 MCP 注册表（按 ID 排序）。headers/凭证只存内部供 P2 注入，不读回。
// 附 live 探测状态（tool_count / last_probe_at / last_probe_error，P2 §5.2）。
func (s *Service) ListMcpServers() ([]dtocapability.McpServerResp, error) {
	items := s.caps.ListMcpServers()
	counts := s.mcpToolCounts()
	probes := s.mcpProbeSnapshot()
	out := make([]dtocapability.McpServerResp, 0, len(items))
	for _, m := range items {
		resp := dtocapability.McpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
			Scope: m.EffectiveScope(), Enabled: m.IsEnabled(),
			EnvKeys:              capability.KeysOf(m.Env),
			SecretEnvKeys:        capability.KeysOf(m.SecretEnv),
			SecretHeaderKeys:     capability.KeysOf(m.SecretHeaders),
			UnresolvedSecretKeys: s.unresolvedSecretKeys(m),
			ToolCount:            counts[m.ID],
		}
		if st, ok := probes[m.ID]; ok {
			if !st.At.IsZero() {
				resp.LastProbeAt = st.At.UTC().Format(time.RFC3339)
			}
			resp.LastProbeErr = st.Err
		}
		out = append(out, resp)
	}
	return out, nil
}

// ListTools 返回 Tool 目录（按 name 排序）。
func (s *Service) ListTools() ([]dtocapability.ToolResp, error) {
	items := s.caps.ListTools()
	out := make([]dtocapability.ToolResp, 0, len(items))
	for _, t := range items {
		out = append(out, dtocapability.ToolResp{
			Name: t.Name, Description: t.Description, Source: t.Source,
			McpServer: t.McpServer, Schema: t.Schema,
		})
	}
	return out, nil
}

// supportedMcpTransports 返回 runtime 声明支持的 MCP 传输（未声明 → 仅 stdio，ACP 全 agent 必须支持）。
// 未知 runtime → ok=false。
func (s *Service) supportedMcpTransports(runtimeID string) ([]string, bool) {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return nil, false
	}
	if len(spec.McpTransports) == 0 {
		return []string{capability.TransportStdio}, true
	}
	return spec.McpTransports, true
}

// unresolvedSecretKeys 列出"绑了引用但当前解析不出来"的密钥键（只读体检，不取回值）。
//
// `store:` 引用用 `McpSecret` 判存在（**读回一个值只为判空**，但那条路上本来就要读它：
// 列表端点不该造第二套"存在性"判据，否则"列表说能解析、注入时说不行"会分叉）。
func (s *Service) unresolvedSecretKeys(m capability.McpServer) []string {
	var out []string
	for _, k := range capability.KeysOf(m.SecretEnv) {
		if !s.secretRefResolvable(m.SecretEnv[k]) {
			out = append(out, k)
		}
	}
	for _, k := range capability.KeysOf(m.SecretHeaders) {
		if !s.secretRefResolvable(m.SecretHeaders[k]) {
			out = append(out, k)
		}
	}
	return out
}

// secretRefResolvable 报告一条引用当前是否"看起来能解析"（词法已在校验期保证，这里只看存在性）。
func (s *Service) secretRefResolvable(ref string) bool {
	switch capability.SecretRefKind(ref) {
	case "store":
		_, err := s.resolveStoreSecret(context.Background(), strings.TrimSpace(ref))
		return err == nil
	case "env":
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), capability.SecretRefEnvPrefix))
		return os.Getenv(name) != ""
	default:
		return false
	}
}

// resolveMcpSecrets 把 server 的明文绑定与密钥引用合成**最终注入值**：
//
//	Env（明文）+ SecretEnv（store:/env: 引用解析后）→ 环境变量
//	Headers（明文）+ SecretHeaders（解析后）        → header
//
// 任一引用解析失败 → 返回全部失败原因（调用方据此跳过注入 + 告警）。失败原因**只含键名与引用**，
// 绝不含密钥值本身——日志不该成为泄漏面。
func (s *Service) resolveMcpSecrets(m capability.McpServer) (env, headers map[string]string, problems []string) {
	env = make(map[string]string, len(m.Env)+len(m.SecretEnv))
	headers = make(map[string]string, len(m.Headers)+len(m.SecretHeaders))
	for k, v := range m.Env {
		env[k] = v
	}
	for k, v := range m.Headers {
		headers[k] = v
	}
	apply := func(field string, bindings map[string]string, dst map[string]string) {
		for _, k := range capability.KeysOf(bindings) {
			ref := bindings[k]
			val, err := s.resolveSecretRef(ref)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s[%q] 引用 %q 无法解析：%v", field, k, ref, err))
				continue
			}
			dst[k] = val
		}
	}
	apply("secret_env", m.SecretEnv, env)
	apply("secret_headers", m.SecretHeaders, headers)
	return env, headers, problems
}

// resolveSecretRef 解析一条密钥引用：
//
//	store:<ns>/<name>  查库里的 `mcp_secrets`（v20 起密钥的权威；没有库时查托管文档）
//	env:<NAME>         进程环境变量（容器/CI 注入；盘上不留痕）
//
// 解析不出来**不是"静默跳过"**，而是明确失败：让 MCP server 带着空密钥跑起来，问题会以
// "tools/list 返回 401"的面目出现在很远的地方。
//
// 旧前缀（v20 之前那套密文机制）在这里**不翻译**：库里的引用已由迁移改写，运行时读到它
// 就是坏数据，翻译只会掩盖它。档案文件里的旧前缀由导入路径显式翻译（见 `TranslateLegacySecretRef`）。
func (s *Service) resolveSecretRef(ref string) (string, error) {
	if err := capability.ValidateSecretRef(ref); err != nil {
		return "", err
	}
	var (
		val string
		err error
	)
	switch capability.SecretRefKind(ref) {
	case "store":
		val, err = s.resolveStoreSecret(context.Background(), strings.TrimSpace(ref))
	case "env":
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), capability.SecretRefEnvPrefix))
		if v := os.Getenv(name); v != "" {
			val = v
		} else {
			err = fmt.Errorf("环境变量 %s 未设置或为空", name)
		}
	default:
		return "", fmt.Errorf("不支持的引用 %q（本期只有 %s<namespace>/<name> 与 %s<ENV_NAME>）",
			ref, capability.SecretRefStorePrefix, capability.SecretRefEnvPrefix)
	}
	if err != nil {
		return "", err
	}
	// 解析出来的明文登记进脱敏闸门的动态表（`pkg/redact` 的第 1 档：按值替换、零误报）。
	//
	// 为什么 MCP 密钥也要登记（v19 及更早只有 provider key 登记）：这些值会被塞进 stdio
	// 子进程的环境变量/请求头，而子进程把凭据打进 stderr 是最常见的形态（"401，token=…"）。
	// 那串会经 `run.error` 的 `stderr_tail` 与事件载荷外露——不登记就只能靠形状正则，
	// 而真实密钥往往没有任何可辨识形状。这里是全仓唯一把引用变成明文的地方，登记放在这里
	// 意味着"凡是被解析过的密钥值，离开进程前必被替换"。
	redact.RegisterDynamicSecret(val)
	return val, nil
}

// McpServersFor 选出可注入某 runtime 的 MCP server 子集（P2 §5.2/§5.3）：
// **stdio 直连** + （有远程 server 时）**一条平台网关条目**（M7-3 的分派）。
//
// 老口径（M7 之前）是"注册表全量 → 该 runtime 的 McpTransports 过滤 → 白名单"，远程 server 由
// 底座自己按传输直连；现在远程一律经网关（P-2），故这里只保留直连那一半，条目的合成在
// `planMcpInjection`（唯一实现，指纹也用它——两处各写一遍会出现"指纹说含 X、注入里没有 X"）。
//
// `sel` 是**会话钉住的能力选定**：新建会话时来自本次请求，恢复会话时来自
// `sessions.capability_json`。两处调用点都必须传它——恢复路径漏传的后果是
// 会话重启后注入集悄悄变回全量默认（用户看不到任何提示）。
//
// v19 起没有"agent 白名单"这一层：会话没钉就是全量（按 runtime transport 过滤）。
func (s *Service) McpServersFor(runtimeID string, sel *CapabilitySelection) []acl.McpServer {
	plan := s.planMcpInjection(runtimeID, sel)
	out := make([]acl.McpServer, 0, len(plan.Direct)+1)
	for _, m := range plan.Direct {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		// §13.3 M1/M6：密钥型绑定在这里**才**解析成明文（定义文件里只有引用）。
		env, headers, problems := s.resolveMcpSecrets(m)
		if len(problems) > 0 {
			// 解析不了就不注入这个 server：半配置的 MCP server 会让 agent 侧报一堆看不懂的连不上，
			// 而少一个工具是"看得见的降级"（日志里有原因、有引用、有出路）。**不静默**：逐条 WARN。
			glog.Warnw(context.Background(), "mcp 注入跳过：密钥引用解析失败", "server", m.ID, "problems", problems)
			continue
		}
		out = append(out, acl.McpServer{
			Name: name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
			Env: env, Headers: headers,
		})
	}
	if plan.Gateway != nil {
		// 网关条目天生无密钥（只有 scope），故直接追加；远程凭据留在网关进程里（§10.6-②）。
		out = append(out, *plan.Gateway)
		if len(plan.Remote) > 0 {
			glog.Debugw(context.Background(), "mcp 注入：远程 server 经平台网关代连",
				"runtime", runtimeID, "remote", len(plan.Remote))
		}
	}
	return out
}

// skillWarnings 把注册表的装载期告警转成 REST 读模型（两处共用一个转换，免漂移）。
func skillWarnings(sk capability.Skill) []dtocapability.SkillWarningResp {
	out := make([]dtocapability.SkillWarningResp, 0, len(sk.Warnings))
	for _, w := range sk.Warnings {
		out = append(out, dtocapability.SkillWarningResp{
			Severity: w.Severity.String(), Pattern: w.Pattern, Message: w.Message,
		})
	}
	return out
}
