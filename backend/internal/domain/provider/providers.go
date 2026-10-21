package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	credval "github.com/morehao/ark-mate/internal/platform/credential"

	"github.com/morehao/ark-mate/internal/platform/credential"

	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/platform/object/objdelivery"
	"github.com/morehao/ark-mate/internal/platform/object/objtime"
	"github.com/morehao/ark-mate/internal/platform/redact"
	"github.com/morehao/ark-mate/internal/platform/resource"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// fileExists 判断路径存在（doctor 与状态面用）。
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(filepath.Clean(p))
	return err == nil
}

// ——— 供应商（v12）：**一个 OpenAI 兼容端点 + 一把密钥** ———
//
// 档案存于 <ProviderDir>/providers.json（memory 载体）或 `providers` 表（sqlite 载体）。
// REST 语义：GET 只回 {id,name,protocol,base_url,has_key,…}（密钥**绝不读回**）；
// PATCH 幂等更新（指针语义）；启动注入由**编排层**按各底座声明的凭据面二选一
// （原生凭据文档 or 子进程 env，见 `resource.CredentialSpec`）。
//
// **v20 的密钥面**：权威是库里的 `providers.key` 一列（sqlite 载体）或托管文档
// `<MATE_HOME>/credentials.json`（memory 载体就是事实源）。档案文件里只剩**派生**的
// `key_ref`（= `providers/<id>`，托管文档里的键）；解析顺序的唯一实现是 `internal/adapters/outbound/credentials`。
// 明文只存在于内存、adapter 的原生凭据文档与子进程 env 三处，绝不写进档案文件。

// applyResolution 把一次凭据解析的结果落到档案上（三个字段同进同出，避免半更新）。
//
// 顺带把**解析出来的值**登记进脱敏闸门的动态表：这是 v20 之后唯一可靠的"真值来源"
// （`internal/platform/redact` 的第 1 档：按值替换、零误报）。放在这里而不是各通道的注入点上，是因为
// 密钥现在可能走**原生凭据文档**那条路（`Credential.Tested && NativeDoc != ""`，dsh 就是），
// 那条路不经过 `ProviderEnvFor`——只在那里登记会让"平台上唯一真实存在的那把密钥"漏出闸门。
func (d *Def) applyResolution(k credential.ProviderKey) {
	d.Key, d.KeySource, d.KeyEnvKey, d.KeyProblems = k.Value, k.Source, k.EnvKey, k.Problems
	if d.Key != "" {
		redact.RegisterDynamicSecret(d.Key)
	}
}

//
// 单独导出是因为**诊断面要按它分类**：镜像里有而库里没有时，两类引用的出路完全不同——provider
// 密钥在「设置 · 模型」里有写入口（重填即对齐），而 MCP 密钥的值这一批**没有**写入口（只能把
// 定义里的引用改成 `env:`）。两边各写一遍 `"providers/"` 字面量，迟早有一边改了另一边还用旧分类。

// providerRef provider id → 托管文档里的键（namespace 只有 providers）。

// loadProvidersFile 从 <providerDir>/providers.json 装载（文件缺失 → 空表；memory 载体）。
//
// 两条历史形态在这里收口：旧前缀 `key_ref` 被**显式翻译**（档案文件不在库里，迁移改不到它们，
// 见 `capability.TranslateLegacySecretRef`），旧明文 `key` 搬进托管文档后从档案里去掉。
func (pl *Plane) loadProvidersFile(dir string) {
	if dir == "" {
		return
	}
	ctx := context.Background()
	b, err := os.ReadFile(filepath.Join(dir, ProvidersFileName))
	if err != nil {
		return // 首次/未建 → 空表
	}
	var m map[string]*ProviderFile
	if err := json.Unmarshal(b, &m); err != nil {
		glog.Warnw(ctx, "providers: providers.json 解析失败（忽略）", "err", err)
		return
	}
	loaded := make(map[string]*Def, len(m))
	migrated := 0
	for id, d := range m {
		if d == nil || id == "" {
			continue
		}
		p := providerDefFromFile(id, d)
		p.KeyRef = credential.ProviderRef(id)
		if legacy := d.KeyRef; legacy != "" {
			if translated, ok := capability.TranslateLegacySecretRef(legacy); ok {
				capability.WarnLegacySecretRef(ctx, legacy, "providers.json 的 key_ref")
				d.KeyRef = translated
			}
		}
		plain := d.Key
		if plain != "" {
			// 旧明文：搬进托管文档（memory 模式它就是事实源），随后把文件里的那份抹掉。
			if err := pl.mirrorSet(p.KeyRef, plain); err != nil {
				p.Key, p.KeyProblems = plain, []string{
					"明文密钥搬进托管文档失败：" + err.Error() +
						"；档案里那份暂时继续生效（不要手工删它）"}
			} else {
				migrated++
			}
		}
		res := pl.KeyFor(ctx, p)
		p.applyResolution(res)
		if plain != "" && p.Key == "" {
			// 搬运失败时档案里那份仍然是唯一的密钥来源：不丢密钥，如实报问题。
			p.Key, p.KeySource = plain, ""
		}
		loaded[id] = p
	}
	pl.mu.Lock()
	pl.providers = loaded
	pl.mu.Unlock()
	if migrated > 0 {
		if err := pl.persistProvidersFile(); err != nil {
			glog.Warnw(ctx, "providers: 搬运后重写 providers.json 失败（明文仍在文件里）", "migrated", migrated, "err", err)
			return
		}
		glog.Infow(ctx, "providers: 明文密钥已搬进托管文档", "count", migrated)
	}
}

