// Package transfer 把**存量定义**在文件载体与持久层之间搬运：导入（文件 → 库）与
// 导出（库 → 文件，回退窗口的唯一回灌路径）。三个域（capability / automation /
// provider）共用同一套纪律：指纹幂等、绝不覆盖、备份一次、只搬不删不回写、
// 对账不一致只告警不阻断。
//
// 边界（改这个包前先读）：
//   - 只允许「某个域的 Import* / Export* 对」与域无关的导入/导出原语；
//   - 单条定义的读写（draft / CAS / mtime 锁）在 `internal/drivers/mechanism`；
//   - 能力/定义的值层在 `internal/core/capability` 与 `internal/core/definition`；
//   - 域声明的窄端口（DefinitionFiles / Secrets）的适配体在 `internal/drivers/registry`（装配点）。
//
// 名字刻意不用「某个域 + io」的形状：那会让人分不清它和那个域的包到底该 import 哪一个。
// 本包与任何一个域都不是从属关系：它面对的是「旧载体 → 新载体」这件事本身。
package transfer
