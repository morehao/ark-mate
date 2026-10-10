package runtime

import (
	"context"

	"github.com/morehao/ark-mate/pkg/acl"
)

// sessionRuntime 是 Runtime 接口（§4.2 会话边界版）在「一条活着的 ACP 会话」上的通用实现：
// ID/ManagesOwnContext/… 均取自该 runtime 的投影 Spec（§6.1/§6.2），Prompt/Capabilities/
// ProviderSessionID 透传底层 acl.Session。
//
// opencode / dsh 两投影共用本实现（New*Runtime 各以对应 Spec 实例化）。
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

// Cancel 取消本会话进行中的 turn（透传底层会话取消；best-effort，错误由调用方决定是否吞）。
func (r *sessionRuntime) Cancel(ctx context.Context) error {
	return r.sess.Cancel(ctx)
}

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

// ConfigOptions 返回本会话的会话配置项（模型目录等，全部由运行时自报）。
func (r *sessionRuntime) ConfigOptions() []acl.ConfigOption {
	return r.sess.ConfigOptions()
}

// ApplyModel 切换模型，返回应用后的全量配置项状态。
//
// 实现原则（决策记录 D1/D2）：
//   - **按 category 取值，不按 option id**：各家 id 不同（dsh 的模型项就叫 "model"，但平台不依赖
//     这个巧合），只有 category（model）是协议统一的；
//   - **值不透明**：m.Model 原样回传，绝不拼接前缀或拆解结构；
//   - **运行时的 set 是唯一判据**：不做平台侧白名单预判（缓存可能滞后，预判会造成假阴性），
//     非法/不可用一律由运行时拒绝并把原始错误上抛；
//   - 运行时未广告模型配置项 → 不支持（平台不臆造进程级模型 env：子进程本就继承 daemon 环境，
//     用户自有的底座配置（如 opencode.json）照常生效，平台不越权改写）。
//
// 思考档位（category=thought_level）不在平台侧下发：运行时若提供该选项，只随全量状态原样
// 透出（展示/可观测），平台不代为选择。
func (r *sessionRuntime) ApplyModel(ctx context.Context, m ModelConfig) ([]acl.ConfigOption, error) {
	opts := r.sess.ConfigOptions()
	mOpt, ok := acl.FindConfigOption(opts, acl.CategoryModel)
	if !ok {
		return opts, unsupported("unsupported_model",
			"runtime "+r.spec.ID+" 未广告模型配置项（category=model），无法在会话内切换模型")
	}
	if m.Model == "" {
		return opts, nil // 空值 = 用运行时默认，不下发设置
	}
	return r.sess.SetConfigOption(ctx, mOpt.ID, m.Model)
}

// Resume 热切换会话。会话恢复的 ACP 承载为 acl.Client.ResumeSession（session/resume 支持随
// 底座而异，待实测）；投影持有单会话，不做隐式热切换，由调用方按需重建投影。
func (r *sessionRuntime) Resume(_ context.Context, _ string) error {
	return unsupported("unsupported_resume",
		"runtime "+r.spec.ID+" 不支持投影内 Resume：恢复走 acl.Client.ResumeSession，需重建投影")
}

// ——— 两投影公开形态（§6.2） ———

// OpencodeRuntime / DeepseekHarnessRuntime 为两个投影的命名别名（均指
// sessionRuntime），New*Runtime 各以对应 Spec 实例化。
type (
	OpencodeRuntime        = sessionRuntime
	DeepseekHarnessRuntime = sessionRuntime
)

// NewOpencodeRuntime 包装已建立的 opencode 会话（后端自管上下文，§6.2）。
func NewOpencodeRuntime(sess *acl.Session) *OpencodeRuntime {
	return newSessionRuntime(opencodeSpec(), sess)
}

// NewDeepseekHarnessRuntime 包装已建立的 deepseek-harness（dsh）会话（后端自管上下文，§6.2）。
func NewDeepseekHarnessRuntime(sess *acl.Session) *DeepseekHarnessRuntime {
	return newSessionRuntime(dshSpec(), sess)
}

// NewProjection 依 Spec 分发到对应投影构造器（Orchestrator 采用 Runtime 的单一接缝，
// design doc §16）。未知/未投影 runtime id 返回 nil（调用方保持原直连路径）。
// sess 为 nil 时已知投影仍返回非 nil 实例（构造不触碰会话；方法调用方负责非 nil）。
func NewProjection(spec Spec, sess *acl.Session) Runtime {
	switch spec.ID {
	case "opencode":
		return NewOpencodeRuntime(sess)
	case "dsh":
		return NewDeepseekHarnessRuntime(sess)
	default:
		return nil
	}
}