// providerDefFromFile 把文件载体的一行转成内存档案，并重建 v12 的 body。
//
// **v11 → v12 字段映射**（迁移逃生门：导入保留对旧 `type` 的识别）：`type` → `protocol`
// + `base_url`（按冻结的已知端点表推导），`projections` 丢弃（v12 无出口）。
func providerDefFromFile(id string, d *ProviderFile) *Def {
	p := &Def{ID: id, Name: d.Name, Protocol: d.Protocol, BaseURL: d.BaseURL, Note: d.Note}
	if p.Protocol == "" {
		p.Protocol = string(resource.ProtocolOpenAI)
	}
	if p.BaseURL == "" {
		p.BaseURL = V11FileBaseURL(d)
	}
	return p
}

// persistProvidersFile 原子写 providers.json（内存现状）。
func (pl *Plane) persistProvidersFile() error { return pl.persistProvidersFileWith(nil) }

// persistProvidersFileWith 原子写 providers.json：**内存现状 + 这一条**（内存里还没有它）。
//
// 为什么需要一个"额外一条"的参数：写面的顺序是「载体提交成功之后才更新内存」，
// 所以提交那一刻这条档案还不在 `pl.providers` 里。若这里只序列化内存，新建的那条会被写成
// **空文件**——一个只会在文件载体上出现、且完全静默的失败。
func (pl *Plane) persistProvidersFileWith(override *Def) error {
	pl.mu.Lock()
	dir := pl.providerDir
	cp := make(map[string]*ProviderFile, len(pl.providers)+1)
	for id, d := range pl.providers {
		cp[id] = providerFileOf(id, d)
	}
	pl.mu.Unlock()
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
	return os.Rename(tmp, filepath.Join(dir, ProvidersFileName))
}

// providerFileOf 把内存档案投影成文件载体的一行（**只写引用，绝不写明文**）。
//
// v20 之前这里有一条"旧机制不可用且确有明文"的降级分支：把明文写回档案，从而让档案成为
// 第二份密钥副本。托管文档现在**必然**可写（它是 memory 载体的事实源），所以那条分支连同
// 它带来的"同一个值两处落盘"一起删除。
func providerFileOf(id string, d *Def) *ProviderFile {
	if d == nil {
		return &ProviderFile{ID: id}
	}
	return &ProviderFile{
		ID: id, Name: d.Name, Protocol: d.Protocol, BaseURL: d.BaseURL, Note: d.Note,
		KeyRef: credential.ProviderRef(id),
	}
}

