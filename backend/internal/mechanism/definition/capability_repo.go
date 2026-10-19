package definition

import (
	"context"
	"fmt"
	"os"

	"github.com/morehao/ark-mate/internal/capability"
	defval "github.com/morehao/ark-mate/internal/definition"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/store"
)

// ——— 能力定义写面的**载体**（M6-3） ———
//
// 写面的三段抽象（草稿 → 校验 → 提交）与全部对外错误码**一个字都不变**，换掉的只是最底层
// "定义存在哪"这一步：
//
//	memory 模式：文件 `<CapabilityDir>/{skills,mcp}/<id>.json`
//	  C9：读成 map → 只改目标键 → 未知键保留 → 同目录 staging + 原子 rename + 权限沿用
//	  + mtime/size 乐观锁（`capability.DefinitionFile`）
//	sqlite 模式：DB 行（v8 的 `skills` / `mcp_servers`）
//	  事务 + 行 revision CAS + 同事务追一条变更审计（`store.Upsert*Definition`）
//
// 两者对写面的**承诺完全相同**，这正是这层抽象存在的理由：CAS 没中 → 同一个
// `definition_changed`(40901)、同一句"请刷新后重试（你的改动未写入）"；读到坏内容 →
// 同一个 `definition_invalid`(40001)。若不抽象而是各写一遍，两份实现迟早在某个错误分支上
// 分叉，而分叉的表现是"某个模式下能覆盖别人的写入"——静默的数据丢失。
//
// 分派依据是 `s.st != nil`（`EnableStore` 只在 sqlite 模式被调用），与 app 的装载分派同源。

// DefinitionRepo 一份能力定义在某个载体上的读写口。
type DefinitionRepo interface {
	// locator 人类可读的定位串（DTO 的 path 字段）。它**不参与任何判据**，只为让人知道
	// "这条定义存在哪"：memory 模式是文件路径，sqlite 模式是 `db:skill/<id>`。
	Locator(id string) string
	// sourceFile 注册表条目 `SourceFile` 的取值（Library 的 origin 依据）：只有文件载体有，
	// DB 载体返回空串——DB 化之后"文件来源"不再成立（计划 §9 的风险项，口径在 M6-5/M6-6 重定义）。
	SourceFile(id string) string
	// exists 报告载体里是否已有该定义。它与注册表一起构成"存在"的两个来源：
	// 注册表可能来自代码注入或本进程刚建的，载体可能是手工建的、本进程还没装载的。
	Exists(ctx context.Context, id string) (bool, error)
	// open 读出定义文档并应用 patch（patch 值为 nil = 删键），返回**未提交**的草稿。
	// 载体里还不存在 → existed=false + 一个可写的空文档。
	// rev 是该载体的乐观锁依据（文件载体恒为 0：它的锁在 Save 内部）。
	Open(ctx context.Context, id string, patch map[string]any) (doc *capability.DefinitionFile, rev int64, existed bool, err error)
	// commit 提交草稿。rev/existed 来自 open；rev=0 表示"我以为是新建"。
	// created = 本次真的新建了；conflict = CAS 没中（文件被别人改过 / 行 revision 变了 /
	// 别人抢先把它建了出来）。
	Commit(ctx context.Context, id string, doc *capability.DefinitionFile, rev int64, existed bool) (created, conflict bool, err error)
	// remove 删除定义。existed = 真的删掉了一个；conflict = CAS 没中
	// （别删别人刚改过的那一版）。删除路径没有对外暴露的"版本"，故由载体在删的瞬间自己读。
	Remove(ctx context.Context, id string) (existed, conflict bool, err error)
}

// definitionRepoFor 按持久层模式取载体。返回错误 = 该模式下写面不可用（memory 模式未配置目录）。
func (m *Mechanism) DefinitionRepoFor(sub string) (DefinitionRepo, error) {
	if m.st != nil {
		return &storeDefinitionRepo{st: m.st, sub: sub, actor: defval.DefinitionWriteActor}, nil
	}
	dir, err := m.CapabilityDirFor(sub)
	if err != nil {
		return nil, err
	}
	return &fileDefinitionRepo{dir: dir, sub: sub}, nil
}

// ——— 文件载体（memory 模式，C9） ———

type fileDefinitionRepo struct {
	dir string
	sub string
}

func (r *fileDefinitionRepo) Locator(id string) string {
	path, err := defval.DefinitionPath(r.dir, r.sub, id)
	if err != nil {
		return ""
	}
	return path
}

func (r *fileDefinitionRepo) SourceFile(id string) string { return r.Locator(id) }

// exists 刻意**吞掉 stat 的非"不存在"错误**（权限/IO 一律当"没有"）。
//
// 这是 M6-3 之前写面的既有行为（`_, statErr := os.Stat(path); onDisk := statErr == nil`），
// 本次换载体不动它：真出错时后续的 open/commit 会以更准确的错误报出来，而在这里提前分流
// 会把"文件读不了"变成一条新的失败路径（属于独立的一次行为变更，不该混在载体重构里）。
func (r *fileDefinitionRepo) Exists(_ context.Context, id string) (bool, error) {
	path, err := defval.DefinitionPath(r.dir, r.sub, id)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	return true, nil
}

func (r *fileDefinitionRepo) Open(_ context.Context, id string, patch map[string]any) (*capability.DefinitionFile, int64, bool, error) {
	draft, err := OpenDefinition(r.dir, r.sub, id, patch)
	if err != nil {
		return nil, 0, false, err
	}
	// rev 恒为 0：文件载体的乐观锁是 mtime/size，锁的读与比都在 `DefinitionFile.Save` 内部。
	// 抽象出来的 rev 只对 DB 行有意义（见 storeDefinitionRepo）。
	return draft.df, 0, draft.existed, nil
}

