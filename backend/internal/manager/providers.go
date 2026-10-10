package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objdelivery"
	"github.com/morehao/ark-mate/pkg/redact"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// fileExists 判断路径存在（doctor 与状态面用）。
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(filepath.Clean(p))
	return err == nil
}

// providersFileName provider 档案文件名（**目录级配置**：AgentDir 下的保留文件，不是 agent 定义）。
//
// 它留在文件侧：档案是"这台机器上有哪些上游凭据"，与 agent 定义的生命周期不同。
const providersFileName = "providers.json"

// ——— 供应商（v12）：**一个 OpenAI 兼容端点 + 一把密钥** ———
//
// 档案存于 <AgentDir>/providers.json（memory 载体）或 `providers` 表（sqlite 载体）。
// REST 语义：GET 只回 {id,name,protocol,base_url,has_key,…}（密钥**绝不读回**）；
// PATCH 幂等更新（指针语义）；启动注入由**编排层**按各底座声明的凭据面二选一
// （原生凭据文档 or 子进程 env，见 `resource.CredentialSpec`）。
//
// 落盘形态：文件里**只留引用** `key_ref: "vault:providers/<id>"`，密文单独落
// `<mate_home>/vault/providers/<id>.enc`（AES-256-GCM，AAD 绑定 {kind, ref}）。
// 明文只存在于内存、adapter 的凭据文档与子进程 env 三处，绝不写进 body。

// providerDef 磁盘/内存中的供应商。
type providerDef struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url,omitempty"`
	// Note 备注（不驱动行为）。它是 body 里的一个普通键，这里提升成字段只为让写面读起来清楚。
	Note string `json:"note,omitempty"`
	// Key 内存明文（注入/判 has_key/进 adapter 的凭据文档用；落盘一律走 KeyRef）。
	Key string `json:"-"`
	// KeyRef 密文引用（落盘字段）。
	KeyRef string `json:"key_ref,omitempty"`
	// keyErr 装载时解密失败的原因（problems 据此给出可执行出路，不静默降级）。
	keyErr error `json:"-"`
	// Body 载体里读到的**原始键值**（除 `key`/`id` 外的全部键，含未知键）。
	// sqlite 模式下它就是 `providers` 行的 body：写面把它原样提交回去，未知键因此不丢。
	Body map[string]any `json:"-"`
	// Revision 载体里的行版本（sqlite = `providers.revision`；memory = 0）。
	Revision int64 `json:"-"`
	// UpdatedAt 最后一次成功提交的时刻（sqlite = 行上的 updated_at；memory = 零值）。
	UpdatedAt time.Time `json:"-"`
}

// providerFile providers.json 的磁盘形状。
//
// Key 字段**只读**：用于把旧版本的明文档案迁移进 vault；除「vault 不可用」这一种情况外
// 一律不再写出。
type providerFile struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Note     string `json:"note,omitempty"`
	KeyRef   string `json:"key_ref,omitempty"`
	Key      string `json:"key,omitempty"`
	// Type/Projections 是 v11 的遗留（导入时按 v11 → v12 字段映射转换，见 provider_import.go）。
	Type        string         `json:"type,omitempty"`
	Projections map[string]any `json:"projections,omitempty"`
}

// providerRef provider id → vault 引用（namespace 只有 providers）。
func providerRef(id string) string { return vault.RefPrefix + "providers/" + id }

// providerVault 返回 vault 句柄（惰性创建 + 缓存）。
//
// home 取 agentDir 的父目录（默认 ~/.ark-mate/agents → ~/.ark-mate）：密钥 ~/.ark-mate/vault.key、
// 密文 ~/.ark-mate/vault/。agentDir 为空（纯内存场景/未配置）→ (nil, nil) = 无 vault，
// 此时密钥只在内存、绝不落盘。
func (s *Service) providerVault() (*vault.Store, error) {
	s.mu.Lock()
	if s.vaultTried {
		st, err := s.vaultStore, s.vaultErr
		s.mu.Unlock()
		return st, err
	}
	s.vaultTried = true
	dir := s.agentDir
	s.mu.Unlock()

	if dir == "" {
		return nil, nil
	}
	st, err := vault.Open(filepath.Dir(dir))
	s.mu.Lock()
	s.vaultStore, s.vaultErr = st, err
	s.mu.Unlock()
	if err != nil {
		slog.Warn("vault: 初始化失败（provider 密钥暂无法加密落盘）", "err", err)
	}
	return st, err
}

