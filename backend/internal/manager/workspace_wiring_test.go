package manager

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/code"
)

// 本文件钉住 M3 的接线（设计 ADR-2/ADR-4/ADR-6/ADR-9）：会话到底跑在哪个目录、归属写没写进库、
// 以及"未指定 cwd"这条路的落点。这些用例走的是**真实的建会话路径**（CreateSession →
// stub runtime 起会话 → 落库），因为它们要验的正是"两处来源会不会分叉"——只测纯函数
// 证明不了接线。

// convRig 是"能真正建起会话"的夹具：stub ACP runtime（有可执行的启动命令）+ 库 + 默认工作空间。
//
// 复用 `validateRig`（它已经装好 stub ACP spec、agent `a1`、能力面与编排器）而不是自己拼：
// 拼出来的最小夹具缺 spec 的启动命令，`session/new` 会以 50201 失败——那正是"夹具与生产线
// 不是同一条路"的典型症状。库与工作空间策略在本用例组里补上。
type convRig struct {
	svc  *Service
	orch *Orchestrator
	st   *store.Store
}

func newConvRig(t *testing.T) *convRig {
	t.Helper()
	svc, orch, _ := validateRig(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	svc.EnableStore(st)
	mustWorkspacePolicy(t, svc, filepath.Join(t.TempDir(), "workspaces"), nil)
	if err := svc.WorkspacePlane().EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}
	return &convRig{svc: svc, orch: orch, st: st}
}

// TestCreateSessionUnspecifiedUsesDefaultWorkspace 未指定 cwd → 落在默认工作空间。
//
// 这条是设计 ADR-4 的核心断言，同时钉住它的两个可观测后果：
//   - 响应带 `workspace_temporary=true`（"这个目录不是你选的"）；
//   - 归属写进了库（`workspace_id` = 默认那一条），而不是留空——留空要靠读面兜，
//     而读面兜不住"用户取消默认登记"之后的形态。
func TestCreateSessionUnspecifiedUsesDefaultWorkspace(t *testing.T) {
	r := newConvRig(t)
	def, ok, err := r.svc.WorkspacePlane().DefaultWorkspace()
	if err != nil || !ok {
		t.Fatalf("默认工作空间: ok=%v err=%v", ok, err)
	}

	res, err := r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !res.WorkspaceTemporary {
		t.Fatal("未指定 cwd 应标 workspace_temporary=true（= 平台给的默认工作空间）")
	}
	if res.WorkspaceID != def.ID {
		t.Fatalf("响应归属 = %q，期望默认工作空间 %q", res.WorkspaceID, def.ID)
	}
	if res.Cwd != def.Path {
		t.Fatalf("响应 cwd = %q，期望默认工作空间路径 %q", res.Cwd, def.Path)
	}

	// 库里必须带同一个归属（重启后不靠"读面兜"）。
	row, ok, err := r.st.GetSession(context.Background(), res.ID)
	if err != nil || !ok {
		t.Fatalf("读库: ok=%v err=%v", ok, err)
	}
	if row.WorkspaceID != def.ID {
		t.Fatalf("库里 workspace_id = %q，期望 %q", row.WorkspaceID, def.ID)
	}
	if row.Cwd != def.Path {
		t.Fatalf("库里 cwd = %q，期望 %q", row.Cwd, def.Path)
	}
}

// TestCreateSessionExplicitCwdKeepsEmptyAssignment 显式 cwd **不自动登记**。
//
// 设计 §3.2 的处置：不把用户没主动选过的目录批量登记成工作空间。归属留空 = 读面归默认工作空间，
// 所以显式 cwd 的会话照样有分组，只是不会在左栏凭空多出一个条目。
func TestCreateSessionExplicitCwdKeepsEmptyAssignment(t *testing.T) {
	r := newConvRig(t)
	dir := t.TempDir()

	res, err := r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{Cwd: dir})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if res.WorkspaceTemporary {
		t.Fatal("显式 cwd 不该标 temporary")
	}
	if res.WorkspaceID != "" {
		t.Fatalf("显式 cwd 未登记时归属应留空（读面归默认），实际 %q", res.WorkspaceID)
	}
	row, _, _ := r.st.GetSession(context.Background(), res.ID)
	if row.WorkspaceID != "" {
		t.Fatalf("库里也不该替它登记: %q", row.WorkspaceID)
	}
	// 且**没有**因此产生登记行：只有默认工作空间那一条。
	list, err := r.svc.WorkspacePlane().WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("显式 cwd 不该凭空登记工作空间: %+v", list.Items)
	}
}

