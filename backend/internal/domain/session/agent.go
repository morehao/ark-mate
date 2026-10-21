package session

import (
	"context"

	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 执行面的**按用例端口**（C2 收尾：连接与生命周期）———
//
// 会话域需要"建/恢复一条原生会话、读这条连接的能力与 stderr、列出这条连接上的已有会话"。
// 这些此前直接是 `*acp.Client` / `acp.SessionOptions` / `*acp.Session`——于是领域为了调一次
// `NewSession` 就认识协议库，换一个非 ACP 底座要先改领域。
//
// 现在的语言是**平台值**（`SessionOptions` / `runtime.McpServer` / `runtime.CapabilityFacts`）
// 与两个小接口；协议形状只在适配器（`adapters/outbound/runtimeconn`）内部出现，转换只在那里做一次。
//
// 与 `Conn` 的分工：`Conn` 是"一条连接的事实"（键、指纹、身份），`AgentConn` 是"它上面能做
// 什么"。分开是因为拆连与取连接只认键，不需要任何协议能力。

// SessionOptions 建/恢复一条原生会话所需的平台事实。
type SessionOptions struct {
	// Cwd 会话的工作目录（底座据此界定沙箱边界）。
	Cwd string
	// AdditionalDirectories 额外可访问目录（空 = 只有 cwd）。
	AdditionalDirectories []string
	// McpServers 本轮注入的 MCP server（已解析密钥；空 = 不注入）。
	McpServers []runtime.McpServer
}

// AgentSession 一条原生会话的平台视图。
//
// 它把**执行**（Prompt/Cancel）、**配置**（ConfigOptions/SetConfigOption）与**身份**
// （ID）收在一处：这三样永远同属一条会话，拆成三个接口只会让调用方同时持有三份。
type AgentSession interface {
	// ACP 执行面（轮次投递与取消；取消是 best-effort）。
	ACP
	// ID 底座侧原生会话 id（原文，未加 `rt_` 壳）。
	ID() string
	// Capabilities 这条会话所属连接握手到的能力事实（与 AgentConn 同一份事实，
	// 但投影只拿得到会话，所以这里也开一个读口）。
	Capabilities() runtime.CapabilityFacts
	// Close 关闭这条原生会话（收尾动作；失败由调用方决定是否吞）。
	Close(ctx context.Context) error
	// SetConfigOption 改写一个会话配置项，返回应用后的**全量**配置项状态。
	SetConfigOption(ctx context.Context, optionID, value string) ([]runtime.ConfigOption, error)
}

// AgentConn 一条连接上**按用例**的能力面。
//
// 只列调用方真正会做的事：读能力事实、建/载/恢复会话、取 stderr 诊断尾巴、列已有会话。
// 没有"发任意 RPC"这种口子——端口一旦能表达任意方法，就不再是端口。
type AgentConn interface {
	// Capabilities 这条连接握手到的能力事实。
	Capabilities() runtime.CapabilityFacts
	// NewSession 建一条新会话。
	NewSession(ctx context.Context, opt SessionOptions) (AgentSession, error)
	// LoadSession 按原生 id 重新载入一条会话（`session/load`）。
	LoadSession(ctx context.Context, nativeID string, opt SessionOptions) (AgentSession, error)
	// ResumeSession 按原生 id 恢复一条会话（`session/resume`）。
	ResumeSession(ctx context.Context, nativeID string, opt SessionOptions) (AgentSession, error)
	// ListSessions 列出这条连接上底座的已有会话（空 cwd = 全部）。
	ListSessions(ctx context.Context, cwd string) ([]runtime.SessionRef, error)
	// StderrTail 子进程 stderr 的最近几行（诊断尾巴；空 = 未采集/无输出）。
	StderrTail() []string
}
