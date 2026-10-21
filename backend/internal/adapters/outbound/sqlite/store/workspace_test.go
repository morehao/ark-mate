package store

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	wkrow "github.com/morehao/ark-mate/internal/domain/workspace/row"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
)

// DefaultTitle 是登记默认工作空间时的语言中立标题（权威定义在领域值包）。
const DefaultTitle = wkrow.DefaultTitle

// 本文件钉住 v14 工作空间登记表的**跨表不变量**（设计 ADR-1/ADR-4/ADR-5/ADR-6）。
//
// 为什么这些必须由 store 层的测试兜住：它们在 manager 的视角下都是"看不见的"——
// 违了也照样能跑一轮会话，只是库慢慢变成自相矛盾的样子（顺序有洞、会话指着不存在的
// 归属、删了登记顺手带走会话）。那种坏法没有任何用户可见的报错。

// TestWorkspaceUniquePath 一个规范化目录至多一条登记；重复登记 = get-or-create 返回既有行。
func TestWorkspaceUniquePath(t *testing.T) {
	s := openTestDB(t)
	dir := t.TempDir()

	first, err := s.CreateWorkspace(ctxT(), dir, "")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	second, err := s.CreateWorkspace(ctxT(), dir, "改了名字也不该新建一行")
	if err != nil {
		t.Fatalf("重复登记应 get-or-create 而不是报错: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("同一目录产生了第二条登记: %s ≠ %s", second.ID, first.ID)
	}

	list, err := s.ListWorkspaces(ctxT())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("登记行数 = %d，期望 1", len(list))
	}
	// get-or-create **不改**既有行的标题：那是"选这个目录"的副作用，不是"改名"。
	if list[0].Title != "" {
		t.Fatalf("重复登记改写了标题: %q", list[0].Title)
	}
}

// TestWorkspaceDefaultBootstrap 默认工作空间：只在空表时创建、幂等、删了不再回来、可关闭。
func TestWorkspaceDefaultBootstrap(t *testing.T) {
	s := openTestDB(t)
	root := t.TempDir()
	dir := filepath.Join(root, "default")

	got, err := s.BootstrapDefaultWorkspace(ctxT(), dir)
	if err != nil {
		t.Fatalf("BootstrapDefaultWorkspace: %v", err)
	}
	if got == nil || !got.IsDefault {
		t.Fatalf("首次 bootstrap 应建出默认工作空间: %+v", got)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("默认工作空间目录应被建立: %v", statErr)
	}
	if got.Title != DefaultTitle {
		t.Fatalf("标题应为语言中立的 %q，实际 %q", DefaultTitle, got.Title)
	}

	// 幂等：再跑一次拿到同一行，不新建目录、不新增行。
	again, err := s.BootstrapDefaultWorkspace(ctxT(), dir)
	if err != nil {
		t.Fatalf("重复 bootstrap: %v", err)
	}
	if again == nil || again.ID != got.ID {
		t.Fatalf("重复 bootstrap 应返回同一行: %+v", again)
	}

	// 代建关闭：不建、不报错、返回 nil（该类部署强制显式 cwd，见 workspace_required）。
	if off, err := s.BootstrapDefaultWorkspace(ctxT(), ""); err != nil || off != nil {
		t.Fatalf("代建关闭时应 (nil, nil)，实际 (%+v, %v)", off, err)
	}
}