// TestCreateSessionByWorkspaceID 选择器选中一个登记项：`workspace_id` 定 cwd，无需 `cwd`。
func TestCreateSessionByWorkspaceID(t *testing.T) {
	r := newConvRig(t)
	dir := t.TempDir()
	ws, err := r.svc.WorkspacePlane().CreateRegisteredWorkspace(dir, "我的项目", false)
	if err != nil {
		t.Fatalf("登记: %v", err)
	}

	res, err := r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		WorkspaceID: ws.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession(by id): %v", err)
	}
	if res.Cwd != ws.Path {
		t.Fatalf("cwd 应取自登记路径 %q，实际 %q", ws.Path, res.Cwd)
	}
	if res.WorkspaceID != ws.ID {
		t.Fatalf("归属 = %q，期望 %q", res.WorkspaceID, ws.ID)
	}
	if res.WorkspaceTemporary {
		t.Fatal("用户选的工作空间不该标 temporary")
	}

	// 未知 id → 40401（前端据此刷新列表，而不是引导改路径）。
	if _, err := r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		WorkspaceID: "wsp-nope",
	}); gerrCode(t, err) != code.NotFound {
		t.Fatalf("未知 workspace_id 应 40401，实际 %d", gerrCode(t, err))
	}
}

// TestCreateSessionWorkspaceConflict 同时给出 `workspace_id` 与不一致的 `cwd` → 拒绝。
//
// 拒绝而不猜：这一处猜错的后果是会话跑在用户没打算用的目录里——而那个目录正是原生权限
// workspace-write 的沙箱边界。一致时（含 /tmp 与 /private/tmp 这类写法差异）必须放行。
func TestCreateSessionWorkspaceConflict(t *testing.T) {
	r := newConvRig(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	wsA, err := r.svc.WorkspacePlane().CreateRegisteredWorkspace(dirA, "A", false)
	if err != nil {
		t.Fatalf("登记 A: %v", err)
	}

	// 不一致 → workspace_conflict（40001），且**零副作用**（不建会话、不写库）。
	_, err = r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		WorkspaceID: wsA.ID, Cwd: dirB,
	})
	if err == nil {
		t.Fatal("workspace_id 与 cwd 指向不同目录必须拒绝")
	}
	if c := gerrCode(t, err); c != code.InvalidRequest {
		t.Fatalf("冲突应为 40001，实际 %d: %v", c, err)
	}
	list, _ := sessionPlaneOf(r.svc).ListSessions(false, "", "")
	if len(list) != 0 {
		t.Fatalf("冲突请求不该建出会话: %+v", list)
	}

	// 一致 → 放行（两条来源指向同一目录时不该被拦）。
	res, err := r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		WorkspaceID: wsA.ID, Cwd: dirA,
	})
	if err != nil {
		t.Fatalf("一致时应放行: %v", err)
	}
	if res.WorkspaceID != wsA.ID || res.Cwd != wsA.Path {
		t.Fatalf("一致时结果不符: %+v (ws=%+v)", res, wsA)
	}
}

