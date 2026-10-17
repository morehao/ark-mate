package manager

import (
	"context"
	"errors"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/gerror"
)

// withinRoots 是 `workspace.WithinRoots` 的**测试兼容别名**（P2b 把纯函数搬进域之后，
// 这一族"策展过滤"用例的判据一字未改——`filepath.Rel` 的逐段比较与两侧真身解析仍在域里那份里）。
func withinRoots(path string, allowedRoots []string) bool {
	return workspace.WithinRoots(path, allowedRoots)
}

// ——— 工作空间解析（workspace.go）单元测试 ———

// gerrCode 取 gerror.Error 的业务码（非 gerror → -1）。
func gerrCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误，得到 nil")
	}
	var ge gerror.Error
	if !errors.As(err, &ge) {
		t.Fatalf("期望 gerror.Error，得到 %T: %v", err, err)
	}
	return ge.Code
}

// aclCode 取 acl.Error 的语义错误码（resolve/validate 层返回 acl 错误，未经 restErr 映射）。
func aclCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误，得到 nil")
	}
	ae, ok := acl.As(err)
	if !ok {
		t.Fatalf("期望 acl.Error，得到 %T: %v", err, err)
	}
	return ae.Code
}

// mustWorkspacePolicy 装配工作空间策略（测试里不自洽即视为装配错误，直接 Fatal）。
func mustWorkspacePolicy(t *testing.T, svc *Service, dir string, roots []string) {
	t.Helper()
	if err := svc.WorkspacePlane().SetWorkspacePolicy(dir, roots); err != nil {
		t.Fatalf("SetWorkspacePolicy(%q, %v): %v", dir, roots, err)
	}
}

// TestValidateWorkspace 三条件（绝对 / 存在 / 是目录）+ 符号链接规范化。
func TestValidateWorkspace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 合法：目录（含尾斜杠 / 冗余片段）→ 规范化后的绝对路径。
	want, _ := filepath.EvalSymlinks(dir)
	for _, in := range []string{dir, dir + string(filepath.Separator), filepath.Join(dir, ".", "..", filepath.Base(dir))} {
		got, err := workspace.ValidateWorkspace(in)
		if err != nil {
			t.Fatalf("workspace.ValidateWorkspace(%q) 应通过: %v", in, err)
		}
		if got != want {
			t.Fatalf("workspace.ValidateWorkspace(%q)=%q want %q", in, got, want)
		}
	}

	// 符号链接 → 规范化为真身（否则同一目录会存成两个字符串，分组/过滤出现重复项）。
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("环境不支持 symlink: %v", err)
	}
	got, err := workspace.ValidateWorkspace(link)
	if err != nil {
		t.Fatalf("symlink 应通过校验: %v", err)
	}
	if got != want {
		t.Fatalf("symlink 未规范化: got=%q want=%q", got, want)
	}

	// 非法：空 / 相对 / 不存在 / 是文件。
	for name, in := range map[string]string{
		"空":   "   ",
		"相对":  "relative/dir",
		"不存在": filepath.Join(dir, "nope"),
		"是文件": file,
	} {
		if _, err := workspace.ValidateWorkspace(in); err == nil {
			t.Fatalf("%s（%q）应被拒", name, in)
		} else if code := aclCode(t, err); code != "workspace_invalid" {
			t.Fatalf("%s 错误码=%q want workspace_invalid", name, code)
		}
	}
}

// TestResolveWorkspace 显式指定 / 默认工作空间 / 没有默认工作空间 三条路径。
//
// v14 起中间那条的含义变了（设计 ADR-4）：未指定 cwd **不再**按会话 id 代建独占目录，
// 而是落在默认工作空间（一个落库的登记项，共享）。
func TestResolveWorkspace(t *testing.T) {
	explicit := t.TempDir()

	// 显式指定 → 原样（规范）使用，非临时、无归属（归属由调用方按路径补）。
	got, err := workspace.ResolveWorkspace(explicit, nil)
	if err != nil {
		t.Fatalf("workspace.ResolveWorkspace(显式): %v", err)
	}
	if got.Temporary {
		t.Fatalf("显式指定不应标记为临时: %+v", got)
	}
	wantExplicit, _ := filepath.EvalSymlinks(explicit)
	if got.Path != wantExplicit {
		t.Fatalf("显式路径=%q want %q", got.Path, wantExplicit)
	}
	if got.WorkspaceID != "" {
		t.Fatalf("显式 cwd 的归属由调用方补，这里不该有值: %q", got.WorkspaceID)
	}

	// 未指定 → 落在默认工作空间（Temporary=true，归属 = 那条登记的 id）。
	def := &workspace.DefaultWorkspaceRef{ID: "wsp-default", Path: t.TempDir()}
	got, err = workspace.ResolveWorkspace("", def)
	if err != nil {
		t.Fatalf("workspace.ResolveWorkspace(默认工作空间): %v", err)
	}
	if !got.Temporary {
		t.Fatalf("落在默认工作空间应标记为 Temporary: %+v", got)
	}
	if got.WorkspaceID != "wsp-default" {
		t.Fatalf("归属应是默认工作空间 id，实际 %q", got.WorkspaceID)
	}
	wantDef, _ := filepath.EvalSymlinks(def.Path)
	if got.Path != wantDef {
		t.Fatalf("默认工作空间路径=%q want %q", got.Path, wantDef)
	}
	// **不再创建任何会话独占目录**：这条路径不该有文件系统副作用。
	if got.Path != wantDef {
		t.Fatalf("解析不该改动默认工作空间路径")
	}

	// 没有默认工作空间（代建关闭，或默认被删）且未指定 → workspace_required；
	// **不回落 os.Getwd()**（那条铁律不变）。
	if _, err := workspace.ResolveWorkspace("", nil); err == nil {
		t.Fatal("没有默认工作空间 + 未指定 应报错")
	} else if code := aclCode(t, err); code != "workspace_required" {
		t.Fatalf("错误码=%q want workspace_required", code)
	}
	// 半个默认工作空间（只有 id 没路径 / 反之）同样按"没有"处理，不半信半疑地用它。
	if _, err := workspace.ResolveWorkspace("", &workspace.DefaultWorkspaceRef{ID: "wsp-x"}); err == nil {
		t.Fatal("默认工作空间缺路径应报错")
	}
	if _, err := workspace.ResolveWorkspace("", &workspace.DefaultWorkspaceRef{Path: t.TempDir()}); err == nil {
		t.Fatal("默认工作空间缺 id 应报错")
	}
}