// TestWorkspaceDefaultNotRecreatedAfterDelete 删掉默认工作空间之后**不再自动重建**（设计 ADR-4）。
//
// 为什么这条重要：自动重建会让"我删掉它"这个动作永远无效——用户删了、下次启动它又回来，
// 而界面上没有任何东西解释这件事。
func TestWorkspaceDefaultNotRecreatedAfterDelete(t *testing.T) {
	s := openTestDB(t)
	dir := filepath.Join(t.TempDir(), "default")
	def, err := s.BootstrapDefaultWorkspace(ctxT(), dir)
	if err != nil || def == nil {
		t.Fatalf("bootstrap: %+v / %v", def, err)
	}

	// 先登记一个普通工作空间，再直接删默认行（绕过"默认不可删"的守卫）模拟"表里有别的行"。
	if _, err := s.CreateWorkspace(ctxT(), t.TempDir(), "别的项目"); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := s.db.WithContext(ctxT()).Exec(`DELETE FROM workspaces WHERE id = ?`, def.ID).Error; err != nil {
		t.Fatalf("删默认行: %v", err)
	}

	// 表非空 → bootstrap **不**创建（这正是"删了不再回来"的实现点）。
	got, err := s.BootstrapDefaultWorkspace(ctxT(), dir)
	if err != nil {
		t.Fatalf("表非空时 bootstrap 不应报错: %v", err)
	}
	if got != nil {
		t.Fatalf("表非空时不应重建默认工作空间，实际拿到 %+v", got)
	}
}

// TestWorkspaceDeleteKeepsSessions 删登记**不删会话**：目录、cwd、历史一律不动（设计 ADR-6）。
func TestWorkspaceDeleteKeepsSessions(t *testing.T) {
	s := openTestDB(t)
	dir := t.TempDir()
	ws, err := s.CreateWorkspace(ctxT(), dir, "要做掉的项目")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := s.UpsertSession(ctxT(), sessionrow.Item{
		ID: "c1", Cwd: dir, WorkspaceID: ws.ID,
		Status: "in_progress", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	// v3.2 刀 3：判定与顺序已上移领域，这里测的是**三个原语能拼成一个原子写**。
	var released int64
	err = s.Atomically(ctxT(), func(tx *WorkspaceTx) error {
		if _, found, err := tx.WorkspaceRow(ctxT(), ws.ID); err != nil || !found {
			return err
		}
		n, err := tx.ReleaseSessions(ctxT(), ws.ID)
		if err != nil {
			return err
		}
		released = n
		_, err = tx.DeleteRow(ctxT(), ws.ID)
		return err
	})
	if err != nil {
		t.Fatalf("Atomically: %v", err)
	}
	if released != 1 {
		t.Fatalf("被释放的会话数 = %d，期望 1", released)
	}

	// 登记没了。
	if _, ok, err := s.GetWorkspace(ctxT(), ws.ID); err != nil || ok {
		t.Fatalf("登记应已删除: ok=%v err=%v", ok, err)
	}
	// 会话还在，cwd 逐字不变，归属清空（读面归默认工作空间）。
	conv, ok, err := s.GetSession(ctxT(), "c1")
	if err != nil || !ok {
		t.Fatalf("会话不该被删: ok=%v err=%v", ok, err)
	}
	if conv.Cwd != dir {
		t.Fatalf("会话 cwd 被改动了: %q ≠ %q", conv.Cwd, dir)
	}
	if conv.WorkspaceID != "" {
		t.Fatalf("归属应被清空，实际 %q", conv.WorkspaceID)
	}
	// 目录本身还在（删除用户数据不是删登记该做的事）。
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("目录不该被删: %v", statErr)
	}
}

// TestWorkspaceMoveOrder 重排后顺序必须是 0..n-1 的一个排列，且返回完整顺序。
func TestWorkspaceMoveOrder(t *testing.T) {
	s := openTestDB(t)
	ids := make([]string, 0, 3)
	for i, name := range []string{"a", "b", "c"} {
		ws, err := s.CreateWorkspace(ctxT(), filepath.Join(t.TempDir(), name), name)
		if err != nil {
			t.Fatalf("CreateWorkspace %s: %v", name, err)
		}
		ids = append(ids, ws.ID)
		if ws.ListOrder != int64(i) {
			t.Fatalf("新登记的 list_order = %d，期望 %d（追加到尾部）", ws.ListOrder, i)
		}
	}

	// 把 c 移到 a 之前 → c, a, b
	seq, err := s.MoveWorkspace(ctxT(), ids[2], ids[0])
	if err != nil {
		t.Fatalf("MoveWorkspace: %v", err)
	}
	want := []string{ids[2], ids[0], ids[1]}
	if len(seq) != len(want) {
		t.Fatalf("返回顺序长度 %d ≠ %d", len(seq), len(want))
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("返回顺序 = %v，期望 %v", seq, want)
		}
	}

	// 库里的顺序与 list_order 必须与返回值逐位一致。
	list, err := s.ListWorkspaces(ctxT())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	for i, row := range list {
		if row.ID != want[i] || row.ListOrder != int64(i) {
			t.Fatalf("位置 %d: id=%s order=%d，期望 id=%s order=%d", i, row.ID, row.ListOrder, want[i], i)
		}
	}
}

