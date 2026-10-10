package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— T5：平台模型清单 → runtime 配置文件（v11 M5b/M5c） ———
//
// 这是本方案唯一一块「写坏就让底座起不来」的代码，所以纪律比别处严：
//
//	① 生成是**纯函数**（不读盘、不写盘、不看时钟）→ 结果可被单测逐字节钉住；
//	② 自校验不省（把生成的字节用同一族的解析器读回来）——模板/转义 bug 只有解析器抓得到；
//	③ 落盘走「同目录 temp + rename」并保留上一份 `.bak-<digest 前 8 位>`；
//	④ 摘要（+ 配置指纹）进 `meta` 并**并进代际键**——不并进键，暖进程会继续用旧清单而界面说"已保存"；
//	⑤ **未实测（`ConfigProjection.Tested=false`）一律只读不写**：这是"用实测结论换写入权"。
//
// dsh 侧的失败半径更大（写坏 patch = 拒启），所以它的写入权要等 H6（闸门 1）——在那之前
// 本文件对 dsh 只回答"会生成什么"，不落盘、不改 `--patch` 参数。

// RuntimeConfigState 某 runtime 的模型配置物化状态。
//
// 类型别名（权威定义在 `dto/dtomodel`）：`GET /models` 的 `config` 段、`POST /models/apply` 的
// 响应与这里必须是**同一个形状**——三套等价结构体迟早漂移，而漂移的后果是"看到的状态不是真的"。
type RuntimeConfigState = dtomodel.RuntimeConfigResp

// runtimeConfigDigestKey meta 键的 value 形状：`{"<runtime>": {...}}`。
type runtimeConfigDigestEntry struct {
	Digest     string `json:"digest"`
	GeneratedAt string `json:"generated_at"`
	// GeneratorVersion 生成器版本（格式 + 模板契约的代数）。改模板时 +1，
	// 于是"旧摘要"会被识别成"需要重新物化"而不是"文件被外部改过"。
	GeneratorVersion int `json:"generator_version"`
}

// runtimeConfigGeneratorVersion 生成器版本（与 `ConfigProjection.Format` 配套的模板契约）。
const runtimeConfigGeneratorVersion = 1

// runtimeConfigDigest 当前进程内存里的配置指纹（runtime → digest）。
//
// 它是**代际键的一部分**（`baseKeyFor` 的第三维，见 `generationKey`）：不并进键，暖进程会继续用
// 旧清单，而界面显示"已保存"。读来源是 `meta.runtime_config_digest`（重启后仍生效）。
func (s *Service) runtimeConfigDigest(runtimeID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeCfgDigests[runtimeID]
}

// loadRuntimeConfigDigests 启动期把 meta 里的配置摘要读进内存（并进代际键用）。
func (s *Service) loadRuntimeConfigDigests(ctx context.Context) {
	if s.st == nil {
		return
	}
	raw, ok, err := s.st.GetMeta(ctx, model.MetaKeyRuntimeConfigDigest)
	if err != nil {
		slog.Warn("runtime config: 读 meta.runtime_config_digest 失败", "err", err)
		return
	}
	out := map[string]string{}
	if ok && raw != "" {
		var m map[string]runtimeConfigDigestEntry
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			slog.Warn("runtime config: meta.runtime_config_digest 解析失败（本次按未物化处理）", "err", err)
		}
		for rt, e := range m {
			if e.GeneratorVersion == runtimeConfigGeneratorVersion {
				out[rt] = e.Digest
			}
		}
	}
	s.mu.Lock()
	s.runtimeCfgDigests = out
	s.mu.Unlock()
}

// RuntimeConfigStates 返回各 runtime 的物化状态（`GET /models` 的 `config` 段用）。
func (s *Service) RuntimeConfigStates(ctx context.Context) map[string]RuntimeConfigState {
	out := map[string]RuntimeConfigState{}
	for _, spec := range s.reg.List() {
		st, err := s.runtimeConfigState(ctx, spec.ID, nil)
		if err != nil {
			continue
		}
		out[spec.ID] = *st
	}
	return out
}