// TestListSessionsByWorkspaceFilter 归属过滤把历史会话算进默认工作空间那一组。
//
// 这条是"默认工作空间显示 N 个会话、点进去却是空的"这类不一致的唯一防线。
func TestListSessionsByWorkspaceFilter(t *testing.T) {
	r := newConvRig(t)
	def, _, _ := r.svc.WorkspacePlane().DefaultWorkspace()
	dir := t.TempDir()
	ws, err := r.svc.WorkspacePlane().CreateRegisteredWorkspace(dir, "项目", false)
	if err != nil {
		t.Fatalf("登记: %v", err)
	}

	// 一条显式归属普通工作空间、一条归默认、一条**历史行**（workspace_id 留空）。
	seedStoreSession(t, r.st, "c-proj", dir, ws.ID)
	seedStoreSession(t, r.st, "c-default", def.Path, def.ID)
	seedStoreSession(t, r.st, "c-legacy", def.Path, "")
	// 内存镜像也要有（列表读的是内存镜像）。
	// 内存镜像按**库里那一行的原样**登记：`c-legacy` 的归属刻意留空（v14 迁移留下的形态，
	// 读面把它解析成默认工作空间）；真实系统里不会有"库里空、内存里有"的分叉。
	r.svc.registerSession(&SessionRec{ID: "c-proj", Cwd: ws.Path, WorkspaceID: ws.ID})
	r.svc.registerSession(&SessionRec{ID: "c-default", Cwd: def.Path, WorkspaceID: def.ID})
	r.svc.registerSession(&SessionRec{ID: "c-legacy", Cwd: def.Path})

	// 按项目过滤 → 只有那一条。
	proj, err := sessionPlaneOf(r.svc).ListSessions(false, "", ws.ID)
	if err != nil {
		t.Fatalf("按项目过滤: %v", err)
	}
	if len(proj) != 1 || proj[0].ID != "c-proj" {
		t.Fatalf("按项目过滤结果不符: %+v", proj)
	}
	if proj[0].WorkspaceID != ws.ID || proj[0].WorkspaceTitle != "项目" {
		t.Fatalf("归属展示字段不符: %+v", proj[0])
	}

	// 按默认工作空间过滤 → 显式归默认的 + 历史行都在。
	dflt, err := sessionPlaneOf(r.svc).ListSessions(false, "", def.ID)
	if err != nil {
		t.Fatalf("按默认过滤: %v", err)
	}
	got := map[string]bool{}
	for _, c := range dflt {
		got[c.ID] = true
	}
	if !got["c-default"] || !got["c-legacy"] {
		t.Fatalf("默认工作空间应同时含显式归属与历史行（workspace_id=''），实际 %+v", dflt)
	}
	if got["c-proj"] {
		t.Fatalf("项目会话不该出现在默认工作空间: %+v", dflt)
	}
	// 历史行下发的归属是**解析后**的默认 id（不是空串）：前端据它渲染分组。
	for _, c := range dflt {
		if c.ID == "c-legacy" && c.WorkspaceID != def.ID {
			t.Fatalf("历史行的归属应解析成默认工作空间 id，实际 %q", c.WorkspaceID)
		}
		if c.WorkspaceTitle == "" {
			t.Fatalf("归属展示名不该为空（前端要靠它渲染分组行）: %+v", c)
		}
	}

	// 无过滤 → 三条都在（参数为空 = 不过滤，与历史行为逐字相同）。
	all, _ := sessionPlaneOf(r.svc).ListSessions(false, "", "")
	if len(all) != 3 {
		t.Fatalf("无过滤应返回 3 条，实际 %d", len(all))
	}
}

