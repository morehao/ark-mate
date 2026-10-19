package doctor

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/morehao/ark-mate/internal/store"
)

// ——— `mate doctor` 的 **读模型**体检与重建（v21–v23） ———
//
// 分工：本文件是**事实的构造函数**（只读）与**唯一的重建循环**，`internal/cliops/
// doctor_readmodels.go` 只负责排版与三档结论。
//
// 本文件尾部另有**一个写路径**（`RebuildAllReadModels`）——它不是体检的一部分，而是体检给出的
// 那条出路（`mate doctor --rebuild-read-models`，显式 opt-in）。放在同一处是因为两者读的是
// 同一套事实："报告说什么坏了"与"怎么修"必须能对照着看。
//
// 为什么读模型值得一段独立的体检：`messages`/`runs` 是 `events` 的**投影**（J7），投影错了
// 不会让任何东西报错——检索少命中一条、界面上少一条消息/"第几条"错位，全都发生在用户侧，
// 而库里看不出异常。所以判据必须是**可判定的不变量**，而不是"看起来对不对"：
//
//	① 引用完整：`messages.run_id` 非空时必须指向一条存在的 `runs` 行（孤儿 = 那一轮的明细
//	   永远少一块，而读面照常返回 200）；
//	② 无空正文行：白名单投影的另一半（`text=''` 且没有工具细节的行是投影产物，不是事实）；
//	③ 会话内序号：`messages.ordinal` 必须是 1..N（有洞 = 少了一行；不从头起 = 与"第几条"
//	   这个说法对不上）；
//	④ 事件位次：`events.seq` 必须是会话内 0..N-1（v21 的口径；有洞说明号序计数器与实际
//	   落库分叉过——那是所有分页与窗口的基准）；
//	⑤ 来源白名单：`messages.events_json` 里出现的事件类型只能是投影白名单那两类——挡住
//	   "新事件类型偷偷进 messages"（在投影里加一个 `case` 很容易，而它会静默改变读模型
//	   的含义：检索会突然命中思考文本）。
//
// **只读**：本文件只 SELECT（库由调用方以 `mode=ro` 打开），不重建、不修数据——医生报出来，
// 处置（重建）由启动钩子或 `mate doctor --rebuild-read-models` 那条写路径负责。
type ReadModelDoctorFacts struct {
	// PendingRebuild `meta.read_models_rebuild` 的值（空 = 读模型与 events 一致，没有被标记失效）。
	// 它是**叙述性事实**：非空只说明"下一次启动会重建"，不参与失败判定（正要重建时序号当然
	// 可能还没铺好）。
	PendingRebuild string `json:"pendingRebuild,omitempty"`
	// Messages `messages` 表总行数。
	Messages int64 `json:"messages"`
	// Runs `runs` 表总行数。
	Runs int64 `json:"runs"`
	// OrphanMessages `run_id` 非空、但 `runs` 里没有这一行的消息数（引用不完整）。
	OrphanMessages int64 `json:"orphanMessages"`
	// EmptyBodies 空正文行数（`text=''` 且 `tool_details_json` 空或 `[]`）。
	EmptyBodies int64 `json:"emptyBodies"`
	// BadOrdinalSessions 会话内 `messages.ordinal` 不是 1..N 的会话数。
	BadOrdinalSessions int64 `json:"badOrdinalSessions"`
	// BadEventSeqSessions 会话内 `events.seq` 不是 0..N-1 的会话数。
	BadEventSeqSessions int64 `json:"badEventSeqSessions"`
	// ForeignSources `messages.events_json` 里出现过的、**不在白名单**的事件类型（去重升序）。
	ForeignSources []string `json:"foreignSources"`
	// Drift 上面五条漂移判据的合计（0 = 读模型与 events 一致；`PendingRebuild` 不计入）。
	Drift int64 `json:"drift"`
	// Note 采集不到事实时的说明（库不可读等）；非空即"无法校验"。
	Note string `json:"note,omitempty"`
}