// runtimeConfigState 组装某 runtime 的物化状态（不写盘）。
func (s *Service) runtimeConfigState(ctx context.Context, runtimeID string, generated []byte) (*RuntimeConfigState, error) {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", runtimeID), nil)
	}
	proj, declared, err := runtime.ResolveConfigProjection(spec)
	if err != nil {
		return nil, acl.NewError(acl.ClassUnsupported, "config_projection_invalid",
			fmt.Sprintf("runtime %q 的模型配置落点声明非法：%v", runtimeID, err), nil)
	}
	st := &RuntimeConfigState{Runtime: runtimeID}
	if !declared {
		st.Note = "该 runtime 未声明模型配置落点：平台清单对它不可达（T5 不适用）"
		return st, nil
	}
	st.Declared, st.Tested, st.Writable = true, proj.Tested, proj.Writable()
	st.Channel, st.Path = proj.Channel, proj.Path
	if d := s.runtimeConfigDigest(runtimeID); d != "" {
		st.Digest = d
		if e, ok := s.runtimeConfigEntry(ctx, runtimeID); ok {
			st.GeneratedAt = e.GeneratedAt
		}
	}
	if generated != nil {
		st.ModelCount = len(generated)
	}
	if !proj.Tested {
		st.Note = "该落点尚未经真实底座端到端实测（H6）：**只读不写**——平台会生成候选内容，但不会落盘、也不会改启动参数。" +
			proj.UnverifiedReason()
	}
	return st, nil
}

// runtimeConfigEntry 从 meta 里取某 runtime 的物化记录（不存在 → false）。
func (s *Service) runtimeConfigEntry(ctx context.Context, runtimeID string) (runtimeConfigDigestEntry, bool) {
	if s.st == nil {
		return runtimeConfigDigestEntry{}, false
	}
	raw, ok, err := s.st.GetMeta(ctx, model.MetaKeyRuntimeConfigDigest)
	if err != nil || !ok || raw == "" {
		return runtimeConfigDigestEntry{}, false
	}
	var m map[string]runtimeConfigDigestEntry
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return runtimeConfigDigestEntry{}, false
	}
	e, found := m[runtimeID]
	return e, found
}

// runtimeConfigModel 生成器输入里的一条模型（**只带生成需要的东西**：不透明 value 不参与）。
type runtimeConfigModel struct {
	ModelID    string
	Name       string
	Extra      map[string]any
	IsDefault  bool
	ProviderID string
}

// providerProjection 生成器输入里的一条凭据在本 runtime 上的声明片段。
//
// `projections{<runtime>:{package?,baseURL?,api?,compat?}}`（H1）——没有 `baseURL` 的自定义
// 模型物化出去也连不上，所以"平台声明的模型真的能连通"这条承诺以它为输入。
type providerProjection struct {
	ID     string
	Type   string
	HasKey bool
	Body   map[string]any
	// Fragment 该 provider 在本 runtime 上的声明片段（`projections.<runtime>`；可为 nil）。
	Fragment map[string]any
}

// collectRuntimeConfigInput 读清单 + 凭据投影（一次生成的全部输入）。
func (s *Service) collectRuntimeConfigInput(ctx context.Context, runtimeID string) ([]runtimeConfigModel, map[string]providerProjection, error) {
	if s.st == nil {
		return nil, nil, nil
	}
	rows, err := s.st.ListModelDefinitions(ctx, runtimeID)
	if err != nil {
		return nil, nil, err
	}
	models := make([]runtimeConfigModel, 0, len(rows))
	providers := map[string]providerProjection{}
	for i := range rows {
		var body map[string]any
		if err := json.Unmarshal([]byte(rows[i].Body), &body); err != nil {
			return nil, nil, fmt.Errorf("manager: 模型条目 %s 的正文非法: %w", rows[i].ID, err)
		}
		m := runtimeConfigModel{
			ModelID:    bodyString(body, "model_id"),
			Name:       bodyString(body, "name"),
			IsDefault:  rows[i].IsDefault,
			ProviderID: rows[i].ProviderID,
		}
		if extra, ok := body["extra"].(map[string]any); ok {
			m.Extra = extra
		}
		models = append(models, m)
		if _, seen := providers[rows[i].ProviderID]; seen {
			continue
		}
		p, err := s.storeProviderProjection(ctx, rows[i].ProviderID, runtimeID)
		if err != nil {
			return nil, nil, err
		}
		providers[rows[i].ProviderID] = p
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelID < models[j].ModelID })
	return models, providers, nil
}

