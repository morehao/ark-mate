package manager

import (
	"context"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// runtimeBinding 是「本进程认的那个底座」在平台侧的绑定（v19）。
//
// **它取代了 v19 之前的 AgentRec，而不是给它改了个名字**：agent 这个概念在本仓没有独立身份——
// 它的每个字段要么是**派生事实**（`is_default`/`session_count`/`default_model`），要么是**平台事实**
// （`permission_policy`、provider 绑定），而 `builtinAgents` 只有一行、`defaultAgentID()` 本来就返回
// 构建期固定的那个 runtime id。于是"哪个底座在跑"有两个答案指向同一个东西。收成一个之后：
//
//   - **单实例**：`s.binding` 在构造期建好、此后只读（`State` 除外，由 `s.mu` 保护）。
//     原先的 `agents map[string]*AgentRec` 与「当前 Agent」的显式选择（`currentAgent` + `default.json`）
//     一起消失——单底座下"选哪个"没有第二个候选；
//   - **没有 Provider 字段**：密钥注入的事实源是**声明的模型清单**（`declaredModels` →
//     `providerForSync`），不是 agent 上的一个可写字段。留着它就会出现"诊断说会注入 A、
//     实际注入 B"的第二份真相（见 `CredentialProblem`）；
//   - **agent 作用域的操作在单底座下等价于"全部会话"作用域**：原先按 agent 过滤的
//     会话句柄作废/连接拆除，现在按 runtime 全局做，行为逐字不变（每个会话都属于那一个底座）。
type runtimeBinding struct {
	// ID 构建期固定的那个 runtime id（`pkg/runtime.BuildRuntime`，不注入 = dsh）。
	// 平台一切归属判定都按它（会话的 `runtime_id` 列、连接、诊断）。
	ID string
	// Name 底座展示名（取自注册表里的 Spec；取不到时回落 ID）。
	Name string
	// State 底座进程状态：running | stopped（见 session.AgentStateRunning/Stopped）。
	// 由 `s.mu` 保护；**唯一的写入方**是连接的建立与拆除。
	State string
}

// newRuntimeBinding 构造绑定（装配期一次）。`spec` 取不到时 Name 回落 ID——
// 构建期选定的 runtime 未注册是**另一个**错误，由 `selectedSpec` 明确报出，这里不重复判定。
func newRuntimeBinding(spec runtime.Spec, specOK bool) *runtimeBinding {
	id := runtime.SelectedRuntime()
	name := id
	if specOK && spec.Name != "" {
		name = spec.Name
	}
	return &runtimeBinding{ID: id, Name: name, State: session.AgentStateStopped}
}

// bindingLocked 取绑定指针（调用方须持 s.mu）。
//
// 构造期后恒非 nil——这正是它比 `requireDefaultAgent` 好的地方：v19 之前"注册表为空"是一条
// 真实的错误路径（`无可用 Agent`），而现在"本二进制认哪个底座"是编译期常量，不存在"没有"这种状态。
func (s *Service) bindingLocked() *runtimeBinding {
	return s.binding
}

// runtimeBindingSnapshot 取绑定的**值拷贝**（免在锁外读到半更新状态；只读消费方用）。
func (s *Service) runtimeBindingSnapshot() runtimeBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	rb := s.binding
	if rb == nil {
		return runtimeBinding{ID: runtime.SelectedRuntime(), Name: runtime.SelectedRuntime(), State: session.AgentStateStopped}
	}
	return *rb
}

// runtimeByID 按 id 取绑定：**只有构建期那个 id 命中**（单底座下没有第二个候选）。
//
// 它顶替了 v19 之前的 AgentByID：调用方给的 id 多来自会话记录或 URL，单底座下"别的 id"
// 就是"不存在的资源"，语义与从前一致（错误码从 agent_not_found 变成 runtime_not_found）。
func (s *Service) runtimeByID(id string) (*runtimeBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binding == nil {
		return nil, false
	}
	if id == "" || id == s.binding.ID {
		return s.binding, true
	}
	return nil, false
}