// TestDiscardWorkspaceIsNoop v14 起 DiscardWorkspace **恒为空操作**。
//
// 这条测试的存在意义是**把"它不再删东西"变成机器判据**：新建会话失败时清理刚建的目录这件事
// 已经不再有对象（没有会话独占目录了），而如果它被误接到默认工作空间上，一次失败的建会话
// 会连带删掉别的会话正在用的文件——那是本设计最不能出的一类事故。
func TestDiscardWorkspaceIsNoop(t *testing.T) {
	def := &workspace.DefaultWorkspaceRef{ID: "wsp-default", Path: t.TempDir()}
	res, err := workspace.ResolveWorkspace("", def)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	workspace.DiscardWorkspace(res)
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("默认工作空间绝不能被 DiscardWorkspace 删掉: %v", err)
	}

	// 用户显式指定的目录同样不动（历史行为）。
	keep := t.TempDir()
	workspace.DiscardWorkspace(workspace.WorkspaceResolution{Path: keep})
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("显式目录不该被删: %v", err)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return out
}

// discardTempWorkspace 清理代建目录（t.TempDir 只删自己建的树，代建目录在 workspaceDir 下，
// 单独删一次避免测试间互相看见残留）。
func discardTempWorkspace(t *testing.T, res workspace.WorkspaceResolution) {
	t.Helper()
	workspace.DiscardWorkspace(res)
}

// ——— 建会话路径的接线（解析先于任何 ACP 动作） ———

// workspaceTestSvc 一个挂在 stub 底座上的 Service（不拨真实子进程：本轮只验证解析与校验顺序）。
//
// v19 起建会话没有"agent 必须先存在"这道前置：底座由构建期固定，夹具把它设成 stub 即可。
func workspaceTestSvc(t *testing.T) *Service {
	t.Helper()
	reg := testutil.StubRegistry()
	svc := NewService(reg, eventbus.New())
	setBuildRuntime(t, "stub") // 夹具注册表只有 stub
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	return svc
}

// TestCreateSessionWorkspaceRejectedBeforeRuntime 非法 cwd 在建会话的第一步就被拒
// （校验先于拨号/建会话：错误码为请求拒绝而非连接/服务错误）。
func TestCreateSessionWorkspaceRejectedBeforeRuntime(t *testing.T) {
	svc := workspaceTestSvc(t)
	mustWorkspacePolicy(t, svc, t.TempDir(), nil)

	_, err := svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		Cwd: "relative/not-absolute",
	})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("相对路径应 40001（请求拒绝），got %d: %v", code, err)
	}

	_, err = svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		Cwd: filepath.Join(t.TempDir(), "nope"),
	})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("不存在的路径应 40001，got %d: %v", code, err)
	}

	f := filepath.Join(t.TempDir(), "f.txt")
	if werr := os.WriteFile(f, []byte("x"), 0o600); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	_, err = svc.CreateSession(context.Background(), dtosession.SessionCreateReq{
		Cwd: f,
	})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("文件路径应 40001，got %d: %v", code, err)
	}
}

// TestCreateSessionWorkspaceRequiredWhenDisabled 代建关闭 + 未指定 cwd →
// workspace_required（40001），且不得回落到 serve 进程目录。
func TestCreateSessionWorkspaceRequiredWhenDisabled(t *testing.T) {
	svc := workspaceTestSvc(t)
	mustWorkspacePolicy(t, svc, "", nil)

	_, err := svc.CreateSession(context.Background(), dtosession.SessionCreateReq{})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("代建关闭应 40001，got %d: %v", code, err)
	}
}