// TestWorkspaceMoveAppendAndNoop 无锚点 = 追加到尾部；移到"自己前面"是空操作。
func TestWorkspaceMoveAppendAndNoop(t *testing.T) {
	s := openTestDB(t)
	a, _ := s.CreateWorkspace(ctxT(), filepath.Join(t.TempDir(), "a"), "a")
	b, _ := s.CreateWorkspace(ctxT(), filepath.Join(t.TempDir(), "b"), "b")

	seq, err := s.MoveWorkspace(ctxT(), a.ID, "") // a 追加到尾部 → b, a
	if err != nil {
		t.Fatalf("MoveWorkspace 追加: %v", err)
	}
	if len(seq) != 2 || seq[0] != b.ID || seq[1] != a.ID {
		t.Fatalf("追加后的顺序 = %v，期望 [%s %s]", seq, b.ID, a.ID)
	}

	// 移到自己前面 → 顺序不变（不写库，也不报错）。
	seq, err = s.MoveWorkspace(ctxT(), a.ID, a.ID)
	if err != nil {
		t.Fatalf("MoveWorkspace 自身锚点: %v", err)
	}
	if len(seq) != 2 || seq[0] != b.ID || seq[1] != a.ID {
		t.Fatalf("自身锚点后顺序 = %v，期望不变", seq)
	}
}

// TestWorkspaceMoveUnknown 目标或锚点不存在 → 稳定信号（不静默变成空操作）。
func TestWorkspaceMoveUnknown(t *testing.T) {
	s := openTestDB(t)
	a, _ := s.CreateWorkspace(ctxT(), filepath.Join(t.TempDir(), "a"), "a")

	if _, err := s.MoveWorkspace(ctxT(), "wsp-nope", ""); err != ErrWorkspaceNotFound {
		t.Fatalf("未知目标应报 ErrWorkspaceNotFound，实际 %v", err)
	}
	if _, err := s.MoveWorkspace(ctxT(), a.ID, "wsp-nope"); err != ErrWorkspaceNotFound {
		t.Fatalf("未知锚点应报 ErrWorkspaceNotFound，实际 %v", err)
	}
}

// TestWorkspaceSessionCounts 计数与最近使用按归属聚合（含 workspace_id=” 的桶）。
func TestWorkspaceSessionCounts(t *testing.T) {
	s := openTestDB(t)
	ws, _ := s.CreateWorkspace(ctxT(), t.TempDir(), "p")
	now := time.Now().UTC()
	mk := func(id, workspaceID string, updated time.Time) {
		if err := s.UpsertSession(ctxT(), sessionrow.Item{
			ID: id, Cwd: "/tmp/x", WorkspaceID: workspaceID,
			Status: "in_progress", CreatedAt: now, UpdatedAt: updated,
		}); err != nil {
			t.Fatalf("UpsertSession %s: %v", id, err)
		}
	}
	mk("c1", ws.ID, now.Add(-time.Hour))
	mk("c2", ws.ID, now)
	mk("legacy", "", now.Add(-2*time.Hour)) // 历史行：归默认工作空间（读面解析）

	counts, err := s.CountWorkspacesByID(ctxT())
	if err != nil {
		t.Fatalf("CountWorkspacesByID: %v", err)
	}
	if counts[ws.ID] != 2 {
		t.Fatalf("该工作空间会话数 = %d，期望 2", counts[ws.ID])
	}
	if counts[""] != 1 {
		t.Fatalf("空归属桶 = %d，期望 1（历史会话）", counts[""])
	}

	last, err := s.LastUsedByWorkspaceID(ctxT())
	if err != nil {
		t.Fatalf("LastUsedByWorkspaceID: %v", err)
	}
	if last[ws.ID] == "" {
		t.Fatal("最近使用不该为空")
	}
	if last[""] == "" {
		t.Fatal("空归属桶的最近使用不该为空")
	}
}