// ListProviders 列出供应商概要（密钥只以 has_key 呈现，绝不读回）。
//
// 每一条都带**引用面**（哪些模型挂靠它、它们可能落到哪些 runtime）、**模型条数**与
// **问题清单**（缺端点/密钥缺失/凭据分层读不了）：这些都是**读取时现算的派生值**——存一份必然漂移。
func (pl *Plane) ListProviders(ctx context.Context) ([]ProviderItem, error) {
	pl.mu.Lock()
	out := make([]ProviderItem, 0, len(pl.providers))
	for id, d := range pl.providers {
		if d == nil {
			continue
		}
		out = append(out, pl.providerRespLocked(ctx, id, d))
	}
	pl.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// providerRespLocked 把一条档案投影成对外表示（**调用方必须持 pl.mu**）。
//
// 密钥面**读时现解析**（`KeyFor`）：解析顺序的唯一实现是 `internal/adapters/outbound/credentials`，
// 而它要读的四处（进程环境、托管文档、两份 `.env`、库）都可能在本进程之外被改动。顺手把结果
// 回写到档案上，让读面与注入面（`providerForSync`）看到同一份事实。
//
// 引用面（谁挂靠它）读的是模型表（v19 起供应商的唯一引用者是模型条目），读库时不再持锁解析：
// 与"档案本身"来自不同时刻的差异只会体现在引用名单上，而名单是**只读展示**、不参与任何判据
// （删除判据在 store 的同一事务里，见 `DeleteProvider`）。
func (pl *Plane) providerRespLocked(ctx context.Context, id string, d *Def) ProviderItem {
	d.applyResolution(pl.KeyFor(ctx, d))
	resp := ProviderItem{
		ID:            d.ID,
		Name:          d.Name,
		Protocol:      d.Protocol,
		BaseURL:       d.BaseURL,
		Note:          d.Note,
		HasKey:        d.Key != "",
		KeySource:     string(d.KeySource),
		KeyRef:        d.KeyRef,
		Revision:      d.Revision,
		ModelCount:    pl.providerModelCount(ctx, id),
		Applicability: pl.providerApplicabilityLocked(d),
		Refs:          ProviderRefs{Models: []string{}, Runtimes: []string{}},
		Problems:      providerProblems(d),
	}
	if !d.UpdatedAt.IsZero() {
		resp.UpdatedAt = objtime.Format(d.UpdatedAt)
	}
	// 引用者 = 挂靠在该供应商下的模型条目；有模型挂靠才谈得上"哪个底座拿得到它"
	// （底座是构建期常量，所以至多一个 runtime）。
	if models := pl.ProviderModelRefsReadOnly(ctx, id); len(models) > 0 {
		resp.Refs.Models = models
		resp.Refs.Runtimes = []string{runtime.SelectedRuntime()}
	}
	sort.Strings(resp.Refs.Models)
	sort.Strings(resp.Refs.Runtimes)
	return resp
}

// providerModelCount 挂靠在该供应商下的模型条数（读取时现算；无清单载体 → 0）。
func (pl *Plane) providerModelCount(ctx context.Context, providerID string) int {
	if pl.st == nil {
		return 0
	}
	rows, err := pl.st.ListModelDefinitionsByProvider(ctx, providerID)
	if err != nil {
		return 0
	}
	return len(rows)
}

// providerApplicabilityLocked 供应商逐**已注册 runtime** 的可达性（**调用方必须持 pl.mu**）。
//
// v12 里它回答两件事，两件都摆出来、不给合并结论：
//
//	① **凭据通道**：该 runtime 走原生凭据文档还是平台 env 注入（由 `CredentialSpec.Tested` 决定）；
//	② **清单通道**：这条端点能不能被该底座写进配置（缺 `base_url` → not_configured）。
//
// 刻意**不**把"缺密钥"并进这里：密钥缺失是这条档案自身的问题（已由 `problems` 回答），
// 与"哪个 runtime 能不能收到它"是两件事。
func (pl *Plane) providerApplicabilityLocked(d *Def) []objdelivery.Applicability {
	runtimes := pl.registeredRuntimeIDs()
	out := make([]objdelivery.Applicability, 0, len(runtimes))
	for _, rt := range runtimes {
		spec, ok := pl.reg.Get(rt)
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
// 归一化与哈希后缀的算法在 `internal/platform/resource.RefFor`——**只有一份**：编排层与诊断面共用它，
// 两处各写一遍迟早分叉（而分叉的后果是"界面显示的变量名不是真正注入的那个"）。
func providerRefNameFor(spec runtime.Spec, providerID string) string {
	return resource.RefFor(spec.Credential.RefPattern, providerID)
}

// providerProblems 本地段能回答的问题清单（零网络请求；**可执行的一句话**）。
//
// v20 起"有没有密钥"的判据是**内存里有没有生效的明文**（它来自库或托管文档的解析结果）。
// 有密钥时另有一类必须说出来的事实：生效值来自**继承的进程环境**——按层序它会遮住面板里
// 保存的那一条，于是"我改了密钥却没生效"会发生且没有任何报错。
func providerProblems(d *Def) []string {
	out := []string{}
	for _, p := range d.KeyProblems {
		out = append(out, "凭据分层："+p)
	}
	switch {
	case d.Key != "" && d.KeySource == credval.SourceEnv:
		out = append(out, "当前生效的密钥来自**继承的进程环境**"+
			credential.OneKeyInParens(d.KeyEnvKey)+"：它按层序遮住面板里保存的那一条——"+
			"在面板里轮换密钥不会生效，除非先清掉这个环境变量")
	case d.Key != "":
		// 密钥可用：无问题。
	case len(d.KeyProblems) == 0:
		out = append(out, "尚未设置密钥：在「设置 · 模型」的供应商块补一条密钥后才会交付")
	}
	if strings.TrimSpace(d.BaseURL) == "" {
		out = append(out, "缺端点（`base_url` 为空）：它的模型不会被交付给任何会话进程——"+
			"在「设置 · 模型」里补一个 OpenAI 兼容端点")
	}
	return out
}

// ProviderEnvFor 返回**某 runtime 上**给某供应商注入的 env（KEY=VALUE 列表；不适用 → nil）。
//
// 为什么必须带 runtimeID（v12 的关键改动）：凭据通道**按 runtime 分面**（ADR-4）——有的底座
// 走原生凭据文档（可热生效），有的只能走子进程 env。所以"要不要注入 env"不能由平台
// 一律决定，必须问该底座自己的声明：
//
//	`Credential.Tested && NativeDoc != ""` → 走原生文档，**平台不注入同名 env**
//	  （dsh 0.2.1 的优先级是"存储文档 > 继承环境"；两条同时启用时其中一条必然是静默死值，
//	   用户轮换了密钥、另一条还在，而生效的仍是旧值，**且没有任何报错**）；
//	否则 → 按该底座的 `RefPattern` 派生**逐供应商唯一**的引用名并注入（ADR-16：
//	  v11 的 `envKeyOf(type)` 让同 type 的两个供应商共用一把密钥，至少一条 route 用错密钥）。
func (pl *Plane) ProviderEnvFor(runtimeID, providerID string) []string {
	spec, ok := pl.reg.Get(runtimeID)
	if !ok || spec.Credential.RefPattern == "" {
		return nil
	}
	if spec.Credential.Tested && strings.TrimSpace(spec.Credential.NativeDoc) != "" {
		// 原生文档通道：密钥由 adapter 写进那份文档，平台绝不在这里重复注入。
		return nil
	}
	pl.mu.Lock()
	d := pl.providers[providerID]
	pl.mu.Unlock()
	if d == nil || d.Key == "" {
		if d != nil && len(d.KeyProblems) > 0 {
			glog.Warnw(context.Background(), "providers: 密钥不可用，不注入 env",
				"provider", providerID, "problems", d.KeyProblems)
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

// ProviderRefName 取该供应商在**某一个**已注册 runtime 上的凭据引用名（诊断面展示用）。
//
// 逐 runtime 的真名由 adapter 侧的 `RefPattern` 决定（各底座可以不同）；诊断面只需要一个
// 代表值，取字典序最小的那个已注册 runtime 的派生结果即可——它同时保证"界面显示的键名"
// 与"真正注入的键名"是同一份算法（`resource.RefFor`）。
func (pl *Plane) ProviderRefName(providerID string) string {
	for _, spec := range pl.reg.List() {
		if spec.Credential.RefPattern != "" {
			return providerRefNameFor(spec, providerID)
		}
	}
	return ""
}

// InjectedProviderID 平台当前要给底座注入密钥的供应商 id（无 → "" = 不注入）。
//
// v19 起它不是"某个 agent 选的"：底座构建期固定、会话不再有 `agent_id`，所以决定注入哪把
// 密钥的只剩**平台声明的默认模型**——默认模型落在哪条供应商上就注入哪把（与下发给底座的
// `resource.Set.DefaultModel` 同源）。没有默认模型 = 平台不注入密钥、用底座自身登录态。
func (pl *Plane) InjectedProviderID(ctx context.Context) string {
	if pl.defaultModel == nil {
		return ""
	}
	providerID, ok := pl.defaultModel(ctx)
	if !ok {
		return ""
	}
	return strings.TrimSpace(providerID)
}

// CredentialProblem 校验**默认模型所在的那条供应商档案**当前是否可用；不可用即
// credential_missing 并带上可执行出路。
//
// 语义：没有默认模型（或它没绑供应商）= 不注入密钥、用底座自身登录态（合法 → nil）；有则必须
// 指向一个**存在且带 key** 的档案。悬空引用会让「本该注入的密钥静默缺失」，属配置错误——
// 宁可显式失败，也不让会话在半配置状态下启动。凭据分层读不了（托管文档权限不合/`.env` 写坏）
// 同样走这里。
func (pl *Plane) CredentialProblem(ctx context.Context) error {
	providerID := pl.InjectedProviderID(ctx)
	if providerID == "" {
		return nil
	}
	pl.mu.Lock()
	d := pl.providers[providerID]
	if d != nil {
		d.applyResolution(pl.KeyFor(ctx, d))
	}
	pl.mu.Unlock()
	ref := fmt.Sprintf("默认模型所在的供应商 %q", providerID)
	if d == nil {
		return code.NewError(code.ClassRequest, "credential_missing", ref+" 不存在：在「设置 · 模型」的供应商块里新建这条供应商，或把默认模型换到另一条", nil)
	}
	if d.Key == "" {
		if len(d.KeyProblems) > 0 {
			return code.NewError(code.ClassRequest, "credential_missing", ref+" 的密钥不可用（"+d.KeyRef+"）："+strings.Join(d.KeyProblems, "；")+
				"；到「设置 · 模型」重设密钥，或把定义里的引用改成 env:<ENV_NAME>", nil)
		}
		return code.NewError(code.ClassRequest, "credential_missing", ref+" 没有密钥：到「设置 · 模型」的供应商块补密钥", nil)
	}
	return nil
}
