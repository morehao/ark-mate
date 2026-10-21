// Package mechanism 是驱动侧的**机制层**：五类定义与凭据的载体读写、CAS、分层取值，
// 以及底座自检。它被 ≥3 个域共享，且**不依赖任何域**（`mechanism/*` 不得向上 import）。
//
// 本包装三件事（2026-10-21 起两个原先是独立子包的机制并到这里）：
//
//   - **定义机制**（`definition_*.go`）：五类定义（skill / mcp / automation / provider / model）
//     共用的双载体读写（库行 ↔ 文件）、版本 CAS（改之前先对 rev）、写冲突的显式报错；
//     导入/导出的框架与逐类适配器的公共部分也在这里。它**不负责**某一种定义该怎么生效
//     （那是各领域的事：skill 的投影与启停、mcp 的注入条目、automation 的调度、provider 的密钥），
//     也不负责对外 DTO 与错误码（`service/svc*` 与 `dto/*`）。
//   - **凭据机制**（`credential_*.go`）：托管文档的读写与镜像、引用 → 值的分层取值
//     （继承环境 > 托管文档 > `<cwd>/.env` > `<home>/.env`）、以及 provider 密钥在托管镜像里的
//     键命名。它**不负责**分层的**顺序**（那是 `infra/credentials` 的 `Layered`，唯一实现，
//     本包只消费）、provider 域自己的档案形状（那是 `domain/provider`）、以及谁在什么时候
//     该取值（那是编排层）。
//     为什么键命名在这里而不是 provider 域：`key_ref` 是**写进凭据文档**的键（也回写到
//     `providers.json`），它的格式属于凭据；放在 provider 域会让"改镜像键"看起来像改 provider。
//   - **自检**（`doctor/` 子包）：`mate doctor` 的检查项实现。它保持独立子包，因为它是
//     命令侧的实现而不是被域共享的机制。
//
// 与 `internal/core/credential` 的分工：`core/credential` 是**值对象**（token、scope、
// 集合的构造与校验，无 I/O）；本包是**实现**（读文档、取值、写镜像）。两者不属于同一层，
// 也不应合并。
package mechanism