// loadProvidersFile 从 <agentDir>/providers.json 装载（文件缺失 → 空表）；
// 发现旧明文 `key` → 加密写入 vault + 写回 key_ref（幂等：迁移后再读走 key_ref 分支）。
func (s *Service) loadProvidersFile(dir string) {
	if dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, providersFileName))
	if err != nil {
		return // 首次/未建 → 空表
	}
	var m map[string]*providerFile
	if err := json.Unmarshal(b, &m); err != nil {
		slog.Warn("providers: providers.json 解析失败（忽略）", "err", err)
		return
	}
	// vault 句柄必须在持锁外取（providerVault 自己要拿 s.mu）。
	st, verr := s.providerVault()
	loaded := make(map[string]*providerDef, len(m))
	migrated := 0
	for id, d := range m {
		if d == nil || id == "" {
			continue
		}
		p := providerDefFromFile(id, d)
		switch {
		case d.Key != "":
			if st == nil {
				p.Key, p.keyErr = d.Key, fmt.Errorf("vault 不可用，密钥仍为明文: %v", vaultUnavailable(verr))
				break
			}
			ref := providerRef(id)
			if err := st.Put(ref, vault.KindProviderKey, []byte(d.Key)); err != nil {
				p.Key, p.keyErr = d.Key, err
				slog.Warn("providers: 密钥加密迁移失败（保留明文，不丢密钥）", "provider", id, "err", err)
				break
			}
			p.Key, p.KeyRef = d.Key, ref
			p.Body["key_ref"] = ref
			migrated++
		case d.KeyRef != "":
			p.KeyRef = d.KeyRef
			p.Body["key_ref"] = d.KeyRef
			if st == nil {
				p.keyErr = fmt.Errorf("vault 不可用，无法解密 %s: %v", d.KeyRef, vaultUnavailable(verr))
				slog.Warn("providers: 有密文但 vault 不可用", "provider", id, "ref", d.KeyRef, "err", p.keyErr)
				break
			}
			pt, err := st.Get(d.KeyRef, vault.KindProviderKey)
			if err != nil {
				p.keyErr = err
				slog.Warn("providers: 密钥密文解密失败（该 provider 在修复前不可用）", "provider", id, "err", err)
				break
			}
			p.Key = string(pt)
		}
		loaded[id] = p
	}
	s.mu.Lock()
	s.providers = loaded
	s.mu.Unlock()
	if migrated > 0 {
		if err := s.persistProvidersFile(); err != nil {
			slog.Warn("providers: 迁移写回失败", "migrated", migrated, "err", err)
			return
		}
		slog.Info("providers: 明文密钥已迁移进 vault", "count", migrated)
	}
}

// providerDefFromFile 把文件载体的一行转成内存档案，并重建 v12 的 body。
//
// **v11 → v12 字段映射**（迁移逃生门：导入保留对旧 `type` 的识别）：`type` → `protocol`
// + `base_url`（按冻结的已知端点表推导），`projections` 丢弃（v12 无出口）。
func providerDefFromFile(id string, d *providerFile) *providerDef {
	p := &providerDef{ID: id, Name: d.Name, Protocol: d.Protocol, BaseURL: d.BaseURL, Note: d.Note}
	if p.Protocol == "" {
		p.Protocol = string(resource.ProtocolOpenAI)
	}
	if p.BaseURL == "" {
		p.BaseURL = v11FileBaseURL(d)
	}
	p.Body = map[string]any{}
	if p.Name != "" {
		p.Body["name"] = p.Name
	}
	p.Body["protocol"] = p.Protocol
	if p.BaseURL != "" {
		p.Body["base_url"] = p.BaseURL
	}
	if p.Note != "" {
		p.Body["note"] = p.Note
	}
	return p
}

// v11FileBaseURL 从 v11 的文件档案里推导端点（`projections.*.baseURL` 优先，其次 type 表）。
func v11FileBaseURL(d *providerFile) string {
	keys := make([]string, 0, len(d.Projections))
	for k := range d.Projections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		frag, ok := d.Projections[k].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := frag["baseURL"].(string); ok && v != "" {
			return v
		}
	}
	if ep, ok := v11KnownProviderEndpoints[strings.ToLower(d.Type)]; ok {
		return ep
	}
	return ""
}

