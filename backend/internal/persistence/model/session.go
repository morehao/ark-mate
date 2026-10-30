package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameSession 会话权威元数据。
	TableNameSession = "sessions"
)

// SessionEntity 是 sessions 表的一行（DB 权威元数据；字段对齐 manager.SessionRec + transcript header）。
type SessionEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// Cwd 会话工作空间（canonical 绝对路径；= 底座 ACP session 的 cwd，
	// 也是原生权限 workspace-write 的边界）。
	Cwd string `gorm:"column:cwd;not null"`
	// WorkspaceID 归属的工作空间登记行（v14；'' = 未登记/登记已删 → 读面归到**默认工作空间**）。
	//
	// 与 Cwd 的分工是本表最容易踩的一处，刻意记在这里：
	//   - **Cwd 是执行事实**：底座 ACP session 拿它当 cwd、原生权限按它画沙箱边界，
	//     它是历史事实、不可变，重建/续聊要按它重放。它**不能**由 WorkspaceID 推导——
	//     工作空间被删登记、或目录后来不可用时，历史会话的 cwd 必须仍然读得出来；
	//   - **WorkspaceID 是归属事实**：只用于「这条会话属于哪个分组」。它与 Cwd 在大多数行上
	//     指向同一目录，但两者不一致是**允许的**（设计 ADR-2/ADR-5），不是脏数据。
	//
	// 刻意**不加外键**：本仓 SQLite driver 不注入 `foreign_keys`（`docs/agents/build-and-runtime.md` 已登记该代价），
	// 写了也不生效；且删登记**本来就不该**连带删会话（设计 ADR-6），级联语义恰恰是错的。
	WorkspaceID string `gorm:"column:workspace_id;not null;default:''"`
	// WorkspaceTemporary 该工作空间是否由平台代建（未指定 cwd 时在 workspace_dir 下建）。
	// 权威标志：任何「临时空间」判定都必须读它，不得从路径形状反推。
	WorkspaceTemporary bool `gorm:"column:workspace_temporary;serializer:arkmate_flag"`
	// 这里曾有 `ProviderID string \`gorm:"column:provider_id"\``：会话级的"用哪个供应商"。
	//
	// v19 删掉 agent 面之后它**再没有任何写入点**（`SessionRec.ProviderID` 只被读、从不被赋值），
	// 线上恒为空串；v26 删列。供应商由**模型行**给出（`models.provider_id`）——会话引用的是一条
	// 平台模型行，那一行已经说清了它挂在哪家供应商下，再存一列就是第二事实源。
	// ModelID 会话级模型引用（空 = 底座默认）。**指向 `models.id`**（平台铸造的 `mdl_<ULID>` 行身份），
	// **不是** `models.model`（那是"发给上游的模型名"，一个外部世界的字符串）。
	//
	// v26 起语义是"存**身份**、运行时值现算"（定案「辰」，设计 §5.4）：库里只留平台行 id，底座认的
	// 不透明值（dsh 下是 `["mate-<provider>","<model>"]` 这个 JSON 数组）由 adapter **每次现算**——
	// 于是路由键改名之后旧会话照样活得下来，而且内核的表里不再出现底座词表的字符串。
	// v16 前叫 `model`（更早还存过"供应商/模型"两段）。
	ModelID string `gorm:"column:model_id;not null"`
	// RuntimeID 最近/首选 runtime（执行事实，非身份，用户不可见）。v17 前叫 `runtime_hint`。
	//
	// 改名的理由（与列同批）：`runtime_hint` 这个名字在本仓同时指过**两个不同的东西**——
	// 本列（这条会话上次是哪个底座跑的，`session_start.go` 用它判 `sameRuntime`）与
	// automation 定义里**已退休的选择面键** `target.runtime_hint`（写面 40001 显式拒绝）。
	// 本列改成 `runtime_id` 后，`runtime_hint` 只剩"退休选择面"一个含义。
	RuntimeID string `gorm:"column:runtime_id;not null"`
	// RuntimeSessionID 原生底座会话 id（形态由各底座自己决定，平台只加 `rt_` 壳）。P3 双 id：
	// sessions.id = 平台 uuid（对外稳定、隐藏底座），本列为恢复承载（load/resume）。
	//
	// v17 前叫 `runtime_session_id`：`provider` 在本仓是**模型供应商**的词（`providers` 表、
	// `provider_id` 列、`provider_not_found`），而本列装的是**底座会话**——一个东西一个词。
	RuntimeSessionID string `gorm:"column:runtime_session_id;not null"`
	Title            string `gorm:"column:title;not null"`
	Pinned           bool   `gorm:"column:pinned;serializer:arkmate_flag"`
	Archived         bool   `gorm:"column:archived;serializer:arkmate_flag"`
	// Status 会话状态。取值权威定义在 internal/domain/session/events.go（SessionStatus*）。
	Status string `gorm:"column:status;not null"`
	// Origin 会话来源（M3；'' = 人工创建，'automation:<id>' = 该 automation 触发产生）。
	// 单独成列而不是塞进 capability_json：会话列表要逐行渲染"这是不是自动跑出来的"，
	// 而逐行解析 JSON 是 O(n) 的解析成本、也让 SQL 侧的过滤/索引无从下手。
	Origin string `gorm:"column:origin;not null"`
	// CapabilityJSON 创建时解析出的**能力选定明细**（JSON；'' = 未声明 → 回落 agent 白名单）。
	CapabilityJSON string `gorm:"column:capability_json;not null"`
	// CapabilityFingerprint 该会话**钉住的**能力指纹（代际）。单独成列是为了代际回收能按它
	// 走索引查"还有哪些会话在用这个代际"；塞进 JSON 就得全表扫 + 逐行解析。
	// '' = 未声明能力选择（与未引入代际时的行为逐字相同）。
	CapabilityFingerprint string `gorm:"column:capability_fingerprint;not null"`
	// NativeMode 会话**钉住**的原生权限模式（v29；`''` = 不注入原生权限配置）。
	//
	// 它是**创建时的快照**（与 `capability_json` 同款）：模式只能以进程 env 下发
	// （`DSH_PERMISSION_MODE`），所以"一个进程内按会话切换"走不通，只能每模式一个暖进程；
	// 而暖进程的键必须在取连接之前已知 ⇒ 模式必须落库。每次现算工作空间默认的后果是——
	// 用户改了工作空间的默认权限模式，**昨天那些会话**的行为跟着变了，而界面上什么都不会说。
	NativeMode string `gorm:"column:native_mode;not null;default:''"`
	// LastSeq 该会话已落库事件的最后 seq（events.seq 全局单调）。
	LastSeq   int64     `gorm:"column:last_seq"`
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (SessionEntity) TableName() string { return TableNameSession }

