package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/object/objid"
)

// 本文件是**工作空间登记表**（v14）的领域层测试（manager/workspace_registry.go）。
//
// 与 `internal/store/workspace_test.go` 的分工：那边钉住跨表不变量（唯一索引、顺序对账、
// 删登记不删会话），这边钉住领域语义——**校验与建会话同源**、**默认工作空间是历史会话的落点**、
// **memory 是明确的不可写而不是内部错误**。后者在 store 层测不了（那里没有持久层模式的差别）。

// registryRig 造一个带库的 Service，并装配工作空间策略（代建根 + 可选白名单）。
func registryRig(t *testing.T, roots []string) (*rig, string) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) { c.store = true })
	provision := filepath.Join(t.TempDir(), "workspaces")
	mustWorkspacePolicy(t, r.Svc, provision, roots)
	return r, provision
}

// TestWorkspaceRegistryDefaultIsProvisioned 默认工作空间由装配期建立，落在 <provision>/default。
func TestWorkspaceRegistryDefaultIsProvisioned(t *testing.T) {
	r, provision := registryRig(t, nil)

	if err := r.Svc.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}
	def, ok, err := r.Svc.DefaultWorkspace()
	if err != nil || !ok {
		t.Fatalf("装配后应有默认工作空间: ok=%v err=%v", ok, err)
	}
	want := filepath.Join(provision, "default")
	if real, rerr := filepath.EvalSymlinks(want); rerr == nil {
		want = real
	}
	if def.Path != want {
		t.Fatalf("默认工作空间路径 = %q，期望 %q", def.Path, want)
	}
	if !def.IsDefault {
		t.Fatal("装配期建立的那条必须带 is_default")
	}
	if def.Title != store.DefaultWorkspaceTitle {
		t.Fatalf("标题应为语言中立的 %q，实际 %q", store.DefaultWorkspaceTitle, def.Title)
	}
	if _, serr := os.Stat(def.Path); serr != nil {
		t.Fatalf("目录应被建立: %v", serr)
	}

	// 幂等：再调一次不出第二行。
	if err := r.Svc.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("重复 EnsureDefaultWorkspace: %v", err)
	}
	list, err := r.Svc.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("重复装配不该新增登记行: %+v", list.Items)
	}
	if list.DefaultID != def.ID || !list.Writable {
		t.Fatalf("读面应标出默认工作空间与可写: %+v", list)
	}
}

// TestWorkspaceRegistryNoProvisionNoDefault 代建关闭（workspace_dir 为空）时不建立默认工作空间。
//
// 这不是"少做一件事"：该类部署强制显式 cwd，凭空建一个目录等于把平台自己的目录
// 塞成用户的默认工作目录——而运维明确说了不要代建。
func TestWorkspaceRegistryNoProvisionNoDefault(t *testing.T) {
	r := newRig(t, func(c *rigConfig) { c.store = true })
	mustWorkspacePolicy(t, r.Svc, "", nil)

	if err := r.Svc.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("代建关闭时 EnsureDefaultWorkspace 应静默通过: %v", err)
	}
	if _, ok, _ := r.Svc.DefaultWorkspace(); ok {
		t.Fatal("代建关闭时不该有默认工作空间")
	}
	list, err := r.Svc.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	if len(list.Items) != 0 || list.DefaultID != "" {
		t.Fatalf("代建关闭时应是空登记表: %+v", list)
	}
	// 可写仍为 true：登记表本身是可用的（用户仍能手选目录登记），只是没有默认落点。
	if !list.Writable {
		t.Fatal("代建关闭不等于登记表不可写")
	}
}