// TestCreateSessionFailureLeavesNoPerSessionDir v14：建会话失败不再留"每会话一个目录"。
//
// 旧行为是"失败时清理刚代建的独占目录"。v14 起未指定 cwd 落在**共享的默认工作空间**，
// 于是这条测试要守的变成两件事（都更严格）：
//   - 失败后 `workspaces/` 下**只有** `default/`（不再有成排的会话目录）；
//   - 默认工作空间**没被删掉**（DiscardWorkspace 若被误接到它上面，这里会红）。
func TestCreateSessionFailureLeavesNoPerSessionDir(t *testing.T) {
	root := t.TempDir()
	svc := workspaceTestSvc(t)
	mustWorkspacePolicy(t, svc, root, nil)
	// 默认工作空间需要**库**才能登记（登记表在 sqlite 里）；memory 模式下它是空操作，
	// 于是未指定 cwd 会停在 workspace_required，测不到这条路径。
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	svc.EnableStore(st)
	if err := svc.WorkspacePlane().EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}

	// echo spec 无启动命令 → acquire 失败。
	_, err := svc.CreateSession(context.Background(), dtosession.SessionCreateReq{})
	if err == nil {
		t.Fatal("无命令的 runtime 应建会话失败")
	}
	if code := gerrCode(t, err); code == 40001 && strings.Contains(err.Error(), "工作空间") {
		t.Fatalf("失败应来自 runtime 层（而非工作空间解析，说明顺序有误）: %v", err)
	}
	entries, rerr := os.ReadDir(root)
	if rerr != nil {
		t.Fatalf("ReadDir(%q): %v", root, rerr)
	}
	if len(entries) != 1 || entries[0].Name() != "default" {
		t.Fatalf("失败后应只剩默认工作空间目录，实际: %v", entries)
	}
	if _, serr := os.Stat(filepath.Join(root, "default")); serr != nil {
		t.Fatalf("默认工作空间不该被 DiscardWorkspace 删掉: %v", serr)
	}
}

// ——— 回读与按工作空间过滤 ———

// TestListSessionsWorkspaceFilter 响应回读 cwd/workspace_temporary + `?cwd=` 精确过滤。
func TestListSessionsWorkspaceFilter(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.registerSession(&SessionRec{ID: "c-a", Cwd: "/work/alpha"})
	svc.registerSession(&SessionRec{ID: "c-b", Cwd: "/work/beta", WorkspaceTemporary: true})
	svc.registerSession(&SessionRec{ID: "c-tmp-sub", Cwd: "/work/alpha/sub"})

	all, err := sessionPlaneOf(svc).ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("无过滤应返回 3，got %d", len(all))
	}
	var seenTemp bool
	for _, c := range all {
		if c.ID == "c-b" {
			seenTemp = c.WorkspaceTemporary && c.Cwd == "/work/beta"
		}
	}
	if !seenTemp {
		t.Fatalf("WorkspaceTemporary/Cwd 未回读: %+v", all)
	}

	// 精确匹配：子目录不算同一工作空间（避免"看起来像同一目录"的误归组）。
	alpha, err := sessionPlaneOf(svc).ListSessions(false, "/work/alpha", "")
	if err != nil {
		t.Fatalf("ListSessions(cwd): %v", err)
	}
	if len(alpha) != 1 || alpha[0].ID != "c-a" {
		t.Fatalf("cwd 过滤应精确命中 c-a，got %+v", alpha)
	}

	none, _ := sessionPlaneOf(svc).ListSessions(false, "/work/nowhere", "")
	if len(none) != 0 {
		t.Fatalf("未用过的工作空间应返回空，got %+v", none)
	}

	// 过滤入参归一化：同一目录的不同写法（macOS 的 /tmp vs /private/tmp、尾斜杠、冗余片段）
	// 必须命中同一批会话——路径字符串不该决定身份。
	rootDir := t.TempDir()
	svc2 := NewService(runtime.NewRegistry(), eventbus.New())
	svc2.registerSession(&SessionRec{ID: "c-real", Cwd: mustEval(t, rootDir)})
	for _, spelling := range []string{rootDir, rootDir + "/", filepath.Join(rootDir, ".", "..", filepath.Base(rootDir))} {
		got, err := sessionPlaneOf(svc2).ListSessions(false, spelling, "")
		if err != nil {
			t.Fatalf("ListSessions(%q): %v", spelling, err)
		}
		if len(got) != 1 || got[0].ID != "c-real" {
			t.Fatalf("写法 %q 未归一到同一工作空间: %+v", spelling, got)
		}
	}
}

// ——— 只读辅助面：目录浏览 + 最近使用 ———

