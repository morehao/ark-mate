package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/resource"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/golib/glog"
)

// ——— 资源同步（M4）：读资源 → 组 Set → 逐 runtime 调端口 → 记摘要 → 回带 Outcome ———
//
// 这一层是「谁懂底座」这件事的分界线：它**不解析任何底座格式**，只说"把这份声明交给该
// runtime"。底座知识全在 adapter；本文件之上（DTO / 路由 / 前端）不知道底座的配置长什么样。
//
// 三条不变量：
//
//	① 同步失败**不回滚资源**（资源已提交，失败如实回带）——沿用 v11 的诚实语义；
//	② 同一份声明重复同步**不换代际**（摘要由声明算出，不由产物算出）；
//	③ 同步结论**持久化**（`GET /models` 是只读面，读一次列表不该写文件、改启动参数）。
//
// 落点为什么是 `registry` 而不是 `manager`（P5a）：同步要读的是**装配事实**——各底座配置写面
// （注册表 + `pkg/resource.Syncer`）、凭据通道（供应商密钥 + 交付面 `Delivery`）、同步结果
// （meta 里的摘要与结论）。这三样没有一样来自会话/编排，而 `mate doctor` 与网关子进程也要
// 同一份事实。把状态、`EnableStore`、同步与结果记录**四件一起**搬过来，避免"同步写了一半、
// 下一轮读到另一份状态"的静默缺口。

// Resources 资源同步面：同步状态 + 装配期装载 + 逐 runtime 交付。
//
// 它自持一把锁（只保护 `st` / 摘要 / 结论三张表）；`provider.Plane` 的读面自己取域锁，
// 所以这里**不再有**改造前 `manager` 那种"持 s.mu 去问供应商域"的锁序风险。
type Resources struct {
	reg       *runtime.Registry
	providers *provider.Plane

	mu       sync.Mutex
	st       *store.Store
	digests  map[string]string
	outcomes map[string]resource.Outcome
}

// NewResources 构造资源同步面（不读盘；库句柄由 `EnableStore` 交进来）。
func NewResources(reg *runtime.Registry, providers *provider.Plane) *Resources {
	return &Resources{reg: reg, providers: providers}
}

// Store 当前库句柄（nil = memory 模式）。只在装配/装载路径读，拷贝出指针避免持锁调用。
func (r *Resources) Store() *store.Store {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

// EnableStore 接上持久层，然后做启动期的两件事：装载同步指纹、与当前声明对齐一次。
//
// 顺序是语义的：指纹先读进内存（它并进代际键，重启后必须仍然生效——否则重启会把"已物化的
// 清单"当成"没有清单"，下一轮凭空重换代际），再同步（否则这一轮交付会把刚读到的结论覆盖成
// "刚刚同步成功"，而它其实还没被观测到）。
func (r *Resources) EnableStore(st *store.Store) {
	r.mu.Lock()
	r.st = st
	r.mu.Unlock()

	r.loadRuntimeConfigDigests(context.Background())
	// 装配期把启动面与当前声明对齐一次（v12：启动参数/内联内容/凭据 env 都由 adapter 经
	// `resource.Delivery` 装卸）。重启后平台必须重新交付——否则上一轮的交付物与新声明可能分叉，
	// 而界面显示的是"已同步"。
	if ids, err := r.runtimeIDsForSync(""); err == nil {
		for _, id := range ids {
			if _, serr := r.SyncRuntime(context.Background(), id); serr != nil {
				glog.Warnw(context.Background(), "runtime sync: 装配期同步失败", "runtime", id, "err", serr)
			}
		}
	}
}

// runtimeConfigDigestEntry meta 里某 runtime 的同步记录（**保留 v11 的键名** `runtime_config_digest`
// ——改名只会造成无收益的迁移，Q8）。
type runtimeConfigDigestEntry struct {
	Digest      string `json:"digest"`
	GeneratedAt string `json:"generated_at"`
	// GeneratorVersion 代际代数。改摘要算法时 +1，于是旧摘要被识别成"需要重新同步"
	// 而不是"文件被外部改过"。
	GeneratorVersion int `json:"generator_version"`
	// Outcome 最后一次同步的**结论**（`GET /models` 的 `sync[]` 直接读它，不再重新同步）。
	//
	// 为什么持久化：`GET` 必须是**无副作用**的读取。若读面顺手重跑一次同步，读一次列表就会
	// 写文件、改启动参数——那不是"看一眼"该有的行为。
	Outcome *resource.Outcome `json:"outcome,omitempty"`
}

// runtimeConfigGeneratorVersion 摘要/代际键的代数。
const runtimeConfigGeneratorVersion = 2

// ConfigDigest 当前进程内存里的配置指纹（runtime → digest）。
//
// 它是**代际键的一部分**（`generationKey` 的第三维）：不并进键，暖进程会继续用旧清单，
// 而界面显示"已保存"。读来源是 `meta.runtime_config_digest`（重启后仍生效）。
func (r *Resources) ConfigDigest(runtimeID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.digests[runtimeID]
}

// loadRuntimeConfigDigests 启动期把 meta 里的同步记录读进内存（并进代际键 + 供读面用）。
func (r *Resources) loadRuntimeConfigDigests(ctx context.Context) {
	st := r.Store()
	if st == nil {
		return
	}
	raw, ok, err := st.GetMeta(ctx, model.MetaKeyRuntimeConfigDigest)
	if err != nil {
		glog.Warnw(ctx, "runtime sync: 读 meta.runtime_config_digest 失败", "err", err)
		return
	}
	digests := map[string]string{}
	outcomes := map[string]resource.Outcome{}
	if ok && raw != "" {
		var m map[string]runtimeConfigDigestEntry
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			glog.Warnw(ctx, "runtime sync: meta.runtime_config_digest 解析失败（本次按未同步处理）", "err", err)
		}
		for rt, e := range m {
			if e.GeneratorVersion != runtimeConfigGeneratorVersion {
				continue
			}
			digests[rt] = e.Digest
			if e.Outcome != nil {
				outcomes[rt] = *e.Outcome
			}
		}
	}
	r.mu.Lock()
	r.digests = digests
	r.outcomes = outcomes
	r.mu.Unlock()
}

