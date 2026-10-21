package maintenance

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/config"
)

// ——— `mate doctor` 的 audit-chain 段（T4 / v32） ———
//
// 这一组钉的是**分档**与"三种形态各自真的会红"：链断 → fail；还没升级到 v32 → warn（不是 fail）；
// 只有 v32 之前的历史行 → warn（**不是**"链完好"——那会把"没验过"说成"验过了"）。

type auditChainRig struct {
	home   string
	dbPath string
}

func newAuditChainRig(t *testing.T) *auditChainRig {
	t.Helper()
	home := t.TempDir()
	return &auditChainRig{home: home, dbPath: home + "/mate.db"}
}

func (r *auditChainRig) seed(t *testing.T, fn func(st *store.Store)) {
	t.Helper()
	st, err := store.Open(r.dbPath)
	if err != nil {
		t.Fatalf("建库失败：%v", err)
	}
	defer func() { _ = st.Close() }()
	fn(st)
}

func (r *auditChainRig) opts() doctorOptions {
	return doctorOptions{Home: r.home, DBPath: r.dbPath, StoreMode: config.StoreModeSQLite}
}

func (r *auditChainRig) check(t *testing.T) doctorCheck {
	t.Helper()
	by := checkByName(t, checkDefinitionRevisionChain(r.opts()))
	c, ok := by["audit-chain"]
	if !ok {
		t.Fatalf("缺少 audit-chain 检查项: %+v", by)
	}
	return c
}

// TestDoctorAuditChainClean 干净的链 → ok，并报出行数。
func TestDoctorAuditChainClean(t *testing.T) {
	r := newAuditChainRig(t)
	r.seed(t, func(st *store.Store) {
		for _, id := range []string{"a", "b"} {
			if ok, err := st.UpsertMcpServerDefinition(context.Background(), id, `{"name":"`+id+`"}`, 0, "rest"); err != nil || !ok {
				t.Fatalf("写定义 %s: ok=%v err=%v", id, ok, err)
			}
		}
	})
	c := r.check(t)
	if c.Status != doctorOK {
		t.Fatalf("完好的链应 ok：%+v", c)
	}
	if !strings.Contains(c.Detail, "2 行") {
		t.Fatalf("人话应报出被校验的行数：%s", c.Detail)
	}
}

// TestDoctorAuditChainDetectsBreak 被改过的行 → fail，并指出 seq 与断法。
//
// 这是这段体检存在的全部意义：没有它，哈希链只是个没人调的库函数。
func TestDoctorAuditChainDetectsBreak(t *testing.T) {
	r := newAuditChainRig(t)
	r.seed(t, func(st *store.Store) {
		for _, id := range []string{"a", "b", "c"} {
			if ok, err := st.UpsertMcpServerDefinition(context.Background(), id, `{"name":"`+id+`"}`, 0, "rest"); err != nil || !ok {
				t.Fatalf("写定义 %s: ok=%v err=%v", id, ok, err)
			}
		}
		// 直接改库（模拟"有人绕过应用改了历史"）。
		if err := st.DB().Exec(`UPDATE definition_revisions SET digest = 'tampered' WHERE seq = 2`).Error; err != nil {
			t.Fatalf("篡改: %v", err)
		}
	})
	c := r.check(t)
	if c.Status != doctorFail {
		t.Fatalf("链断了必须 fail：%+v", c)
	}
	for _, want := range []string{"seq=2", store.ChainBreakContent} {
		if !strings.Contains(c.Detail, want) {
			t.Fatalf("人话应含 %q：%s", want, c.Detail)
		}
	}
}

// TestDoctorAuditChainTiers 三种"无法校验"都只能 warn（判红的只能是"事实与声明不符"）：
// memory 模式、只有历史行（无链）、库 schema 早于 v32。
func TestDoctorAuditChainTiers(t *testing.T) {
	t.Run("memory 模式", func(t *testing.T) {
		c := checkByName(t, checkDefinitionRevisionChain(doctorOptions{
			Home: t.TempDir(), StoreMode: config.StoreModeMemory,
		}))["audit-chain"]
		if c.Status != doctorWarn {
			t.Fatalf("memory 模式应 warn：%+v", c)
		}
	})

	t.Run("只有 v32 之前的历史行", func(t *testing.T) {
		r := newAuditChainRig(t)
		r.seed(t, func(st *store.Store) {
			if err := st.DB().Exec(`INSERT INTO definition_revisions (kind, definition_id, action, actor, at, digest)
				VALUES ('skill', 'old', 'create', 'rest', '2026-01-01T00:00:00Z', 'd')`).Error; err != nil {
				t.Fatalf("种历史行: %v", err)
			}
		})
		c := r.check(t)
		if c.Status != doctorWarn {
			t.Fatalf("没有可校验的链时应 warn（不能报「链完好」）：%+v", c)
		}
		if !strings.Contains(c.Detail, "历史行") {
			t.Fatalf("人话要说清为什么没验：%s", c.Detail)
		}
	})

	t.Run("schema 早于 v32", func(t *testing.T) {
		r := newAuditChainRig(t)
		// 把库退回"没有链列"的形态：删两列（SQLite 3.35+ 支持 DROP COLUMN；驱动是 wasm 的新版本）。
		r.seed(t, func(st *store.Store) {
			// 版本号只能**删掉** v32 及其之后的记录（改成 31 会与既有的 v31 行撞主键）。
			// 用常量而不是字面量：以后再加迁移步，这条夹具不该需要回来改。
			for _, sqlText := range []string{
				`ALTER TABLE definition_revisions DROP COLUMN prev_hash`,
				`ALTER TABLE definition_revisions DROP COLUMN hash`,
				fmt.Sprintf(`DELETE FROM schema_migrations WHERE version >= %d`, store.DefinitionRevisionChainSchemaVersion),
			} {
				if err := st.DB().Exec(sqlText).Error; err != nil {
					t.Fatalf("回退 schema 形态（%s）: %v", sqlText, err)
				}
			}
		})
		c := r.check(t)
		if c.Status != doctorWarn {
			t.Fatalf("库还没升级到 v32 时应 warn（不是 fail）：%+v", c)
		}
		want := fmt.Sprintf("v%d", store.DefinitionRevisionChainSchemaVersion-1)
		if !strings.Contains(c.Detail, want) {
			t.Fatalf("人话应报出实际 schema 版本（%s）：%s", want, c.Detail)
		}
	})
}
