// Package skill 是**技能平面**：prompt 侧的投放声明与启停投影。
//
// 它**负责**：技能定义读面的投影（`ListSkills`）、投放声明（`runtimes[]`）的归一与校验、
// 启停开关的写回、以及把技能投影到各 runtime 的目标上。
//
// 它**不负责**：定义的双载体读写与版本 CAS（那是 `mechanism/definition`）、
// 四层凭据取值（技能没有密钥面——那是 mcp 平面的事）、以及能力读面的联合聚合
// （skill 与 mcp 的联合视图是"能力读写面"的事，两个平面都只是它的使用者）。
//
// 依赖方向：`domain/skill → mechanism/definition`（向下）；**不得**反向 import `manager`。
package skill