// SyncModels 重新同步（`POST /models/sync`）。runtimeID 为空 = 全部已注册 runtime。
//
// 它**不绕过任何一道门**：未实测仍然如实报 untested、清单为空仍然走撤回路径、
// 自校验失败仍然一个字节不写。返回里也没有 `advertised`——那要新进程才拿得到。
func (r *Resources) SyncModels(ctx context.Context, runtimeID string) (*dtomodel.ModelSyncResp, error) {
	ids, err := r.runtimeIDsForSync(runtimeID)
	if err != nil {
		return nil, err
	}
	out := &dtomodel.ModelSyncResp{Applied: make([]dtomodel.RuntimeSyncResp, 0, len(ids))}
	var failed []string
	for _, id := range ids {
		o, err := r.SyncRuntime(ctx, id)
		if err != nil {
			return nil, err
		}
		out.Applied = append(out.Applied, outcomeToDTO(id, o))
		if o.Status == resource.StatusFailed {
			failed = append(failed, id+"："+strings.Join(o.Problems, "；"))
		}
	}
	if len(failed) > 0 {
		// 失败面按设计映 50001（见 `pkg/code/acl.go` 的 classCodeOverrides）：
		// 这是**服务侧**失败，不是 50201"上游连接失败"——后者的排障方向是底座进程/网络，
		// 而这里真因更可能在平台自己的生成器、校验门或磁盘上。
		return nil, acl.NewError(acl.ClassService, "resource_sync_failed",
			"同步失败（**资源没有被回滚**，只是没交付到这些会话进程）："+strings.Join(failed, " | ")+
				"；修掉原因后重试 POST /models/sync", nil)
	}
	return out, nil
}

