package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/config"
)

// ——— `mate doctor` 的 read-models 段（v21–v23 的读模型漂移） ———
//
// 这一组钉的是**分档**（哪一条该 fail、哪一条只能 warn）与**五条漂移判据各自真的会红**——
// 分档错了就是退出码错了，而判据"从不触发"（写了个永远为 0 的条件）与"读模型干净"在报告里
// 长得一模一样。
//
// 事实是全 SQL 采的（`manager.CollectReadModelDoctorFacts`），所以这里的夹具刻意**直插**：
// 手写 `events_json` 才能造出"越界来源"这种投影不会产出的形态。

type readModelRig struct {
	home   string
	dbPath string
}

func newReadModelRig(t *testing.T) *readModelRig {
	t.Helper()
	home := t.TempDir()
	return &readModelRig{home: home, dbPath: filepath.Join(home, "mate.db")}
}

// seed 打开可写库跑一段夹具动作后**关掉**——体检那一路要用 `mode=ro` 打开同一个文件。
func (r *readModelRig) seed(t *testing.T, fn func(st *store.Store)) {
	t.Helper()
	st, err := store.Open(r.dbPath)
	if err != nil {
		t.Fatalf("建库失败：%v", err)
	}
	defer func() { _ = st.Close() }()
	fn(st)
}

func (r *readModelRig) opts() doctorOptions {
	return doctorOptions{Home: r.home, DBPath: r.dbPath, StoreMode: config.StoreModeSQLite}
}

const rmSession = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QE"

// insertSession / insertRun / insertMessage 是直插夹具（都用**生产形状**的 id 与位次）。
func insertSession(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if err := st.DB().Exec(`INSERT INTO sessions (id, status, created_at, updated_at)
		VALUES (?, 'completed', '2026-05-06T10:00:00Z', '2026-05-06T10:00:00Z')`, id).Error; err != nil {
		t.Fatalf("写会话夹具: %v", err)
	}
}

func insertRunRow(t *testing.T, st *store.Store, id, session string) {
	t.Helper()
	if err := st.DB().Exec(`INSERT INTO runs (id, session_id, user_text, status, first_seq, last_seq, started_at, ended_at)
		VALUES (?, ?, 'q', 'completed', 0, 2, '2026-05-06T10:00:00Z', '2026-05-06T10:00:01Z')`,
		id, session).Error; err != nil {
		t.Fatalf("写 runs 夹具: %v", err)
	}
}

func insertEventRow(t *testing.T, st *store.Store, id, session, typ string, seq int64) {
	t.Helper()
	if err := st.DB().Exec(`INSERT INTO events (id, session_id, seq, type, payload_json, created_at)
		VALUES (?, ?, ?, ?, '{}', '2026-05-06T10:00:00Z')`, id, session, seq, typ).Error; err != nil {
		t.Fatalf("写 events 夹具: %v", err)
	}
}

func insertMessageRow(t *testing.T, st *store.Store, id, session, runID, text string, ordinal, seqLo int64, eventsJSON string) {
	t.Helper()
	if eventsJSON == "" {
		eventsJSON = `[{"seq":0,"type":"session.message","payload":{"text":"x"}}]`
	}
	if err := st.DB().Exec(`INSERT INTO messages
		(id, session_id, run_id, role, text, events_json, tool_details_json, ordinal, kind, seq_lo, seq_hi, created_at)
		VALUES (?,?,?, 'user', ?, ?, '[]', ?, '', ?, ?, '2026-05-06T10:00:00Z')`,
		id, session, runID, text, eventsJSON, ordinal, seqLo, seqLo).Error; err != nil {
		t.Fatalf("写 messages 夹具: %v", err)
	}
}

// seedCleanReadModel 造一个**一致**的现场：一条会话、一条 run、两条消息（ordinal 1..2）、
// 位次 0..2 连续、来源全在白名单里。
func seedCleanReadModel(t *testing.T, st *store.Store) {
	t.Helper()
	insertSession(t, st, rmSession)
	insertRunRow(t, st, "run_01J9Z6Q0M7X2V8K4B3N5TRW2QF", rmSession)
	insertEventRow(t, st, "evt_01J9Z6Q0M7X2V8K4B3N5TRW2QG", rmSession, "session.created", 0)
	insertEventRow(t, st, "evt_01J9Z6Q0M7X2V8K4B3N5TRW2QH", rmSession, "session.user_message", 1)
	insertEventRow(t, st, "evt_01J9Z6Q0M7X2V8K4B3N5TRW2QJ", rmSession, "session.message", 2)
	insertMessageRow(t, st, "msg_01J9Z6Q0M7X2V8K4B3N5TRW2QK", rmSession, "run_01J9Z6Q0M7X2V8K4B3N5TRW2QF", "问一句", 1, 1,
		`[{"seq":1,"type":"session.user_message","payload":{"text":"问一句"}}]`)
	insertMessageRow(t, st, "msg_01J9Z6Q0M7X2V8K4B3N5TRW2QM", rmSession, "run_01J9Z6Q0M7X2V8K4B3N5TRW2QF", "答一句", 2, 2,
		`[{"seq":2,"type":"session.message","payload":{"text":"答一句"}}]`)
}