// TestWorkspaceRegistryCreateValidatesLikeSession 「能登记」与「能建会话」必须同源。
//
// 这条是设计里最要紧的一致性之一：登记时放过一个目录、建会话时被拒，就是一条死路
// ——用户在界面上选了它，然后得到一个必然失败的会话。
func TestWorkspaceRegistryCreateValidatesLikeSession(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	r, _ := registryRig(t, []string{allowed})

	// 白名单内：能登记。
	resp, err := r.Svc.CreateRegisteredWorkspace(allowed, "", false)
	if err != nil {
		t.Fatalf("白名单内应能登记: %v", err)
	}
	if resp.Path == "" || resp.DirStatus != "ok" {
		t.Fatalf("登记结果应带规范化路径与 ok 状态: %+v", resp)
	}

	// 名录外：**照常登记**（v14.5：名录已降级为浏览起点偏好，不再拒绝任何可用目录）。
	outsideResp, err := r.Svc.CreateRegisteredWorkspace(outside, "", false)
	if err != nil {
		t.Fatalf("名录外的可用目录应能登记（选工作区是全系统）: %v", err)
	}
	if outsideResp.DirStatus != "ok" {
		t.Fatalf("登记结果状态应为 ok: %+v", outsideResp)
	}

	// 相对路径 / 不存在 / 不是目录：三条都拒（与 ValidateWorkspace 同一套）。
	for _, bad := range []string{"relative/path", filepath.Join(allowed, "nope"), writeFile(t, allowed)} {
		if _, err := r.Svc.CreateRegisteredWorkspace(bad, "", false); err == nil {
			t.Fatalf("非法路径 %q 必须拒绝", bad)
		}
	}
}

// TestWorkspaceRegistryGetOrCreateAndStrict 默认 get-or-create；strict=true 时重复报错。
func TestWorkspaceRegistryGetOrCreateAndStrict(t *testing.T) {
	dir := t.TempDir()
	r, _ := registryRig(t, nil)
	mustWorkspacePolicy(t, r.Svc, "", nil)

	first, err := r.Svc.CreateRegisteredWorkspace(dir, "一号", false)
	if err != nil {
		t.Fatalf("首次登记: %v", err)
	}
	again, err := r.Svc.CreateRegisteredWorkspace(dir, "改了名也不该新建", false)
	if err != nil {
		t.Fatalf("重复登记应 get-or-create: %v", err)
	}
	if again.ID != first.ID || again.Title != "一号" {
		t.Fatalf("get-or-create 应返回既有行且不改标题: %+v", again)
	}
	if _, err := r.Svc.CreateRegisteredWorkspace(dir, "", true); err == nil {
		t.Fatal("strict=true 时重复登记必须报错")
	} else if c := gerrCode(t, err); c != code.Conflict {
		t.Fatalf("重复登记（strict）应为 40901，实际 %d", c)
	}
}

// TestWorkspaceRegistryListAggregatesLegacyIntoDefault 历史会话（workspace_id=”）归默认工作空间那一行。
//
// 为什么这条必须由领域层保证：`workspace_id=”` 是 v14 迁移留给存量会话的形态。若读面不把它
// 并到默认那一行，界面会显示"默认工作空间 0 个会话"而点进去有会话——那种不一致没有任何解释余地。
func TestWorkspaceRegistryListAggregatesLegacyIntoDefault(t *testing.T) {
	r, _ := registryRig(t, nil)
	if err := r.Svc.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}
	def, _, _ := r.Svc.DefaultWorkspace()

	// 两条历史会话（workspace_id 留空）+ 一条显式归属普通工作空间。
	userDir := t.TempDir()
	ws, err := r.Svc.CreateRegisteredWorkspace(userDir, "项目", false)
	if err != nil {
		t.Fatalf("登记项目: %v", err)
	}
	seedStoreSession(t, r.St, "legacy-1", def.Path, "")
	seedStoreSession(t, r.St, "legacy-2", def.Path, "")
	seedStoreSession(t, r.St, "member", userDir, ws.ID)

	list, err := r.Svc.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	byID := map[string]int{}
	for _, it := range list.Items {
		byID[it.ID] = it.Sessions
	}
	if byID[def.ID] != 2 {
		t.Fatalf("默认工作空间应含 2 条历史会话，实际 %d（整表 %+v）", byID[def.ID], list.Items)
	}
	if byID[ws.ID] != 1 {
		t.Fatalf("项目工作空间应含 1 条，实际 %d", byID[ws.ID])
	}
}

