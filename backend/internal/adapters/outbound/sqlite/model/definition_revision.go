package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameDefinitionRevision **定义变更**审计（**全局单调 seq = 代际 rev 的来源**）。
	//
	// v8 落地时它叫 `capability_revisions`，v16 改成现在的名字（设计
	// `.dsh/docs/specs/2026-10-11-data-model-v16-design.md` D3）。改名理由是**一个词被三种
	// 范围占用**，而这一种的覆盖范围与另外两种**直接冲突**：
	//   - `ctrcapability` / `dtocapability` / `internal/platform/capability` / `mate capability migrate`
	//     的 capability = **skill + mcp**（+ tool 目录）；
	//   - `GET /capabilities` 的 `CapabilityScope` / `session.capability_json` 的 capability
	//     = **底座能力声明面**（运行期事实）；
	//   - 本表审计的是**6 种用户可编辑定义**（skill/mcp/agent/automation/provider/model），
	//     其中 agent/automation/provider/model 四种根本不在 `ctrcapability` 的管辖内。
	//
	// `definition` 是本仓**既有**词（`store.DefinitionImportItem`、`validDefinitionID`、
	// `internal/platform/capability/definition.go`），不需要新造。**`kind` 列名刻意不动**：列名活在表内
	// 名字空间，表名已经给出作用域；需要带表名前缀的是索引名（与表名同一名字空间），不是列名
	// （设计 D2 与 D3 是同一条逻辑的两面）。
	TableNameDefinitionRevision = "definition_revisions"
)

// DefinitionRevisionEntity 是一条定义变更审计。
//
// **刻意不加外键**：审计必须在定义被删除后仍然存在——这与 `events` 表"必须挂会话"
// （`events.session_id NOT NULL REFERENCES sessions(id)`）的约束正好相反，别照抄那套。
//
// `Seq` 是 `AUTOINCREMENT`（单调、不复用），因此可以直接当作设计文档 §3.3/§10.5-③ 的
// "当前代际 rev"：M1 落地时它取 `MAX(seq)` 即可，不需要另造一个进程内计数器
// （设计文档 §"M6 与 M1 有直接交叠"那条建议）。
//
// 注意本表**没有 `id` 列**（主键叫 `seq`）：gormdao 以 `id` 为主键的泛型方法
// （GetByID/UpdateByID/UpdateMap/Delete）一律不可用，见 `internal/adapters/outbound/sqlite/dao/doc.go`。
type DefinitionRevisionEntity struct {
	// Seq 全局单调序（AUTOINCREMENT 保证不复用）。
	Seq int64 `gorm:"column:seq;primaryKey;autoIncrement"`
	// Kind 定义种类。取值见 DefinitionKind* 六个常量（v16 之前叫 CapabilityKind*——
	// 那一族名字与"能力面"（skill+mcp）和"底座能力声明面"撞词，见 TableNameDefinitionRevision）。
	Kind string `gorm:"column:kind;not null"`
	// DefinitionID 被改动的定义 id（定义删除后本行仍留着它）。
	DefinitionID string `gorm:"column:definition_id;not null"`
	// Action 变更动作。取值见 DefinitionActionCreate / …Update / …Delete / …Import。
	Action string `gorm:"column:action;not null"`
	// Actor 变更发起者（当前是调用方标识，如 "rest" / "cli" / "import"）。
	Actor string `gorm:"column:actor;not null"`
	// At 变更时刻（RFC3339Nano(UTC)）。
	At time.Time `gorm:"column:at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// Digest 提交后 body 的摘要（sha256 hex）：审计要能回答"那一版到底是什么"，
	// 而不是只留一个自增号。
	Digest string `gorm:"column:digest;not null"`
	// PrevHash 链上**前一条**的 `Hash`；创世条写 `store.DefinitionRevisionGenesisHash`。
	//
	// v32 起（T4）：每行挂在前一行的哈希上，于是"改过"从"看不出来"变成"链断在哪儿"。
	// **空串 = v32 之前的行**（那时没有链）：它们构成链的前缀、不参与校验，也不假装被保护过
	// ——把历史行就地补算成链，等于把"当时没人校验"这件事写成"当时就被校验过"。
	PrevHash string `gorm:"column:prev_hash;not null;default:''"`
	// Hash 本行的链哈希（对"归一化后的内容 + PrevHash"取 sha256 hex）。
	//
	// 归一化规则与算法只有一份实现（`store/definition_revision_chain.go`）：它一旦改了，
	// 既有库的链会**当场断**——这是刻意的（"验证算法"和"写入算法"必须是同一段代码）。
	Hash string `gorm:"column:hash;not null;default:''"`
}

// TableName 实现 gormdao.Entity。
func (DefinitionRevisionEntity) TableName() string { return TableNameDefinitionRevision }

// DefinitionRevisionEntityList 是 DefinitionRevisionEntity 切片类型（gormdao 泛型参数要求具名切片）。
type DefinitionRevisionEntityList []DefinitionRevisionEntity