// runtimeIDsForSync 解析要同步的 runtime 集合（空 = 全部已注册，升序）。
func (r *Resources) runtimeIDsForSync(runtimeID string) ([]string, error) {
	if runtimeID != "" {
		if _, ok := r.reg.Get(runtimeID); !ok {
			return nil, acl.NewError(acl.ClassRequest, "runtime_not_found",
				fmt.Sprintf("runtime %q 未注册", runtimeID), nil)
		}
		return []string{runtimeID}, nil
	}
	ids := make([]string, 0, 4)
	for _, spec := range r.reg.List() {
		ids = append(ids, spec.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// SyncRuntime 把一个 runtime 的整份声明交付给它（编排的唯一入口）。
//
// 步骤（设计 §关键逻辑 ③）：
//
//  1. 未注册 / 未声明 syncer → `unsupported`（**不报错**：那是"该交付面不承载清单"的事实）；
//  2. 组 Set（读资源 + 解密密钥 + 凭据通道二选一）——组装过程中的问题**不阻断**，
//     而是让那条供应商整体不进 Set 并记一条 problem（宁可不下发，也不下发半条）；
//  3. 调端口；err != nil → `failed` + 可执行出路，**不覆盖上一份成功的状态**；
//  4. 仅 applied/unchanged 时记摘要并更新代际键输入。
func (r *Resources) SyncRuntime(ctx context.Context, runtimeID string) (resource.Outcome, error) {
	spec, ok := r.reg.Get(runtimeID)
	if !ok {
		return resource.Outcome{
			Status:   resource.StatusUnsupported,
			Problems: []string{fmt.Sprintf("runtime %q 未注册：平台不会为未知交付面猜形态", runtimeID)},
		}, nil
	}
	syncer, ok := r.reg.Syncer(runtimeID)
	if !ok {
		return resource.Outcome{
			Status: resource.StatusUnsupported,
			Where:  "未声明模型清单交付面",
			Problems: []string{"该交付面没有实现同步端口（pkg/resource.Syncer）：" +
				"平台只交付它认得的交付面，不写猜出来的配置文件"},
		}, nil
	}

	set, problems := r.buildSet(ctx, spec)
	outcome, err := syncer.SyncModels(ctx, set)
	if err != nil {
		// 失败**不覆盖**上一份成功的状态：界面继续显示上一次的真实结论，而不是被这次失败抹掉。
		return resource.Outcome{
			Status:   resource.StatusFailed,
			Where:    outcome.Where,
			Effect:   resource.EffectNextProcess,
			Problems: append(problems, "同步失败："+err.Error()), Credential: outcome.Credential,
		}, nil
	}
	outcome.Problems = append(problems, outcome.Problems...)
	if outcome.Status == resource.StatusApplied || outcome.Status == resource.StatusUnchanged {
		if rerr := r.recordSyncOutcome(ctx, runtimeID, set, outcome); rerr != nil {
			return outcome, rerr
		}
	}
	r.mu.Lock()
	if r.outcomes == nil {
		r.outcomes = map[string]resource.Outcome{}
	}
	r.outcomes[runtimeID] = outcome
	r.mu.Unlock()
	return outcome, nil
}

// buildSet 组装某 runtime 该拿到的**一整份**声明（顺序固定：同输入同字节，逐字节 golden 依赖它）。
//
// 两条「整体不进 Set」的判据都是刻意的：缺端点或密钥解不开时**不下发半条**
// （半条配置会让底座报一个与真实原因无关的错），改为记一条可执行的 problem。
func (r *Resources) buildSet(ctx context.Context, spec runtime.Spec) (resource.Set, []string) {
	set := resource.Set{Runtime: spec.ID}
	problems := []string{}

	models, err := r.declaredModels(ctx)
	if err != nil {
		return set, []string{"读清单失败：" + err.Error()}
	}
	byProvider := map[string][]resource.Model{}
	for _, m := range models {
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}
	providerIDs := make([]string, 0, len(byProvider))
	for id := range byProvider {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)

	defaultID, _ := r.declaredDefaultModelID(ctx)

	for _, pid := range providerIDs {
		// 只保留「至少有一条模型」的供应商：底座侧一条 route 挂零模型没有意义。
		d, key, kerr := r.providerForSync(pid)
		if d == nil {
			problems = append(problems, fmt.Sprintf(
				"供应商 %q 已不存在，挂靠它的 %d 条模型不会被交付：在「设置 · 模型」里把它改挂到别的供应商，或删掉这些模型",
				pid, len(byProvider[pid])))
			continue
		}
		if strings.TrimSpace(d.BaseURL) == "" {
			problems = append(problems, fmt.Sprintf(
				"供应商 %q 缺端点（`base_url` 为空），它的 %d 条模型不会被交付：在「设置 · 模型」里补一个 OpenAI 兼容端点",
				pid, len(byProvider[pid])))
			continue
		}
		if kerr != nil || key == "" {
			reason := "尚未设置密钥"
			if kerr != nil {
				reason = "密钥不可用：" + kerr.Error()
			}
			problems = append(problems, fmt.Sprintf(
				"供应商 %q %s，它的 %d 条模型不会被交付：在「设置 · 模型」里补一条密钥"+
					"（或把环境变量设好——进程环境按层序优先于面板里保存的那条）",
				pid, reason, len(byProvider[pid])))
			continue
		}
		ref := resource.RefFor(spec.Credential.RefPattern, pid)
		if ref == "" {
			problems = append(problems, fmt.Sprintf(
				"供应商 %q 无法派生凭据引用名（该交付面未声明 `RefPattern`），它的模型不会被交付", pid))
			continue
		}
		set.Providers = append(set.Providers, resource.Provider{
			ID: d.ID, Name: d.Name, Protocol: resource.Protocol(d.Protocol),
			BaseURL: d.BaseURL, Ref: ref,
		})
		if pd := byProvider[pid]; len(pd) > 0 {
			rows := append([]resource.Model(nil), pd...)
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].ProviderID != rows[j].ProviderID {
					return rows[i].ProviderID < rows[j].ProviderID
				}
				return rows[i].Model < rows[j].Model
			})
			set.Models = append(set.Models, rows...)
		}
	}
	sort.Slice(set.Providers, func(i, j int) bool { return set.Providers[i].ID < set.Providers[j].ID })
	sort.Slice(set.Models, func(i, j int) bool {
		if set.Models[i].ProviderID != set.Models[j].ProviderID {
			return set.Models[i].ProviderID < set.Models[j].ProviderID
		}
		return set.Models[i].Model < set.Models[j].Model
	})

	// 默认模型必须仍存在于 Set.Models 里，否则忽略并记 problem（模型被删了 → 默认跟着失效）。
	if defaultID != "" {
		found := false
		for _, m := range set.Models {
			if m.ID == defaultID {
				set.DefaultModel = defaultID
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, "默认模型指向的条目已不在清单里（或它挂靠的供应商当前不可交付），"+
				"本次不下发默认模型：到「设置 · 模型」重新选一个默认模型")
		}
	}

	// 凭据通道**二选一**（决定权在编排层，不在 adapter——"要不要注入 env"是启动者的事）。
	cred, credProblems := r.applyCredentialChannel(ctx, spec, &set, byProvider)
	problems = append(problems, credProblems...)
	set.CredentialDocPath = cred.docPath
	if cred.docPath != "" {
		// 原生文档通道：密钥交给 adapter（它知道该底座的文档格式）。
		set.Providers = cred.providers
	}
	_ = cred.outcome
	return set, problems
}