// TestWorkspaceRegistryMissingDirStillListed 目录消失后登记**仍然列出**，只是不给 ok 状态。
//
// 登记不会因为目录消失就凭空不见：用户得能看见它并清理（或把目录找回来）。
// 若读面直接把它藏掉，"我以为登记丢了、又重新登记一次"就成了一条没人能解释的路。
func TestWorkspaceRegistryMissingDirStillListed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "will-vanish")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	r, _ := registryRig(t, nil)
	mustWorkspacePolicy(t, r.Svc, "", nil)
	ws, err := r.Svc.CreateRegisteredWorkspace(dir, "会被删掉", false)
	if err != nil {
		t.Fatalf("登记: %v", err)
	}
	// 外部进程把目录删掉（模拟用户在 Finder 里删了它）。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("删目录: %v", err)
	}

	list, err := r.Svc.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	var found *struct{ status string }
	for i := range list.Items {
		if list.Items[i].ID == ws.ID {
			found = &struct{ status string }{list.Items[i].DirStatus}
		}
	}
	if found == nil {
		t.Fatal("目录消失后登记仍应列出（否则用户无法清理它）")
	}
	if found.status != "missing" {
		t.Fatalf("目录消失后 dir_status 应为 missing，实际 %q", found.status)
	}
}

// TestWorkspaceRegistryDeleteDefaultRefusedAndUnknown 默认不可删；未知 id → 40401。
func TestWorkspaceRegistryDeleteDefaultRefusedAndUnknown(t *testing.T) {
	r, _ := registryRig(t, nil)
	if err := r.Svc.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}
	def, _, _ := r.Svc.DefaultWorkspace()

	_, err := r.Svc.DeleteRegisteredWorkspace(def.ID)
	if err == nil {
		t.Fatal("默认工作空间必须拒绝删除")
	}
	if c := gerrCode(t, err); c != code.InvalidRequest {
		t.Fatalf("默认不可删应为 40001，实际 %d", c)
	}

	_, err = r.Svc.DeleteRegisteredWorkspace("wsp-nope")
	if c := gerrCode(t, err); c != code.NotFound {
		t.Fatalf("未知 id 应为 40401，实际 %d", c)
	}

	// 普通工作空间可删，且响应带回被释放的会话数（界面那句话要有事实支撑）。
	dir := t.TempDir()
	ws, err := r.Svc.CreateRegisteredWorkspace(dir, "", false)
	if err != nil {
		t.Fatalf("登记: %v", err)
	}
	seedStoreSession(t, r.St, "c-del", dir, ws.ID)
	del, err := r.Svc.DeleteRegisteredWorkspace(ws.ID)
	if err != nil {
		t.Fatalf("删登记: %v", err)
	}
	if del.ReleasedSessions != 1 {
		t.Fatalf("released_sessions = %d，期望 1", del.ReleasedSessions)
	}
}

// TestWorkspaceRegistryMemoryIsExplicitlyUnsupported memory 持久层：读面空 + 不可写，写面 40501。
//
// 关键在于**明确**：memory 没有 workspaces 表是形态的确定限制（与 memory 下 /usage/summary
// 40501 完全同形）。若这里回一个 50000，"平台坏了"与"换个持久层就好了"就分不开。
// 口径（2026-10-11 写精确）：本用例验的是**代建关闭**（`workspace_dir` 为空）+ memory 模式
// ——那时确实没有默认工作空间可列。而"memory + 有代建根"是**列出默认那一条**：
// 默认工作空间是平台代建的东西，不是用户登记项，不该因为"登记表没处存"而消失
// （见 TestWorkspaceRegistryDefaultIsProvisionedInMemory）。
func TestWorkspaceRegistryMemoryIsExplicitlyUnsupported(t *testing.T) {
	r := newRig(t) // 无库 = memory
	mustWorkspacePolicy(t, r.Svc, "", nil)

	list, err := r.Svc.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("memory 下读面不该报错: %v", err)
	}
	if len(list.Items) != 0 || list.Writable {
		t.Fatalf("memory 下应是空列表 + 不可写: %+v", list)
	}

	if _, err := r.Svc.CreateRegisteredWorkspace(t.TempDir(), "", false); err == nil {
		t.Fatal("memory 下创建必须被拒")
	} else if c := gerrCode(t, err); c != code.NotSupported {
		t.Fatalf("memory 下创建应为 40501，实际 %d", c)
	}
	if _, err := r.Svc.MoveRegisteredWorkspace("any", ""); err == nil {
		t.Fatal("memory 下重排必须被拒")
	} else if c := gerrCode(t, err); c != code.NotSupported {
		t.Fatalf("memory 下重排应为 40501，实际 %d", c)
	}
}

