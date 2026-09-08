package runtime

import (
	"context"

	"github.com/morehao/ark-forge/internal/acl"
)

// EchoRuntime 是 echo 投影的可执行形态（§4.2 会话边界版）：包装一条已拨活的 echo ACP 会话。
// echo 为无模型演示底座 → 无原生权限模式/模型切换/持久会话，Apply*/Resume 显式返回不支持错误。
type EchoRuntime struct {
	sess *acl.Session
	spec Spec
}

// NewEchoRuntime 包装已建立的 echo 会话。调用方负责会话生命周期（aclsession.Close 等，与
// Orchestrator 持有连接回收的方式一致）。
func NewEchoRuntime(sess *acl.Session) *EchoRuntime {
	return &EchoRuntime{sess: sess, spec: echoSpec()}
}

// 编译期断言：EchoRuntime 实现 Runtime 接口（§4.2 会话边界版）。
var _ Runtime = (*EchoRuntime)(nil)

// unsupported 构造「该投影不支持此操作」的请求类错误（REST 面可据 code 精确映射）。
func unsupported(code, details string) error {
	return acl.NewError(acl.ClassRequest, code, details, nil)
}

func (r *EchoRuntime) ID() string              { return r.spec.ID }
func (r *EchoRuntime) ManagesOwnContext() bool { return r.spec.ManagesOwnContext }
func (r *EchoRuntime) Capabilities() acl.Capabilities {
	return r.sess.Client().Capabilities()
}
func (r *EchoRuntime) ProviderSessionID() string { return r.sess.ID() }

// Prompt 透传底层会话 Prompt（echo agent 驱动到 end_turn；权限/elicitation 决策由 Orchestrator
// 经 Dial 的 ClientOptions 钩子注入）。
func (r *EchoRuntime) Prompt(ctx context.Context, req PromptRequest) (string, error) {
	return r.sess.Prompt(ctx, req.Text)
}

func (r *EchoRuntime) ApplyMode(_ context.Context, _ Mode) error {
	return unsupported("unsupported_mode",
		"echo 投影不支持 ApplyMode：无原生模式映射（§6.2 ModeMapping 待实测）")
}
func (r *EchoRuntime) ApplyModel(_ context.Context, _ ModelConfig) error {
	return unsupported("unsupported_model", "echo 投影不支持 ApplyModel：演示底座无模型概念")
}
func (r *EchoRuntime) Resume(_ context.Context, _ string) error {
	return unsupported("unsupported_resume", "echo 投影不支持 Resume：演示会话无持久化")
}
