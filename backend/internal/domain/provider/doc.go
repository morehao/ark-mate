// Package provider 是**模型供应商**这个域：平台侧的供应商档案（`providers` 表 / `providers.json`）
// 与它们声明的模型清单（`models` 表）。
//
// 它**负责**（今天已落地的部分）：存量档案文件的一次性导入与导出、v11 文件形状的历史映射
// （`ProviderFile` / `V11FileBaseURL`）、id 词法闸门（`ValidateProviderID`），以及
// "运行时拒绝了这个取值"的人话说明（`ModelApplyReason`）。
//
// 它**不负责**：密钥怎么存与怎么取（那是 `mechanism/credential`——本域只持 `key_ref`）、
// 定义载体的读写纪律（`mechanism/definition`）、会话与供应商的交汇（"这次物化对谁生效"读的是
// 会话，留在编排侧）、对外 DTO 与错误码出口（`service/svc*` 与 `dto/*`）。
//
// 为什么先搬这一块：它是供应商组里**唯一零 `s.*` 依赖**的部分（纯函数 + `*store.Store` + 目录），
// 所以不必先做并发审查就能搬——而它顺带把四个跨面小符号里的两个（备份文件名、对账标记）
// 带到了它们的自然归宿。
package provider