// TestWorkspaceRegistryRenameKeepsIdentity 改名不改身份（id / path 不变）。
func TestWorkspaceRegistryRenameKeepsIdentity(t *testing.T) {
	dir := t.TempDir()
	r, _ := registryRig(t, nil)
	mustWorkspacePolicy(t, r.Svc, "", nil)
	ws, err := r.Svc.CreateRegisteredWorkspace(dir, "旧名", false)
	if err != nil {
		t.Fatalf("登记: %v", err)
	}
	renamed, err := r.Svc.RenameRegisteredWorkspace(ws.ID, "新名")
	if err != nil {
		t.Fatalf("改名: %v", err)
	}
	if renamed.ID != ws.ID || renamed.Path != ws.Path || renamed.Title != "新名" {
		t.Fatalf("改名不该动身份: 旧 %+v 新 %+v", ws, renamed)
	}
	// 清空标题 = 未命名（界面按末级目录名渲染），不是错误。
	cleared, err := r.Svc.RenameRegisteredWorkspace(ws.ID, "")
	if err != nil {
		t.Fatalf("清空标题: %v", err)
	}
	if cleared.Title != "" {
		t.Fatalf("标题应被清空，实际 %q", cleared.Title)
	}
	// 未知 id → 40401。
	if _, err := r.Svc.RenameRegisteredWorkspace("wsp-nope", "x"); gerrCode(t, err) != code.NotFound {
		t.Fatalf("未知 id 改名应为 40401，实际 %d", gerrCode(t, err))
	}
}

// seedStoreSession 往库里写一条会话行（**只写库**，不进内存镜像）。
//
// 读面（WorkspaceRegistry）的计数取的是库里的聚合，所以夹具只需要库里有行——走
// registerSession 反而会带上编排器状态，而这些用例关心的是归属的聚合口径。
func seedStoreSession(t *testing.T, st *store.Store, id, cwd, workspaceID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.UpsertSession(context.Background(), model.SessionEntity{
		ID: id, Cwd: cwd, WorkspaceID: workspaceID,
		Status: SessionStatusCompleted, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写会话行 %s: %v", id, err)
	}
}

// writeFile 在 dir 下建一个文件并返回它的路径（"不是目录"的用例）。
func writeFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "not-a-dir.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	return p
}