// TestDoctorReadModelsClean 一致现场：`read-models` 必须 **ok**（这是我唯一能证明"判据没有
// 恒红"的方向），并且把人话里的规模报出来。
func TestDoctorReadModelsClean(t *testing.T) {
	r := newReadModelRig(t)
	r.seed(t, func(st *store.Store) { seedCleanReadModel(t, st) })

	by := checkByName(t, checkReadModels(r.opts()))
	c, ok := by["read-models"]
	if !ok {
		t.Fatalf("缺少 read-models 检查项: %+v", by)
	}
	if c.Status != doctorOK {
		t.Fatalf("一致现场应 ok：%+v", c)
	}
	for _, want := range []string{"messages=2", "runs=1", "孤儿=0", "空正文=0", "序号不连续会话=0", "位次不连续会话=0"} {
		if !strings.Contains(c.Detail, want) {
			t.Fatalf("人话应报出 %q：%s", want, c.Detail)
		}
	}
}

// TestDoctorReadModelsDetectsDrift 五条漂移判据各自**真的会红**（每条一个独立库）。
//
// 判据不是"某一条非 0 就红"（那样一条坏条件就能让整组看起来在工作）：每例都断言
// **对应的那一项**恰好 == 1，其余项为 0。
func TestDoctorReadModelsDetectsDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, st *store.Store)
		want   func(t *testing.T, f readModelDriftView)
	}{
		{
			name: "孤儿消息",
			mutate: func(t *testing.T, st *store.Store) {
				insertMessageRow(t, st, "msg_orphan", rmSession, "run_does_not_exist", "孤儿", 3, 3, "")
			},
			want: func(t *testing.T, f readModelDriftView) {
				if f.Orphans != 1 {
					t.Fatalf("孤儿数应 1：%+v", f)
				}
			},
		},
		{
			name: "空正文行",
			mutate: func(t *testing.T, st *store.Store) {
				insertMessageRow(t, st, "msg_empty", rmSession, "run_01J9Z6Q0M7X2V8K4B3N5TRW2QF", "", 3, 3, "")
			},
			want: func(t *testing.T, f readModelDriftView) {
				if f.EmptyBodies != 1 {
					t.Fatalf("空正文数应 1：%+v", f)
				}
			},
		},
		{
			name: "序号有洞",
			mutate: func(t *testing.T, st *store.Store) {
				// 1,2 → 1,3：留下一个洞。外键与唯一索引都不拦"洞"（它们管的是引用与重号）。
				if err := st.DB().Exec(`UPDATE messages SET ordinal = 3 WHERE ordinal = 2`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, f readModelDriftView) {
				if f.BadOrdinalSessions != 1 {
					t.Fatalf("序号不连续会话数应 1：%+v", f)
				}
			},
		},
		{
			name: "序号不从头起",
			mutate: func(t *testing.T, st *store.Store) {
				// 1,2 → 3,4：序号连续，但"第几条"从 3 起（与"第一条"这个说法对不上）。
				// 加 2 而不是加 1：加 1 会在中途撞上唯一索引（1→2 时 2 还在），那是另一条判据。
				if err := st.DB().Exec(`UPDATE messages SET ordinal = ordinal + 2`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, f readModelDriftView) {
				if f.BadOrdinalSessions != 1 {
					t.Fatalf("序号不从头起的会话数应 1：%+v", f)
				}
			},
		},
		{
			name: "事件位次有洞",
			mutate: func(t *testing.T, st *store.Store) {
				if err := st.DB().Exec(`DELETE FROM events WHERE seq = 1`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, f readModelDriftView) {
				if f.BadEventSeqSessions != 1 {
					t.Fatalf("位次不连续会话数应 1：%+v", f)
				}
			},
		},
		{
			name: "越界来源类型",
			mutate: func(t *testing.T, st *store.Store) {
				// 投影**不会**产出这种行（thought 不成行）——它模拟的是"某人往投影里加了
				// 一个 case"之后库里已经有的形态。
				insertMessageRow(t, st, "msg_thought", rmSession, "run_01J9Z6Q0M7X2V8K4B3N5TRW2QF", "想出的一句话", 3, 3,
					`[{"seq":3,"type":"session.thought","payload":{"text":"想出的一句话"}}]`)
			},
			want: func(t *testing.T, f readModelDriftView) {
				if len(f.Foreign) != 1 || f.Foreign[0] != "session.thought" {
					t.Fatalf("越界来源应恰好是 session.thought：%+v", f)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReadModelRig(t)
			r.seed(t, func(st *store.Store) {
				seedCleanReadModel(t, st)
				tc.mutate(t, st)
			})
			by := checkByName(t, checkReadModels(r.opts()))
			c := by["read-models"]
			if c.Status != doctorFail {
				t.Fatalf("有漂移时必须 fail（事实与声明不符）：%+v", c)
			}
			f := collectFactsForTest(t, r.dbPath)
			tc.want(t, f)
			// 其余项必须为 0：否则"某一条非 0 就红"会让判据失去区分力。
			if f.Drift != 1 {
				t.Fatalf("本用例只该触发一条漂移，实际 drift=%d：%+v", f.Drift, f)
			}
		})
	}
}

// readModelDriftView 是本文件对事实的**薄视图**（只取判据用到的字段，避免测试到处写长名）。
type readModelDriftView struct {
	Orphans             int64
	EmptyBodies         int64
	BadOrdinalSessions  int64
	BadEventSeqSessions int64
	Foreign             []string
	Drift               int64
}

func collectFactsForTest(t *testing.T, dbPath string) readModelDriftView {
	t.Helper()
	st, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("只读开库: %v", err)
	}
	defer func() { _ = st.Close() }()
	f := manager.CollectReadModelDoctorFacts(context.Background(), st)
	return readModelDriftView{
		Orphans: f.OrphanMessages, EmptyBodies: f.EmptyBodies,
		BadOrdinalSessions: f.BadOrdinalSessions, BadEventSeqSessions: f.BadEventSeqSessions,
		Foreign: f.ForeignSources, Drift: f.Drift,
	}
}

// TestDoctorReadModelsWarnWhenNoFacts 三种"没有可比事实"的处境一律 **warn**（不阻塞退出码）。
//
// memory 模式是**配置选择**；库文件不存在是"还没跑过 serve"；两者都不是事实坏了。
func TestDoctorReadModelsWarnWhenNoFacts(t *testing.T) {
	r := newReadModelRig(t)
	cases := []struct {
		name string
		opts doctorOptions
	}{
		{"memory 模式", doctorOptions{Home: r.home, DBPath: r.dbPath, StoreMode: config.StoreModeMemory}},
		{"库文件不存在", doctorOptions{Home: r.home, DBPath: filepath.Join(r.home, "nope.db"), StoreMode: config.StoreModeSQLite}},
		{"没配库路径", doctorOptions{Home: r.home, StoreMode: config.StoreModeSQLite}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			by := checkByName(t, checkReadModels(tc.opts))
			if c := by["read-models"]; c.Status != doctorWarn {
				t.Fatalf("应记 warn（无法校验）：%+v", c)
			}
		})
	}
}

// TestDoctorReadModelsPendingRebuildDoesNotFail 重建标记非空只写进人话，**不参与分档**：
// 升级后第一次 doctor 恰好在重建之前跑，把它判红会让那条命令永远红。
func TestDoctorReadModelsPendingRebuildDoesNotFail(t *testing.T) {
	r := newReadModelRig(t)
	r.seed(t, func(st *store.Store) {
		seedCleanReadModel(t, st)
		if err := st.SetMeta(context.Background(), model.MetaKeyReadModelsRebuild, "23"); err != nil {
			t.Fatal(err)
		}
	})
	by := checkByName(t, checkReadModels(r.opts()))
	c := by["read-models"]
	if c.Status != doctorOK {
		t.Fatalf("只有重建标记时仍应 ok：%+v", c)
	}
	if !strings.Contains(c.Detail, "read_models_rebuild=23") {
		t.Fatalf("标记应写进人话：%s", c.Detail)
	}
}

// TestDoctorReadModelsIsReadOnly 体检不改库文件（同 provider 段那条判据）。
func TestDoctorReadModelsIsReadOnly(t *testing.T) {
	r := newReadModelRig(t)
	r.seed(t, func(st *store.Store) { seedCleanReadModel(t, st) })
	before, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	checkReadModels(r.opts())
	after, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("体检改了库文件：size %d→%d mtime %s→%s", before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
}
