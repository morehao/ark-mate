package runtimeconn

import (
	"context"
	"sync"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// Binding 是「本进程认的那个底座」在平台侧的绑定（v19）。
//
// **它取代了 v19 之前的 AgentRec，而不是给它改了个名字**：agent 这个概念在本仓没有独立身份——
// 它的每个字段要么是**派生事实**（`is_default`/`session_count`/`default_model`），要么是**平台事实**
// （`permission_policy`、provider 绑定），而 `builtinAgents` 只有一行、`defaultAgentID()` 本来就返回
// 构建期固定的那个 runtime id。于是"哪个底座在跑"有两个答案指向同一个东西。收成一个之后：
//
//   - **单实例**：平面在构造期建好、此后只读（`State` 除外，由平面自己的锁保护）。
//     原先的 `agents map[string]*AgentRec` 与「当前 Agent」的显式选择一起消失——单底座下没有第二个候选；
//   - **没有 Provider 字段**：密钥注入的事实源是**声明的模型清单**，不是绑定上的一个可写字段。
//     留着它就会出现"诊断说会注入 A、实际注入 B"的第二份真相；
//   - **agent 作用域的操作在单底座下等价于"全部会话"作用域**：原先按 agent 过滤的句柄作废/连接拆除，
//     现在按 runtime 全局做，行为逐字不变（每个会话都属于那一个底座）。
type Binding struct {
	// ID 构建期固定的那个 runtime id（`pkg/runtime.BuildRuntime`，不注入 = dsh）。
	// 平台一切归属判定都按它（会话的 `runtime_id` 列、连接、诊断）。
	ID string
	// Name 底座展示名（取自注册表里的 Spec；取不到时回落 ID）。
	Name string
	// State 底座进程状态：running | stopped（见 session.AgentStateRunning/Stopped）。
	// 由本平面的锁保护；**唯一的写入方**是连接的建立与拆除。
	State string
}

// Plane 执行面的**有状态主体**（P3a-2 分批搬入；本批只装绑定与诊断状态）。
type Plane struct {
	// reg runtime 注册表（取构建期选定那个的 Spec）。
	reg *runtime.Registry

	mu      sync.Mutex
	binding *Binding

	// diagMu 独立于 mu：诊断读面（`/runtime/diagnostics` 每次调用）与进程状态变更不该互相等。
	diagMu    sync.Mutex
	lastErr   *LastErr
	ambiguous *AmbiguityNote

	// 两个口子（装配期登记；nil = 该事实不可得，诊断如实少给一条出路，不臆造）。
	cred CredentialSource
	live LiveClientSource
}

// NewPlane 造一个执行面。`spec` 取不到（未注册）时 Name 回落 ID——构建期选定的 runtime 未注册是
// **另一个**错误，由 `SelectedSpec` 明确报出，这里不重复判定。
func NewPlane(reg *runtime.Registry, spec runtime.Spec, specOK bool) *Plane {
	id := runtime.SelectedRuntime()
	name := id
	if specOK && spec.Name != "" {
		name = spec.Name
	}
	return &Plane{
		reg:     reg,
		binding: &Binding{ID: id, Name: name, State: session.AgentStateStopped},
	}
}

// NewPlaneFor 装配层的入口：自己读"构建期选定的那个底座"再建平面。
//
// 为什么要有它（而不是让调用方先 `reg.Get(runtime.SelectedRuntime())`）：**"选哪个 + 注册了吗"
// 这条判据只许有一处**。装配层与 `manager.NewService` 各写一遍，迟早漂移成"装配层认为已注册、
// 平面认为没注册"——而那时的表现是诊断说 `runtime_not_found`、连接却接得上。
func NewPlaneFor(reg *runtime.Registry) *Plane {
	spec, ok := runtime.Spec{}, false
	if reg != nil {
		spec, ok = reg.Get(runtime.SelectedRuntime())
	}
	return NewPlane(reg, spec, ok)
}

// SetRegistry 装配期登记注册表（构造期没给时用）。
func (pl *Plane) SetRegistry(reg *runtime.Registry) { pl.reg = reg }

// Snapshot 取绑定的**值拷贝**（免在锁外读到半更新状态；只读消费方用）。
func (pl *Plane) Snapshot() Binding {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.binding == nil {
		id := runtime.SelectedRuntime()
		return Binding{ID: id, Name: id, State: session.AgentStateStopped}
	}
	return *pl.binding
}

// SetState 更新底座进程状态（建/拆连接时调用）。
func (pl *Plane) SetState(state string) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.binding != nil {
		pl.binding.State = state
	}
}

// Resolve 返回**构建期选定的**底座 id，并校验它确实已装配。
//
// 历史：这里曾是 FirstAvailable / CapabilityRouted 路由引擎（按 agent 声明的 runtime 候选试探）。
// 候选面随产品约束（用户不能选择 runtime）下线之后，路由的每个输入都成了常量——引擎只剩
// 「取那一个 + 校验已装配」这一步。
func (pl *Plane) Resolve() (string, error) {
	spec, err := pl.SelectedSpec()
	if err != nil {
		return "", err
	}
	return spec.ID, nil
}

// SelectedSpec 取构建期选定的底座 spec；未装配 → 明确错误（不静默回落、不返回空串）。
//
// 正常路径由装配期的 `runtime.ProductRegistry` fail-closed 拦住；这里是"注册表被别处动过"时的
// 兜底判据——返回空串会让调用方拿到一个没有底座的会话，错误会推迟到某个更远的地方才出现。
func (pl *Plane) SelectedSpec() (runtime.Spec, error) {
	id := runtime.SelectedRuntime()
	if pl.reg == nil {
		return runtime.Spec{}, unregisteredErr(id)
	}
	if spec, ok := pl.reg.Get(id); ok {
		return spec, nil
	}
	return runtime.Spec{}, unregisteredErr(id)
}

// Identity 返回 `/meta` 的底座身份（id + 展示名 + 版本）。
//
// 为什么 `/meta` 而不是新开一条路由：`/meta` 是既有的**装配事实出口**（版本/资产/数据目录），
// "本二进制认哪个底座"正是同一类事实；加字段比加路由便宜，也不给用户一个新的选择面。
func (pl *Plane) Identity(_ context.Context) dtoruntime.IdentityResp {
	id, name, version := pl.Info()
	return dtoruntime.IdentityResp{ID: id, Name: name, Version: version}
}

// Info 返回底座身份三元组（id 恒为构建期选的；name/version 取注册表声明，取不到就回落）。
func (pl *Plane) Info() (id, name, version string) {
	id = runtime.SelectedRuntime()
	name = id
	if pl.reg != nil {
		if spec, ok := pl.reg.Get(id); ok {
			if spec.Name != "" {
				name = spec.Name
			}
			version = spec.Version
		}
	}
	return id, name, version
}

// unregisteredErr 构建期选定的 runtime 没在注册表里（单底座下只可能是装配/配置问题）。
func unregisteredErr(id string) *acl.Error {
	return acl.NewError(acl.ClassRequest, "runtime_not_found",
		"构建期选定的 runtime "+id+" 未注册（检查 -ldflags 注入的 pkg/runtime.BuildRuntime "+
			"与 .ark-mate/config/runtimes.json）", nil)
}
