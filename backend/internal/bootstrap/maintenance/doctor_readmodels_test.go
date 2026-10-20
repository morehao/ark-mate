package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/object/objmeta"
	"github.com/morehao/ark-mate/internal/domain/session"
	doctorfacts "github.com/morehao/ark-mate/internal/drivers/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/persistence/store"
)

// ——— `mate doctor` 的 read-models 段（v21–v23 的读模型漂移） ———
//
// 这一组钉的是**分档**（哪一条该 fail、哪一条只能 warn）与**五条漂移判据各自真的会红**——
// 分档错了就是退出码错了，而判据"从不触发"（写了个永远为 0 的条件）与"读模型干净"在报告里
// 长得一模一样。
//
// 事实是全 SQL 采的（`doctorfacts.CollectReadModelDoctorFacts`），所以这里的夹具刻意**直插**：
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
	f := doctorfacts.CollectReadModelDoctorFacts(context.Background(), st, session.MessageRowSourceTypes())
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
		if err := st.SetMeta(context.Background(), objmeta.KeyReadModelsRebuild, "23"); err != nil {
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

// TestDoctorReadModelsRebuildFixesDrift `--rebuild-read-models` 是体检报红之后的**出路**：
// 它在体检之前跑，于是同一次报告里的 `read-models` 是"修完之后"的状态。
//
// 这条用例同时钉住了三件事：① 漂移确实会被重建修掉（而不是"重建了但没变化"）；
// ② 重建之后 `read-models` 判绿（判据之间不自相矛盾）；③ 待处理的**重建标记被清掉**
// （否则下一次启动会再全量重建一遍）。
func TestDoctorReadModelsRebuildFixesDrift(t *testing.T) {
	r := newReadModelRig(t)
	r.seed(t, func(st *store.Store) {
		seedCleanReadModel(t, st)
		// 三种漂移一起来：孤儿行（指向不存在的 run）、序号有洞、以及一条投影**不会**产出的行。
		insertMessageRow(t, st, "msg_orphan", rmSession, "run_missing", "孤儿", 3, 3, "")
		if err := st.DB().Exec(`UPDATE messages SET ordinal = 4
			WHERE id = 'msg_01J9Z6Q0M7X2V8K4B3N5TRW2QM'`).Error; err != nil {
			t.Fatal(err)
		}
	})

	// 修之前必须判红——否则这条用例证明不了"重建是那条出路"。
	if c := checkByName(t, checkReadModels(r.opts()))["read-models"]; c.Status != doctorFail {
		t.Fatalf("重建之前应判红：%+v", c)
	}

	opts := r.opts()
	opts.RebuildReadModels = true
	c := checkByName(t, checkReadModelRebuild(opts))["read-models-rebuild"]
	if c.Status != doctorOK {
		t.Fatalf("重建应成功：%+v", c)
	}
	if !strings.Contains(c.Detail, "重建了 1 个会话") {
		t.Fatalf("人话应报出重建规模：%s", c.Detail)
	}
	if !strings.Contains(c.Detail, "已清重建标记") {
		t.Fatalf("应报出清了重建标记（v23 迁移留下的那个）：%s", c.Detail)
	}

	// 修之后必须绿（读模型是从 events 重建的，所以孤儿与洞都该消失）。
	after := checkByName(t, checkReadModels(r.opts()))["read-models"]
	if after.Status != doctorOK {
		t.Fatalf("重建之后应判绿：%+v", after)
	}
	// 标记确实清了（不能只信人话）。
	r.seed(t, func(st *store.Store) {
		if v, ok, err := st.GetMeta(context.Background(), objmeta.KeyReadModelsRebuild); err != nil {
			t.Fatalf("读重建标记: %v", err)
		} else if ok && v != "" {
			t.Fatalf("重建标记应被清掉，实际 %q", v)
		}
	})
}

// TestDoctorReadModelsRebuildRequiresFlag 默认路径**一个字节都不写**：同一个漂移库，
// 没有那个开关时重建函数必须拒绝（记 warn），库文件保持不变。
func TestDoctorReadModelsRebuildRequiresFlag(t *testing.T) {
	r := newReadModelRig(t)
	r.seed(t, func(st *store.Store) {
		seedCleanReadModel(t, st)
		if err := st.DB().Exec(`UPDATE messages SET ordinal = 4
			WHERE id = 'msg_01J9Z6Q0M7X2V8K4B3N5TRW2QM'`).Error; err != nil {
			t.Fatal(err)
		}
	})
	before, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	c := checkByName(t, checkReadModelRebuild(r.opts()))["read-models-rebuild"]
	if c.Status != doctorWarn || !strings.Contains(c.Detail, "--rebuild-read-models") {
		t.Fatalf("没有显式开关时应拒绝并给出开关名：%+v", c)
	}
	after, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("默认路径改了库文件：size %d→%d mtime %s→%s",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
	// 漂移还在（没有被"顺手修一下"）。
	if c := checkByName(t, checkReadModels(r.opts()))["read-models"]; c.Status != doctorFail {
		t.Fatalf("没给开关时不该发生任何写入，漂移应仍然在：%+v", c)
	}
}

// TestDoctorReadModelsUnverifiableIsWarnNotFail v21 之前的库在这段判据下必然采集不到
// （没有 `messages.ordinal`），而那正是"刚换上新二进制、还没起 serve"的**正常状态**——
// 升级后第一次体检在这里判红，红的原因却与读模型无关，人话还写着"无法校验"（判档与文案矛盾）。
//
// 这条用例钉两件事：① 采集不到 → **warn**（退出码只由"事实与声明不符"驱动）；
// ② 能看出库比判据旧时，人话里要**直接给那条出路**（而不是丢一句 `no such column: ordinal`）。
func TestDoctorReadModelsUnverifiableIsWarnNotFail(t *testing.T) {
	r := newReadModelRig(t)
	r.seed(t, func(st *store.Store) {
		seedCleanReadModel(t, st)
		// 造出"老库"的两种特征：判据要的列没了（采集必然失败）+ 版本停在 v20 之前。
		if err := st.DB().Exec(`DROP TABLE messages`).Error; err != nil {
			t.Fatal(err)
		}
		if err := st.DB().Exec(`DELETE FROM schema_migrations WHERE version >= 21`).Error; err != nil {
			t.Fatal(err)
		}
	})
	c := checkByName(t, checkReadModels(r.opts()))["read-models"]
	if c.Status != doctorWarn {
		t.Fatalf("采集不到应记 warn（无法校验不参与退出码）：%+v", c)
	}
	if !strings.Contains(c.Detail, "--rebuild-read-models") || !strings.Contains(c.Detail, "v20") {
		t.Fatalf("应点名出路与实际库版本：%s", c.Detail)
	}
}

// TestDoctorReadModelsUnreadableFileIsWarn 库文件坏了/不是 SQLite 也归"无法校验"（warn），
// 同样是"判档只由事实驱动"这一条的另一面。
func TestDoctorReadModelsUnreadableFileIsWarn(t *testing.T) {
	r := newReadModelRig(t)
	r.seed(t, func(st *store.Store) { seedCleanReadModel(t, st) })
	if err := os.WriteFile(r.dbPath, []byte("这不是一个 SQLite 库"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := checkByName(t, checkReadModels(r.opts()))["read-models"]
	if c.Status != doctorWarn {
		t.Fatalf("打不开应记 warn：%+v", c)
	}
	if !strings.Contains(c.Detail, "无法校验读模型") {
		t.Fatalf("人话应说清是「无法校验」而不是判红：%s", c.Detail)
	}
}
