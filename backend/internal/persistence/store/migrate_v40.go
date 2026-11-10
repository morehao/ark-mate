package store

// 本文件是 **v40**（应用体系：新增 `apps` + `app_members` 两表）的版本步本体。
//
// 为什么需要它：一期 A+Slot「应用体系」把「组合型应用」与「实体型应用」都收敛到一条 **App 定义**
// 上——一条 app 声明它引用的成员（专家/自动化/skill/mcp）与它拥有的实体，以及在固定扩展点各登记的
// 一项（工具域 / App Slot / 页面 / 能力 scope / 归属 / 版本门）。设计见
// `.dsh/docs/specs/2026-11-09-app-system-design.md` §5.9 正文表 + 附录 A（DDL 唯一真源）；
// 计划见 `.dsh/docs/plans/2026-11-09-app-system-a-plus-slot-plan.md` §2。
//
// 两张表的分工：
//
//	apps        应用定义本体（一条 app 一行）
//	app_members app 与其成员定义的多对多**归属**关系（member_kind 指向既有 6 种定义 + entity）
//
// **为什么成员落关联表而不是 JSON 字符串数组**（设计 §5.2）：`Expert.Skills`/`.Mcps` 那种弱引用
// 无法回答「这个 skill 属于哪个 app」（反向查询），也无引用完整性。归属需要按 (member_kind,
// member_id) 反向查，故落成关联表。`owned` 区分「拥有」（可随 app 删）/「引用」（只解除归属，
// 默认 0=引用，设计 Q2）。
//
// 纯 DDL、无回填：旧库升级后两表为空（历史没有 app，不臆造）。
//
// 时间列口径（`docs/agents/time.md`）：`created_at` / `updated_at` 是**服务端产生的瞬时**，
// 落库用 **INTEGER NOT NULL 存 Unix 毫秒**（`objtime.Milli`）——这里刻意不用本仓 SQLite 时间列
// 惯用的 RFC3339Nano 文本（那是 transcript / 落库 serializer 对**既有**表的硬契约）：app 是新表，
// 直接采用对外 wire 同量纲的毫秒整数，读写两侧零换算。
//
// 可空性（设计 §5.9）：可空列全为「否」——空语义用空串 / '[]' / 0 表达，不用 SQL NULL
// （对齐本仓「避免可空三态」惯例，`migrate_v39.go` 对思考能力列同样 NOT NULL DEFAULT）。
//
// 语句里的表名/列名都是**字面量**（与 v15–v39 同一纪律：版本步只许写当时那一版的名字，绝不引用
// `model.TableName*` 或 Go 字段名）。
var migrationV40 = migration{
	version: 40,
	name:    "应用体系：apps + app_members 两表",
	// 备份：本步新增两张表（不改既有表——理论上旧二进制仍能跑），但 `latestSchemaVersion` 前移会让
	// 旧二进制对 v40 库**拒绝启动**（与 v12+ 同档），所以保留备份窗口（设计 §5.10）。
	backup: "v40",
	statements: []string{
		`CREATE TABLE apps (
			id                    TEXT    PRIMARY KEY,
			name                  TEXT    NOT NULL,
			description           TEXT    NOT NULL DEFAULT '',
			avatar                TEXT    NOT NULL DEFAULT '',
			tags_json             TEXT    NOT NULL DEFAULT '[]',
			entities_json         TEXT    NOT NULL DEFAULT '[]',
			tool_domains_json     TEXT    NOT NULL DEFAULT '[]',
			slots_json            TEXT    NOT NULL DEFAULT '[]',
			page_json             TEXT    NOT NULL DEFAULT '',
			settings_schema_json  TEXT    NOT NULL DEFAULT '',
			api_version           TEXT    NOT NULL DEFAULT '^1',
			enabled               INTEGER NOT NULL DEFAULT 1,
			created_at            INTEGER NOT NULL,
			updated_at            INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_apps_enabled ON apps(enabled)`,
		`CREATE TABLE app_members (
			app_id       TEXT    NOT NULL,
			member_kind  TEXT    NOT NULL,
			member_id    TEXT    NOT NULL,
			owned        INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (app_id, member_kind, member_id)
		)`,
		`CREATE INDEX idx_app_members_member ON app_members(member_kind, member_id)`,
	},
}