// TestWorkspaceRegistryDefaultIsProvisionedInMemory memory 模式**也要**有默认工作空间。
//
// 2026-10-11 修：`EnsureDefaultWorkspace` 原先在 `s.st == nil` 时直接 return，于是
// `MATE_STORE=memory`（以及 auto 在"还没有 mate.db"时的首次运行）下
// `POST /sessions {"prompt":"hi"}`（默认工作空间不存在时）报
// `40001 未指定工作空间，且没有可用的默认工作空间…请显式传入 cwd`——**最普通的一条路走不通**，
// 而用户什么都没做错。默认工作空间是"未指定 cwd 时落在哪"的唯一落点，与"用户登记表能不能持久化"
// 是两件事，不该被同一个 `s.st == nil` 判掉。
func TestWorkspaceRegistryDefaultIsProvisionedInMemory(t *testing.T) {
	r := newRig(t, func(c *rigConfig) { c.store = false }) // 无库 = memory 模式
	provision := filepath.Join(t.TempDir(), "workspaces")
	mustWorkspacePolicy(t, r.Svc, provision, nil)

	if err := r.Svc.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("memory 模式下 EnsureDefaultWorkspace 应建出默认工作空间: %v", err)
	}
	def, ok, err := r.Svc.DefaultWorkspace()
	if err != nil || !ok {
		t.Fatalf("memory 模式下装配后应有默认工作空间: ok=%v err=%v", ok, err)
	}
	want := filepath.Join(provision, "default")
	if real, rerr := filepath.EvalSymlinks(want); rerr == nil {
		want = real
	}
	if def.Path != want {
		t.Fatalf("默认工作空间路径 = %q，期望 %q", def.Path, want)
	}
	if !def.IsDefault || def.Title != store.DefaultWorkspaceTitle {
		t.Fatalf("memory 模式的默认项形状应与 sqlite 侧逐字一致: %+v", def)
	}
	if !strings.HasPrefix(def.ID, objid.Workspace) || !objid.IsPlatformID(def.ID) {
		t.Fatalf("memory 模式的默认工作空间 id 也要是平台铸造形状（%s_<ULID>），实际 %q", objid.Workspace, def.ID)
	}
	if _, serr := os.Stat(def.Path); serr != nil {
		t.Fatalf("目录应被建立: %v", serr)
	}

	// 读面：**要列出它**（否则左栏少一个分组、未指定 cwd 建的会话无处可挂），
	// 但 `writable` 仍是 false（用户登记表在 memory 模式下无处可存，设计 ADR-11）。
	list, err := r.Svc.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	if len(list.Items) != 1 || list.DefaultID != def.ID {
		t.Fatalf("memory 模式应列出那一条默认工作空间: %+v", list)
	}
	if list.Writable {
		t.Fatal("memory 模式的登记表不可写（writable=false）——那是 ADR-11 的口径，不该被这条改动带偏")
	}
	if list.Items[0].DirStatus != "ok" {
		t.Fatalf("刚建出来的目录状态应为 ok: %+v", list.Items[0])
	}

	// 归属解析：建会话未指定 cwd 时，cwd 与 workspace_id 都落在这一条上。
	target, assignment, err := r.Svc.resolveTargetWorkspace("")
	if err != nil || target == nil {
		t.Fatalf("未指定 cwd 时应解析到默认工作空间: target=%+v err=%v", target, err)
	}
	if target.ID != def.ID || target.Path != def.Path || assignment != "" {
		t.Fatalf("解析目标应指向默认工作空间且不额外指定归属: target=%+v assignment=%q", target, assignment)
	}
	res, err := ResolveWorkspace("", target)
	if err != nil {
		t.Fatalf("ResolveWorkspace(空 spec, 默认工作空间) 应成功（这就是那条 40001 的判据）: %v", err)
	}
	if !res.Temporary || res.WorkspaceID != def.ID {
		t.Fatalf("未指定 cwd 得到的应是\"平台代建\"归属: %+v", res)
	}

	// 展示面：归属 id → 展示名（库里存的是语言中立的 default-workspace）。
	if id, title := r.Svc.workspaceDisplayOf(def.ID); id != def.ID || title != "默认工作空间" {
		t.Fatalf("默认工作空间的展示名应是「默认工作空间」: id=%q title=%q", id, title)
	}
}

// TestWorkspaceRegistryMemoryNoProvisionNoDefault 代建显式关闭时，memory 模式同样不建
// （配置意图优先于持久层模式：说了不要代建就是不要）。
func TestWorkspaceRegistryMemoryNoProvisionNoDefault(t *testing.T) {
	r := newRig(t, func(c *rigConfig) { c.store = false })
	mustWorkspacePolicy(t, r.Svc, "", nil)

	if err := r.Svc.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("代建关闭时 EnsureDefaultWorkspace 应静默通过: %v", err)
	}
	if _, ok, _ := r.Svc.DefaultWorkspace(); ok {
		t.Fatal("代建关闭时不该有默认工作空间")
	}
	list, err := r.Svc.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	if len(list.Items) != 0 || list.DefaultID != "" {
		t.Fatalf("代建关闭时应是空登记表: %+v", list)
	}
}
