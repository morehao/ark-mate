package runtime

import (
	"context"

	"github.com/morehao/ark-forge/internal/acl"
)

// sessionRuntime 是 Runtime 接口（§4.2 会话边界版）在「一条活着的 ACP 会话」上的通用实现：
// ID/ManagesOwnContext/… 均取自该 runtime 的投影 Spec（§6.1/§6.2），Prompt/Capabilities/
// ProviderSessionID 透传底层 acl.Session。
//
// echo / opencode / deepseekharness 三投影共用本实现（New*Runtime 各以对应 Spec 实例化）。
// ApplyMode/ApplyModel/Resume 需底座侧原生支持或映射实测，当前统一显式返回 unsupported_*
// 错误（§6.2 ModeMapping / §5.5 模型面为文档开放项），不静默吞掉。
type sessionRuntime struct {
	spec Spec
	sess *acl.Session
}

// newSessionRuntime 以给定 Spec 包装已拨活的会话。调用方负责会话生命周期（acl.Session 的
// Close，与 Orchestrator 持有连接回收的方式一致）。
func newSessionRuntime(spec Spec, sess *acl.Session) *sessionRuntime {
	return &sessionRuntime{spec: spec, sess: sess}
}

// 编译期断言：sessionRuntime 实现 Runtime 接口（§4.2 会话边界版）。
var _ Runtime = (*sessionRuntime)(nil)

// unsupported 构造「该投影不支持此操作」的请求类错误（REST 面可据 code 精确映射）。
func unsupported(code, details string) error {
	return acl.NewError(acl.ClassRequest, code, details, nil)
}

func (r *sessionRuntime) ID() string              { return r.spec.ID }
func (r *sessionRuntime) ManagesOwnContext() bool { return r.spec.ManagesOwnContext }
func (r *sessionRuntime) Capabilities() acl.Capabilities {
	return r.sess.Client().Capabilities()
}
func (r *sessionRuntime) ProviderSessionID() string { return r.sess.ID() }

// Prompt 透传底层会话 Prompt（权限/elicitation 决策由 Orchestrator 经 Dial 的 ClientOptions
// 钩子注入）。
func (r *sessionRuntime) Prompt(ctx context.Context, req PromptRequest) (string, error) {
	return r.sess.Prompt(ctx, req.Text)
}

// ApplyMode 应用统一权限模式。三投影的 §6.2 ModeMapping 均为文档开放项（待实测回填），
// 映射未落地前不支持；回填后此处实现「统一模式 → runtime 原生 mode」会话应用原语。
func (r *sessionRuntime) ApplyMode(_ context.Context, _ Mode) error {
	return unsupported("unsupported_mode",
		"runtime "+r.spec.ID+" 不支持 ApplyMode：ModeMapping 待实测（§6.2 开放项）")
}

// ApplyModel 切换模型。投影 Spec.ModelConfigOptionID 为空 = 模型走 env/provider 配置
// （§5.5 模型面，无运行时切换原语），不支持；非空时亦需 ACP 侧支持后落地。
func (r *sessionRuntime) ApplyModel(_ context.Context, _ ModelConfig) error {
	if r.spec.ModelConfigOptionID == "" {
		return unsupported("unsupported_model",
			"runtime "+r.spec.ID+" 不支持 ApplyModel：模型走 env/provider 配置（ModelConfigOptionID 空）")
	}
	return unsupported("unsupported_model",
		"runtime "+r.spec.ID+" 的模型切换原语未接入 ACP 会话")
}

// Resume 热切换会话。会话恢复的 ACP 承载为 acl.Client.ResumeSession（session/resume 支持随
// 底座而异，待实测）；投影持有单会话，不做隐式热切换，由调用方按需重建投影。
func (r *sessionRuntime) Resume(_ context.Context, _ string) error {
	return unsupported("unsupported_resume",
		"runtime "+r.spec.ID+" 不支持投影内 Resume：恢复走 acl.Client.ResumeSession，需重建投影")
}

// ——— 三投影公开形态（§6.2） ———

// EchoRuntime / OpencodeRuntime / DeepseekHarnessRuntime 为三个投影的命名别名（均指
// sessionRuntime），New*Runtime 各以对应 Spec 实例化。
type (
	EchoRuntime            = sessionRuntime
	OpencodeRuntime        = sessionRuntime
	DeepseekHarnessRuntime = sessionRuntime
)

// NewEchoRuntime 包装已建立的 echo 会话（无模型演示底座，§6.2）。
func NewEchoRuntime(sess *acl.Session) *EchoRuntime {
	return newSessionRuntime(echoSpec(), sess)
}

// NewOpencodeRuntime 包装已建立的 opencode 会话（后端自管上下文，§6.2）。
func NewOpencodeRuntime(sess *acl.Session) *OpencodeRuntime {
	return newSessionRuntime(opencodeSpec(), sess)
}

// NewDeepseekHarnessRuntime 包装已建立的 deepseek-harness（dsh）会话（后端自管上下文，§6.2）。
func NewDeepseekHarnessRuntime(sess *acl.Session) *DeepseekHarnessRuntime {
	return newSessionRuntime(deepseekharnessSpec(), sess)
}

// NewProjection 依 Spec 分发到对应投影构造器（Orchestrator 采用 Runtime 的单一接缝，
// design doc §16）。未知/未投影 runtime id 返回 nil（调用方保持原直连路径）。
// sess 为 nil 时已知投影仍返回非 nil 实例（构造不触碰会话；方法调用方负责非 nil）。
func NewProjection(spec Spec, sess *acl.Session) Runtime {
	switch spec.ID {
	case "echo":
		return NewEchoRuntime(sess)
	case "opencode":
		return NewOpencodeRuntime(sess)
	case "deepseekharness":
		return NewDeepseekHarnessRuntime(sess)
	default:
		return nil
	}
}