// storeProviderProjection 读一条 provider 档案在本 runtime 上的投影输入。
func (s *Service) storeProviderProjection(ctx context.Context, providerID, runtimeID string) (providerProjection, error) {
	out := providerProjection{ID: providerID}
	if s.st != nil {
		row, found, err := s.st.GetProviderDefinition(ctx, providerID)
		if err != nil {
			return out, err
		}
		if found {
			var body map[string]any
			if err := json.Unmarshal([]byte(row.Body), &body); err == nil {
				out.Body = body
				out.Type = bodyString(body, "type")
				out.HasKey = bodyString(body, "key_ref") != "" || bodyString(body, "key") != ""
				if projs, ok := body["projections"].(map[string]any); ok {
					if frag, ok := projs[runtimeID].(map[string]any); ok {
						out.Fragment = frag
					}
				}
			}
			return out, nil
		}
	}
	// memory 模式（或 sqlite 里没有这一行）：退到内存注册表。
	s.mu.Lock()
	d := s.providers[providerID]
	s.mu.Unlock()
	if d != nil {
		out.Type, out.HasKey = d.Type, d.Key != "" || d.KeyRef != ""
	}
	return out, nil
}

// buildRuntimeConfig 是 T5 的**唯一算法**：纯函数，按 `Format` 穷举（未知格式报错不静默回落）。
//
// 清单为空 → 返回 (nil, nil)：**空清单不是"写一份空配置"**。两个底座上空的语义都极坏
// （opencode：目录空 → 起不了会话；dsh：空 patch 文件 → **拒启**），所以"没有模型"这条路径
// 的正确形态是"什么都不写、什么都不注入"（见 `MaterializeRuntimeConfig`）。
func buildRuntimeConfig(proj runtime.ConfigProjectionPath, models []runtimeConfigModel, providers map[string]providerProjection) ([]byte, error) {
	if len(models) == 0 {
		return nil, nil
	}
	switch proj.Format {
	case runtime.ConfigFormatOpencodeJSON:
		return buildOpencodeJSON(models, providers)
	case runtime.ConfigFormatDshPatchYAML:
		return buildDshPatchYAML(models, providers)
	default:
		// 不可达（ResolveConfigProjection 已按 ValidConfigFormats 校验）：宁可报错也不要
		// 静默回落成空内容——那等于把用户的清单静默清空。
		return nil, fmt.Errorf("manager: 未知的模型配置格式 %q", proj.Format)
	}
}

// opencodeConfigDoc opencode `opencode.json` 的结构（生成侧的唯一形态）。
//
// 语义要点（v2，逐条对过随包 2.0.26 载荷）：
//   - 顶层键是 **`providers`（复数）**：v2 把 v1 的 `provider` 改了名，并删掉了
//     `enabled_providers`/`disabled_providers`——发那两个键是**静默的未知字段**；
//   - `providers.<route>.models` 是**追加（upsert）**语义：声明的条目进目录、
//     命中同 id 的内置条目则以它为基底覆盖。所以对账时 `advertised_not_declared` 默认非空
//     （内置目录里的模型会一直在），这不是故障；
//   - 顶层 `model` 是默认模型（短形式 `<route>/<model>`）；
//   - 未知顶层键被 schema **剥掉**（zod 默认行为），所以归属标记 `_arkMate` 不会影响解析。
type opencodeConfigDoc struct {
	Schema    string                          `json:"$schema"`
	Providers map[string]opencodeProviderDoc  `json:"providers"`
	Model     string                          `json:"model,omitempty"`
	Ownership map[string]any                  `json:"_arkMate,omitempty"`
}

// opencodeProviderDoc opencode 的一个 provider 路由声明。
type opencodeProviderDoc struct {
	Name     string                       `json:"name,omitempty"`
	Env      []string                     `json:"env,omitempty"`
	Package  string                       `json:"package,omitempty"`
	Settings map[string]any               `json:"settings,omitempty"`
	Models   map[string]opencodeModelDoc  `json:"models,omitempty"`
	Options  map[string]any               `json:"options,omitempty"`
}

// opencodeModelDoc opencode 的模型条目（`models.<id>`，key 就是条目的本地 id）。
//
// 用 map 而不是定形结构体：条目的字段面由**底座**定义（`name` / `disabled` / `modelID` / …），
// 而平台的 `extra` 就是"把这个条目原样写进去"的口子（例如 `extra:{"disabled":true}` 对齐底座原生的
// "从选择器隐藏"）。定形结构体会把 unknown 键静默吃掉，那样"我写了它为什么不生效"会再次出现。
type opencodeModelDoc map[string]any

