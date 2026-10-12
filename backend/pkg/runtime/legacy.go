package runtime

import (
	"strings"

	"github.com/morehao/ark-mate/pkg/runtime/dsh"
)

// ——— runtime 标识的历史别名（**只管持久化数据，不管运行时**） ———
//
// 2026-10-11 之前，注册表键是 `deepseekharness`（展示名 `deepseek-harness`）；此后统一为
// `dsh`——它本来就是同一个东西在其余六个面上的名字：二进制 `dsh`、隔离根 `<MATE_HOME>/runtime/dsh`、
// 随包载荷目录 `payloads/dsh/`、桌面启动器分支 `runtimeId === 'dsh'`、环境变量前缀 `DSH_`、
// 种子 agent id `dsh`。也就是说那次归一**不是**改名，是**消掉一个孤儿**。
//
// 别名只会出现在**老数据**里，新写入的任何地方都不会再有它：
//
//	sessions.runtime_id（sqlite 列）           → RestoreFromStore 读回时归一
//	transcript header 的 runtime 字段（append-only）  → 读时归一（历史文件按历史对待，不重写）
//	skill 定义里的 runtimes[]（文件 / DB 行）          → 装载与写面归一（否则技能**不再投放**）
//	config 的 projection_dirs 键（`<runtime>:<kind>`） → 解析时归一（否则自定义投影根**静默失效**）
//	~/.agents/skills/*/.mate-managed 的归属集合        → 物化重校时用新 id 认领并摘除旧 id
//	                                                  （不摘除的后果是**禁用/删除技能时副本删不掉**，
//	                                                   底座照样读到已禁用的技能——静默失效）
//
// **为什么是"读时归一 + 下次写回自愈"，而不是启动时横扫一遍**：
//   - transcript 是 append-only 的历史记录，重写它等于篡改历史；
//   - 其余四处都是"读得到就够"的配置类数据，归一到内存 + 在**已有的**写路径上顺手落成新值即可；
//   - 启动时扫描并改写用户家目录（含与底座**共享**的 `~/.agents/skills`）比别名本身危险得多，
//     而且要处理"写一半崩了"的中间态。
//
// 删除条件：当 `mate doctor` 能报告"本机没有任何老数据引用它"之后，本常量与 NormalizeID 一并删。
const LegacyDeepseekharnessID = "deepseekharness"

// NormalizeID 把**持久化数据里读出来的** runtime 标识归一到当前取值。
//
// 三条边界：
//   - 空串原样返回——"未指定"不是一个别名（调用方会把它当"没有值"处理）；
//   - 只认登记在案的历史别名（当前一条），**不做模糊匹配**：把 `deepseek-harness`（展示名）
//     也当成 id 来映射，会让"用户自己声明了一个叫这名字的远程 runtime"被静默改写；
//   - 未知 id 原样返回——远程 runtime 的 id 由用户在 `runtimes.json` 里声明，平台不该改写它。
func NormalizeID(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == LegacyDeepseekharnessID {
		return dsh.ID
	}
	return trimmed
}

// NormalizeIDs 逐个归一（nil 原样返回 nil，保持"未声明"与"空数组"在 wire 上的可区分性）。
func NormalizeIDs(ids []string) []string {
	if ids == nil {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = NormalizeID(id)
	}
	return out
}

// LegacyAliasesOf 返回某个 runtime id 的历史别名（当前只有 dsh 有一条）。
//
// 用途是**认领旧产物**：物化层要能在归属标记里认出"这条是我的旧名字"，写入时替换成新名字，
// 删除联动时把旧名字一并摘掉。返回空切片 = 没有历史别名（opencode 与远程 runtime 都是这样）。
func LegacyAliasesOf(id string) []string {
	if id == dsh.ID {
		return []string{LegacyDeepseekharnessID}
	}
	return nil
}
