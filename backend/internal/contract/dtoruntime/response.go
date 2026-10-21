package dtoruntime

// DiagnosticsResp 底座诊断快照（`GET /runtime/diagnostics`）：连接态 + stderr 诊断尾巴 + 最近失败。
//
// 形状 = v19 之前的 `/agents/:id/diagnostics` 去掉 agent 身份，与前端
// `packages/types/src/runtime.ts` 的 `RuntimeDiagnostics` **逐字段一致**：
//
//   - 没有 `agent_id`（底座只有一个，身份由 `runtime` 回答）；
//   - 没有 `name`（那是 agent 的展示名；展示名现在只在 `GET /meta` 的 `runtime.name` 一处，
//     诊断面不重复第二份）。
//
// 前端那份是消费方，改这里必须同时改那里（两侧手工一致，由界面验收脚本端到端覆盖）。
type DiagnosticsResp struct {
	// State 底座进程状态：running | stopped。
	//
	// 它是**事实**（当前有没有活着的连接），不是 agent 的"运行态字段"——v19 之前这一项来自
	// 每个 agent 自己的一份 state，现在直接由连接集合推导（单底座 ⇒ 一个答案）。
	State string `json:"state"`
	// Runtime 构建期固定的底座 id（`-X internal/runtime.BuildRuntime`，不注入 = dsh）。
	Runtime string `json:"runtime"`
	// RuntimeName 底座的展示名（空 = 取不到，界面回落用 Runtime）。
	RuntimeName string `json:"runtime_name,omitempty"`
	// Command 实际拉起的启动命令（空 = 还没起过进程）。
	Command   string `json:"command,omitempty"`
	Connected bool   `json:"connected"`
	PID       int    `json:"pid,omitempty"`
	// StderrSource 尾巴来源：live（当前子进程实时）| last_error（上次失败采集）| none。
	StderrSource string         `json:"stderr_source"`
	StderrTail   []string       `json:"stderr_tail"`
	LastError    *LastErrorResp `json:"last_error,omitempty"`
	// Remediation 可执行修复步骤（按事实推导：runtime 未装配 / 启动命令不可执行 / 要注入的
	// 供应商悬空或无密钥 / 底座自报的鉴权方式 / 底座声明的安装步骤）。健康快照不带该字段。
	Remediation []string `json:"remediation,omitempty"`
	CheckedAt   string   `json:"checked_at"`
}

// LastErrorResp 底座最近一次失败的诊断快照（进程起不来 / 握手失败 / 轮次失败）。
// IdentityResp 构建期固定的底座身份（`/meta` 的 `runtime` 段）。
//
// 与 `ctrsystem.RuntimeInfo` 字段同名同形：controller 那层只把它投影进信封，不再自己拼一份——
// 两份拼法会在加字段时静默漂移（前端 `packages/types/src/runtime.ts` 是第三个消费点）。
type IdentityResp struct {
	ID string `json:"id"`
	// Name 展示名（取不到即空串，界面回落用 ID）。
	Name string `json:"name"`
	// Version 取不到即空串：核心**不探测**本机安装。
	Version string `json:"version"`
}

type LastErrorResp struct {
	At         string   `json:"at"`
	SessionID  string   `json:"session_id,omitempty"`
	Code       string   `json:"code"`
	Class      string   `json:"class"`
	Details    string   `json:"details"`
	StderrTail []string `json:"stderr_tail,omitempty"`
}