// buildOpencodeJSON 生成 opencode 的整份配置。
//
// **不写 `apiKey`**：密钥只以 `env: ["<TYPE>_API_KEY"]` **引用**（T1 注入子进程 env）。
// 这条是硬的：内联内容走环境变量，同机同用户 `ps eww` 可见。
func buildOpencodeJSON(models []runtimeConfigModel, providers map[string]providerProjection) ([]byte, error) {
	doc := opencodeConfigDoc{
		Schema:    "https://opencode.ai/config.json",
		Providers: map[string]opencodeProviderDoc{},
		Ownership: map[string]any{"manager": "ark-mate", "generator_version": runtimeConfigGeneratorVersion},
	}
	// provider 顺序由 map 决定不了 → 先收集并排序，保证同输入同字节。
	routes := make([]string, 0, len(providers))
	for id := range providers {
		routes = append(routes, id)
	}
	sort.Strings(routes)

	for _, routeID := range routes {
		p := providers[routeID]
		pd := opencodeProviderDoc{Name: routeID, Models: map[string]opencodeModelDoc{}}
		if k := envKeyOf(p.Type); k != "" {
			pd.Env = []string{k}
		}
		if frag := p.Fragment; frag != nil {
			pd.Package = bodyString(frag, "package")
			if base := bodyString(frag, "settings_baseURL"); base != "" {
				pd.Settings = map[string]any{"baseURL": base}
			} else if st, ok := frag["settings"].(map[string]any); ok {
				pd.Settings = st
			}
			if opt, ok := frag["options"].(map[string]any); ok {
				pd.Options = opt
			}
		}
		doc.Providers[routeID] = pd
	}

	defaultRoute, defaultModel := "", ""
	for _, m := range models {
		pd, ok := doc.Providers[m.ProviderID]
		if !ok {
			continue
		}
		if pd.Models == nil {
			pd.Models = map[string]opencodeModelDoc{}
		}
		md := opencodeModelDoc{}
		if m.Name != "" {
			md["name"] = m.Name
		}
		// extra 原样并入（平台不解释它的形状——那是该 runtime 的 schema 的事）。
		for k, v := range m.Extra {
			if k == "name" || k == "model_id" || k == "hidden" || k == "pinned" || k == "is_default" {
				continue // 这些是平台自己的列/键，不该由 extra 复写
			}
			md[k] = v
		}
		pd.Models[m.ModelID] = md
		doc.Providers[m.ProviderID] = pd
		if m.IsDefault {
			defaultRoute, defaultModel = m.ProviderID, m.ModelID
		}
	}
	if defaultRoute != "" {
		// 短形式；底座在默认值不在可用集时会**静默退到第一个可用模型**（事实 #46），
		// 所以对账/生效回执必须如实报，而不能假定这个值一定生效。
		doc.Model = defaultRoute + "/" + defaultModel
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// buildDshPatchYAML 生成 dsh 的 `--patch` 文档（顶层数组，条目目标是 `id: acp`）。
//
// 三条从随包文档逐字抄来的约束（写错任何一条都不是"没生效"而是"**拒启**"或"无声"）：
//
//  1. 目标条目是 **`id: acp`**——ACP 平面唯一消费模型的行（`fallbackSelection` →
//     `AcpModelControl.selected`）。写 `agent-default-model` 对 ACP 会话是**空操作**
//     （那个平面不消费它），所以生成器**默认不写它**（不写就没有第二个会漂移的事实）。
//  2. patch 里除 `id` 外的键会被**原样拷到目标 entry 上**，而 `config` 是**整体替换**该行的
//     config（不与它合并）⇒ 必须**重述**要保留的每个字段。本生成器只写模型路由与默认模型，
//     其余字段由平台自己的 overlay 层承担（它不是 dsh 的 home patch）。
//  3. **绝不能产出空文件 / 只有注释的文件 / 只有 `---` 的文件**——dsh 逐字说那会 `fails boot`。
//     本函数在 `len(models) == 0` 时由调用方短路（`buildRuntimeConfig` 返回 nil），
//     所以这里只会在"有内容"时被调用。
//
// 输出是**手写的确定性 YAML**（不引第三方 YAML 库：本仓零新依赖，且键序必须可钉）。
func buildDshPatchYAML(models []runtimeConfigModel, providers map[string]providerProjection) ([]byte, error) {
	var b []byte
	b = append(b, []byte("# ark-mate 生成的模型清单 overlay（平台自有文件；经 dsh --patch 递入）\n")...)
	b = append(b, []byte("# 归属标记：本文件只有平台会写；dsh 自己的 cordis.patch.yml 不在此列。\n")...)
	for _, m := range models {
		if !m.IsDefault {
			continue
		}
		// 目标行 `id: acp`：平台会话真正消费的那一行。
		b = append(b, []byte("- id: acp\n  config:\n")...)
		b = append(b, fmt.Sprintf("    provider: %s\n    model: %s\n", yamlQuote(m.ProviderID), yamlQuote(m.ModelID))...)
	}
	// 路由声明：按 route 分组（`llm-pi-ai` 的 `providers.<route>.models[]` 是**整体替换**语义，
	// 所以平台列出的就是该路由的全部——首版**不自动把底座默认值补进来**，那会造出平台没声明的条目）。
	// 本版只写 `llm-pi-ai` 路由；`llm-deepseek` 的内置目录保持不动（它的 `models` 语义是
	// 咨询性目录，写它反而会把内置两条挤掉）。
	if len(providers) > 0 {
		routes := make([]string, 0, len(providers))
		for id := range providers {
			routes = append(routes, id)
		}
		sort.Strings(routes)
		for _, route := range routes {
			p := providers[route]
			b = append(b, []byte("- id: llm-pi-ai\n  config:\n    providers:\n")...)
			b = append(b, fmt.Sprintf("      %s:\n        displayName: %s\n", yamlQuote(route), yamlQuote(route))...)
			if p.Type != "" {
				b = append(b, fmt.Sprintf("        api: %s\n", yamlQuote(p.Type))...)
			}
			if k := envKeyOf(p.Type); k != "" {
				b = append(b, fmt.Sprintf("        apiKeyEnv: %s\n", yamlQuote(k))...)
			}
			if frag := p.Fragment; frag != nil {
				if base := bodyString(frag, "baseURL"); base != "" {
					b = append(b, fmt.Sprintf("        baseURL: %s\n", yamlQuote(base))...)
				}
				if api := bodyString(frag, "api"); api != "" {
					b = append(b, fmt.Sprintf("        api: %s\n", yamlQuote(api))...)
				}
			}
			b = append(b, []byte("        models:\n")...)
			for _, m := range models {
				if m.ProviderID != route {
					continue
				}
				b = append(b, fmt.Sprintf("          - id: %s\n", yamlQuote(m.ModelID))...)
				if m.Name != "" {
					b = append(b, fmt.Sprintf("            name: %s\n", yamlQuote(m.Name))...)
				}
			}
		}
	}
	return b, nil
}

// yamlQuote 给 YAML 标量加引号（**总是加**：模型 id 里可能出现 `:`、`/`、`#` 这些在裸标量里
// 有意义的字符，靠"看起来不需要引号"判断迟早出事）。
func yamlQuote(s string) string {
	return `"` + yamlEscape(s) + `"`
}

// yamlEscape 只转义 JSON 字符串里必须转义的字符（双引号 YAML 标量与 JSON 字符串规则一致）。
func yamlEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(b[1 : len(b)-1])
}

// selfCheckRuntimeConfig 自校验：把生成的字节用**同一族的解析器**读回来。
//
// 这一步不能省，也不能偷懒成"检查非空"：dsh 侧解析失败 = **拒启**，而模板/转义 bug
// 只有解析器抓得到。校验**只判"能不能被解析成预期形状"**，不判"底座认不认这些字段"
// （那是 T8 对账与 H6 的事——`schemastery` 默认 strict=false，未知字段会被静默合并）。
func selfCheckRuntimeConfig(proj runtime.ConfigProjectionPath, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("自校验：生成内容为空")
	}
	switch proj.Format {
	case runtime.ConfigFormatOpencodeJSON:
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("自校验：opencode 配置不是合法 JSON: %w", err)
		}
		provs, ok := doc["providers"].(map[string]any)
		if !ok || len(provs) == 0 {
			return fmt.Errorf("自校验：缺少 providers（复数）——v1 的 `provider`/`enabled_providers` 在 v2 上是静默的未知字段")
		}
		for route, v := range provs {
			pd, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("自校验：providers.%s 不是对象", route)
			}
			if _, ok := pd["models"].(map[string]any); !ok {
				return fmt.Errorf("自校验：providers.%s 缺少 models", route)
			}
		}
	case runtime.ConfigFormatDshPatchYAML:
		lines, arrayItems := 0, 0
		for _, ln := range splitLines(data) {
			t := trimSpace(ln)
			if t == "" || hasPrefix(t, "#") {
				continue
			}
			lines++
			if hasPrefix(t, "- id:") {
				arrayItems++
			}
		}
		if lines == 0 {
			// dsh 逐字：`an empty or comments-only file fails boot`。
			return fmt.Errorf("自校验：dsh patch 只有注释/空行——这会让 dsh **拒启**（必须写出字面量 [] 或干脆不传 --patch）")
		}
		if arrayItems == 0 {
			// 顶层必须是数组，每项一个 `- id:`（注释行不计）。
			return fmt.Errorf("自校验：dsh patch 必须是顶层数组（每项一个 `- id:` 条目）")
		}
	default:
		return fmt.Errorf("自校验：未知格式 %q", proj.Format)
	}
	return nil
}