func (r *fileDefinitionRepo) Commit(_ context.Context, _ string, doc *capability.DefinitionFile, _ int64, existed bool) (bool, bool, error) {
	if err := doc.Save(); err != nil {
		return false, capability.IsConflict(err), DefinitionSaveErr(doc.Path(), err)
	}
	return !existed, false, nil
}

func (r *fileDefinitionRepo) Remove(_ context.Context, id string) (bool, bool, error) {
	deleted, err := RemoveDefinitionPermanently(r.dir, r.sub, id)
	if err != nil {
		return false, false, err
	}
	return deleted, false, nil
}

// ——— DB 载体（sqlite 模式：事务 + revision CAS + 审计） ———

type storeDefinitionRepo struct {
	st    *store.Store
	sub   string
	actor string
}

func (r *storeDefinitionRepo) Kind() string {
	if r.sub == defval.CapSubMcps {
		return "mcp"
	}
	return "skill"
}

func (r *storeDefinitionRepo) Locator(id string) string {
	return fmt.Sprintf("db:%s/%s", r.Kind(), id)
}

// sourceFile DB 载体没有文件来源（见接口注释）。
func (r *storeDefinitionRepo) SourceFile(string) string { return "" }

// Read 读一行：返回 (body, revision, found)。
func (r *storeDefinitionRepo) Read(ctx context.Context, id string) (string, int64, bool, error) {
	if r.sub == defval.CapSubMcps {
		row, found, err := r.st.GetMcpServerDefinition(ctx, id)
		if err != nil || !found {
			return "", 0, found, err
		}
		return row.Body, row.Revision, true, nil
	}
	row, found, err := r.st.GetSkillDefinition(ctx, id)
	if err != nil || !found {
		return "", 0, found, err
	}
	return row.Body, row.Revision, true, nil
}

func (r *storeDefinitionRepo) Exists(ctx context.Context, id string) (bool, error) {
	_, _, found, err := r.Read(ctx, id)
	return found, err
}

func (r *storeDefinitionRepo) Open(ctx context.Context, id string, patch map[string]any) (*capability.DefinitionFile, int64, bool, error) {
	locator := r.Locator(id)
	body, rev, found, err := r.Read(ctx, id)
	if err != nil {
		return nil, 0, false, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("读取能力定义 %s 失败：%v", locator, err), err)
	}
	var doc *capability.DefinitionFile
	if !found {
		doc = capability.NewDefinitionDoc(locator)
	} else {
		doc, err = capability.DecodeDefinitionDoc(locator, []byte(body))
		if err != nil {
			// 内容坏掉 → 与文件载体同一条出路：**先修那一行**（40001），
			// 而不是"写入失败，重试"（50000）。混成一个码的话真正该做的事就丢了。
			return nil, 0, false, acl.NewError(acl.ClassRequest, "definition_invalid",
				fmt.Sprintf("能力定义 %s 不是合法 JSON 对象，先修好或删掉它再写：%v", locator, err), err)
		}
	}
	for _, k := range defval.SortedPatchKeys(patch) {
		if patch[k] == nil {
			doc.Delete(k)
			continue
		}
		if err := doc.Set(k, patch[k]); err != nil {
			return nil, 0, false, acl.NewError(acl.ClassService, "definition_write_failed",
				fmt.Sprintf("能力定义 %s 的键 %q 无法序列化：%v", locator, k, err), err)
		}
	}
	return doc, rev, found, nil
}

func (r *storeDefinitionRepo) Commit(ctx context.Context, id string, doc *capability.DefinitionFile, rev int64, _ bool) (bool, bool, error) {
	body, err := doc.Marshal()
	if err != nil {
		return false, false, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("序列化能力定义 %s 失败：%v", r.Locator(id), err), err)
	}
	ok, err := r.Upsert(ctx, id, string(body), rev)
	if err != nil {
		return false, false, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("写入能力定义 %s 失败：%v", r.Locator(id), err), err)
	}
	if !ok {
		return false, true, nil
	}
	// rev=0 即"新建"（CreateCAS 的 OnConflict DoNothing 命中 = 真的插入了）。
	return rev == 0, false, nil
}

func (r *storeDefinitionRepo) Upsert(ctx context.Context, id, body string, rev int64) (bool, error) {
	if r.sub == defval.CapSubMcps {
		return r.st.UpsertMcpServerDefinition(ctx, id, body, rev, r.actor)
	}
	return r.st.UpsertSkillDefinition(ctx, id, body, rev, r.actor)
}

func (r *storeDefinitionRepo) Remove(ctx context.Context, id string) (bool, bool, error) {
	_, rev, found, err := r.Read(ctx, id)
	if err != nil {
		return false, false, acl.NewError(acl.ClassService, "definition_delete_failed",
			fmt.Sprintf("读取能力定义 %s 失败：%v", r.Locator(id), err), err)
	}
	if !found {
		return false, false, nil // 幂等：本来就没有
	}
	var ok bool
	if r.sub == defval.CapSubMcps {
		ok, err = r.st.DeleteMcpServerDefinition(ctx, id, rev, r.actor)
	} else {
		ok, err = r.st.DeleteSkillDefinition(ctx, id, rev, r.actor)
	}
	if err != nil {
		return false, false, acl.NewError(acl.ClassService, "definition_delete_failed",
			fmt.Sprintf("删除能力定义 %s 失败：%v", r.Locator(id), err), err)
	}
	if !ok {
		return false, true, nil
	}
	return true, false, nil
}