// TestBrowseWorkspace 只列目录（文件/隐藏目录按开关过滤）+ 与建会话同一条路径规范化。
func TestBrowseWorkspace(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"alpha", "beta", ".git"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	resp, err := svc.WorkspacePlane().BrowseWorkspace(root, false)
	if err != nil {
		t.Fatalf("BrowseWorkspace: %v", err)
	}
	if resp.Path != mustEval(t, root) {
		t.Fatalf("path 未规范化: %q want %q", resp.Path, mustEval(t, root))
	}
	if len(resp.Entries) != 2 || resp.Entries[0].Name != "alpha" || resp.Entries[1].Name != "beta" {
		t.Fatalf("应只列 2 个非隐藏目录（按名排序）: %+v", resp.Entries)
	}
	// entries[].path 必须可直接当 cwd 提交（与 ValidateWorkspace 结果一致）。
	if got, err := workspace.ValidateWorkspace(resp.Entries[0].Path); err != nil || got != resp.Entries[0].Path {
		t.Fatalf("entry path 不是可直接使用的 cwd: %q err=%v", resp.Entries[0].Path, err)
	}
	if resp.Parent != filepath.Dir(resp.Path) {
		t.Fatalf("parent 不符: %q", resp.Parent)
	}

	withHidden, err := svc.WorkspacePlane().BrowseWorkspace(root, true)
	if err != nil {
		t.Fatalf("BrowseWorkspace(hidden): %v", err)
	}
	if len(withHidden.Entries) != 3 {
		t.Fatalf("hidden=true 应含 .git: %+v", withHidden.Entries)
	}

	// 非法 path（不存在/相对）与建会话同一套错误码。
	if _, err := svc.WorkspacePlane().BrowseWorkspace(filepath.Join(root, "nope"), false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("不存在的 path 应 workspace_invalid: %v", err)
	}
	if _, err := svc.WorkspacePlane().BrowseWorkspace("relative", false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("相对 path 应 workspace_invalid: %v", err)
	}
	// path 为空 → 从服务进程家目录起步（拿不到家目录时才允许报错）。
	if resp, err := svc.WorkspacePlane().BrowseWorkspace("", false); err == nil && resp.Path == "" {
		t.Fatalf("空 path 应回落到家目录: %+v", resp)
	}
}

// TestRecentWorkspaces 最近工作空间从会话派生：去重计数、按最近使用倒序、排除代建临时空间。
func TestRecentWorkspaces(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	svc.registerSession(&SessionRec{ID: "r-1", Cwd: "/work/alpha", UpdatedAt: base})
	svc.registerSession(&SessionRec{ID: "r-2", Cwd: "/work/alpha", UpdatedAt: base.Add(time.Hour)})
	svc.registerSession(&SessionRec{ID: "r-3", Cwd: "/work/beta", UpdatedAt: base.Add(2 * time.Hour)})
	svc.registerSession(&SessionRec{ID: "r-4", Cwd: "/work/tmp", WorkspaceTemporary: true, UpdatedAt: base.Add(3 * time.Hour)})
	svc.registerSession(&SessionRec{ID: "r-5", UpdatedAt: base.Add(4 * time.Hour)}) // 无 cwd（历史行）

	list, err := svc.WorkspacePlane().RecentWorkspaces(10)
	if err != nil {
		t.Fatalf("RecentWorkspaces: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应返回 2 条（排除代建/空 cwd）: %+v", list)
	}
	if list[0].Path != "/work/beta" || list[0].Name != "beta" || list[0].Sessions != 1 {
		t.Fatalf("最近使用应排在首位: %+v", list[0])
	}
	if list[1].Path != "/work/alpha" || list[1].Sessions != 2 {
		t.Fatalf("同目录应聚合计数: %+v", list[1])
	}
	if list[1].LastUsedAt != base.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("last_used_at 应取该目录下最新会话: %q", list[1].LastUsedAt)
	}

	// limit 截断（默认 10，上限由 handler 侧收敛）。
	if only, _ := svc.WorkspacePlane().RecentWorkspaces(1); len(only) != 1 || only[0].Path != "/work/beta" {
		t.Fatalf("limit=1 应只回最近一条: %+v", only)
	}
}

// ——— 工作区根白名单（MATE_WORKSPACE_ROOTS，A 的边界） ———

// TestWithinRoots 边界判定：逐段比较（不是字符串前缀）+ 两侧都解析符号链接真身。
func TestWithinRoots(t *testing.T) {
	base := t.TempDir() // t.TempDir 已是规范化路径（macOS 上 /var → /private/var 已解析）
	root := filepath.Join(base, "ws")
	sibling := filepath.Join(base, "ws-evil") // 前缀相同但不是子目录：字符串前缀实现会误判
	inside := filepath.Join(root, "proj")
	outside := filepath.Join(base, "other")
	for _, d := range []string{root, sibling, inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// 根内指向外部的符号链接：不解析真身就会被当成"在根内"（经典绕过）。
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cases := []struct {
		name  string
		path  string
		roots []string
		want  bool
	}{
		{"空白名单 = 不限制", outside, nil, true},
		{"根本身", root, []string{root}, true},
		{"根内子目录", inside, []string{root}, true},
		{"前缀相同但不同目录", sibling, []string{root}, false},
		{"根外", outside, []string{root}, false},
		{"多根之一命中", inside, []string{outside, root}, true},
		{"嵌套根的内层", inside, []string{base}, true},
		{"符号链接逃逸（指向根外）", escape, []string{root}, false},
		{"未规范化写法也能命中", root + "/./", []string{root}, true},
	}
	for _, tc := range cases {
		if got := withinRoots(tc.path, tc.roots); got != tc.want {
			t.Fatalf("%s: withinRoots(%q, %v)=%v want %v", tc.name, tc.path, tc.roots, got, tc.want)
		}
	}
}

// TestValidateWorkspaceIgnoresPreferenceRoots **名录不再是边界**（v14.5 的产品口径）。
//
// 这条替代了原来的 `TestValidateWorkspaceRoots`（那条断言"根外即拒"）。口径变了之后，
// 唯一还该被拒的是"路径本身不能用"（不存在 / 不是目录 / 相对路径）；"不在偏好名录里"不再构成拒绝。
func TestValidateWorkspaceIgnoresPreferenceRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	inside := filepath.Join(root, "proj")
	outside := filepath.Join(base, "other")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	if _, err := workspace.ValidateWorkspace(inside); err != nil {
		t.Fatalf("路径可用即应通过: %v", err)
	}
	// 偏好名录之外：**照样通过**（这正是这次要钉住的语义）。
	if _, err := workspace.ValidateWorkspace(outside); err != nil {
		t.Fatalf("名录外的可用目录不该被拒（选工作区是全系统）: %v", err)
	}
	// 仍然被拒的只有"路径本身不能用"。
	if _, err := workspace.ValidateWorkspace(filepath.Join(base, "nope")); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("不存在的路径应 workspace_invalid: %v", err)
	}
	if _, err := workspace.ValidateWorkspace("relative"); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("相对路径应 workspace_invalid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := workspace.ValidateWorkspace(filepath.Join(base, "a.txt")); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("文件应 workspace_invalid: %v", err)
	}
}