// credentialPlan 凭据通道的决策结果。
type credentialPlan struct {
	docPath   string
	providers []resource.Provider
	outcome   resource.CredentialOutcome
}

// applyCredentialChannel 按 `CredentialSpec.Tested` 二选一（ADR-4 / 风险 R11）。
//
// 两条通道**绝不**同时启用：dsh 0.2.1 的优先级是"存储文档 > 继承环境"（0.2.0 相反），同时启用
// 会让其中一条变成静默死值——用户轮换了密钥、另一条还在，而生效的仍是旧值，**且没有任何报错**。
func (r *Resources) applyCredentialChannel(
	ctx context.Context, spec runtime.Spec, set *resource.Set, _ map[string][]resource.Model,
) (credentialPlan, []string) {
	problems := []string{}
	// 逐供应商取明文（只在这一层短暂存在；去向只有 adapter 的凭据文档或子进程 env 两处）。
	withKeys := make([]resource.Provider, 0, len(set.Providers))
	for _, p := range set.Providers {
		_, key, kerr := r.providerForSync(p.ID)
		if kerr != nil || key == "" {
			continue
		}
		p.APIKey = key
		withKeys = append(withKeys, p)
	}

	native := spec.Credential.Tested && strings.TrimSpace(spec.Credential.NativeDoc) != ""
	if native {
		docPath, err := r.resolveCredentialDoc(spec)
		if err != nil {
			problems = append(problems, "原生凭据文档路径无法解析（"+err.Error()+"）：本次回退到子进程 env 注入")
			native = false
		} else {
			// 原生通道：**平台不注入同名 env**（否则 env 会遮蔽文档，轮换静默失效）。
			for _, p := range set.Providers {
				if err := r.reg.DeliveryFor(spec.ID).SetEnv(p.Ref, ""); err != nil {
					glog.Warnw(context.Background(), "runtime sync: 撤销凭据 env 失败", "runtime", spec.ID, "ref", p.Ref, "err", err)
				}
			}
			return credentialPlan{
				docPath:   docPath,
				providers: withKeys,
				outcome: resource.CredentialOutcome{
					Channel: resource.ChannelNativeDoc, Status: resource.StatusApplied,
					Effect: resource.EffectNextRun, Tested: true,
					Where: "原生凭据文档（落盘 0600，改密钥下一轮生效）",
				},
			}, problems
		}
	}

	// env 通道：平台把明文注入子进程 env（按**逐供应商**的引用名，ADR-16 修 v11 的共用密钥缺陷）。
	delivery := r.reg.DeliveryFor(spec.ID)
	for _, p := range withKeys {
		if err := delivery.SetEnv(p.Ref, p.APIKey); err != nil {
			problems = append(problems, "注入凭据 env 失败（"+p.Ref+"）："+err.Error())
			continue
		}
	}
	if len(withKeys) == 0 {
		return credentialPlan{outcome: resource.CredentialOutcome{
			Channel: resource.ChannelNone, Status: resource.StatusUntested,
			Where: "没有可交付的密钥",
		}}, problems
	}
	tested := spec.Credential.Tested
	effect := resource.EffectNextProcess
	if tested {
		effect = resource.EffectNextRun
	}
	return credentialPlan{
		outcome: resource.CredentialOutcome{
			Channel: resource.ChannelEnv, Status: resource.StatusApplied, Effect: effect, Tested: tested,
			Where: "子进程环境变量（逐供应商独立引用名）",
		},
	}, problems
}

