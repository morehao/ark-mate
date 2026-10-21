package maintenance

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/config"
)

// 本文件是 `mate doctor` 的 **审计链** 那一段（T4 / v32）。
//
// 为什么它必须出现在这里（而不是只作为一个库函数存在）：哈希链的价值全在"**有人会去查**"——
// 一条永远没人调用的校验函数等于没有校验。体检命令是这条链**唯一**的常规消费者：升级后、怀疑
// 有人动过库时，`mate doctor` 应当直接把"链断在第几条"报出来，而不是让人去写一段 Go。
//
// 分档口径（与 read-models 段逐条同形，别凭感觉调档）：
//
//	memory 模式 / 没有库文件 / 打不开 / schema 早于 v32 / 采集失败 → warn（**无法校验**）
//	库里有 v32 之后的审计行且链完好                          → ok
//	发现断点                                                → fail（**事实与声明不符**：每行都该挂在前一行的哈希上）
//	只有 v32 之前的行（checked=0）                            → warn（**还没有可校验的链**，不是"链完好"）
//
// "schema 早于 v32" 归 warn 的理由与 read-models 段那次实测改稿一致：刚换上新二进制、还没起
// serve 时库本来就在上一版，这时判红会让"升级后第一条要跑的命令"无条件红，而红的原因与审计链
// 毫无关系。
func checkDefinitionRevisionChain(opts doctorOptions) []doctorCheck {
	const name = "audit-chain"

	if opts.StoreMode == config.StoreModeMemory {
		return []doctorCheck{{name, doctorWarn,
			"memory 模式：定义变更审计不落库，没有可校验的链"}}
	}
	if strings.TrimSpace(opts.DBPath) == "" {
		return []doctorCheck{{name, doctorWarn, "未配置库文件路径（MATE_DB）：审计链无从校验"}}
	}
	// 先自己判"文件在不在"：`store.OpenReadOnly` 的错误文案不该由这里按字符串猜（同 provider 段）。
	if _, err := os.Stat(opts.DBPath); err != nil {
		if os.IsNotExist(err) {
			return []doctorCheck{{name, doctorWarn,
				fmt.Sprintf("库文件 %s 不存在：还没跑过 serve/migrate，审计链无从校验", opts.DBPath)}}
		}
		return []doctorCheck{{name, doctorWarn,
			fmt.Sprintf("无法校验审计链：库文件 %s 读不到（%v）", opts.DBPath, err)}}
	}
	st, err := store.OpenReadOnly(opts.DBPath)
	if err != nil {
		return []doctorCheck{{name, doctorWarn, "无法校验审计链：" + err.Error()}}
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	// 先看 schema 版本：v32 之前的库连这两列都没有，那时"校验"等于把没做过的事说成做过。
	// v32 的版本号取自 store 的导出常量（迁移内核是唯一事实源，这里不写字面量）。
	if v, vErr := st.SchemaVersion(ctx); vErr == nil && v < store.DefinitionRevisionChainSchemaVersion {
		return []doctorCheck{{name, doctorWarn,
			fmt.Sprintf("库 schema = v%d（哈希链从 v%d 起）：还没跑过 serve/migrate，暂无可校验的链",
				v, store.DefinitionRevisionChainSchemaVersion)}}
	}

	checked, brk, err := st.VerifyDefinitionRevisionChain(ctx)
	if err != nil {
		return []doctorCheck{{name, doctorWarn, "无法校验审计链：" + err.Error()}}
	}
	if brk != nil {
		return []doctorCheck{{name, doctorFail, fmt.Sprintf(
			"审计链断在 seq=%d（kind=%s id=%s）：断法=%s 期望=%s 实际=%s"+
				"——每行都该挂在前一行的哈希上，这条对不上说明库被改过（或行被删过）",
			brk.Seq, brk.Kind, brk.DefinitionID, brk.Reason, orDash(brk.Want), orDash(brk.Got))}}
	}
	if checked == 0 {
		return []doctorCheck{{name, doctorWarn,
			"库里还没有 v32 之后的定义变更审计行：历史行不参与链校验，暂时无链可验"}}
	}
	return []doctorCheck{{name, doctorOK, fmt.Sprintf("链完好：%d 行逐行相接，内容与哈希一致", checked)}}
}