// MaterializeRuntimeConfig 物化某 runtime 的模型配置：生成 → 自校验 → 落盘/注入 → 摘要入 meta。
//
// `force=true`（`POST /models/apply`）只是"重跑一遍"，不会绕过任何一道门：未实测仍然只读，
// 自校验失败仍然一个字节都不写。
func (s *Service) MaterializeRuntimeConfig(ctx context.Context, runtimeID string, force bool) (*RuntimeConfigState, error) {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", runtimeID), nil)
	}
	proj, declared, err := runtime.ResolveConfigProjection(spec)
	if err != nil || !declared {
		// 未声明：不是错误（该 runtime 不承载平台清单），但物化无从谈起——回如实状态。
		st, serr := s.runtimeConfigState(ctx, runtimeID, nil)
		if serr != nil {
			return nil, serr
		}
		return st, nil
	}
	models, providers, err := s.collectRuntimeConfigInput(ctx, runtimeID)
	if err != nil {
		return nil, err
	}
	// ① 生成（纯函数）。
	data, err := buildRuntimeConfig(proj, models, providers)
	if err != nil {
		return nil, configWriteFailed(err, "生成")
	}
	if data == nil {
		// 清单为空：**什么都不写、什么都不注入**（空配置在两个底座上都是坏输入）。
		st, serr := s.runtimeConfigState(ctx, runtimeID, nil)
		if serr != nil {
			return nil, serr
		}
		st.ModelCount = 0
		st.Changed = false
		st.Note = "平台清单为空：不生成、不落盘、不注入（空配置会让 opencode 起不了会话、让 dsh 拒启）"
		return st, nil
	}
	// ② 自校验（失败即中止，一个字节都不写）。
	if err := selfCheckRuntimeConfig(proj, data); err != nil {
		return nil, configWriteFailed(err, "自校验")
	}
	// ②′ 底座级校验门（仅 dsh 的 `cli_patch_arg`）：把候选**作为 `--patch`** 递进去跑一次 dump。
	// 判据是 exitCode==0 **且 diagnostics 为空** **且 complete==true**——只看退出码会漏掉
	// 「目标 entry id 不存在」（那是 warning、退出码 0）。未实测时这一步不跑（因为压根不写）。
	if proj.Writable() {
		if err := s.validateDshCandidateIfWritable(ctx, spec, proj, data); err != nil {
			return nil, configWriteFailed(err, "底座校验门")
		}
	}
	digest := sha256Hex(data)

	st, serr := s.runtimeConfigState(ctx, runtimeID, data)
	if serr != nil {
		return nil, serr
	}
	st.ModelCount = len(models)

	if !proj.Writable() {
		// 未实测（或未声明）= 只读：把"会写成什么"如实交出来，但不落盘、不改启动参数。
		st.Digest, st.Pending, st.Changed = digest, true, false
		if st.Note == "" {
			st.Note = "落点未实测：只读（生成候选成功，未落盘）。实测通过后写入权自动打开"
		}
		// 供 H6 探针取用：未实测时也把候选内容记进内存，`RuntimeConfigCandidate` 能取到它。
		s.mu.Lock()
		if s.runtimeCfgCandidates == nil {
			s.runtimeCfgCandidates = map[string]string{}
		}
		s.runtimeCfgCandidates[runtimeID] = string(data)
		s.mu.Unlock()
		return st, nil
	}

	// ③ 交付：写文件（同目录 temp + rename + 上一份 `.bak-<digest 前 8>`）……
	prev := s.runtimeConfigDigest(runtimeID)
	st.Changed = prev != digest
	if st.Changed {
		if err := writeFileAtomic(proj.Path, data, 0o600); err != nil {
			return nil, configWriteFailed(err, "落盘")
		}
		if prev != "" {
			if err := writeFileAtomic(proj.Path+".bak-"+prev[:min(8, len(prev))], data, 0o600); err != nil {
				slog.Warn("runtime config: 保留上一份失败（不影响本次写入）", "runtime", runtimeID, "err", err)
			}
		}
	}
	// ……并按通道把同一份内容交给底座（env_content：内联注入；cli_patch_arg：启动参数）。
	if err := s.applyRuntimeConfigDelivery(runtimeID, proj, data); err != nil {
		return nil, configWriteFailed(err, "交付")
	}

	// ④ 摘要入 meta + 并进代际键。
	if err := s.recordRuntimeConfigDigest(ctx, runtimeID, digest); err != nil {
		return nil, err
	}
	st.Digest = digest
	st.Pending = true
	st.Note = "已写入并交给底座；**新进程启动时读它**——此刻底座的目录仍是旧的那份。改清单会让引用该 runtime 的会话在轮次边界换代际"
	if !st.Changed {
		st.Note = "内容与上一份相同（幂等重放）：没有重写任何字节"
	}
	_ = force
	_ = spec
	return st, nil
}