// TestResolveWorkspacePreferenceRoots 显式 cwd 与偏好名录**无关**；未指定时用默认工作空间。
//
// 口径：`MATE_WORKSPACE_ROOTS` 只是"目录浏览从哪起步"的偏好，不参与 cwd 解析。
func TestResolveWorkspacePreferenceRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 名录外的目录必须先存在：不存在会被更早的 workspace_invalid 挡住，
	// 测不到"名录不参与 cwd 解析"这一层。
	outside := filepath.Join(base, "other")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 名录外的显式 cwd → 照常解析（不再是错）。
	if res, err := workspace.ResolveWorkspace(outside, nil); err != nil || res.Path != mustEval(t, outside) {
		t.Fatalf("名录外的显式 cwd 应照常解析: res=%+v err=%v", res, err)
	}
	// 未指定 → 用默认工作空间，结果带归属且标 Temporary。
	def := &workspace.DefaultWorkspaceRef{ID: "wsp-default", Path: root}
	res, err := workspace.ResolveWorkspace("", def)
	if err != nil {
		t.Fatalf("默认工作空间: %v", err)
	}
	if !res.Temporary || res.WorkspaceID != "wsp-default" {
		t.Fatalf("默认工作空间结果不符: %+v", res)
	}
}

// TestSetWorkspacePolicyMergesProvisionDir 代建根落在白名单外**不再是**配置错误：自动并入生效名单。
//
// 旧行为是 fail closed（serve 启动即失败），代价是"平台自己的目录"必须由运维手写进白名单才自洽——
// 那是把平台的实现细节泄漏成运维负担。现在平台自己并进来，运维只需关心**用户的**目录边界。
func TestSetWorkspacePolicyMergesProvisionDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	outside := filepath.Join(base, "elsewhere")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	svc := NewService(runtime.NewRegistry(), eventbus.New())

	// 形式错误仍必须挡住：相对路径的根不是边界。
	if err := svc.WorkspacePlane().SetWorkspacePolicy("", []string{"relative/root"}); err == nil {
		t.Fatalf("相对工作区根应被拒")
	}
	if svc.WorkspacePlane().WorkspaceDir() != "" || len(svc.WorkspacePlane().WorkspaceRoots()) != 0 {
		t.Fatalf("失败的设置不应改动策略: dir=%q roots=%v", svc.WorkspacePlane().WorkspaceDir(), svc.WorkspacePlane().WorkspaceRoots())
	}

	// 代建根刻意**不预先创建**：平台自己按需创建它，判定不能因为"目录还没建"就误判
	// （macOS 上 /var 与 /private/var 会踩这个坑）。
	provision := filepath.Join(outside, "tmp")
	mustWorkspacePolicy(t, svc, provision, []string{root})
	got := svc.WorkspacePlane().WorkspaceRoots()
	if len(got) != 2 {
		t.Fatalf("生效名单应为「配置的根 + 隐式并入的代建根」: %v", got)
	}
	if got[0] != mustEval(t, root) {
		t.Fatalf("配置项应排在前（browse 的起步点取 roots[0]，不能被派生根顶掉）: %v", got)
	}
	if want := filepath.Join(mustEval(t, outside), "tmp"); got[1] != want {
		t.Fatalf("代建根应按真身并入，want %q got %q", want, got[1])
	}
	// 并入的代建根必须真的放行：默认工作空间就在它下面，否则平台自己给出的落点
	// 会被自己的白名单拒掉（"未指定 cwd"这条路直接不可用）。
	defDir := filepath.Join(provision, "default")
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatalf("mkdir default: %v", err)
	}
	res, err := workspace.ResolveWorkspace("", &workspace.DefaultWorkspaceRef{ID: "wsp-default", Path: defDir})
	if err != nil {
		t.Fatalf("默认工作空间: %v", err)
	}
	if !res.Temporary || !withinRoots(res.Path, got) {
		t.Fatalf("默认工作空间应落在生效名单内: %+v roots=%v", res, got)
	}

	// 代建根已经落在某个配置根内时不再重复追加（避免 roots 里出现冗余项）。
	nested := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, nested, filepath.Join(root, "tmp"), []string{root})
	if got := nested.WorkspacePlane().WorkspaceRoots(); len(got) != 1 {
		t.Fatalf("代建根已在配置根内时不应重复追加: %v", got)
	}

	// 未配置白名单 = 不限制：不能因为存在代建根就把"不限制"悄悄缩成"只允许代建目录"
	// （那会让默认配置的用户突然选不了自己的项目目录）。
	open := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, open, provision, nil)
	if got := open.WorkspacePlane().WorkspaceRoots(); len(got) != 0 {
		t.Fatalf("未配置白名单应保持「不限制」: %v", got)
	}
}

