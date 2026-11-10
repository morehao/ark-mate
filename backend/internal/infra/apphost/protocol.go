package apphost

// ——— 隔离 host 的 JSON-lines RPC 线协议（C3，设计 §17.2；与 magpie Bun host 同构）———
//
// 一个应用包 = 一个长驻子进程（`mate app-host --pkg=<dir>`）。父子进程经子进程的 stdin/stdout
// 交换 **JSON-lines**：每行一个完整 JSON 对象（`\n` 结尾），一条消息一个请求/响应。绝不把多行
// JSON 拼进一行，也绝不把诊断写进 stdout（那会毁掉整条协议流——诊断一律走 stderr，与 `mate mcp`
// 的 stdout 纪律同源）。
//
// # 为什么每包一进程（隔离彻底）
//
// 进程是最硬的隔离边界：一个包 panic/OOM/死循环只炸它自己那个进程，内核与其它包完全不受影响
// （崩溃后该包的 impl/tool 标记 FAILED，重启退避若干次仍失败则永久 FAILED）。代价是进程数随包
// 数线性增长——单机单操作员场景可接受（对齐设计 §9「不引入新拓扑」的取舍：这里确实多了子进程，
// 但它们是隔离的、可回收的，且只在装了运行期包时才有）。
//
// # 帧形状
//
// 请求（父 → 子，一行）：
//
//	{"id":1,"method":"invokeTool","params":{"name":"fooai_query","args":{...}}}
//
// 响应（子 → 父，一行，二者其一）：
//
//	{"id":1,"result":{...}}                      // 成功
//	{"id":1,"error":{"code":"unknown_method","message":"...","class":"request_rejected"}}
//
// `id` 是父进程铸造的递增请求号，用于把响应配回请求（并发 RPC 时靠它配对）。子进程必须回带
// 同一个 id。null id（缺省）保留给子进程的**主动事件**（本期不用，留给 [增强项] 流式/日志流）。

// 方法名（唯一事实源；父子两侧都引用这里，改名即两侧同时改，不会漂）。
const (
	MethodHandshake   = "handshake"
	MethodListImpls   = "listImpls"
	MethodListTools   = "listTools"
	MethodInvokeTool  = "invokeTool"
	MethodApplySlot   = "applySlot"
	MethodDisposeSlot = "disposeSlot"
	MethodHealth      = "health"
	MethodShutdown    = "shutdown"
)

// 错误码（host 侧返回；父进程据此映射到 failure 分类，见 ClassOfCode）。
const (
	// ErrCodeUnknownMethod 子进程不认识这个方法名。
	ErrCodeUnknownMethod = "unknown_method"
	// ErrCodeBadParams 参数解析失败 / 缺必填。
	ErrCodeBadParams = "bad_params"
	// ErrCodeUnknownTool 请求调用的工具名不在本包声明的工具集里。
	ErrCodeUnknownTool = "unknown_tool"
	// ErrCodeUnknownImpl 请求的 impl_id 不在本包提供的实现里。
	ErrCodeUnknownImpl = "unknown_impl"
	// ErrCodeToolFailed 工具执行本身失败（业务错误，不是协议错误）。
	ErrCodeToolFailed = "tool_failed"
	// ErrCodeSlotFailed slot apply/dispose 失败。
	ErrCodeSlotFailed = "slot_failed"
	// ErrCodeInternal 子进程内部错误（含被 recover 掉的 panic，尽量在协议层转成这类而不是崩进程）。
	ErrCodeInternal = "internal"
	// ErrCodeAPIVersionMismatch 握手时 api_version 不兼容。
	ErrCodeAPIVersionMismatch = "api_version_mismatch"
	// ErrCodeResponseTooLarge 响应超过父进程设定的单次大小上限（父侧合成，不来自子进程）。
	ErrCodeResponseTooLarge = "response_too_large"
	// ErrCodeTimeout 单次 RPC 超时（父侧合成）。
	ErrCodeTimeout = "rpc_timeout"
	// ErrCodeHostGone 子进程已退出/崩溃，无法完成 RPC（父侧合成）。
	ErrCodeHostGone = "host_gone"
)

// Request 是一条 RPC 请求帧（JSON-lines 一行）。
type Request struct {
	// ID 父进程铸造的请求号（回带配对）。0 视为缺省（子进程应视为无效请求或事件）。
	ID int64 `json:"id"`
	// Method 方法名（Method* 常量之一）。
	Method string `json:"method"`
	// Params 方法参数（各方法自带形状；未携带时为空）。
	Params RawParams `json:"params,omitempty"`
}