// CollectReadModelDoctorFacts 采集读模型体检的事实（只读；库不可用时记 Note 并返回零值）。
//
// `messageSources` 是**消息白名单投影的来源类型集合**，由调用方（组合根）交下来：
// 那份集合的声明面必须与投影的 `switch` 同源（守卫 G5 钉住），所以它属于会话侧；机制层
// **不认识**事件类型词汇，只按调用方给的集合判"库里有没有别的东西混进 messages"。
// 这与 `credential.ResolveProviderKey` 收窄成 `(id, keyRef)` 是同一手法：机制层不 import 领域，
// 由组合根把领域事实交下去。
func CollectReadModelDoctorFacts(ctx context.Context, st *store.Store, messageSources []string) ReadModelDoctorFacts {
	var f ReadModelDoctorFacts
	if st == nil {
		f.Note = "没有可读的库句柄"
		return f
	}
	db := st.DB()

	// 叙述性事实：重建标记（读不到只是一条信息，不影响结论）。
	if v, ok, err := st.GetMeta(ctx, "read_models_rebuild"); err == nil && ok {
		f.PendingRebuild = v
	}

	count := func(label, query string) (int64, bool) {
		var n int64
		if err := db.Raw(query).Row().Scan(&n); err != nil {
			f.Note = fmt.Sprintf("%s 采集失败：%v", label, err)
			return 0, false
		}
		return n, true
	}

	var ok bool
	if f.Messages, ok = count("messages 行数", `SELECT COUNT(*) FROM messages`); !ok {
		return f
	}
	if f.Runs, ok = count("runs 行数", `SELECT COUNT(*) FROM runs`); !ok {
		return f
	}
	if f.OrphanMessages, ok = count("孤儿消息", `SELECT COUNT(*) FROM messages m
		WHERE m.run_id <> '' AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.id = m.run_id)`); !ok {
		return f
	}
	if f.EmptyBodies, ok = count("空正文行", `SELECT COUNT(*) FROM messages
		WHERE text = '' AND (tool_details_json = '' OR tool_details_json = '[]')`); !ok {
		return f
	}
	// 1..N：`MIN`/`MAX`/`COUNT` 三条一起判（洞与"不从头起"是两种不同的坏法），
	// `COUNT(DISTINCT)` 顺带覆盖重号——唯一索引在时它不可能触发，但判据不该依赖索引存在。
	if f.BadOrdinalSessions, ok = count("会话内 ordinal", `SELECT COUNT(*) FROM (
		SELECT session_id, COUNT(*) AS n, MIN(ordinal) AS lo, MAX(ordinal) AS hi,
		       COUNT(DISTINCT ordinal) AS d
		FROM messages GROUP BY session_id
		HAVING lo <> 1 OR hi <> n OR d <> n)`); !ok {
		return f
	}
	if f.BadEventSeqSessions, ok = count("会话内事件位次", `SELECT COUNT(*) FROM (
		SELECT session_id, COUNT(*) AS n, MIN(seq) AS lo, MAX(seq) AS hi
		FROM events GROUP BY session_id
		HAVING lo <> 0 OR hi <> n - 1)`); !ok {
		return f
	}
	// 来源白名单：`json_each` 把每行的来源数组摊开，取出去重后的类型集合，再与调用方给的
	// 那份白名单（会话侧唯一声明处）比对。
	var types []string
	if err := db.Raw(`SELECT DISTINCT json_extract(je.value, '$.type') AS t
		FROM messages m, json_each(m.events_json) je
		WHERE json_extract(je.value, '$.type') IS NOT NULL ORDER BY t`).Scan(&types).Error; err != nil {
		// `events_json` 不是合法 JSON（手写夹具/旧形态）时 json_each 会报错——那是事实，
		// 如实记下来当一条漂移来源，而不是把整段体检判成"无法校验"。
		f.ForeignSources = []string{"<events_json 不是合法 JSON 数组：" + err.Error() + ">"}
	} else {
		for _, t := range types {
			if !slices.Contains(messageSources, t) {
				f.ForeignSources = append(f.ForeignSources, t)
			}
		}
	}
	sort.Strings(f.ForeignSources)

	f.Drift = f.OrphanMessages + f.EmptyBodies + f.BadOrdinalSessions + f.BadEventSeqSessions +
		int64(len(f.ForeignSources))
	return f
}
