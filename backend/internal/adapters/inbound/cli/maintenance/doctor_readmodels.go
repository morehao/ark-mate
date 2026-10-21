package maintenance

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/platform/object/objmeta"
	"github.com/morehao/ark-mate/internal/domain/session"
	doctorfacts "github.com/morehao/ark-mate/internal/drivers/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
)

// 本文件是 `mate doctor` 的 **read-models（读模型）** 那一段（v21–v23）。
//
// 分工与 provider 段逐条同形：**事实由 `doctorfacts.CollectReadModelDoctorFacts` 构造**（SQL 与
// 不变量都在那一侧），本文件只做两件事——打开只读句柄、把事实翻成三档结论与人话。
//
// 分档口径（`doctor.go` 的既有三档，逐条对应，避免下一个人凭感觉调档）：
//
//	memory 模式 / 没有库文件 / 打不开 / 采集失败 → warn（**无法校验**：读模型不落库，或还没跑过 serve/migrate）
//	五条漂移判据全 0           → ok
//	任意一条非 0               → fail（**事实与声明不符**：投影说得出"第几条"，而库里对不上）
//
// "打不开 / 采集失败" 归 warn **不是**降档（2026-10-14 实测改）：本条判据的退出码只该由
// "事实与声明不符"驱动，而"库还是 v20、还没有 ordinal 列"恰恰是**刚换上新二进制、还没起 serve**
// 的正常状态（`mate doctor` 本来就是升级后第一个要跑的命令）。此前它落进 fail，于是升级后第一次
// 体检无条件红、人话却写着"无法校验"——判档与文案自相矛盾，而红的原因与读模型毫无关系。
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
		return []doctorCheck{{name, doctorWarn,
			fmt.Sprintf("无法校验读模型：库文件 %s 读不到（%v）", opts.DBPath, err)}}
	}
	st, err := store.OpenReadOnly(opts.DBPath)
	if err != nil {
		return []doctorCheck{{name, doctorWarn, "无法校验读模型：" + err.Error()}}
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	facts := doctorfacts.CollectReadModelDoctorFacts(ctx, st, session.MessageRowSourceTypes())
	if facts.Note != "" {
		return []doctorCheck{{name, doctorWarn, readModelUnverifiableDetail(ctx, st, facts.Note)}}
	}
	return []doctorCheck{readModelCheck(name, facts)}
}

// checkReadModelRebuild 是 `mate doctor --rebuild-read-models` 的执行面（**显式 opt-in**）。
//
// 它为什么可以存在（"doctor 是只读体检"这条口径没有破）：默认路径一个字节都不写，写只在用户
// 显式给了那个开关时发生；而且它**不是**体检判据的一部分——它只是体检给出漂移结论之后的
// **出路**（报告里 `read-models` 判红时，人需要一条"现在修"的命令，而不是"去起一次 serve"）。
// 反过来，把重建塞进默认路径会让"体检"变成"每次跑都改库"，那是另一件事了（守卫 G7 钉住
// "只能在开关后面"）。
//
// 为什么用 `store.Open`（会跑迁移）而不是只开读写句柄：重建读模型的前提是"schema 已经是这一版"，
// 而用户跑这条命令的典型时机正是"刚升级完、还没起 serve"。
func checkReadModelRebuild(opts doctorOptions) []doctorCheck {
	const name = "read-models-rebuild"

	// 保险栓：调用点也在开关后面（见 `runDoctor`），这里再判一次是为了"被误接线到默认路径"时
	// **拒绝写**而不是写——体检变成"每次跑都改库"是另一件事。
	if !opts.RebuildReadModels {
		return []doctorCheck{{name, doctorWarn, "未显式要求重建（--rebuild-read-models）：跳过（体检默认只读）"}}
	}
	if opts.StoreMode == config.StoreModeMemory {
		return []doctorCheck{{name, doctorWarn, "memory 模式：读模型只在进程内，没有可重建的落点"}}
	}
	if strings.TrimSpace(opts.DBPath) == "" {
		return []doctorCheck{{name, doctorWarn, "未配置库文件路径（MATE_DB）：没有可重建的落点"}}
	}
	st, err := store.Open(opts.DBPath)
	if err != nil {
		return []doctorCheck{{name, doctorFail, "打开库（读写）失败：" + err.Error()}}
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	// 先记下标记：重建成功后要把它清掉（否则下一次启动会**再**全量重建一遍——结果一样，
	// 但白花一次启动时间，而"标记"这件事本身就变成噪声）。
	pending, hadPending, _ := st.GetMeta(ctx, objmeta.KeyReadModelsRebuild)
	// 循环在机制层；"重建一个会话"的实现（与启动钩子共用同一份）在会话域——组合根直接把
	// 会话平面交下去（`domain/session` 是下层，命令层 import 它没有任何越界）。
	pl := session.NewPlane()
	if st != nil {
		pl.SetStore(st)
	}
	total, failed, err := pl.RebuildAllReadModels(ctx)
	if err != nil {
		return []doctorCheck{{name, doctorFail, "重建读模型失败：" + err.Error()}}
	}
	if failed > 0 {
		// 标记刻意**留着**（下次启动重试）：与启动钩子同一条处置。
		return []doctorCheck{{name, doctorFail,
			fmt.Sprintf("重建了 %d 个会话，其中 %d 个失败（重建标记保留，下次启动会重试；"+
				"失败原因见日志）", total, failed)}}
	}
	tail := "；没有待处理的重建标记"
	if hadPending && pending != "" {
		if derr := st.DeleteMeta(ctx, objmeta.KeyReadModelsRebuild); derr != nil {
			return []doctorCheck{{name, doctorWarn,
				fmt.Sprintf("重建了 %d 个会话，但清重建标记失败：%v（重建本身已完成）", total, derr)}}
		}
		tail = fmt.Sprintf("；已清重建标记（原值 %s）", pending)
	}
	return []doctorCheck{{name, doctorOK, fmt.Sprintf("重建了 %d 个会话的读模型%s", total, tail)}}
}

// readModelMinSchemaVersion 读模型判据要求的最低 schema 版本（v23 才有 `messages.ordinal`/`kind`）。
//
// 刻意写成"这条判据的要求"而不是"当前最新版"：未来 v24 再加别的列时，这段判据的最低要求
// 仍然是 v23——跟着最新版一起涨只会让老库被判成"无法校验"。
const readModelMinSchemaVersion = 23

// readModelUnverifiableDetail 把"采集不到"翻成人话：能看出库比判据旧时**直接给那条出路**，
// 而不是把 `no such column: ordinal` 这种底层文案丢给用户。
func readModelUnverifiableDetail(ctx context.Context, st *store.Store, note string) string {
	ver, _ := st.SchemaVersion(ctx)
	if ver > 0 && ver < readModelMinSchemaVersion {
		return fmt.Sprintf("库结构还是 v%d（读模型判据需要 v%d）：跑一次 `mate serve`，"+
			"或 `mate doctor --rebuild-read-models` 一步把迁移与重建都跑掉；原始错误：%s",
			ver, readModelMinSchemaVersion, note)
	}
	return "无法校验读模型（采集不到事实；库结构被改动过？）：" + note
}

// readModelCheck 把事实翻成人话与三档结论。
//
// ok 时也把**规模**报出来（messages/runs 行数）：一条只说"没问题"的检查在排障时没有信息量，
// 而"这个库有多少行"是下一个问题必问的。
func readModelCheck(name string, f doctorfacts.ReadModelDoctorFacts) doctorCheck {
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