// TestMigrateV14LeavesExistingSessionsUnassigned v14 把既有会话的归属留空（设计 §3.2）。
//
// 这条是"存量会话不会掉进未分组"的机器判据：留空 = 读面归默认工作空间。
// 刻意**不**把 DISTINCT cwd 批量登记成工作空间（那会产生用户从未选过的条目）。
func TestMigrateV14LeavesExistingSessionsUnassigned(t *testing.T) {
	s := openTestDB(t)
	// 造一条 cwd 很明确、看起来"该被登记"的会话，确认迁移不拿它建登记。
	dir := t.TempDir()
	if err := s.UpsertSession(ctxT(), sessionrow.Item{
		ID: "legacy", Cwd: dir, WorkspaceTemporary: true,
		Status: "in_progress", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	list, err := s.ListWorkspaces(ctxT())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("迁移不该凭空产生登记行，实际 %d 行", len(list))
	}
	conv, _, _ := s.GetSession(ctxT(), "legacy")
	if conv.WorkspaceID != "" {
		t.Fatalf("既有会话的归属应留空，实际 %q", conv.WorkspaceID)
	}
	if conv.Cwd != dir {
		t.Fatalf("既有会话的 cwd 被改动了: %q ≠ %q", conv.Cwd, dir)
	}
}

// TestMigrateV14RecordsNotes 迁移留一条可读事实（不承载错误，供 doctor 读）。
func TestMigrateV14RecordsNotes(t *testing.T) {
	s := openTestDB(t)
	v, ok, err := s.meta.Get(context.Background(), "migration_notes_v14")
	if err != nil || !ok {
		t.Fatalf("v14 迁移说明应落 meta: ok=%v err=%v", ok, err)
	}
	if v == "" {
		t.Fatal("v14 迁移说明不该为空串")
	}
	if !strings.Contains(v, `"workspaces_created":0`) {
		t.Fatalf("迁移说明应如实登记 workspaces_created=0: %s", v)
	}
}

// TestWorkspaceUniqueIndexEnforced 库级唯一索引真的生效，且 `isUniqueViolation` 认得出来。
//
// 前半（索引存在）挡住"唯一约束被写进注释/被后来某次建表迁移丢掉"；
// 后半（认得出来）挡住 get-or-create 在并发下**静默退化成报错**——那条路没有并发时看不出问题，
// 而它坏了的表现是"偶尔创建失败一次，再点一下就好"，最难被发现的一类。
func TestWorkspaceUniqueIndexEnforced(t *testing.T) {
	s := openTestDB(t)
	dir := t.TempDir()
	if _, err := s.CreateWorkspace(ctxT(), dir, ""); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// 绕过 get-or-create 的预检查，直接插一条同路径的行：必须被唯一索引拦下。
	dup := model.WorkspaceEntity{
		ID: "wsp-dup", Path: dir, Title: "dup", ListOrder: 99,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	err := s.workspaces.Insert(ctxT(), &dup)
	if err == nil {
		t.Fatal("同路径的第二次插入必须被唯一索引拦下（否则会出现两条同目录登记）")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("isUniqueViolation 认不出唯一索引冲突错误：%v\n"+
			"（认不出 = 并发创建时输家会把这行错误抛给用户，而不是重读既有行）", err)
	}

	// 默认工作空间的部分唯一索引：第二条 is_default=1 也必须被拦下。
	def := model.WorkspaceEntity{
		ID: "wsp-d1", Path: t.TempDir(), Title: DefaultTitle, IsDefault: true, ListOrder: 100,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.workspaces.Insert(ctxT(), &def); err != nil {
		t.Fatalf("第一条默认工作空间应能插入: %v", err)
	}
	def2 := def
	def2.ID = "wsp-d2"
	def2.Path = t.TempDir()
	if err := s.workspaces.Insert(ctxT(), &def2); err == nil {
		t.Fatal("第二条 is_default=1 必须被部分唯一索引拦下（否则默认工作空间会有两个，落点变得不确定）")
	}
}

// TestWorkspaceRelocate 改登记路径（W4）：**id 不变、会话不动、冲突显式报**。
//
// 三条都容易被"顺手写得更方便"：
//
//	① 改路径时顺手把会话的 `cwd` 也刷一遍——看起来贴心，实际上是把历史指到别处
//	   （`cwd` 是执行事实，transcript 就在那个目录里）；
//	② 目标目录已被另一条登记占着时，get-or-create 返回那条既有行——两个入口给出同一个"成功"，
//	   而用户改的那个工作空间还在老地方；
//	③ 顺手重新生成 id——那会让该工作空间下的会话归属悬空。
func TestWorkspaceRelocate(t *testing.T) {
	s := openTestDB(t)
	a := t.TempDir()
	b := t.TempDir()

	rec, err := s.CreateWorkspace(ctxT(), a, "A")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	// 种一行指向 rec 的会话（本用例要的只是"有一条会话归属它"）。
	if err := s.DB().Exec(`INSERT INTO sessions (id, title, cwd, workspace_id, status, capability_json, model_id, created_at, updated_at)
		VALUES ('ses-1', '', ?, ?, 'idle', '', 'mdl_x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, rec.Path, rec.ID).Error; err != nil {
		t.Fatalf("种会话行: %v", err)
	}

	// ① 正常改：id 不变、path 变。
	ok, err := s.RelocateWorkspace(ctxT(), rec.ID, b)
	if err != nil || !ok {
		t.Fatalf("RelocateWorkspace: ok=%v err=%v", ok, err)
	}
	after, ok, err := s.GetWorkspace(ctxT(), rec.ID)
	if err != nil || !ok {
		t.Fatalf("改路径后应仍能按**原 id** 取到: ok=%v err=%v", ok, err)
	}
	if after.Path != b {
		t.Fatalf("路径应改成 %q，got %q", b, after.Path)
	}

	// ② 会话一个字节都不动（`cwd` 与归属都还是旧值）。
	var cwd, workspaceID string
	if err := s.DB().Raw(`SELECT cwd, workspace_id FROM sessions WHERE id = 'ses-1'`).Row().Scan(&cwd, &workspaceID); err != nil {
		t.Fatalf("读会话行: %v", err)
	}
	if cwd != rec.Path || workspaceID != rec.ID {
		t.Fatalf("改登记路径**不许**动会话：cwd %q（want %q）/ workspace_id %q（want %q）",
			cwd, rec.Path, workspaceID, rec.ID)
	}

	// ③ 冲突显式报（不 get-or-create）。
	other := t.TempDir()
	if _, err := s.CreateWorkspace(ctxT(), other, "B"); err != nil {
		t.Fatalf("CreateWorkspace(B): %v", err)
	}
	if _, err := s.RelocateWorkspace(ctxT(), rec.ID, other); err != ErrWorkspacePathTaken {
		t.Fatalf("改到已被占用的目录应报 ErrWorkspacePathTaken，got %v", err)
	}
	// 冲突之后那行**还是老路径**（失败要能回滚到"什么都没发生"）。
	again, _, _ := s.GetWorkspace(ctxT(), rec.ID)
	if again.Path != b {
		t.Fatalf("冲突失败后路径不应变化，got %q", again.Path)
	}
}
