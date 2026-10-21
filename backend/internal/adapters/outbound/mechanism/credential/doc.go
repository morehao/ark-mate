// Package credential 是**凭据**这个机制：托管文档的读写与镜像、引用 → 值的分层取值
// （继承环境 > 托管文档 > `<cwd>/.env` > `<home>/.env`）、以及 provider 密钥在托管镜像里的
// 键命名。
//
// 它**负责**：凭据文档（`<MATE_HOME>/credentials.json`，0600）的读取与对账、平台侧的取值、
// 以及"托管镜像里的键长什么样"这条**格式**。
//
// 它**不负责**：分层的**顺序**（那是 `internal/adapters/outbound/credentials` 的 `Layered` —— 唯一实现，本包只消费）、
// provider 域自己的档案形状（那是 `domain/provider`）、以及谁在什么时候该取值（那是编排层）。
//
// 为什么键命名在这里而不是 provider 域：`key_ref` 是**写进凭据文档**的键（也回写到
// `providers.json`），它的格式属于凭据；放在 provider 域会让"改镜像键"看起来像改 provider。
// 依赖方向因此是 `domain/provider → mechanism/credential`（向下 ✓），不是反过来。
package credential