// v11KnownProviderEndpoints v11 的 `type` → 端点（**冻结副本**：`internal/store/migrate_v12.go`
// 里有一份同源的，那是迁移用的历史快照，这里是导入（逃生门）用的；两处都描述历史，不复用活代码）。
var v11KnownProviderEndpoints = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"deepseek":   "https://api.deepseek.com/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"groq":       "https://api.groq.com/openai/v1",
}

// vaultUnavailable 把「没有 vault」与「vault 初始化失败」统一成可读原因。
func vaultUnavailable(err error) string {
	if err == nil {
		return "agentDir 未配置（无 mate home）"
	}
	return err.Error()
}

// persistProvidersFile 原子写 providers.json（内存现状）。
func (s *Service) persistProvidersFile() error { return s.persistProvidersFileWith(nil) }

// persistProvidersFileWith 原子写 providers.json：**内存现状 + 这一条**（内存里还没有它）。
//
// 为什么需要一个"额外一条"的参数：写面的顺序是「载体提交成功之后才更新内存」，
// 所以提交那一刻这条档案还不在 `s.providers` 里。若这里只序列化内存，新建的那条会被写成
// **空文件**——一个只会在文件载体上出现、且完全静默的失败。
func (s *Service) persistProvidersFileWith(override *providerDef) error {
	s.mu.Lock()
	dir := s.agentDir
	cp := make(map[string]*providerFile, len(s.providers)+1)
	for id, d := range s.providers {
		cp[id] = providerFileOf(id, d)
	}
	s.mu.Unlock()
	if override != nil {
		cp[override.ID] = providerFileOf(override.ID, override)
	}
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".providers.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, providersFileName))
}

// providerFileOf 把内存档案投影成文件载体的一行（只写引用；仅"vault 不可用且确有明文"时写 key）。
func providerFileOf(id string, d *providerDef) *providerFile {
	if d == nil {
		return &providerFile{ID: id}
	}
	f := &providerFile{
		ID: id, Name: d.Name, Protocol: d.Protocol, BaseURL: d.BaseURL, Note: d.Note, KeyRef: d.KeyRef,
	}
	if d.KeyRef == "" && d.Key != "" {
		// vault 不可用的降级路径：明文已在内存里，写回时必须保留（否则等于删密钥）。
		f.Key = d.Key
	}
	return f
}