// TestBrowseWorkspacePreferenceOnly 名录**只是起始点偏好**：浏览面全系统、上级直到文件系统根。
//
// 这条替代了原来的 `TestBrowseWorkspaceClampedByRoots`（断言"越界拒绝、上级止步于根、根外符号链接不列"）。
// 现在的语义是：
//   - 空 path → 从偏好起点（第一个根）起步，这只是"从哪儿开始看"；
//   - 名录外目录照样能浏览，越界不再是错；
//   - `parent` 一直给到文件系统根（只有真到根才为空）；
//   - `home` 恒下发（它是"主目录"按钮的目标）；
//   - 根内指向根外的符号链接子项**照列**（它是个可进入的目录，能选它当工作空间）。
func TestBrowseWorkspacePreferenceOnly(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	inside := filepath.Join(root, "proj")
	outsideDir := filepath.Join(base, "outside")
	escape := filepath.Join(root, "escape")
	for _, d := range []string{inside, outsideDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.Symlink(outsideDir, escape); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc, filepath.Join(root, "tmp"), []string{mustEval(t, root)})

	// 空 path → 从偏好起点起步（"从哪儿开始看"）。
	resp, err := svc.WorkspacePlane().BrowseWorkspace("", false)
	if err != nil {
		t.Fatalf("BrowseWorkspace(\"\"): %v", err)
	}
	if resp.Path != mustEval(t, root) {
		t.Fatalf("空 path 应从偏好起点起步: %q", resp.Path)
	}
	if len(resp.Roots) != 1 || resp.Roots[0] != mustEval(t, root) {
		t.Fatalf("应回读偏好名录（面板据此列快捷入口）: %v", resp.Roots)
	}
	// 偏好起点不再是"不许上去的边界"：上级就是它的真实父目录。
	if resp.Parent != mustEval(t, base) {
		t.Fatalf("上级应为真实父目录（名录不再是边界）: %q", resp.Parent)
	}
	// 家目录恒下发。
	if resp.Home == "" {
		t.Fatal("home 应恒下发（全系统口径下不存在「不在范围内」这回事）")
	}
	// 根内指向根外的符号链接子项照列（它可进入、可被选为工作空间）。
	names := map[string]bool{}
	for _, e := range resp.Entries {
		names[e.Name] = true
	}
	if !names["escape"] {
		t.Fatalf("符号链接子项应照列（不再会「点了必被拒」）: %+v", resp.Entries)
	}

	// 名录外的目录照样能浏览。
	if _, err := svc.WorkspacePlane().BrowseWorkspace(outsideDir, false); err != nil {
		t.Fatalf("名录外的目录应可浏览: %v", err)
	}
	// 只有"路径本身不能用"才报错。
	if _, err := svc.WorkspacePlane().BrowseWorkspace(filepath.Join(base, "nope"), false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("不存在的目录应 workspace_invalid: %v", err)
	}
}

// TestBrowseWorkspaceCrumbs 面包屑（v14.3）：从文件系统根到当前目录（含自身），逐级可跳。
//
// 为什么由服务端算：前端按路径字符串切分会在盘符 / UNC / 符号链接（macOS 的 /var → /private/var）
// 三处切错，而"位置显示错了"比"选不到"更让人困惑。
func TestBrowseWorkspaceCrumbs(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "proj", "src")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	resp, err := svc.WorkspacePlane().BrowseWorkspace(deep, false)
	if err != nil {
		t.Fatalf("BrowseWorkspace: %v", err)
	}
	if len(resp.Crumbs) < 3 {
		t.Fatalf("面包屑至少含根/…/自身: %+v", resp.Crumbs)
	}
	// 链首是文件系统根，且根的 Name 用它自身的路径（basename 是空串，编名字没有意义）。
	first := resp.Crumbs[0]
	if first.Path != filepath.Dir(first.Path) || first.Name != first.Path {
		t.Fatalf("面包屑首项应为文件系统根（名字=自身路径）: %+v", first)
	}
	// 尾项是当前目录，且与响应里的 path 逐字一致（前端据此显示"我在哪"）。
	last := resp.Crumbs[len(resp.Crumbs)-1]
	if last.Path != resp.Path {
		t.Fatalf("面包屑尾项应为当前目录: %+v want %q", last, resp.Path)
	}
	// 逐级关系：每一项都是前一项的直接子目录。
	for i := 1; i < len(resp.Crumbs); i++ {
		if got := filepath.Dir(resp.Crumbs[i].Path); got != resp.Crumbs[i-1].Path {
			t.Fatalf("面包屑不是逐级链: %q 的父不是 %q", resp.Crumbs[i].Path, resp.Crumbs[i-1].Path)
		}
	}
}