// RuntimeConfigCandidate 返回最近一次生成的候选内容（未实测时也保留；H6 探针与诊断用）。
func (s *Service) RuntimeConfigCandidate(runtimeID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeCfgCandidates[runtimeID]
}

// applyRuntimeConfigDelivery 按 `Channel` 把内容交给底座。
//
// `env_content`：把同一份 JSON 写进该 spec 的 `Launch.Env`（**同键替换**，否则隔离表里那条
// 空的 `OPENCODE_CONFIG_CONTENT=` 会因为"先出现者胜"而把我们的值吃掉）。
// `cli_patch_arg`：dsh 的 `--patch <绝对路径>` 由 adapter 的 Args 承担（M5c 落地）。
func (s *Service) applyRuntimeConfigDelivery(runtimeID string, proj runtime.ConfigProjectionPath, data []byte) error {
	switch proj.Channel {
	case runtime.ConfigChannelEnvContent:
		key := "OPENCODE_CONFIG_CONTENT"
		if runtimeID != "opencode" {
			return fmt.Errorf("manager: runtime %q 声明了 env_content 通道，但平台不知道该注入哪个环境变量名", runtimeID)
		}
		return s.reg.SetLaunchEnv(runtimeID, key, string(data))
	case runtime.ConfigChannelCLIPatchArg:
		// dsh：文件已经由调用方写好，这里把 `--patch <绝对路径>` 同步进启动参数。
		s.mu.Lock()
		if s.runtimeCfgCandidates == nil {
			s.runtimeCfgCandidates = map[string]string{}
		}
		s.runtimeCfgCandidates[runtimeID] = string(data)
		s.mu.Unlock()
		return s.syncPatchArg(runtimeID, proj.Path)
	case runtime.ConfigChannelDirRead:
		// 只写文件（没有额外通道）。文件已经在调用方写好。
		return nil
	default:
		return fmt.Errorf("manager: 未知的模型配置通道 %q", proj.Channel)
	}
}

