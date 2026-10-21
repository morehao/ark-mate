package archtest

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"gorm.io/gorm"

	// 本守卫要开一个临时库跑真实迁移，而 `dbclient.Open` → `glog.NewLogger` 需要 zap driver：
	// 日志出口归装配根（`cmd/mate` 的匿名引入），test 二进制不含它（同 `internal/persistence/store` 的做法）。
	_ "github.com/morehao/golib/glog/driver/zap"
)

// 本文件是 v16「表/列/索引命名」的两条**收口**守卫（设计 §4 / D1 / D2）：
//
//	A1 `TestIdentifiersFollowLexicalForm`  标识符词法：表名/列名/索引名一律 `^[a-z][a-z0-9_]*$`
//	A2 `TestIndexNamesFollowTablePrefix`   索引名必须带**表名段**：`idx_<表>_<用途>`
//
// 为什么判据落在**真实 schema**（跑完整迁移后读 `sqlite_master` + `PRAGMA table_info`）而不是
// 扫 DDL 文本：索引与列在 16 条迁移里被建了又改又删（`v15` 重建 7 张表、`v16` 改列名与索引名），
// 文本扫描要么把已经删掉的历史名字算进来（假红），要么要写一个 DDL 模拟器（自己成了新的
// 出错面）。跑一遍真实迁移拿到的就是**最终形态**本身，而且顺带把"实体 gorm 标签与 DDL 不同步"
// 这类漂移暴露成一条读得到具体名字的失败。
//
// 为什么值得钉：这两条是**纯风格**判据，错了不会让任何别的测试变红——一个新索引叫
// `idx_memories_session`（表名段被缩写成 `memories`）照样能建出来、查询照样能跑，只是
// "这条索引属于哪张表"从此只能靠人记；而列名写成 `TargetTunID` 时 gorm 照样编译，
// 只有运行时那条 SQL 才会报 `no such column`。风格类漂移的代价全是**累积**的：
// 它不会失败，只会让下一个人读不懂。

// schemaIdentifier 是一个真实 schema 里的具名对象（表 / 列 / 索引）。
type schemaIdentifier struct {
	kind  string // "table" / "column" / "index"
	owner string // 列所属的表 / 索引所属的表（表自身为空）
	name  string
}

// identifierForm 是表名 / 列名 / 索引名的唯一合法形态（对齐 DSH 的 `UNIT_NAME_RE`）。
//
// 为什么必须是"小写字母开头 + 小写字母数字下划线"：SQLite 的标识符**大小写不敏感**，
// 于是 `TargetRunID` 与 `target_runid` 在库里是同一个东西——写成前者时"这条 SQL 到底查的是
// 哪一列"要靠人脑做一次不敏感匹配，而 Go 侧的类型/字段名是敏感的。两端各退一步的公约数
// 就是全小写下划线：它是 v1 起所有表与列的实际形态，不是新规矩。
var identifierForm = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// migratedSchemaDB 造一个临时库、跑完整迁移，返回可读的句柄（连接在测试结束时关闭）。
//
// 与 migrateToLatestSchema 的分工：那个把 schema 摊成"具名对象列表"（A1/A2 要的是名字集合），
// 这个留给需要**原始 SQL / 行内容**的守卫（例如 G1 要读索引定义、G4 要查某个 meta 键在不在）。
func migratedSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema-naming.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开临时库: %v", err)
	}
	s := store.New(db)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("跑完整迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return db
}

// migrateToLatestSchema 造一个临时库、跑完整迁移，返回其中的全部具名对象。
//
// 用**真实迁移**而不是建表 SQL：迁移内核是 schema 的唯一事实源（`internal/persistence/store/migrate.go`
// 的 `schemaMigrations`），A1/A2 要判的就是它跑完之后的样子。
func migrateToLatestSchema(t *testing.T) []schemaIdentifier {
	t.Helper()
	db := migratedSchemaDB(t)

	var out []schemaIdentifier
	rows, err := db.Raw(`SELECT type, name, tbl_name FROM sqlite_master
		WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%'
		ORDER BY type, name`).Rows()
	if err != nil {
		t.Fatalf("读 sqlite_master: %v", err)
	}
	for rows.Next() {
		var typ, name, tbl string
		if err := rows.Scan(&typ, &name, &tbl); err != nil {
			t.Fatalf("扫 sqlite_master: %v", err)
		}
		if typ == "table" {
			out = append(out, schemaIdentifier{kind: "table", name: name})
			continue
		}
		out = append(out, schemaIdentifier{kind: "index", owner: tbl, name: name})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 sqlite_master: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("关闭 sqlite_master 游标: %v", err)
	}

	// 列：逐表 `PRAGMA table_info`（表名取自 sqlite_master，不是外部输入）。
	for _, obj := range out {
		if obj.kind != "table" {
			continue
		}
		colRows, err := db.Raw(`PRAGMA table_info(` + obj.name + `)`).Rows()
		if err != nil {
			t.Fatalf("读 %s 的列: %v", obj.name, err)
		}
		for colRows.Next() {
			var cid, notNull, pk int
			var name, ctype string
			var dflt *string
			if err := colRows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
				t.Fatalf("扫 %s 的列: %v", obj.name, err)
			}
			out = append(out, schemaIdentifier{kind: "column", owner: obj.name, name: name})
		}
		if err := colRows.Err(); err != nil {
			t.Fatalf("遍历 %s 的列: %v", obj.name, err)
		}
		if err := colRows.Close(); err != nil {
			t.Fatalf("关闭 %s 的列表游标: %v", obj.name, err)
		}
	}
	if len(out) == 0 {
		t.Fatal("真实 schema 里一个具名对象都没读到（fail-closed：判据本身失效）")
	}
	return out
}