// TestBrowseWorkspaceMarksHiddenAndTruncates 隐藏标记与单层截断（v14.3）。
//
// 两条都是"界面要能说清"的事：hidden 标记让「显示隐藏文件」成为纯客户端开关（切换不重发请求），
// truncated 让"还有未显示的子目录"被说出来——静默少几行是最坏的一种（用户以为目录就这些）。
func TestBrowseWorkspaceMarksHiddenAndTruncates(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"visible", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	// hidden=false：隐藏行不下发（但 visible 行必须带 Hidden=false 这个零值语义）。
	plain, err := svc.WorkspacePlane().BrowseWorkspace(root, false)
	if err != nil {
		t.Fatalf("BrowseWorkspace: %v", err)
	}
	if len(plain.Entries) != 1 || plain.Entries[0].Name != "visible" || plain.Entries[0].Hidden {
		t.Fatalf("hidden=false 应只列非隐藏目录且不带标记: %+v", plain.Entries)
	}
	if plain.Truncated {
		t.Fatalf("小目录不该报截断: %+v", plain)
	}
	// hidden=true：隐藏行下发且带标记，供前端自己做显示开关。
	withHidden, err := svc.WorkspacePlane().BrowseWorkspace(root, true)
	if err != nil {
		t.Fatalf("BrowseWorkspace(hidden): %v", err)
	}
	if len(withHidden.Entries) != 2 {
		t.Fatalf("hidden=true 应含隐藏目录: %+v", withHidden.Entries)
	}
	marked := false
	for _, e := range withHidden.Entries {
		if e.Name == ".hidden" && e.Hidden {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("隐藏目录应带 hidden 标记: %+v", withHidden.Entries)
	}

	// 截断：造出超过上限的子目录（上限是常量，这里只验"越界即置位 + 条数不超上限"）。
	many := t.TempDir()
	for i := 0; i <= workspace.BrowseEntryLimit; i++ {
		if err := os.Mkdir(filepath.Join(many, fmt.Sprintf("d%05d", i)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	big, err := svc.WorkspacePlane().BrowseWorkspace(many, false)
	if err != nil {
		t.Fatalf("BrowseWorkspace(大目录): %v", err)
	}
	if len(big.Entries) != workspace.BrowseEntryLimit || !big.Truncated {
		t.Fatalf("超上限应截断并置位: len=%d truncated=%v", len(big.Entries), big.Truncated)
	}
}

// TestCreateWorkspaceDir 建目录的四条规则（v14.3；目录浏览面的「新建文件夹」）。
func TestCreateWorkspaceDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc, filepath.Join(root, "tmp"), []string{mustEval(t, root)})

	// 合法：建在根内子目录下，回**规范化后**的绝对路径（前端拿它直接当 cwd）。
	resp, err := svc.WorkspacePlane().CreateWorkspaceDir(proj, "新项目")
	if err != nil {
		t.Fatalf("CreateWorkspaceDir: %v", err)
	}
	if resp.Path != filepath.Join(mustEval(t, proj), "新项目") {
		t.Fatalf("应回规范化后的新目录路径: %q", resp.Path)
	}
	if st, serr := os.Stat(resp.Path); serr != nil || !st.IsDir() {
		t.Fatalf("目录应真的建出来了: %v", serr)
	}

	// 名字规则：单段、非 "." / ".."。
	for _, bad := range []string{"", "  ", "a/b", `a\b`, ".", ".."} {
		if _, err := svc.WorkspacePlane().CreateWorkspaceDir(proj, bad); aclCode(t, err) != "workspace_invalid" {
			t.Fatalf("名字 %q 应被拒: %v", bad, err)
		}
	}
	// 已存在 → 40901（不覆盖、不静默复用："我建的那个"与"原来就有那个"是两件事）。
	if _, err := svc.WorkspacePlane().CreateWorkspaceDir(proj, "新项目"); aclCode(t, err) != "workspace_exists" {
		t.Fatalf("同名目录应 workspace_exists: %v", err)
	}
	// 名录外照样能建（白名单不再是边界）：落点由用户自己选。
	if _, err := svc.WorkspacePlane().CreateWorkspaceDir(base, "outside-ok"); err != nil {
		t.Fatalf("名录外的父目录应可建目录: %v", err)
	}
	// 父目录不存在 → workspace_invalid（与浏览面同一条校验）。
	if _, err := svc.WorkspacePlane().CreateWorkspaceDir(filepath.Join(proj, "nope"), "x"); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("不存在的父目录应 workspace_invalid: %v", err)
	}
}

// TestListWorkspaceFiles 目录内容：目录在前、文件在后、带大小与时间，排除非资料项。
func TestListWorkspaceFiles(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "docs")
	sub := filepath.Join(root, "合同")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte("hello 资料")
	if err := os.WriteFile(filepath.Join(root, "a.pdf"), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// 指向根外的符号链接不列（与浏览面同一判据）；断链跳过。
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(base, "nope"), filepath.Join(root, "broken")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc, filepath.Join(root, "tmp"), []string{mustEval(t, root)})

	resp, err := svc.WorkspacePlane().ListWorkspaceFiles(root, false)
	if err != nil {
		t.Fatalf("ListWorkspaceFiles: %v", err)
	}
	if resp.Path != mustEval(t, root) || len(resp.Roots) != 1 {
		t.Fatalf("path/roots 不符: %+v", resp)
	}
	// v14.5：只有"隐藏项"与"断链"不列——根外真身的符号链接照列（它可进入，也可能被选为工作空间）。
	if len(resp.Entries) != 3 {
		t.Fatalf("应列 合同/ + escape/ + a.pdf: %+v", resp.Entries)
	}
	byName := map[string]int{}
	for i, e := range resp.Entries {
		byName[e.Name] = i
	}
	if i, ok := byName["合同"]; !ok || !resp.Entries[i].IsDir {
		t.Fatalf("目录应在前半段且标 is_dir: %+v", resp.Entries)
	}
	if i, ok := byName["a.pdf"]; !ok {
		t.Fatalf("文件条目应列出: %+v", resp.Entries)
	} else if got := resp.Entries[i]; got.IsDir || got.Size != int64(len(body)) || got.ModifiedAt == "" {
		t.Fatalf("文件条目应带大小与修改时间: %+v", got)
	}
	if _, ok := byName["broken"]; ok {
		t.Fatalf("断链不应列出: %+v", resp.Entries)
	}

	// hidden=true → 含隐藏项。
	withHidden, err := svc.WorkspacePlane().ListWorkspaceFiles(root, true)
	if err != nil {
		t.Fatalf("ListWorkspaceFiles(hidden): %v", err)
	}
	if len(withHidden.Entries) != 4 {
		t.Fatalf("hidden=true 应含 .hidden（断链仍不列）: %+v", withHidden.Entries)
	}

	// path 必填：不给 path 是调用方的错，不是"回落到某个默认目录"——看资料必须明确看哪里。
	if _, err := svc.WorkspacePlane().ListWorkspaceFiles("", false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("空 path 应 workspace_invalid: %v", err)
	}
	// 名录外的目录照常可看（v14.5：不再按名录过滤）。
	if _, err := svc.WorkspacePlane().ListWorkspaceFiles(outside, false); err != nil {
		t.Fatalf("名录外的目录应可列出内容: %v", err)
	}
	if _, err := svc.WorkspacePlane().ListWorkspaceFiles(filepath.Join(root, "nope"), false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("不存在目录应 workspace_invalid: %v", err)
	}
	// 文件不是目录：看内容也要给目录（工作空间三条件之一）。
	file := filepath.Join(root, "a.pdf")
	if _, err := svc.WorkspacePlane().ListWorkspaceFiles(file, false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("对文件列内容应 workspace_invalid: %v", err)
	}
	// 符号链接指向的目录仍按真身判定边界：根内的链接可看，根外的不可。
	linkIn := filepath.Join(root, "selflink")
	if err := os.Symlink(sub, linkIn); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := svc.WorkspacePlane().ListWorkspaceFiles(linkIn, false); err != nil {
		t.Fatalf("根内符号链接目录应可读: %v", err)
	}
}