// resolveCredentialDoc 把 `CredentialSpec.NativeDoc` 的占位符展开成**绝对路径**。
//
// 只看该 runtime 自己的 `Launch.Env`（隔离表注入的那份）——**刻意不回落到进程环境**：
// 回落会让平台去写用户自己的 `~/.dsh`，而那正是本机隔离纪律禁止的事，且它是**静默**的。
func (r *Resources) resolveCredentialDoc(spec runtime.Spec) (string, error) {
	launch := map[string]string{}
	for _, kv := range spec.Launch.Env {
		if name, val, ok := strings.Cut(kv, "="); ok {
			launch[strings.TrimSpace(name)] = val
		}
	}
	lookup := func(name string) (string, bool) {
		v, ok := launch[name]
		if !ok || strings.TrimSpace(v) == "" {
			return "", false
		}
		return v, true
	}
	out, err := runtime.ExpandProjectionRoot(spec.Credential.NativeDoc, lookup)
	if err != nil {
		return "", fmt.Errorf("%w（提示：装配期应已调用 Registry.ApplyIsolationEnv；平台只在**自己的**隔离根上物化）", err)
	}
	return out, nil
}

// providerForSync 取某供应商的在册定义 + 生效明文密钥（不存在 → (nil,"",nil)）。
//
// 密钥走 `provider.Plane.KeyFor`（四层回落的唯一入口）：内存里那条与库里的那一列都是它的输入，
// 所以这里不再有"两条取密钥的路"（v13–v19 的 `Key` / `KeyRef + 密文句柄` 二选一就是那种分叉）。
func (r *Resources) providerForSync(providerID string) (*provider.Def, string, error) {
	ctx := context.Background()
	// **不持本结构的锁去问域**：`Snapshot` 自己取供应商域的锁并返回值语义副本。
	d, _ := r.providers.Snapshot(providerID)
	var cp *provider.Def
	if d != nil {
		c := *d
		cp = &c
	}
	if cp == nil {
		// sqlite 载体可能比内存先有（导入/并发装配）：回落到行（v13 起是纯列投影）。
		if st := r.Store(); st != nil {
			row, found, err := st.GetProviderDefinition(ctx, providerID)
			if err != nil || !found {
				return nil, "", err
			}
			cp = &provider.Def{
				ID: row.ID, Name: row.Name, Protocol: row.Protocol, BaseURL: row.BaseURL,
				Note: row.Note, KeyRef: credential.ProviderRef(row.ID), Revision: row.Revision,
			}
		}
	}
	if cp == nil {
		return nil, "", nil
	}
	res := r.providers.KeyFor(ctx, cp)
	if res.Value == "" && len(res.Problems) > 0 {
		return cp, "", errors.New(strings.Join(res.Problems, "；"))
	}
	return cp, res.Value, nil
}

