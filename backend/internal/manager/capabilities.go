package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
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
	s.defs.SetDir(dir)
	return s.caps.LoadDir(dir, skill.NormalizeRuntimeDecls).Total()
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

// skill.NormalizeRuntimeDecls 把 skill 的投放声明归一到当前 runtime 标识（历史别名，
// 见 runtime.NormalizeID）。它是 `capability.LoadDir` 的归一钩子，也用在 DB 装载与写面。
//
// **必须做，不是美化**：老定义写着 `runtimes: ["deepseekharness"]`，而平台现在拿 `dsh` 去比对
// ——不归一，这条技能**再也不投放**，而界面上它仍是"已启用"（静默失效，正是本仓反复被咬的那类）。
// 归一后，写面/重校会把新值落回定义文件，别名自然会收敛掉。

// reloadCapabilityDefinitions 装载内核（公开入口与网关共用；level 只作用于汇总行）。
func (s *Service) reloadCapabilityDefinitions(ctx context.Context, importDir string, level glog.Level) (int, error) {
	if s.caps == nil || s.st == nil {
		return 0, fmt.Errorf("能力注册表或持久层未初始化")
	}
	if importDir != "" {
		s.defs.SetDir(importDir)
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
		skill.NormalizeRuntimeDecls(&sk)
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

// ——— P3d 过渡期转发层（纪律：一行 + TODO）———
//
// 密钥解析面（引用 → 明文、存在性体检、脱敏登记）已落 `domain/mcp.Secrets`（P2a-13）。
// 保留这三个同名转发是因为 `mcp_secret_test.go` 是**跨域集成测试**（真 Service + 真库 +
// 托管镜像对账），它验的正是"装配期注入的那份凭据面"——搬到 `domain/mcp` 的会是另一批纯单测，
// 两件事不互相替代。整个 mcp 平面搬完（P2a 的 mcp 那一半）再一起收口。

// TODO(P4b): 删转发（调用点直接依赖 mcp.Secrets）
func (s *Service) unresolvedSecretKeys(m capability.McpServer) []string {
	return s.mcpSecrets.UnresolvedKeys(m)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Secrets）
func (s *Service) resolveMcpSecrets(m capability.McpServer) (env, headers map[string]string, problems []string) {
	return s.mcpSecrets.Resolve(m)
}

// TODO(P4b): 删转发（测试直接依赖 mcp.Secrets）
func (s *Service) resolveSecretRef(ref string) (string, error) {
	return s.mcpSecrets.ResolveRef(ref)
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

// TODO(P4b): 删转发（调用点直接依赖 skill.Plane）
func (s *Service) ListSkills() ([]dtocapability.SkillResp, error) { return s.skillPlane.ListSkills() }
