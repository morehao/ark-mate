package provider

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/credentials"
	goruntime "github.com/morehao/ark-mate/pkg/runtime"
)

// Plane 供应商域的**有状态主体**：持有内存档案表、档案目录、库句柄、注册表与凭据机制。
//
// 并发审查（搬之前必做，§3.1.19 的先例，结论见计划 §3.1.35）：本域 29 处 `s.mu` 临界区里
// **0 处跨域**——19 处只碰本域字段（`providers` 映射 / 写面 / 探活），另外几处各碰一个单域字段。
// 所以这里自持一把锁是安全的：**不存在"读会话 + 改供应商必须原子"的要求**。
type Plane struct {
	mu          sync.Mutex
	providers   map[string]*Def
	providerDir string
	st          *store.Store
	reg         *goruntime.Registry
	creds       *credential.Store

	// effect 写面回执（读会话与代际裁决，属编排侧）。nil = 没有编排器（demo/migrate/纯列表）。
	effect EffectFn
	// defaultModel 平台默认模型落在哪条供应商上（空 = 没有默认模型 → 不注入密钥）。
	defaultModel DefaultModelProvider

	// catalog 运行时模型目录缓存与拒绝值反馈（独立锁；见 catalog.go）。
	catalog *configCatalog

	// 目录取样的三个窄端口（见 catalog_read.go）：活会话 / 历史会话取样 / 连接期事实。
	// 任一为 nil = 该来源不可用（纯列表装配、无执行面）；**不是错误**，说明文案会如实写出来。
	live      LiveConfigOptionsSource
	sampler   CatalogSampler
	connFacts CatalogConnectionFacts
}

// EffectFn 计算"这次改动对谁何时生效"的回执。
//
// 为什么是端口：它要 `sessions` 与 `generation` 的裁决（编排侧的东西），而写面只是把结论塞进
// 响应。`nil` 是**合法取值**（没有编排器时"哪些会话会顺延"这个问题本身不成立），
// 与 `providerEffect` 今天的返回 nil 逐字同义。
type EffectFn func(providerID string) *dtoprovider.ProviderEffect

// DefaultModelProvider 平台默认模型落在哪条供应商上（`ok=false` = 没有默认模型）。
//
// 为什么是端口：`defaultModelRow` 要读 `models` 表与 `meta.default_model`，而它有四个消费面
// （三个不在本域），所以实现留在原处、由端口交下来。
type DefaultModelProvider func(ctx context.Context) (providerID string, ok bool)

// NewPlane 造一个供应商域。`reg` 与 `creds` 必须由装配期注入**同一个实例**（两份 = 两个事实源：
// 注册表决定"这条档案能不能被交付"，凭据机制决定"密钥从哪一层来"）。
func NewPlane(reg *goruntime.Registry, creds *credential.Store) *Plane {
	return &Plane{
		providers: make(map[string]*Def),
		reg:       reg,
		creds:     creds,
		catalog:   newConfigCatalog(),
	}
}

// SetStore 装配期登记库句柄（**必须在装配期调用**：构造时按值传一个当时的 `st` 会永远停在
// nil，而表现是"该走库的走了文件"——静默）。
func (pl *Plane) SetStore(st *store.Store) { pl.st = st }

// SetEffect 装配期登记写面回执（没有编排器时不登记 → 回执为 nil，与今天同义）。
func (pl *Plane) SetEffect(fn EffectFn) { pl.effect = fn }

// SetDefaultModel 装配期登记"默认模型落在哪条供应商"（不登记 → 视为没有默认模型）。
func (pl *Plane) SetDefaultModel(fn DefaultModelProvider) { pl.defaultModel = fn }

// Exists 档案在不在册（外部读点用；`model_list.go` 与 `resource_sync.go` 各一处）。
func (pl *Plane) Exists(id string) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.providers[id] != nil
}

// Snapshot 取一份**值拷贝**的档案快照（nil = 不在册）。
//
// 值语义是刻意的：调用方（探活）拿到的快照不该随后续写入变化——`probeLocal`/`probeUpstream`
// 依赖这一点（它们会在快照上解析密钥并把结论写进回执）。
func (pl *Plane) Snapshot(id string) (*Def, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	d := pl.providers[id]
	if d == nil {
		return nil, false
	}
	c := *d
	return &c, true
}

// CredentialDoc 托管凭据文档（测试与诊断面读它的现状；**只读**，写入一律走本域的方法）。
func (pl *Plane) CredentialDoc() *credentials.Doc { return pl.credentialDoc() }

