package skill

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// ——— 定义载体的窄端口（v3.2 刀 4）———
//
// 技能域的写面必须落盘/落库，但这些读写**不属于领域知识**：它是定义载体（文件 / SQLite 行）
// 的机制。按六边形口径，消费方声明自己需要的那几个方法，装配根把机制注进来——
// 于是 `internal/domain/skill` 不必再 import `internal/drivers/mechanism/definition`（L6 组合驱动），
// 领域层的出边只剩 L0/L1。
//
// 端口刻意只列技能域真正用到的六个方法（机制层的 `DefinitionRepo` 是它的超集）：
// 领域层看得到的越窄，将来换载体时被牵连的地方越少。

// DefinitionRepo 一类定义（技能 = `skills` 子目录）的载体。
type DefinitionRepo interface {
	// Locator 人类可读的定位串（响应里的 `path`）。不参与任何判据。
	Locator(id string) string
	// SourceFile 注册表条目 `SourceFile` 的取值（Library 的 origin 依据）；DB 载体返回空串。
	SourceFile(id string) string
	// Exists 报告载体里是否已有该定义（与注册表一起构成"存在"的两个来源）。
	Exists(ctx context.Context, id string) (bool, error)
	// Open 读出定义并应用 patch（值为 nil = 删键），返回**未提交**的草稿与乐观锁依据。
	Open(ctx context.Context, id string, patch map[string]any) (doc *capability.DefinitionFile, rev int64, existed bool, err error)
	// Commit 提交草稿；created = 本次新建，conflict = CAS 没中（→ 40901，绝不 last-write-wins）。
	Commit(ctx context.Context, id string, doc *capability.DefinitionFile, rev int64, existed bool) (created, conflict bool, err error)
	// Remove 删除定义；existed = 真删掉了一个，conflict = CAS 没中。
	Remove(ctx context.Context, id string) (existed, conflict bool, err error)
}

// Definitions 定义载体工厂 + 目录事实。
type Definitions interface {
	// Dir 能力定义根目录；空串 = 本进程没装载能力数据面（memory 模式下启停无处写回）。
	Dir() string
	// RepoFor 取某一类定义的载体；返回错误 = 该模式下写面不可用。
	RepoFor(sub string) (DefinitionRepo, error)
}