// declaredModels 读全部平台清单并折成端口值对象。
func (r *Resources) declaredModels(ctx context.Context) ([]resource.Model, error) {
	st := r.Store()
	if st == nil {
		return nil, nil
	}
	rows, err := st.ListModelDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]resource.Model, 0, len(rows))
	for i := range rows {
		name := rows[i].Name
		if name == "" {
			name = rows[i].Model
		}
		out = append(out, resource.Model{
			ID: rows[i].ID, ProviderID: rows[i].ProviderID, Model: rows[i].Model, Name: name,
			// **总是带值**（默认 `["text"]`）：adapter 据此总是写底座声明里的 `input`，
			// 于是"默认 text"是明确值而不是"没设"（ADR-12）。
			InputModalities: resource.ModalitiesFromColumn(rows[i].InputModalities),
			// 容量（v25）：指针原样透传——nil 与"声明了一个数"是两件事，这里不做任何归一，
			// 由 adapter 决定"非空才写"。
			MaxInputTokens: rows[i].MaxInputTokens, MaxOutputTokens: rows[i].MaxOutputTokens,
		})
	}
	return out, nil
}

// capacitySentinel 把容量折成指纹里的一个**不含歧义**的串：nil → `-`，有值 → 十进制。
//
// 为什么不用空串：容量是正整数（写面已把 0 折成 nil），用 `-` 能让"未声明"在任何位置都读得出来，
// 而空串在拼接型指纹里与"少写一个字段"长得一样。
func capacitySentinel(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}

// declaredDefaultModelID 读**单一**默认模型（未设 → ""）。
func (r *Resources) declaredDefaultModelID(ctx context.Context) (string, bool) {
	row, ok, err := r.providers.DefaultModelRow(ctx)
	if err != nil || !ok {
		return "", false
	}
	return row.ID, true
}

