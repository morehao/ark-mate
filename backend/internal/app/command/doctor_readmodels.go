package command

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/config"
)

// 本文件是 `mate doctor` 的 **read-models（读模型）** 那一段（v21–v23）。
//
// 分工与 provider 段逐条同形：**事实由 `manager.CollectReadModelDoctorFacts` 构造**（SQL 与
// 不变量都在那一侧），本文件只做两件事——打开只读句柄、把事实翻成三档结论与人话。
//
// 分档口径（`doctor.go` 的既有三档，逐条对应，避免下一个人凭感觉调档）：
//
//	memory 模式 / 没有库文件   → warn（**无法校验**：读模型不落库，或还没跑过 serve/migrate）
//	库文件在、打不开 / 采集失败 → fail（事实坏了）
//	五条漂移判据全 0           → ok
//	任意一条非 0               → fail（**事实与声明不符**：投影说得出"第几条"，而库里对不上）
//
// `meta.read_models_rebuild` 非空只写进人话（"下一次启动会重建"），**不参与分档**：正要重建时
// 序号当然可能还没铺好，把它判红只会让"升级后第一次 doctor"永远红——而那条命令恰恰是升级后
// 第一个要跑的东西。
func checkReadModels(opts doctorOptions) []doctorCheck {
	const name = "read-models"

	// memory 模式不是一个"读不到库"的故障，而是**配置选择**：读模型只活在进程内。
	if opts.StoreMode == config.StoreModeMemory {
		return []doctorCheck{{name, doctorWarn,
			"memory 模式（MATE_STORE=memory，或 MATE_STORE 未设而库文件不存在/为空 → auto 解析成 memory）：" +
				"events/messages 只在进程内，读模型漂移不适用"}}
	}
	if strings.TrimSpace(opts.DBPath) == "" {
		return []doctorCheck{{name, doctorWarn, "未配置库文件路径（MATE_DB）：读模型漂移无从校验"}}
	}
	// 先自己判"文件在不在"：`store.OpenReadOnly` 的错误文案不该由这里按字符串猜（同 provider 段）。
	if _, err := os.Stat(opts.DBPath); err != nil {
		if os.IsNotExist(err) {
			return []doctorCheck{{name, doctorWarn,
				fmt.Sprintf("库文件 %s 不存在：还没跑过 serve/migrate，读模型漂移无从校验", opts.DBPath)}}
		}
		return []doctorCheck{{name, doctorFail,
			fmt.Sprintf("库文件 %s 不可读：%v", opts.DBPath, err)}}
	}
	st, err := store.OpenReadOnly(opts.DBPath)
	if err != nil {
		return []doctorCheck{{name, doctorFail, "无法校验读模型：" + err.Error()}}
	}
	defer func() { _ = st.Close() }()

	facts := manager.CollectReadModelDoctorFacts(context.Background(), st)
	if facts.Note != "" {
		return []doctorCheck{{name, doctorFail, "无法校验读模型：" + facts.Note}}
	}
	return []doctorCheck{readModelCheck(name, facts)}
}

// readModelCheck 把事实翻成人话与三档结论。
//
// ok 时也把**规模**报出来（messages/runs 行数）：一条只说"没问题"的检查在排障时没有信息量，
// 而"这个库有多少行"是下一个问题必问的。
func readModelCheck(name string, f manager.ReadModelDoctorFacts) doctorCheck {
	pending := ""
	if f.PendingRebuild != "" {
		pending = fmt.Sprintf("；meta.read_models_rebuild=%s（下一次启动会整会话重建，重建前序号可能还没铺好）",
			f.PendingRebuild)
	}
	base := fmt.Sprintf("messages=%d runs=%d 孤儿=%d 空正文=%d 序号不连续会话=%d 位次不连续会话=%d 越界来源=%s%s",
		f.Messages, f.Runs, f.OrphanMessages, f.EmptyBodies, f.BadOrdinalSessions,
		f.BadEventSeqSessions, bracketList(f.ForeignSources), pending)
	if f.Drift == 0 {
		return doctorCheck{name, doctorOK, base}
	}
	return doctorCheck{name, doctorFail, base + "——读模型与 events 不一致（判据 J7：可从 events 重建）"}
}