// ListProviders 列出供应商概要（密钥只以 has_key 呈现，绝不读回）。
//
// 每一条都带**引用面**（哪些 agent 在用它、它们可能落到哪些 runtime）、**模型条数**与
// **问题清单**（缺端点/密钥缺失/密文解不开）：这些都是**读取时现算的派生值**——存一份必然漂移。
func (s *Service) ListProviders(ctx context.Context) ([]dtoprovider.ProviderResp, error) {
	s.mu.Lock()
	out := make([]dtoprovider.ProviderResp, 0, len(s.providers))
	for id, d := range s.providers {
		if d == nil {
			continue
		}
		out = append(out, s.providerRespLocked(ctx, id, d))
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// providerRespLocked 把一条档案投影成对外表示（**调用方必须持 s.mu**）。
//
// 为什么引用面也要在持锁时算：`s.agents` 与 `s.providers` 同属这一个锁的保护区，分成两段读会
// 让"引用面"与"档案本身"来自两个不同时刻（删 agent 与改档案并发时会看到不存在的引用）。
func (s *Service) providerRespLocked(ctx context.Context, id string, d *providerDef) dtoprovider.ProviderResp {
	resp := dtoprovider.ProviderResp{
		ID: id, Name: d.Name, Protocol: d.Protocol, BaseURL: d.BaseURL, Note: d.Note,
		HasKey:        d.Key != "" || d.KeyRef != "",
		Revision:      d.Revision,
		ModelCount:    s.providerModelCount(ctx, id),
		Applicability: s.providerApplicabilityLocked(d),
		Refs:          dtoprovider.ProviderRefs{Agents: []string{}, Runtimes: []string{}},
		Problems:      providerProblems(d),
	}
	if !d.UpdatedAt.IsZero() {
		resp.UpdatedAt = model.FormatTime(d.UpdatedAt)
	}
	seenRT := map[string]bool{}
	for agentID, a := range s.agents {
		if a == nil || a.Provider != id {
			continue
		}
		resp.Refs.Agents = append(resp.Refs.Agents, agentID)
		for _, rt := range a.Runtimes {
			if !seenRT[rt] {
				seenRT[rt] = true
				resp.Refs.Runtimes = append(resp.Refs.Runtimes, rt)
			}
		}
	}
	sort.Strings(resp.Refs.Agents)
	sort.Strings(resp.Refs.Runtimes)
	return resp
}

// providerModelCount 挂靠在该供应商下的模型条数（读取时现算；无清单载体 → 0）。
func (s *Service) providerModelCount(ctx context.Context, providerID string) int {
	if s.st == nil {
		return 0
	}
	rows, err := s.st.ListModelDefinitionsByProvider(ctx, providerID)
	if err != nil {
		return 0
	}
	return len(rows)
}

// providerApplicabilityLocked 供应商逐**已注册 runtime** 的可达性（**调用方必须持 s.mu**）。
//
// v12 里它回答两件事，两件都摆出来、不给合并结论：
//
//	① **凭据通道**：该 runtime 走原生凭据文档还是平台 env 注入（由 `CredentialSpec.Tested` 决定）；
//	② **清单通道**：这条端点能不能被该底座写进配置（缺 `base_url` → not_configured）。
//
// 刻意**不**把"缺密钥"并进这里：密钥缺失是这条档案自身的问题（已由 `problems` 回答），
// 与"哪个 runtime 能不能收到它"是两件事。
func (s *Service) providerApplicabilityLocked(d *providerDef) []objdelivery.Applicability {
	runtimes := s.registeredRuntimeIDs()
	out := make([]objdelivery.Applicability, 0, len(runtimes))
	for _, rt := range runtimes {
		spec, ok := s.reg.Get(rt)
		if !ok {
			continue
		}
		if strings.TrimSpace(d.BaseURL) == "" {
			out = append(out, objdelivery.Applicability{
				Runtime: rt, OK: false, Status: objdelivery.StatusNotConfigured,
				Reason: "缺端点：这条供应商没有 `base_url`，它的模型不会被交付给任何会话进程。" +
					"在「设置 · 模型」里给它补一个 OpenAI 兼容端点",
			})
			continue
		}
		channel := resource.ChannelEnv
		if spec.Credential.Tested && spec.Credential.NativeDoc != "" {
			channel = resource.ChannelNativeDoc
		}
		where := "子进程环境变量 " + providerRefNameFor(spec, d.ID)
		if channel == resource.ChannelNativeDoc {
			where = "原生凭据文档（落盘 0600）"
		}
		out = append(out, objdelivery.Applicability{
			Runtime: rt, OK: true, Status: objdelivery.StatusProjected,
			Reason: "端点 " + d.BaseURL + "；凭据走 " + where + "（新进程启动时读清单）",
		})
	}
	return out
}

// providerRefNameFor 派生某供应商在某 runtime 上的凭据引用名（逐供应商唯一，ADR-16）。
//
// 归一化与哈希后缀的算法在 `pkg/resource.RefFor`——**只有一份**：编排层与诊断面共用它，
// 两处各写一遍迟早分叉（而分叉的后果是"界面显示的变量名不是真正注入的那个"）。
func providerRefNameFor(spec runtime.Spec, providerID string) string {
	return resource.RefFor(spec.Credential.RefPattern, providerID)
}

// providerProblems 本地段能回答的问题清单（零网络请求；**可执行的一句话**）。
func providerProblems(d *providerDef) []string {
	out := []string{}
	switch {
	case d.Key == "" && d.KeyRef != "" && d.keyErr != nil:
		out = append(out, "密钥密文无法解密（"+d.KeyRef+"）："+d.keyErr.Error()+
			"；确认 vault.key 未丢失/未被更换（备份清单见 docs/deployment.md），或重设密钥")
	case d.Key == "" && d.KeyRef == "":
		out = append(out, "尚未设置密钥：在「设置 · 模型」的供应商块补一条密钥后才会交付")
	case d.Key == "" && d.KeyRef != "":
		out = append(out, "有密文引用但明文不可用（vault 不可达）：确认 mate home 下的 vault 可用")
	}
	if strings.TrimSpace(d.BaseURL) == "" {
		out = append(out, "缺端点（`base_url` 为空）：它的模型不会被交付给任何会话进程——"+
			"在「设置 · 模型」里补一个 OpenAI 兼容端点")
	}
	return out
}

// providerEnvFor 返回**某 runtime 上**给某供应商注入的 env（KEY=VALUE 列表；不适用 → nil）。
//
// 为什么必须带 runtimeID（v12 的关键改动）：凭据通道**按 runtime 分面**（ADR-4）——
// dsh 走原生凭据文档（可热生效），opencode 走子进程 env。所以"要不要注入 env"不能由平台
// 一律决定，必须问该底座自己的声明：
//
//	`Credential.Tested && NativeDoc != ""` → 走原生文档，**平台不注入同名 env**
//	  （实测：dsh 的优先级是"继承环境 > 存储文档"，同时启用会让已存文档被陈旧的 env 永久遮蔽，
//	   用户轮换了密钥、文档也更新了，而生效的仍是旧值，**且没有任何报错**）；
//	否则 → 按该底座的 `RefPattern` 派生**逐供应商唯一**的引用名并注入（ADR-16：
//	  v11 的 `envKeyOf(type)` 让同 type 的两个供应商共用一把密钥，至少一条 route 用错密钥）。
func (s *Service) providerEnvFor(runtimeID, providerID string) []string {
	spec, ok := s.reg.Get(runtimeID)
	if !ok || spec.Credential.RefPattern == "" {
		return nil
	}
	if spec.Credential.Tested && strings.TrimSpace(spec.Credential.NativeDoc) != "" {
		// 原生文档通道：密钥由 adapter 写进那份文档，平台绝不在这里重复注入。
		return nil
	}
	s.mu.Lock()
	d := s.providers[providerID]
	s.mu.Unlock()
	if d == nil || d.Key == "" {
		if d != nil && d.KeyRef != "" {
			slog.Warn("providers: 密钥不可用（密文未解密），不注入 env", "provider", providerID, "err", d.keyErr)
		}
		return nil
	}
	ref := resource.RefFor(spec.Credential.RefPattern, providerID)
	if ref == "" {
		return nil
	}
	// 登记真实密钥值（引用计数去重）：无论密钥形状如何，出现在日志/报错/事件里都必被替换。
	// 这是脱敏最可靠的一档（零误报）——provider key 是当前唯一已知的真实密钥来源。
	redact.RegisterDynamicSecret(d.Key)
	return []string{ref + "=" + d.Key}
}

// providerRefName 取该供应商在**某一个**已注册 runtime 上的凭据引用名（诊断面展示用）。
//
// 逐 runtime 的真名由 adapter 侧的 `RefPattern` 决定（各底座可以不同）；诊断面只需要一个
// 代表值，取字典序最小的那个已注册 runtime 的派生结果即可——它同时保证"界面显示的键名"
// 与"真正注入的键名"是同一份算法（`resource.RefFor`）。
func (s *Service) providerRefName(providerID string) string {
	for _, spec := range s.reg.List() {
		if spec.Credential.RefPattern != "" {
			return providerRefNameFor(spec, providerID)
		}
	}
	return ""
}

// CredentialProblem 校验 agent 绑定的供应商当前是否可用；不可用即 credential_missing
// 并带上可执行出路。
//
// 语义：agent.provider 为空 = 不注入密钥、用底座自身登录态（合法 → nil）；非空则必须指向一个
// **存在且带 key** 的档案。悬空引用会让「本该注入的密钥静默缺失」，属配置错误——宁可显式失败，
// 也不让会话在半配置状态下启动。密钥密文解不开（vault.key 丢失/被更换）同样走这里。
func (s *Service) CredentialProblem(agent *AgentRec) error {
	if agent == nil || agent.Provider == "" {
		return nil
	}
	s.mu.Lock()
	d := s.providers[agent.Provider]
	s.mu.Unlock()
	ref := fmt.Sprintf("agent %q 绑定的供应商 %q", agent.ID, agent.Provider)
	if d == nil {
		return acl.NewError(acl.ClassRequest, "credential_missing",
			ref+" 不存在：在「设置 · 模型」的供应商块里新建这条供应商，或清空该 agent 的凭据选择"+
				"（改用它自身的登录态）", nil)
	}
	if d.Key == "" {
		if d.KeyRef != "" && d.keyErr != nil {
			return acl.NewError(acl.ClassRequest, "credential_missing",
				ref+" 的密钥密文无法解密（"+d.KeyRef+"）："+d.keyErr.Error()+
					"；确认 vault.key 未丢失/未被更换（备份清单见 docs/deployment.md），"+
					"或到「设置 · 模型」重设密钥", nil)
		}
		return acl.NewError(acl.ClassRequest, "credential_missing",
			ref+" 没有密钥：到「设置 · 模型」的供应商块补密钥，或清空该 agent 的凭据选择", nil)
	}
	return nil
}