// recordRuntimeConfigDigest 把本次摘要写进 meta 与内存（内存里那份并进代际键）。
func (s *Service) recordRuntimeConfigDigest(ctx context.Context, runtimeID, digest string) error {
	if s.st == nil {
		return nil
	}
	m := map[string]runtimeConfigDigestEntry{}
	if raw, ok, err := s.st.GetMeta(ctx, model.MetaKeyRuntimeConfigDigest); err == nil && ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	m[runtimeID] = runtimeConfigDigestEntry{
		Digest:           digest,
		GeneratedAt:      model.FormatTime(time.Now().UTC()),
		GeneratorVersion: runtimeConfigGeneratorVersion,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := s.st.SetMeta(ctx, model.MetaKeyRuntimeConfigDigest, string(b)); err != nil {
		return err
	}
	s.mu.Lock()
	if s.runtimeCfgDigests == nil {
		s.runtimeCfgDigests = map[string]string{}
	}
	s.runtimeCfgDigests[runtimeID] = digest
	s.mu.Unlock()
	return nil
}

// configWriteFailed T5 的失败出口：**服务侧失败**（50001），并带上"上一份文件是否仍完好"。
//
// 为什么必须说清后一份：dsh 侧配置文件不可解析 = **拒启**，所以用户最关心的第一件事就是
// "现在这份还能不能启动"。三步都没走完就中止（生成/自校验/落盘失败都在覆盖之前），
// 所以答案是"上一份完好"——这句必须说出来，而不是让用户去猜。
func configWriteFailed(cause error, stage string) error {
	return acl.NewError(acl.ClassService, "config_write_failed",
		fmt.Sprintf("模型配置物化失败（%s 阶段）：%v。**上一份配置仍完好**（失败发生在覆盖之前或已回退），"+
			"底座可以照常启动；修掉原因后重试 POST /models/apply", stage, cause), nil)
}

// sha256Hex 内容摘要（`meta.runtime_config_digest` 与代际键都用它）。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// splitLines / trimSpace / hasPrefix 是标准库的薄封装（避免在本文件里散落 strings 调用，
// 也便于将来把 YAML 解析换成真解析器时只改一处）。
func splitLines(b []byte) []string {
	out := []string{}
	cur := make([]byte, 0, 64)
	for _, c := range b {
		if c == '\n' {
			out = append(out, string(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, c)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r' || s[j-1] == '\n') {
		j--
	}
	return s[i:j]
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// fileExists 判断路径存在（doctor 与状态面用）。
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(filepath.Clean(p))
	return err == nil
}

// ApplyModels 强制重新物化（`POST /models/apply`）。runtimeID 为空 = 全部已声明落点的 runtime。
//
// 它**不绕过任何一道门**：未实测仍然只读、清单为空仍然什么都不写、自校验失败仍然一个字节不写。
// 返回里也没有 `advertised`——那要新进程才拿得到。
func (s *Service) ApplyModels(ctx context.Context, runtimeID string) (*dtomodel.ModelApplyResp, error) {
	ids := []string{}
	if runtimeID != "" {
		if _, ok := s.reg.Get(runtimeID); !ok {
			return nil, acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", runtimeID), nil)
		}
		ids = append(ids, runtimeID)
	} else {
		for _, spec := range s.reg.List() {
			if _, declared, err := runtime.ResolveConfigProjection(spec); err == nil && declared {
				ids = append(ids, spec.ID)
			}
		}
		sort.Strings(ids)
	}
	// 先按 overlay 的存在性同步启动参数（`cli_patch_arg` 通道）：这既是"写入权关着时也能生效"
	// 的那条路径（探针手写 overlay），也是"坏配置的恢复路径"（文件删掉 → 参数自动摘掉）。
	s.SyncRuntimeConfigArgs()
	out := &dtomodel.ModelApplyResp{Applied: make([]dtomodel.RuntimeConfigResp, 0, len(ids))}
	for _, id := range ids {
		st, err := s.MaterializeRuntimeConfig(ctx, id, true)
		if err != nil {
			return nil, err
		}
		out.Applied = append(out.Applied, *st)
	}
	return out, nil
}

// tryMaterialize 写面末尾的尽力物化：清单行**已经提交**，所以物化失败不回滚它，
// 而是把失败如实带回写面回执（note），并留一条 WARN。
//
// 为什么不直接返回错误：那会让前端以为"清单没保存"——它保存了。真实情况是
// "清单已保存，但配置文件没写成功"，这两件事必须分开说（设计 R4 的"四件事分开报"）。
func (s *Service) tryMaterialize(ctx context.Context, runtimeID string) string {
	if s.st == nil {
		return ""
	}
	st, err := s.MaterializeRuntimeConfig(ctx, runtimeID, true)
	if err != nil {
		slog.Warn("runtime config: 清单写面后的物化失败（清单已保存，配置未更新）",
			"runtime", runtimeID, "err", err)
		return "⚠ 清单已保存，但配置物化失败（" + err.Error() + "）：重试 POST /models/apply"
	}
	if st != nil && st.Note != "" {
		return st.Note
	}
	return ""
}