// TestIdentifiersFollowLexicalForm 守卫 A1（设计 D1）：真实 schema 的每个标识符 + `internal/persistence/model`
// 里每个 gorm `column:` 标签都必须匹配 `^[a-z][a-z0-9_]*$`。
//
// 两侧都要查，是因为它们是**两份手工同步的约定**（AGENTS.md「改事件 type / 数据 schema 须同步
// `internal/persistence/model` 实体与 `internal/persistence/store` 迁移内核」）：DDL 那侧由真实 schema 兜住，
// gorm 标签那侧只能扫源码。只查一侧的后果是另一半静默漂移——`column:TargetRunID` 这种标签
// 编译期无感，`ORDER BY`/`SELECT` 拼出来才报错，而报错的位置离写它的地方很远。
//
// fail-closed：schema 读不到、或 gorm 标签一个都解析不到 → 红（改名/搬迁不许让守卫空转）。
func TestIdentifiersFollowLexicalForm(t *testing.T) {
	objs := migrateToLatestSchema(t)
	for _, obj := range objs {
		if !identifierForm.MatchString(obj.name) {
			where := obj.kind
			if obj.owner != "" {
				where += "（属于 " + obj.owner + "）"
			}
			t.Fatalf("schema 标识符 %q 不符合 `^[a-z][a-z0-9_]*$`（%s）："+
				"SQLite 标识符大小写不敏感，混合大小写会让「这条 SQL 查的是哪一列」只能靠人脑做不敏感匹配，"+
				"而 Go 侧字段名是大小写敏感的——两端各退一步的公约数是全小写下划线", obj.name, where)
		}
	}

	// gorm 标签一侧：`internal/persistence/model/*.go` 的 `column:<名字>`。
	dir := filepath.Join(backendRoot(t), "internal", "persistence", "model")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("扫不到 %s/*.go: %v（fail-closed）", dir, err)
	}
	columnTag := regexp.MustCompile(`column:([A-Za-z0-9_]+)`)
	tags := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", path, err)
		}
		src := stripLineComments(string(raw))
		for _, m := range columnTag.FindAllStringSubmatch(src, -1) {
			tags++
			if !identifierForm.MatchString(m[1]) {
				t.Fatalf("%s 的 gorm 标签 `column:%s` 不符合标识符形态（A1）："+
					"库里这一列的真实名字是全小写下划线，标签跟着写错时 SELECT/ORDER BY 会在"+
					"运行期报 no such column，而错误位置离这里很远", filepath.Base(path), m[1])
			}
		}
	}
	if tags < 50 {
		t.Fatalf("只解析到 %d 个 `column:` 标签——实体改名/搬迁了？（fail-closed）", tags)
	}
}

// TestIndexNamesFollowTablePrefix 守卫 A2（设计 D2）：索引名必须是 `idx_<所属表>_<用途>`。
//
// 判据：`name == "idx_" + owner + "_" + 用途`，即**前缀是 `idx_<表名>_`**（用途段自由，
// 但必须非空——`idx_sessions` 这种"索引名等于表名"查不出属主，等于没带表名段）。
//
// 为什么索引名要带表名而**列名不用**（这是 D2 与 D3 的同一面）：列在表的命名空间里
// （`sessions.provider_id` 一眼看得出是哪个实体的），索引在**库的全局命名空间**里
// （`idx_memories_session` 到底是 `memories` 还是 `permission_memories`，只能靠翻 DDL）。
//
// 为什么只查**最终形态**：历史索引名（`idx_conversations_list`、`idx_capability_revisions_capability`）
// 是冻结的迁移字面量，按纪律绝不回改——它们由 v15/v16 的 `DROP INDEX` + `CREATE INDEX` 收口，
// 所以"今天还活着的索引"才是这条守卫的对象。
func TestIndexNamesFollowTablePrefix(t *testing.T) {
	objs := migrateToLatestSchema(t)
	indexes := 0
	for _, obj := range objs {
		if obj.kind != "index" {
			continue
		}
		indexes++
		want := "idx_" + obj.owner + "_"
		if !strings.HasPrefix(obj.name, want) || obj.name == "idx_"+obj.owner {
			t.Fatalf("索引 %q 所属表是 %q，但它没带表名段（期望前缀 `%s`）——"+
				"索引名在库的全局命名空间里，不带表名段就只能靠翻 DDL 才知道它属于哪张表（D2）",
				obj.name, obj.owner, want)
		}
	}
	if indexes < 10 {
		t.Fatalf("真实 schema 里只读到 %d 条索引——索引都建不出来？（fail-closed）", indexes)
	}

	// 反向：`idx_memories_session` 这类"表名段被缩写"的旧名字必须是**真删了**，
	// 而不是被新名追加上去（两条索引同时存在时，查询优化器可能选中旧那条，
	// 而"新索引没生效"在外观上完全看不出来）。
	legacy := map[string]string{
		"idx_memories_session":                "v16 已改名为 idx_permission_memories_session",
		"idx_capability_revisions_capability": "v16 已改名为 idx_definition_revisions_history",
	}
	var present []string
	for _, obj := range objs {
		if obj.kind == "index" {
			if why, ok := legacy[obj.name]; ok {
				present = append(present, obj.name+"（"+why+"）")
			}
		}
	}
	if len(present) > 0 {
		sort.Strings(present)
		t.Fatalf("旧索引名仍在库里：%s", strings.Join(present, "、"))
	}
}
