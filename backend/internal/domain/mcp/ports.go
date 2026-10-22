package mcp

import (
	"context"
	"encoding/json"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/protocol"
)

// ——— MCP 域的窄端口（v3.2 刀 5）———
//
// 与技能域同一套做法：消费方声明自己用到的几个方法，装配根把机制注进来，
// 领域层不再 import `internal/drivers/mechanism/*`。两个域各自声明、**不共用**同一份接口：
// 它们用到的形状恰好相同，但这是各自的契约——将来一个域要加方法，不该牵动另一个。
//
// 端口只列本域真正用到的：`DefinitionRepo` 六个方法，`SecretResolver` 一个方法。
// 方法集越窄，换载体/换凭据来源时被牵连的地方越少。

// DefinitionRepo 一类定义（MCP server = `mcp` 子目录）的载体。
type DefinitionRepo interface {
	// Locator 人类可读的定位串（响应里的 `path`）。不参与任何判据。
	Locator(id string) string
	// SourceFile 注册表条目 `SourceFile` 的取值（Library 的 origin 依据）；DB 载体返回空串。
	SourceFile(id string) string
	// Exists 报告载体里是否已有该定义（与注册表一起构成"存在"的两个来源）。
	Exists(ctx context.Context, id string) (bool, error)
	// Open 读出定义并应用 patch（值为 nil = 删键），返回**未提交**的草稿与乐观锁依据。
	Open(ctx context.Context, id string, patch map[string]any) (doc *capability.DefinitionFile, rev int64, existed bool, err error)
	// Commit 提交草稿；created = 本次新建，conflict = CAS 没中（→ 40901，绝不 last-write-wins）。
	Commit(ctx context.Context, id string, doc *capability.DefinitionFile, rev int64, existed bool) (created, conflict bool, err error)
	// Remove 删除定义；existed = 真删掉了一个，conflict = CAS 没中。
	Remove(ctx context.Context, id string) (existed, conflict bool, err error)
}

// Definitions 定义载体工厂 + 目录事实。
type Definitions interface {
	// Dir 能力定义根目录；空串 = 本进程没装载能力数据面。
	Dir() string
	// RepoFor 取某一类定义的载体；返回错误 = 该模式下写面不可用。
	RepoFor(sub string) (DefinitionRepo, error)
}

// SecretResolver 把 `store:` 引用解析成明文（"引用指向哪一行"的知识在凭据机制里）。
//
// `*mechanism.Store` 的方法集是它的超集且签名相同，所以装配期**直接注入即可**，
// 不需要适配层。
type SecretResolver interface {
	ResolveStoreSecret(ctx context.Context, ref string) (string, error)
}

// LiveClient 是**实时访问 MCP server 的窄端口**（A2，2026-10-22）：探测、工具调用、资源、
// 提示词四条 live 路径共用它。
//
// 为什么要有这一层：这些方法原先直接调 `infra/protocol/mcp` 的包级函数——于是"域的每一条
// live 路径都为一份返回值形状去认识协议实现"，`infra` 也因此被迫跨带（L1 + L5）。端口化之后
// 域只认 `protocol` 平台值，实现体（连接、握手、三个传输、错误分类）由装配根注入。
//
// 输入形状用 `protocol.McpServer`（`Name` 即定义 id：客户端只拿它当身份/日志键）；
// 返回形状一律是 `protocol.Mcp*` 平台值——域不认识任何 MCP 线类型。
//
// **错误约定**：实现必须用本仓的稳定 code 词表（`mcp_transport_unsupported` /
// `mcp_tool_name_required` / `mcp_timeout` …）表达失败，域据 `CodeOf` 决定 REST 分类与信封文案；
// 词表的事实源仍是实现侧（`infra/protocol/mcp` 的 `Error`），域名只读不造。
type LiveClient interface {
	// ListTools 探测 tools/list（含 initialize 握手与翻页）。
	ListTools(ctx context.Context, s protocol.McpServer) (protocol.McpToolList, error)
	// CallTool 调用一个工具（args 为 arguments 对象；nil = 无参数）。
	CallTool(ctx context.Context, s protocol.McpServer, name string, args json.RawMessage) (protocol.McpCallResult, error)
	// ListResources 拉取 resources/list + resources/templates/list（模板不支持时降级为空）。
	ListResources(ctx context.Context, s protocol.McpServer) (protocol.McpResourceList, error)
	// ReadResource 读取一个资源（resources/read）。
	ReadResource(ctx context.Context, s protocol.McpServer, uri string) (protocol.McpReadResourceResult, error)
	// ListPrompts 拉取 prompts/list（翻页）。
	ListPrompts(ctx context.Context, s protocol.McpServer) ([]protocol.McpPrompt, error)
	// GetPrompt 取用一条提示词模板（prompts/get）。
	GetPrompt(ctx context.Context, s protocol.McpServer, name string, args map[string]string) (protocol.McpGetPromptResult, error)
	// CodeOf 取错误的稳定 code（非本实现产生的错误 → ""）。
	CodeOf(err error) string
}