// TestRecentWorkspacesExcludesRegistered 选择器的"未登记"一栏不出现已登记/默认的目录。
//
// 否则用户会看到"已经是我的工作空间"的条目出现在"未登记"里，而点它的语义（登记）已经无意义。
func TestRecentWorkspacesExcludesRegistered(t *testing.T) {
	r := newConvRig(t)
	def, _, _ := r.svc.WorkspacePlane().DefaultWorkspace()
	registeredDir := t.TempDir()
	looseDir := t.TempDir()
	ws, err := r.svc.WorkspacePlane().CreateRegisteredWorkspace(registeredDir, "已登记", false)
	if err != nil {
		t.Fatalf("登记: %v", err)
	}

	// 三条会话：默认工作空间、已登记目录、没登记过的目录。
	//
	// 刻意用 **规范化后**的路径登记（`mustEval`）：真实会话的 cwd 一律是
	// `ValidateWorkspace` 规范化过的形态，而 `workspaceUsage` 按 cwd 聚合——
	// 夹具里用未规范化的拼写会造出一个真实系统里不存在的形态（macOS 上 /var vs /private/var）。
	r.svc.registerSession(&SessionRec{ID: "c-def", Cwd: def.Path, WorkspaceID: def.ID})
	r.svc.registerSession(&SessionRec{ID: "c-reg", Cwd: ws.Path, WorkspaceID: ws.ID})
	r.svc.registerSession(&SessionRec{ID: "c-loose", Cwd: mustEval(t, looseDir)})

	recent, err := r.svc.RecentWorkspaces(10)
	if err != nil {
		t.Fatalf("RecentWorkspaces: %v", err)
	}
	paths := map[string]bool{}
	for _, x := range recent {
		paths[x.Path] = true
	}
	// 临时目录在 macOS 上会被 EvalSymlinks 改写成 /private/...：比对一律用**规范化后**的形态
	// （登记与 workspaceUsage 都按它算），而不是夹具给的拼写。
	looseCanon := mustEval(t, looseDir)
	// looseCanon 未登记 → 必须在；另外两个已登记/默认 → 必须不在。
	if !paths[looseCanon] {
		t.Fatalf("未登记目录应出现在最近使用里（键 %q）: %+v", looseCanon, recent)
	}
	if paths[def.Path] {
		t.Fatalf("默认工作空间不该出现在「未登记」一栏: %+v", recent)
	}
	for _, x := range recent {
		if x.Path == ws.Path || x.Path == def.Path || x.Path == looseCanon && false {
			t.Fatalf("已登记/默认的目录不该出现在「未登记」一栏: %+v", recent)
		}
	}
}

// TestDeleteWorkspaceReleasesSessionsImmediately 删登记后，**不重启**按默认工作空间过滤就能看到那些会话。
//
// 这条是实测出来的缺陷的回归判据（2026-10-11 真机探针）：store 只改库里的列，而列表读的是
// 内存镜像——不同步内存时，删完登记立刻按默认工作空间过滤会返回**空**，要等重启才回来。
// 库是对的、界面是空的，而中间没有任何报错，所以必须由测试钉住。
func TestDeleteWorkspaceReleasesSessionsImmediately(t *testing.T) {
	r := newConvRig(t)
	def, _, _ := r.svc.WorkspacePlane().DefaultWorkspace()
	dir := t.TempDir()
	ws, err := r.svc.WorkspacePlane().CreateRegisteredWorkspace(dir, "要被移除的", false)
	if err != nil {
		t.Fatalf("登记: %v", err)
	}

	res, err := r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		WorkspaceID: ws.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if res.WorkspaceID != ws.ID {
		t.Fatalf("前置不符: %+v", res)
	}

	// 删登记（模型层 + store 层都要清）。
	del, err := r.svc.WorkspacePlane().DeleteRegisteredWorkspace(ws.ID)
	if err != nil {
		t.Fatalf("DeleteRegisteredWorkspace: %v", err)
	}
	if del.ReleasedSessions != 1 {
		t.Fatalf("released_sessions = %d，期望 1", del.ReleasedSessions)
	}

	// **立刻**按默认工作空间过滤：那条会话必须已经在这儿（内存镜像已同步）。
	got, err := sessionPlaneOf(r.svc).ListSessions(false, "", def.ID)
	if err != nil {
		t.Fatalf("按默认过滤: %v", err)
	}
	found := false
	for _, c := range got {
		if c.ID == res.ID {
			found = true
			if c.WorkspaceID != def.ID {
				t.Fatalf("释放后的归属应解析成默认工作空间，实际 %q", c.WorkspaceID)
			}
			if c.Cwd != ws.Path {
				t.Fatalf("cwd 不该被改动: %q ≠ %q", c.Cwd, ws.Path)
			}
		}
	}
	if !found {
		t.Fatalf("删登记后立刻按默认工作空间过滤应能看到那条会话（内存镜像未同步）：%+v", got)
	}

	// 按被删的那个归属过滤 → 空（id 已不存在，不该再返回任何东西）。
	if stale, _ := sessionPlaneOf(r.svc).ListSessions(false, "", ws.ID); len(stale) != 0 {
		t.Fatalf("已删登记不该再返回会话: %+v", stale)
	}
}
