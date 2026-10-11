// Package dsh 声明 deepseek-harness（dsh）的 ACP server 启动形态与
// 能力基线（纯数据，不依赖父包）。
//
// 形态说明（基于 deepseek-harness 源码 2026-08 快照核实，见
// docs/dsh-acp-adaptation.md）：当前版本不再提供独立 `dsh-acp-demo`
// bin，ACP server 是统一启动器 `dsh` 的一个 profile：`dsh --profile acp`。
package dsh

// ID 是注册表键。
const ID = "dsh"

// Name 是展示名。
const Name = "dsh"

// Version 是展示用启动形态。
const Version = "dsh --profile acp"

// Command：统一启动器 `dsh`（@deepseek-ai/dsh，npm 分发，bin=dsh）。
// 未安装时可经 `npx @deepseek-ai/dsh --profile acp` 启动；profile 首用自动初始化。
const Command = "dsh"

// Args：--profile acp 选择 ACP profile；其余参数（--patch 覆盖等）留待上层按需追加。
var Args = []string{"--profile", "acp"}

// Caps 是静态能力基线声明（真实值以 ACP initialize 探测为准）：
// initialize 广告 sessionCapabilities { close, list, resume }（无 load/fork）、
// authMethods 为空、audio/embeddedContext 为 false、image 视持久附件仓与精确路由而定。
type DeclaredCapabilities struct {
	SessionsPersistent     bool
	MultiSessionConcurrent bool
	Images                 bool
	EmbeddedContext        bool
}

// Caps 依据 packages/acp/acp（dsh-acp bridge）源码声明的基线。
//
// **`Images: false` 是"按源码声明的保守基线"，不是 dsh 的能力事实，更不得当作门禁**
// （ADR-14）：真值取决于两件这里看不到的东西——① 附件仓是否可用；② **当前路由**声明的
// `models[].input` 是否含 image（桥在 initialize 里如实广告 `promptCapabilities.image`，
// 未广告就送图会被拒）。拿这个静态值当判据，用户会在一个其实能收图的 dsh 上被告知"不支持"。
//
// 消费者：**零**。唯一的旧出口 `Registry.ListItems`（`/runtimes`）已随该端点一起删除，
// 而"能不能收图"一律走连接期事实（`pkg/acl` 解析 initialize 得到的 `Capabilities.Images`）。
// 由 archtest 的 `TestStaticCapsImagesHasNoConsumer` 钉住"没有新的读法"。
var Caps = DeclaredCapabilities{
	SessionsPersistent:     true, // list/resume/close 跨进程持久（同一 $DSH_HOME profile 持久化根）
	MultiSessionConcurrent: true, // 同连接可并行多会话
	Images:                 false,
	EmbeddedContext:        false,
}

// SkillFormat 该 runtime 消费的 skill 内容格式（投影 Projection.Format 的取值；语义同 opencode.SkillFormat）。
const SkillFormat = "SKILL.md+frontmatter"

// Notes 约束说明（给 README/文档位，暂不外露 REST）。
//  1. 无鉴权：authenticate 立即成功（authMethods 为空）。
//  2. 会话持久化：session/new 即持久；disconnect/close 走同一 quiescent teardown，
//     结束后仍可 list/resume（「断连即失效」不成立；断连时进行中的 turn 被取消）。
//  3. 不支持：session/load、fork/delete、additional directories、终端、客户端 FS、SSE/传输级 MCP。
//  4. 需绝对路径 cwd 且先校验；provider/model 在 profile 行配置（默认 deepseek-official/deepseek-v4-flash），
//     DEEPSEEK_API_KEY 由 provider 从其进程环境读取（无需额外注入，可随子进程环境透传）。
//  5. 接口迭代快，可能 breaking（README 明示开发者预览期）。
//  6. 版本要求：须含 acp profile 模板的 dsh（实测 npm latest=0.1.1-rc.2 缺 acp profile，
//     报 "profile \"acp\" does not exist"；用 `@deepseek-ai/dsh@next` 0.1.2-rc.1 或更新，
//     或随 deepseek-harness 仓库源码构建）。
const Notes = "ACP server 由统一 dsh 启动器提供（dsh --profile acp）；无鉴权；会话可持久化（list/resume/close 跨进程）；不支持 load/fork/删除/附加目录/终端/客户端 FS；DEEPSEEK_API_KEY 走子进程环境透传；需含 acp profile 模板的 dsh（npm latest 0.1.1-rc.2 缺失，用 @next 0.1.2-rc.1+）；迭代快可能 breaking"