// DeleteForTest 从内存表里摘掉一条档案（**只给测试**：模拟"库里删了、内存还没同步"这类竞态）。
//
// 为什么不给生产用：档案的删除有完整路径（`DeleteProvider`：写库 + 摘表 + 撤镜像），
// 绕过它的后果是"盘上还在、内存没了"。
func (pl *Plane) DeleteForTest(id string) {
	pl.mu.Lock()
	delete(pl.providers, id)
	pl.mu.Unlock()
}

// ——— 凭据面的胶水：域直接调 `credential.Store`（P3d 起机制层已收口） ———

// KeyFor 解析一条档案的生效密钥（四层回落：继承环境 > 托管 > <cwd>/.env > <home>/.env）。
//
// 那一行的 `nil` 守卫是**类型擦除的产物、不是第二份逻辑**：调用方可能拿到 nil 档案
// （map 查找的结果），而机制层不认识本域的 `Def`，守卫只能留在这里。
func (pl *Plane) KeyFor(ctx context.Context, d *Def) credential.ProviderKey {
	if d == nil {
		return credential.ProviderKey{}
	}
	return pl.creds.ResolveProviderKey(ctx, d.ID, d.KeyRef)
}

func (pl *Plane) mirrorSet(ref, value string) error {
	return credential.MirrorSet(pl.creds.Doc(), ref, value)
}

func (pl *Plane) mirrorDelete(ref string) error {
	return credential.MirrorDelete(pl.creds.Doc(), ref)
}

func (pl *Plane) credentialDoc() *credentials.Doc { return pl.creds.Doc() }

// registeredRuntimeIDs 已注册 runtime id（升序，保证响应稳定）。
//
// 唯一实现落在 `pkg/runtime.Registry.IDs()` 上（迁出本域前它是 `Service` 的私有方法，
// 而 `library.go` 也用它——两处各写一遍就是"两份实现"）。
func (pl *Plane) registeredRuntimeIDs() []string {
	if pl.reg == nil {
		return nil
	}
	ids := pl.reg.IDs()
	sort.Strings(ids)
	return ids
}

// Def provider 档案的内存形态。
//
// 字段的语义与 v11–v20 的历史见 `providerFile`（磁盘形状）与 `Def.KeyRef` 的注释；
// 导出是因为 `resource_sync.go`（运行期同步面）与 `service/svcprovider`（自检；P4a 起从
// 执行面的旧实现搬进用例层）都要构造/拷贝它。
type Def struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url,omitempty"`
	// Note 备注（不驱动行为）。它是 body 里的一个普通键，这里提升成字段只为让写面读起来清楚。
	Note string `json:"note,omitempty"`
	// Key **生效的**明文（注入/判 has_key/进 adapter 的凭据文档用；绝不落档案文件）。
	//
	// v20 起它是解析结果而不是落盘物：权威在库（无库时在托管文档），装载与写面各解析一次。
	Key string `json:"-"`
	// KeySource 这把密钥是从哪一层来的（空 = 没有密钥）。
	//
	// 它必须摆到读面上：`继承的进程环境 > 托管` 这个顺序意味着**进程环境里的同名变量会遮住
	// 面板里保存的那一条**——用户轮换了密钥却毫无变化，且没有任何报错（2026-10-10 实测）。
	KeySource credentials.Source `json:"-"`
	// KeyEnvKey 生效值来自哪个环境变量名（仅 `KeySource == env` 时有值；诊断面展示用）。
	KeyEnvKey string `json:"-"`
	// KeyRef 托管文档里的键（**派生量**，= `providers/<id>`）。
	//
	// 它不再是载体字段：库里没有 `key_ref` 列（纯函数副本 + 两份值不一致时的表现是"凭据坏了"，
	// ADR-7）。档案文件与导出物里仍然写它（那是 v11 的落盘形状，也是"这条密钥落在哪"的线索）。
	KeyRef string `json:"key_ref,omitempty"`
	// KeyProblems 密钥面读不了/对不上的原因（分层的问题、库里没有等）。problems 据此给出可执行
	// 出路，**不静默降级**：`has_key` 只看 `Key` 是否为空，不看它。
	KeyProblems []string `json:"-"`
	// Revision 载体里的行版本（sqlite = `providers.revision`；memory = 0）。
	Revision int64 `json:"-"`
	// UpdatedAt 最后一次成功提交的时刻（sqlite = 行上的 updated_at；memory = 零值）。
	UpdatedAt time.Time `json:"-"`
}