// Response 是一条 RPC 响应帧（JSON-lines 一行）。Result 与 Error 二选一。
type Response struct {
	ID     int64     `json:"id"`
	Result RawResult `json:"result,omitempty"`
	Error  *RPCError `json:"error,omitempty"`
}

// RPCError 是 host 侧错误帧（code + 人话 + 分类建议）。
type RPCError struct {
	// Code 稳定错误码（ErrCode* 之一）。
	Code string `json:"code"`
	// Message 人话说明（面向排查；不含密钥）。
	Message string `json:"message"`
	// Class 子进程建议的失败分类（failure.Class 取值；父进程可据此直映射，缺省按 code 推断）。
	Class string `json:"class,omitempty"`
}

// ——— 各方法参数/结果形状（父子两侧共用，避免手抄漂移）———

// HandshakeParams：父进程告知子进程它期望的核心 API 版本与自身标识。
type HandshakeParams struct {
	// APIVersion 核心对外应用 API 版本（如 "1.0"；与 capability.AppAPI 同源，由父进程传入）。
	APIVersion string `json:"api_version"`
}

// HandshakeResult：子进程自述（包 id/版本、提供的 impl 与工具）。
type HandshakeResult struct {
	// PackageID 包 id（= 清单 id / app 定义 id）。
	PackageID string `json:"package_id"`
	// PackageVersion 包自身版本（自述）。
	PackageVersion string `json:"package_version"`
	// APIVersion 包面向的 api_version（caret，如 "^1"）。
	APIVersion string `json:"api_version"`
	// Impls 本进程提供的 impl_id 列表。
	Impls []string `json:"impls"`
	// Tools 本进程提供的工具名列表（扁平，带域前缀，如 "fooai_query"）。
	Tools []string `json:"tools"`
}

// ListImplsResult：本进程提供的 impl_id 列表。
type ListImplsResult struct {
	Impls []string `json:"impls"`
}

// ToolSpec 是一个工具的自述（name + 人话描述 + 参数 JSON Schema 原文）。
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// InputSchema 工具参数 JSON Schema（原样 JSON；父侧/模型侧读取，不解析）。
	InputSchema RawResult `json:"input_schema,omitempty"`
}

// ListToolsResult：本进程提供的工具清单。
type ListToolsResult struct {
	Tools []ToolSpec `json:"tools"`
}

// InvokeToolParams：调用一个工具。
type InvokeToolParams struct {
	Name string    `json:"name"`
	Args RawResult `json:"args,omitempty"`
}

// InvokeToolResult：工具返回（本期非流式：一次性 result + 可选 is_error 标记）。
type InvokeToolResult struct {
	// Content 工具结果原文（MCP 风格 content 数组或任意 JSON；由 app 决定，父侧透传）。
	Content RawResult `json:"content,omitempty"`
	// IsError 业务层错误标记（工具"正常返回了一个错误结果"与"RPC 层失败"是两回事）。
	IsError bool `json:"is_error,omitempty"`
}

// ApplySlotParams：apply 一个 slot（impl_id + 父侧快照的 ctx，子进程据此执行 apply）。
type ApplySlotParams struct {
	ImplID string `json:"impl_id"`
	// CtxSnapshot 父侧 Ctx 的可序列化快照（本期约定：provided service 名 → 说明字符串；
	// 真正的跨进程服务注入属 [增强项]，C3 只做「能 apply 成功」的骨架）。
	CtxSnapshot map[string]string `json:"ctx_snapshot,omitempty"`
}

// DisposeSlotParams：撤销一个 slot。
type DisposeSlotParams struct {
	ImplID string `json:"impl_id"`
}

// HealthResult：子进程健康自述。
type HealthResult struct {
	// OK 恒为真（能回这条即进程活着）。
	OK bool `json:"ok"`
	// UptimeMs 子进程自启动以来的毫秒数（诊断用）。
	UptimeMs int64 `json:"uptime_ms"`
}

// ShutdownResult：子进程将随即退出。
type ShutdownResult struct {
	OK bool `json:"ok"`
}

// ——— 原始 JSON 透传载体（不解码、原样搬运）———

// RawParams 是请求参数原文。提供按方法解码的便捷入口（各方法形状不同，延迟解码）。
type RawParams []byte

// UnmarshalJSON 让 RawParams 原样承接参数 JSON（不在这里做形状校验）。
func (r *RawParams) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}

// MarshalJSON 原样输出（空则输出 null）。
func (r RawParams) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

// RawResult 是结果/参数原文（同样原样透传，避免多层结构体重复定义）。
type RawResult []byte

// UnmarshalJSON 原样承接。
func (r *RawResult) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}

// MarshalJSON 原样输出（空则输出 null）。
func (r RawResult) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}