// requireRuntime 取绑定；未装配（构造期就失败）时给出可执行出路。
//
// 与 v19 之前的 requireDefaultAgent 的区别：那个函数的错误是**运行期**的（用户删掉了默认 agent），
// 这里的错误只可能来自"注册表被别处动过"，且 `selectedSpec` 已经会报得更准。
func (s *Service) requireRuntime() (*runtimeBinding, error) {
	rb, ok := s.runtimeByID(runtime.SelectedRuntime())
	if !ok {
		return nil, fmt.Errorf("构建期选定的 runtime %q 无绑定（装配顺序异常）", runtime.SelectedRuntime())
	}
	return rb, nil
}

// setRuntimeState 更新底座进程状态（Orchestrator 建/拆连接时调用）。
func (s *Service) setRuntimeState(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binding != nil {
		s.binding.State = state
	}
}

// ResolveRuntime 返回**构建期选定的**底座 id，并校验它确实已装配。
//
// 历史：这里曾是 FirstAvailable / CapabilityRouted 路由引擎（按 agent 声明的 runtime 候选试探）。
// 候选面随产品约束（用户不能选择 runtime）下线之后，路由的每个输入都成了常量——引擎只剩
// 「取那一个 + 校验已装配」这一步。
func (s *Service) ResolveRuntime() (string, error) {
	spec, err := s.selectedSpec()
	if err != nil {
		return "", err
	}
	return spec.ID, nil
}

// selectedSpec 取构建期选定的底座 spec；未装配 → 明确错误（不静默回落、不返回空串）。
//
// 正常路径由装配期的 `runtime.ProductRegistry` fail-closed 拦住（见 internal/app 与
// internal/gateway）；这里是"注册表被别处动过"时的兜底判据——返回空串会让调用方拿到一个
// 没有底座的会话，错误会推迟到某个更远的地方才出现。
func (s *Service) selectedSpec() (runtime.Spec, error) {
	id := runtime.SelectedRuntime()
	spec, ok := s.reg.Get(id)
	if !ok {
		return runtime.Spec{}, acl.NewError(acl.ClassRequest, "runtime_not_found",
			fmt.Sprintf("构建期选定的 runtime %q 未注册（检查 -ldflags 注入的 pkg/runtime.BuildRuntime "+
				"与 .ark-mate/config/runtimes.json）", id), nil)
	}
	return spec, nil
}

// RuntimeIdentity 见 `svcruntime.RuntimeSvc`：`/meta` 的底座身份（id + 展示名 + 版本）。
func (s *Service) RuntimeIdentity(_ context.Context) dtoruntime.IdentityResp {
	id, name, version := s.runtimeInfo()
	return dtoruntime.IdentityResp{ID: id, Name: name, Version: version}
}

// runtimeInfo 返回 `/meta` 的底座身份（id + 展示名 + 版本）。
//
// 为什么 `/meta` 而不是新开一条路由：`/meta` 是既有的**装配事实出口**（版本/资产/数据目录），
// "本二进制认哪个底座"正是同一类事实；加字段比加路由便宜，也不给用户一个新的选择面。
func (s *Service) runtimeInfo() (id, name, version string) {
	id = runtime.SelectedRuntime()
	name = id
	if spec, ok := s.reg.Get(id); ok {
		if spec.Name != "" {
			name = spec.Name
		}
		version = spec.Version
	}
	return id, name, version
}

// runtimeRequestTimeout 诊断/探测类调用的统一超时（避免装配路径被慢底座拖住）。
const runtimeRequestTimeout = 10 * time.Second

// runtimeCtx 给诊断类调用一个带超时的 ctx（调用方传 nil 时也不 panic）。
func runtimeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, runtimeRequestTimeout)
}