// TestSessionWorksOutsidePreferenceRoots 名录**不是边界**：名录外的目录照样能开会话（v14.5 的产品口径）。
//
// 为什么单独立一条：上面那些用例各自只钉一个函数（Validate / Browse / DirCreate / Register），
// 而"选工作区是全系统"这句话的用户可见形态是**建会话能落在名录外**。这条走的是与
// `POST /sessions` 同一条解析链（ResolveWorkspace → ValidateWorkspace），
// 所以它红了就意味着"用户在应用内选不到名录外的目录"。
func TestSessionWorksOutsidePreferenceRoots(t *testing.T) {
	base := t.TempDir()
	preferred := filepath.Join(base, "preferred")
	other := filepath.Join(base, "somewhere-else")
	for _, d := range []string{preferred, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc, filepath.Join(base, "tmp"), []string{mustEval(t, preferred)})

	res, err := workspace.ResolveWorkspace(other, nil)
	if err != nil {
		t.Fatalf("名录外的目录应能作为会话 cwd（选工作区是全系统）: %v", err)
	}
	if res.Path != mustEval(t, other) || res.Temporary {
		t.Fatalf("显式 cwd 应原样生效且不标 Temporary: %+v", res)
	}
	// 未指定 cwd 时仍落默认工作空间（这条与名录无关，别被上一条改动带偏）。
	def := &workspace.DefaultWorkspaceRef{ID: "wsp-default", Path: preferred}
	if got, err := workspace.ResolveWorkspace("", def); err != nil || !got.Temporary || got.WorkspaceID != "wsp-default" {
		t.Fatalf("未指定 cwd 应落默认工作空间: %+v err=%v", got, err)
	}
}
