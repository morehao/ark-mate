// Package registry 拥有本次进程里跨域的**领域平面集合**（`Planes`）：能力 / 技能 / MCP /
// 供应商 / 工作空间 / 自动化 + 凭据与定义机制，以及资源同步面与身份交付面。
//
// `Planes` 是它唯一的构造点。核心进程、网关子进程与 `mate doctor` 都从这里拿**同一套**事实，
// 而不是各自 `manager.NewService` 一遍：分叉的表现是"核心能注入的服务器，网关注入不了"
// （用户只看到工具少了一半，没有任何报错）。
//
// 启停序列（`Sequence` / `Step` / `Phase`）**已搬进 `internal/bootstrap/assembly`**（C3 4.5）：
// 它只被装配根使用，留在本包会让"装配动作"与"装配事实"继续混在一起。
//
// 本包同时是**窄端口适配体**的落点：`workspaceStore` / `skillDefs` / `mcpDefs` /
// `AutomationDefinitionFiles` / `providerSecrets`。它们只做「把机制的宽面收窄成域声明的端口」，
// 而这件事只有同时看得见两侧的装配点写得了（机制层禁止 import 域）。
//
// 依赖方向单一：registry → domain/mechanism/store/pkg，绝不反向。
package registry