// setFingerprint 由**声明**（而不是产物）算出同步指纹。
//
// 为什么由声明算：幂等的判据是"同一份声明重复同步不换代际"。若用产物算，任何一次格式化差异
// （键序、缩进）都会让代际无谓地翻一次——而代际一翻，所有引用该底座的会话都要重建连接。
//
// **密钥摘要显式并进**（ADR-13）：启用原生凭据文档后密钥不再进 env，v11 那条"env 变了所以
// 基础键变"的路径就断了。不并进去，用户改了密钥、界面说保存成功，而换代际的判据消失
// ——"生效"这件事会变成不可观测。摘要**不进日志、不进 API 响应、不进事件**。
func setFingerprint(set resource.Set) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			fmt.Fprintf(h, "%s\x00", p)
		}
	}
	write("v1", set.Runtime, set.DefaultModel)
	for _, p := range set.Providers {
		write("P", p.ID, p.Name, string(p.Protocol), p.BaseURL, p.Ref)
		if p.APIKey != "" {
			sum := sha256.Sum256([]byte(p.APIKey))
			write("K", hex.EncodeToString(sum[:8]))
		}
	}
	for _, m := range set.Models {
		// 模态并进指纹：它改变交付字节（dsh 的 `models[].input`），所以它变了代际必须翻——
		// 否则用户改了"能不能收图"、界面说保存成功，而"下一轮生效"这件事不可观测。
		//
		// 容量（v25）同理并进指纹：它改变交付字节（`contextWindow`/`maxTokens`）。nil 与"有值"
		// 必须可分——用 `-` 当"未声明"的哨兵（容量恒为正整数，与任何合法取值都不撞车）。
		write("M", m.ID, m.ProviderID, m.Model, m.Name, strings.Join(m.InputModalities, ","),
			capacitySentinel(m.MaxInputTokens), capacitySentinel(m.MaxOutputTokens))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// recordSyncOutcome 把本次摘要与结论写进 meta 与内存（内存里那份并进代际键）。
func (r *Resources) recordSyncOutcome(ctx context.Context, runtimeID string, set resource.Set, outcome resource.Outcome) error {
	digest := setFingerprint(set)
	r.mu.Lock()
	if r.digests == nil {
		r.digests = map[string]string{}
	}
	r.digests[runtimeID] = digest
	r.mu.Unlock()
	st := r.Store()
	if st == nil {
		return nil
	}
	m := map[string]runtimeConfigDigestEntry{}
	if raw, ok, err := st.GetMeta(ctx, model.MetaKeyRuntimeConfigDigest); err == nil && ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	o := outcome
	m[runtimeID] = runtimeConfigDigestEntry{
		Digest: digest, GeneratedAt: model.FormatTime(time.Now().UTC()),
		GeneratorVersion: runtimeConfigGeneratorVersion, Outcome: &o,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return st.SetMeta(ctx, model.MetaKeyRuntimeConfigDigest, string(b))
}

// TrySyncModels 「提交就同步」：写面提交后把清单推给各底座，返回一句给用户看的话。
//
// 写面末尾的尽力同步：资源行**已经提交**，所以同步失败不回滚它，而是把失败如实带回写面回执
// （note），并留一条 WARN。为什么不直接返回错误：那会让前端以为"清单没保存"——它保存了。
// 真实情况是"清单已保存，但交付没成功"，这两件事必须分开说。
func (r *Resources) TrySyncModels(ctx context.Context) string {
	if r.Store() == nil {
		return ""
	}
	ids, err := r.runtimeIDsForSync("")
	if err != nil {
		return ""
	}
	notes := make([]string, 0, len(ids))
	for _, id := range ids {
		o, serr := r.SyncRuntime(ctx, id)
		if serr != nil {
			glog.Warnw(ctx, "runtime sync: 写面后的同步失败（资源已保存，交付未更新）", "runtime", id, "err", serr)
			notes = append(notes, "⚠ 已保存，但同步失败（"+serr.Error()+"）：重试 POST /models/sync")
			continue
		}
		if o.Status == resource.StatusFailed {
			notes = append(notes, "⚠ 已保存，但同步失败："+strings.Join(o.Problems, "；")+"：重试 POST /models/sync")
		}
	}
	return strings.Join(notes, "；")
}

// RuntimeConfigStates 返回各 runtime 最后一次同步的结论（`mate doctor` 与读面用）。
func (r *Resources) RuntimeConfigStates(ctx context.Context) map[string]dtomodel.RuntimeSyncResp {
	out := map[string]dtomodel.RuntimeSyncResp{}
	r.mu.Lock()
	outcomes := map[string]resource.Outcome{}
	for k, v := range r.outcomes {
		outcomes[k] = v
	}
	r.mu.Unlock()
	for _, spec := range r.reg.List() {
		o, ok := outcomes[spec.ID]
		if !ok {
			if _, hasSyncer := r.reg.Syncer(spec.ID); !hasSyncer {
				o = resource.Outcome{Status: resource.StatusUnsupported,
					Where:    "未声明模型清单交付面",
					Problems: []string{"该交付面没有实现同步端口（pkg/resource.Syncer）"}}
			} else {
				o = resource.Outcome{Status: resource.StatusUntested,
					Where:    "尚未同步过",
					Problems: []string{"平台还未交付这份清单：改一次清单或调 POST /models/sync"}}
			}
		}
		out[spec.ID] = outcomeToDTO(spec.ID, o)
	}
	return out
}

// outcomeToDTO 把端口结论折成机器面 DTO。
func outcomeToDTO(runtimeID string, o resource.Outcome) dtomodel.RuntimeSyncResp {
	status := o.Status
	if status == "" {
		status = resource.StatusUntested
	}
	pending := status == resource.StatusApplied || status == resource.StatusUnchanged
	problems := o.Problems
	if problems == nil {
		problems = []string{}
	}
	return dtomodel.RuntimeSyncResp{
		Runtime: runtimeID,
		Status:  status,
		Where:   o.Where,
		Effect:  o.Effect,
		Pending: pending,
		Credential: dtomodel.CredentialSyncResp{
			Channel: o.Credential.Channel, Status: o.Credential.Status,
			Effect: o.Credential.Effect, Tested: o.Credential.Tested, Where: o.Credential.Where,
		},
		Problems: problems,
	}
}
