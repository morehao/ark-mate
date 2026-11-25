package store

// 本文件是 **v44**（控制面认证数据模型：ark-iam RP 接入）的版本步本体。
//
// 为什么需要它：控制面作为 ark-iam 的 RP，需要在本地存四类**平台侧**数据（设计见
// `.dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md` §详细设计·数据模型）：
//  1. `control_tenants`       租户的平台属性（隔离档 / 配额 / 展示名）；
//  2. `control_users`         ark-iam 用户 → 平台映射 + 开通快照（user_id = ark-iam user.id）；
//  3. `control_identity_audits` 认证/登出审计；
//  4. `control_logout_jtis`   SLO 的 jti 去重（多副本必需）。
//
// **关键口径（DM-2 / C1）**：这些表**不是登录真源、不是所有权真相**——身份真源唯一 = ark-iam；
// 鉴权与资源所有权读**实时令牌 claim**，绝不读这里的快照行。表只承载「映射 + 平台属性 + 审计 +
// 去重」。所以这里刻意**没有** password / role / membership 之类的列（那些在 ark-iam）。
//
// 时间列一律 INTEGER Unix 毫秒（对齐 v40+ 新表与本仓 wire 口径）。表名用复数（与 apps/sessions 一致）。
//
// 纯新增四张表（不改既有表），但 `latestSchemaVersion` 前移会让旧二进制对 v44 库拒绝启动，故保留
// 备份窗口。语句里的表名/列名是**字面量**（与 v15–v43 同一纪律）。
var migrationV44 = migration{
	version: 44,
	name:    "控制面认证数据模型：control_tenants/control_users/control_identity_audits/control_logout_jtis 四表",
	backup:  "v44",
	statements: []string{
		`CREATE TABLE control_tenants (
			tenant_id        TEXT    PRIMARY KEY,
			display_name     TEXT    NOT NULL DEFAULT '',
			isolation_level  TEXT    NOT NULL DEFAULT 'sandboxed',
			quota_cpu        INTEGER NOT NULL DEFAULT 0,
			quota_disk_gb    INTEGER NOT NULL DEFAULT 0,
			created_at       INTEGER NOT NULL,
			updated_at       INTEGER NOT NULL
		)`,
		`CREATE TABLE control_users (
			tenant_id        TEXT    NOT NULL,
			user_id          TEXT    NOT NULL,
			person_id        TEXT    NOT NULL DEFAULT '',
			user_type        TEXT    NOT NULL DEFAULT 'normal',
			status_snapshot  TEXT    NOT NULL DEFAULT 'enable',
			acted_at         INTEGER NOT NULL DEFAULT 0,
			created_at       INTEGER NOT NULL,
			PRIMARY KEY (tenant_id, user_id)
		)`,
		`CREATE INDEX idx_control_users_person ON control_users(person_id)`,
		`CREATE TABLE control_identity_audits (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id       TEXT    NOT NULL DEFAULT '',
			actor_user_id   TEXT    NOT NULL DEFAULT '',
			action          TEXT    NOT NULL,
			result          TEXT    NOT NULL,
			src_ip          TEXT    NOT NULL DEFAULT '',
			at              INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_control_identity_audits_tenant_at ON control_identity_audits(tenant_id, at)`,
		`CREATE TABLE control_logout_jtis (
			jti_hash        TEXT    PRIMARY KEY,
			expires_at      INTEGER NOT NULL,
			created_at      INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_control_logout_jtis_expires ON control_logout_jtis(expires_at)`,
	},
}