// SessionEntityList 是 SessionEntity 切片类型（gormdao 泛型参数要求具名切片）。
type SessionEntityList []SessionEntity

// SessionMetaPatch 会话元数据子集更新（nil = 不更新；updated_at 恒刷新）。
type SessionMetaPatch struct {
	Title    *string
	Pinned   *bool
	Archived *bool
	Status   *string
	// ModelID 平台模型行 id（v26 起；`nil` = 不改）。删掉了与之并列的 `ProviderID`——那一列已出列。
	ModelID *string
	// CapabilityJSON 会话钉住的能力选定（M3-8 会话内改能力；`nil` = 不改）。
	//
	// 它与 `CapabilityFingerprint` 的分工不变：这一列是**用户要的**，指纹是**此刻跑着的**
	// （下一轮边界才追上来）。刻意**不**在这里一起写指纹——改能力不换代际，换代际发生在
	// 轮次边界的迁移路径上（`ConnectSession` 落账）。
	CapabilityJSON *string
}

// Empty 判断补丁是否为空（空补丁不产生 SQL）。
func (p SessionMetaPatch) Empty() bool {
	return p.Title == nil && p.Pinned == nil && p.Archived == nil &&
		p.Status == nil && p.ModelID == nil && p.CapabilityJSON == nil
}
