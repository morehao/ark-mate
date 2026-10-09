package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/internal/dto/dtoconversation"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/gerror"
)

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
	if err := svc.SetWorkspacePolicy(dir, roots); err != nil {
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
		got, err := ValidateWorkspace(in, nil)
		if err != nil {
			t.Fatalf("ValidateWorkspace(%q) 应通过: %v", in, err)
		}
		if got != want {
			t.Fatalf("ValidateWorkspace(%q)=%q want %q", in, got, want)
		}
	}

	// 符号链接 → 规范化为真身（否则同一目录会存成两个字符串，分组/过滤出现重复项）。
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("环境不支持 symlink: %v", err)
	}
	got, err := ValidateWorkspace(link, nil)
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
		if _, err := ValidateWorkspace(in, nil); err == nil {
			t.Fatalf("%s（%q）应被拒", name, in)
		} else if code := aclCode(t, err); code != "workspace_invalid" {
			t.Fatalf("%s 错误码=%q want workspace_invalid", name, code)
		}
	}
}

// TestResolveWorkspace 显式指定 / 代建 / 代建关闭三条路径。
func TestResolveWorkspace(t *testing.T) {
	root := t.TempDir()
	explicit := t.TempDir()

	// 显式指定 → 原样（规范）使用，非临时。
	got, err := ResolveWorkspace(explicit, root, nil, "conv-1")
	if err != nil {
		t.Fatalf("ResolveWorkspace(显式): %v", err)
	}
	if got.Temporary {
		t.Fatalf("显式指定不应标记为临时: %+v", got)
	}
	wantExplicit, _ := filepath.EvalSymlinks(explicit)
	if got.Path != wantExplicit {
		t.Fatalf("显式路径=%q want %q", got.Path, wantExplicit)
	}

	// 未指定 → 在 root 下按会话 id 代建（目录必须先于 session/new 存在）。
	got, err = ResolveWorkspace("", root, nil, "conv-2")
	if err != nil {
		t.Fatalf("ResolveWorkspace(代建): %v", err)
	}
	if !got.Temporary {
		t.Fatalf("代建应标记为临时: %+v", got)
	}
	if filepath.Base(got.Path) != "conv-2" || filepath.Dir(got.Path) != mustEval(t, root) {
		t.Fatalf("代建目录不符: %q (root=%q)", got.Path, root)
	}
	if st, err := os.Stat(got.Path); err != nil || !st.IsDir() {
		t.Fatalf("代建目录未创建: %v", err)
	}

	// 代建关闭（root 为空）且未指定 → workspace_required；**不回落 os.Getwd()**。
	if _, err := ResolveWorkspace("", "", nil, "conv-3"); err == nil {
		t.Fatal("代建关闭 + 未指定 应报错")
	} else if code := aclCode(t, err); code != "workspace_required" {
		t.Fatalf("错误码=%q want workspace_required", code)
	}

	// 内部错误：缺会话 id 时无法定名代建目录。
	if _, err := ResolveWorkspace("", root, nil, ""); err == nil {
		t.Fatal("缺会话 id 应报错")
	} else if code := aclCode(t, err); code != "workspace_unavailable" {
		t.Fatalf("错误码=%q want workspace_unavailable", code)
	}
}

// TestDiscardWorkspace 只有平台代建的目录才会被清理。
func TestDiscardWorkspace(t *testing.T) {
	root := t.TempDir()
	res, err := ResolveWorkspace("", root, nil, "conv-doomed")
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	DiscardWorkspace(res)
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Fatalf("代建目录应被清理: %v", err)
	}

	// 非临时（用户显式指定）→ 绝不能删。
	keep := t.TempDir()
	DiscardWorkspace(WorkspaceResolution{Path: keep})
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("非临时工作空间不应被清理: %v", err)
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
func discardTempWorkspace(t *testing.T, res WorkspaceResolution) {
	t.Helper()
	DiscardWorkspace(res)
}

// ——— 建会话路径的接线（解析先于任何 ACP 动作） ———

// workspaceTestSvc 播一个 echo runtime 上的 agent（不拨真实子进程：本轮只验证解析与校验顺序）。
func workspaceTestSvc(t *testing.T) *Service {
	t.Helper()
	reg := testutil.StubRegistry()
	svc := NewService(reg, eventbus.New())
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	if _, err := svc.CreateAgent(context.Background(), dtoagent.AgentCreateReq{
		ID: "ws-probe", Name: "工作空间测试", Runtimes: []string{"stub"},
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	return svc
}

// TestCreateConversationWorkspaceRejectedBeforeRuntime 非法 cwd 在建会话的第一步就被拒
// （校验先于拨号/建会话：错误码为请求拒绝而非连接/服务错误）。
func TestCreateConversationWorkspaceRejectedBeforeRuntime(t *testing.T) {
	svc := workspaceTestSvc(t)
	mustWorkspacePolicy(t, svc, t.TempDir(), nil)

	_, err := svc.CreateConversation(context.Background(), dtoconversation.ConversationCreateReq{
		AgentID: "ws-probe", Cwd: "relative/not-absolute",
	})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("相对路径应 40001（请求拒绝），got %d: %v", code, err)
	}

	_, err = svc.CreateConversation(context.Background(), dtoconversation.ConversationCreateReq{
		AgentID: "ws-probe", Cwd: filepath.Join(t.TempDir(), "nope"),
	})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("不存在的路径应 40001，got %d: %v", code, err)
	}

	f := filepath.Join(t.TempDir(), "f.txt")
	if werr := os.WriteFile(f, []byte("x"), 0o600); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	_, err = svc.CreateConversation(context.Background(), dtoconversation.ConversationCreateReq{
		AgentID: "ws-probe", Cwd: f,
	})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("文件路径应 40001，got %d: %v", code, err)
	}
}

// TestCreateConversationWorkspaceRequiredWhenDisabled 代建关闭 + 未指定 cwd →
// workspace_required（40001），且不得回落到 serve 进程目录。
func TestCreateConversationWorkspaceRequiredWhenDisabled(t *testing.T) {
	svc := workspaceTestSvc(t)
	mustWorkspacePolicy(t, svc, "", nil)

	_, err := svc.CreateConversation(context.Background(), dtoconversation.ConversationCreateReq{AgentID: "ws-probe"})
	if code := gerrCode(t, err); code != 40001 {
		t.Fatalf("代建关闭应 40001，got %d: %v", code, err)
	}
}

// TestCreateConversationDiscardsTemporaryWorkspaceOnFailure 会话最终没建起来时，
// 刚代建的目录必须被清理（否则每次失败都在 ~/.forge/workspaces 下留垃圾）。
func TestCreateConversationDiscardsTemporaryWorkspaceOnFailure(t *testing.T) {
	root := t.TempDir()
	svc := workspaceTestSvc(t)
	mustWorkspacePolicy(t, svc, root, nil)

	// echo spec 无启动命令 → acquire 失败；此时代建目录应已被回收。
	_, err := svc.CreateConversation(context.Background(), dtoconversation.ConversationCreateReq{AgentID: "ws-probe"})
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
	if len(entries) != 0 {
		t.Fatalf("失败的会话不应残留代建目录: %v", entries)
	}
}

// ——— 回读与按工作空间过滤 ———

// TestListConversationsWorkspaceFilter 响应回读 cwd/workspace_temporary + `?cwd=` 精确过滤。
func TestListConversationsWorkspaceFilter(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.registerConversation(&ConversationRec{ID: "c-a", AgentID: "a1", Cwd: "/work/alpha"})
	svc.registerConversation(&ConversationRec{ID: "c-b", AgentID: "a1", Cwd: "/work/beta", WorkspaceTemporary: true})
	svc.registerConversation(&ConversationRec{ID: "c-tmp-sub", AgentID: "a1", Cwd: "/work/alpha/sub"})

	all, err := svc.ListConversations(false, "")
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
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
	alpha, err := svc.ListConversations(false, "/work/alpha")
	if err != nil {
		t.Fatalf("ListConversations(cwd): %v", err)
	}
	if len(alpha) != 1 || alpha[0].ID != "c-a" {
		t.Fatalf("cwd 过滤应精确命中 c-a，got %+v", alpha)
	}

	none, _ := svc.ListConversations(false, "/work/nowhere")
	if len(none) != 0 {
		t.Fatalf("未用过的工作空间应返回空，got %+v", none)
	}

	// 过滤入参归一化：同一目录的不同写法（macOS 的 /tmp vs /private/tmp、尾斜杠、冗余片段）
	// 必须命中同一批会话——路径字符串不该决定身份。
	rootDir := t.TempDir()
	svc2 := NewService(runtime.NewRegistry(), eventbus.New())
	svc2.registerConversation(&ConversationRec{ID: "c-real", AgentID: "a1", Cwd: mustEval(t, rootDir)})
	for _, spelling := range []string{rootDir, rootDir + "/", filepath.Join(rootDir, ".", "..", filepath.Base(rootDir))} {
		got, err := svc2.ListConversations(false, spelling)
		if err != nil {
			t.Fatalf("ListConversations(%q): %v", spelling, err)
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
	resp, err := svc.BrowseWorkspace(root, false)
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
	if got, err := ValidateWorkspace(resp.Entries[0].Path, nil); err != nil || got != resp.Entries[0].Path {
		t.Fatalf("entry path 不是可直接使用的 cwd: %q err=%v", resp.Entries[0].Path, err)
	}
	if resp.Parent != filepath.Dir(resp.Path) {
		t.Fatalf("parent 不符: %q", resp.Parent)
	}

	withHidden, err := svc.BrowseWorkspace(root, true)
	if err != nil {
		t.Fatalf("BrowseWorkspace(hidden): %v", err)
	}
	if len(withHidden.Entries) != 3 {
		t.Fatalf("hidden=true 应含 .git: %+v", withHidden.Entries)
	}

	// 非法 path（不存在/相对）与建会话同一套错误码。
	if _, err := svc.BrowseWorkspace(filepath.Join(root, "nope"), false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("不存在的 path 应 workspace_invalid: %v", err)
	}
	if _, err := svc.BrowseWorkspace("relative", false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("相对 path 应 workspace_invalid: %v", err)
	}
	// path 为空 → 从服务进程家目录起步（拿不到家目录时才允许报错）。
	if resp, err := svc.BrowseWorkspace("", false); err == nil && resp.Path == "" {
		t.Fatalf("空 path 应回落到家目录: %+v", resp)
	}
}

// TestRecentWorkspaces 最近工作空间从会话派生：去重计数、按最近使用倒序、排除代建临时空间。
func TestRecentWorkspaces(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	svc.registerConversation(&ConversationRec{ID: "r-1", AgentID: "a1", Cwd: "/work/alpha", UpdatedAt: base})
	svc.registerConversation(&ConversationRec{ID: "r-2", AgentID: "a1", Cwd: "/work/alpha", UpdatedAt: base.Add(time.Hour)})
	svc.registerConversation(&ConversationRec{ID: "r-3", AgentID: "a1", Cwd: "/work/beta", UpdatedAt: base.Add(2 * time.Hour)})
	svc.registerConversation(&ConversationRec{ID: "r-4", AgentID: "a1", Cwd: "/work/tmp", WorkspaceTemporary: true, UpdatedAt: base.Add(3 * time.Hour)})
	svc.registerConversation(&ConversationRec{ID: "r-5", AgentID: "a1", UpdatedAt: base.Add(4 * time.Hour)}) // 无 cwd（历史行）

	list, err := svc.RecentWorkspaces(10)
	if err != nil {
		t.Fatalf("RecentWorkspaces: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应返回 2 条（排除代建/空 cwd）: %+v", list)
	}
	if list[0].Path != "/work/beta" || list[0].Name != "beta" || list[0].Conversations != 1 {
		t.Fatalf("最近使用应排在首位: %+v", list[0])
	}
	if list[1].Path != "/work/alpha" || list[1].Conversations != 2 {
		t.Fatalf("同目录应聚合计数: %+v", list[1])
	}
	if list[1].LastUsedAt != base.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("last_used_at 应取该目录下最新会话: %q", list[1].LastUsedAt)
	}

	// limit 截断（默认 10，上限由 handler 侧收敛）。
	if only, _ := svc.RecentWorkspaces(1); len(only) != 1 || only[0].Path != "/work/beta" {
		t.Fatalf("limit=1 应只回最近一条: %+v", only)
	}
}

// ——— 工作区根白名单（FORGE_WORKSPACE_ROOTS，A 的边界） ———

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

// TestValidateWorkspaceRoots 显式 cwd 越界即拒（浏览面与创建面共用这条校验）。
func TestValidateWorkspaceRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	inside := filepath.Join(root, "proj")
	outside := filepath.Join(base, "other")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	roots := []string{mustEval(t, root)}

	if _, err := ValidateWorkspace(inside, roots); err != nil {
		t.Fatalf("根内路径应通过: %v", err)
	}
	err := func() error { _, e := ValidateWorkspace(outside, roots); return e }()
	if code := aclCode(t, err); code != "workspace_outside_roots" {
		t.Fatalf("根外路径应 workspace_outside_roots，得到 %s: %v", code, err)
	}
	// 错误信息要能指导运维改哪个配置（否则只会看到"被拒"）。
	if !strings.Contains(err.Error(), "FORGE_WORKSPACE_ROOTS") {
		t.Fatalf("越界错误应点明配置键: %v", err)
	}
}

// TestResolveWorkspaceRoots 白名单对代建无副作用（代建路径的合法性由装配时的策略检查保证）。
func TestResolveWorkspaceRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	roots := []string{mustEval(t, root)}
	// 越界目录必须先存在：不存在会被更早的 workspace_invalid 挡住，测不到边界这一层。
	outside := filepath.Join(base, "other")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 越界的显式 cwd → 拒。
	if _, err := ResolveWorkspace(outside, root, roots, "conv-x"); aclCode(t, err) != "workspace_outside_roots" {
		t.Fatalf("越界显式 cwd 应被拒: %v", err)
	}
	// 未指定 → 在根内代建，且结果落在白名单内（平台不产出越界会话）。
	res, err := ResolveWorkspace("", root, roots, "conv-y")
	if err != nil {
		t.Fatalf("代建: %v", err)
	}
	if !res.Temporary || !withinRoots(res.Path, roots) {
		t.Fatalf("代建结果应在白名单内: %+v", res)
	}
	discardTempWorkspace(t, res)
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
	if err := svc.SetWorkspacePolicy("", []string{"relative/root"}); err == nil {
		t.Fatalf("相对工作区根应被拒")
	}
	if svc.WorkspaceDir() != "" || len(svc.WorkspaceRoots()) != 0 {
		t.Fatalf("失败的设置不应改动策略: dir=%q roots=%v", svc.WorkspaceDir(), svc.WorkspaceRoots())
	}

	// 代建根刻意**不预先创建**：平台自己按需创建它，判定不能因为"目录还没建"就误判
	// （macOS 上 /var 与 /private/var 会踩这个坑）。
	provision := filepath.Join(outside, "tmp")
	mustWorkspacePolicy(t, svc, provision, []string{root})
	got := svc.WorkspaceRoots()
	if len(got) != 2 {
		t.Fatalf("生效名单应为「配置的根 + 隐式并入的代建根」: %v", got)
	}
	if got[0] != mustEval(t, root) {
		t.Fatalf("配置项应排在前（browse 的起步点取 roots[0]，不能被派生根顶掉）: %v", got)
	}
	if want := filepath.Join(mustEval(t, outside), "tmp"); got[1] != want {
		t.Fatalf("代建根应按真身并入，want %q got %q", want, got[1])
	}
	// 并入的代建根必须真的放行：否则平台自己代建的会话会被自己的白名单拒掉。
	res, err := ResolveWorkspace("", provision, got, "conv-z")
	if err != nil {
		t.Fatalf("代建: %v", err)
	}
	if !res.Temporary || !withinRoots(res.Path, got) {
		t.Fatalf("代建结果应落在生效名单内: %+v roots=%v", res, got)
	}
	discardTempWorkspace(t, res)

	// 代建根已经落在某个配置根内时不再重复追加（避免 roots 里出现冗余项）。
	nested := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, nested, filepath.Join(root, "tmp"), []string{root})
	if got := nested.WorkspaceRoots(); len(got) != 1 {
		t.Fatalf("代建根已在配置根内时不应重复追加: %v", got)
	}

	// 未配置白名单 = 不限制：不能因为存在代建根就把"不限制"悄悄缩成"只允许代建目录"
	// （那会让默认配置的用户突然选不了自己的项目目录）。
	open := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, open, provision, nil)
	if got := open.WorkspaceRoots(); len(got) != 0 {
		t.Fatalf("未配置白名单应保持「不限制」: %v", got)
	}
}

// TestBrowseWorkspaceClampedByRoots 浏览面被白名单收窄：越界拒绝、上级止步于根、家目录不在根内则不返回。
func TestBrowseWorkspaceClampedByRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	inside := filepath.Join(root, "proj")
	outsideDir := filepath.Join(base, "outside")
	// 根内指向根外的符号链接子项：列出来就是死路（点了必被拒），必须不列。
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

	// 空 path → 从第一个根起步（不是家目录：白名单下"允许工作的目录"才是正确的着陆点）。
	resp, err := svc.BrowseWorkspace("", false)
	if err != nil {
		t.Fatalf("BrowseWorkspace(\"\"): %v", err)
	}
	if resp.Path != mustEval(t, root) {
		t.Fatalf("空 path 应从工作区根起步: %q", resp.Path)
	}
	if len(resp.Roots) != 1 || resp.Roots[0] != mustEval(t, root) {
		t.Fatalf("应回读生效的白名单（面板据此解释为什么上不去）: %v", resp.Roots)
	}
	// 根：上级为空（不许越过根向上）。
	if resp.Parent != "" {
		t.Fatalf("到达工作区根后不应再给上级: %q", resp.Parent)
	}
	// 家目录不在根内 → 不返回（前端按钮自动消失，而不是给一个必然报错的入口）。
	if resp.Home != "" {
		t.Fatalf("家目录不在白名单内时不应返回 home: %q", resp.Home)
	}
	// 符号链接逃逸的子项不列（只应有 proj，escape 真身在根外）。
	for _, e := range resp.Entries {
		if e.Name == "escape" {
			t.Fatalf("真身落在根外的符号链接子项不应列出: %+v", resp.Entries)
		}
	}

	// 根内子目录：上级 = 根（合法，允许回去）。
	sub, err := svc.BrowseWorkspace(inside, false)
	if err != nil {
		t.Fatalf("BrowseWorkspace(根内): %v", err)
	}
	if sub.Parent != mustEval(t, root) {
		t.Fatalf("根内子目录的上级应为根: %q", sub.Parent)
	}
	// 越界 path → 与建会话同一个错。
	if _, err := svc.BrowseWorkspace(outsideDir, false); aclCode(t, err) != "workspace_outside_roots" {
		t.Fatalf("越界浏览应 workspace_outside_roots: %v", err)
	}
}

// TestRecentWorkspacesFilteredByRoots 白名单外的历史工作空间不再作为"可再选用"的候选。
func TestRecentWorkspacesFilteredByRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	inside := filepath.Join(root, "proj")
	outside := filepath.Join(base, "legacy")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	base0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	// 白名单是后配的：旧的 legacy 会话仍在（且可继续），但不该被推荐为新的工作空间。
	svc.registerConversation(&ConversationRec{ID: "n-1", AgentID: "a1", Cwd: mustEval(t, outside), UpdatedAt: base0})
	svc.registerConversation(&ConversationRec{ID: "n-2", AgentID: "a1", Cwd: mustEval(t, inside), UpdatedAt: base0.Add(time.Hour)})
	mustWorkspacePolicy(t, svc, filepath.Join(root, "tmp"), []string{mustEval(t, root)})

	list, err := svc.RecentWorkspaces(10)
	if err != nil {
		t.Fatalf("RecentWorkspaces: %v", err)
	}
	if len(list) != 1 || list[0].Path != mustEval(t, inside) {
		t.Fatalf("白名单外的历史工作空间不应出现在最近列表: %+v", list)
	}
}

// ——— 资料库（根的一级子目录）+ 目录内容（看资料） ———

// TestWorkspaceLibraries 资料库 = 工作区根的一级子目录：零新状态、按名排序、带会话聚合。
func TestWorkspaceLibraries(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "docs")
	rootB := filepath.Join(base, "repos")
	// A 下两个资料库 + 一个隐藏目录 + 一个文件（都不算资料库）；
	// A 下还有一个指向根外的符号链接（点进去必被拒 → 不列）。
	for _, d := range []string{filepath.Join(rootA, "合同归档"), filepath.Join(rootA, ".private"), filepath.Join(rootB, "ark-mate")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootA, "readme.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	outside := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(rootA, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	// 未配 roots → 没有资料库（策展能力的前提是先画边界），且不报错。
	if list, err := svc.WorkspaceLibraries(); err != nil || len(list) != 0 {
		t.Fatalf("未配白名单应返回空列表: %v err=%v", list, err)
	}

	// 代建根刻意**建出来**并放在根内（roots 配置下它必然在某个根内）：
	// 它绝不能作为一个"资料库"出现在列表里——那是平台的实现细节，不是用户的资料。
	provision := filepath.Join(rootA, "tmp")
	if err := os.MkdirAll(provision, 0o755); err != nil {
		t.Fatalf("mkdir provision: %v", err)
	}
	mustWorkspacePolicy(t, svc, provision, []string{mustEval(t, rootA), mustEval(t, rootB)})
	// 会话聚合：合同归档用过 2 次、ark-mate 用过 1 次。
	base0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	svc.registerConversation(&ConversationRec{ID: "l-1", AgentID: "a1", Cwd: mustEval(t, filepath.Join(rootA, "合同归档")), UpdatedAt: base0})
	svc.registerConversation(&ConversationRec{ID: "l-2", AgentID: "a1", Cwd: mustEval(t, filepath.Join(rootA, "合同归档")), UpdatedAt: base0.Add(time.Hour)})
	svc.registerConversation(&ConversationRec{ID: "l-3", AgentID: "a1", Cwd: mustEval(t, filepath.Join(rootB, "ark-mate")), UpdatedAt: base0.Add(2 * time.Hour)})

	list, err := svc.WorkspaceLibraries()
	if err != nil {
		t.Fatalf("WorkspaceLibraries: %v", err)
	}
	// 期望恰好 2 个：合同归档（A）、ark-mate（B）——隐藏目录/文件/根外符号链接/代建根 tmp 都不算。
	// 排序按名（"ark-mate" < "合同归档"，ASCII 先于中文）。
	if len(list) != 2 {
		t.Fatalf("资料库数量不符（代建根 tmp 不该出现）: %+v", list)
	}
	if list[0].Name != "ark-mate" || list[0].Root != mustEval(t, rootB) || list[0].Conversations != 1 {
		t.Fatalf("第一个资料库不符: %+v", list[0])
	}
	if list[1].Name != "合同归档" || list[1].Root != mustEval(t, rootA) {
		t.Fatalf("第二个资料库不符: %+v", list[1])
	}
	if list[1].Conversations != 2 || list[1].LastUsedAt != base0.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("资料库应带会话数与最近使用时间: %+v", list[1])
	}
	// path 必须可直接当 cwd 用（与建会话同一条校验），否则"选了资料库却建不了会话"。
	for _, l := range list {
		if _, err := ValidateWorkspace(l.Path, svc.WorkspaceRoots()); err != nil {
			t.Fatalf("资料库 path 不是可直接使用的 cwd: %q err=%v", l.Path, err)
		}
	}

	// 代建根本身一旦（隐式）成为生效名单的一员，它**下面**的第一层子目录就是按会话 id 命名的
	// 沙箱目录：它们同样不能进资料库列表——否则用户会看到一串 "ses_xxx" 被当成"资料库"。
	if err := os.MkdirAll(filepath.Join(provision, "ses_ws_temp"), 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	svc2 := NewService(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc2, provision, []string{mustEval(t, rootA)})
	list2, err := svc2.WorkspaceLibraries()
	if err != nil {
		t.Fatalf("WorkspaceLibraries: %v", err)
	}
	for _, l := range list2 {
		if l.Name == "tmp" || l.Name == "ses_ws_temp" {
			t.Fatalf("代建根与其下的会话目录都不该是资料库: %+v", list2)
		}
	}
}

// TestWorkspaceLibrariesRootUnreadable 单个根不可读不该让整个列表失败（挂载掉了就少显示一个）。
func TestWorkspaceLibrariesRootUnreadable(t *testing.T) {
	readable := t.TempDir()
	if err := os.MkdirAll(filepath.Join(readable, "ok"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "gone") // 不创建：ReadDir 会失败
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	// 策略设置只做形态校验（root 不存在不报错，挂载点可能晚于进程出现），故这里能配上。
	mustWorkspacePolicy(t, svc, "", []string{mustEval(t, readable), missing})

	list, err := svc.WorkspaceLibraries()
	if err != nil {
		t.Fatalf("不可读的根不应让列表失败: %v", err)
	}
	if len(list) != 1 || list[0].Name != "ok" {
		t.Fatalf("应只回可读根下的资料库: %+v", list)
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

	resp, err := svc.ListWorkspaceFiles(root, false)
	if err != nil {
		t.Fatalf("ListWorkspaceFiles: %v", err)
	}
	if resp.Path != mustEval(t, root) || len(resp.Roots) != 1 {
		t.Fatalf("path/roots 不符: %+v", resp)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("应只列 合同/ + a.pdf（隐藏/根外链接/断链都不列）: %+v", resp.Entries)
	}
	if !resp.Entries[0].IsDir || resp.Entries[0].Name != "合同" {
		t.Fatalf("目录应排在前: %+v", resp.Entries)
	}
	if got := resp.Entries[1]; got.IsDir || got.Name != "a.pdf" || got.Size != int64(len(body)) || got.ModifiedAt == "" {
		t.Fatalf("文件条目应带大小与修改时间: %+v", got)
	}

	// hidden=true → 含隐藏项（仍不含根外符号链接）。
	withHidden, err := svc.ListWorkspaceFiles(root, true)
	if err != nil {
		t.Fatalf("ListWorkspaceFiles(hidden): %v", err)
	}
	if len(withHidden.Entries) != 3 {
		t.Fatalf("hidden=true 应含 .hidden: %+v", withHidden.Entries)
	}

	// path 必填：不给 path 是调用方的错，不是"回落到某个默认目录"——看资料必须明确看哪里。
	if _, err := svc.ListWorkspaceFiles("", false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("空 path 应 workspace_invalid: %v", err)
	}
	// 越界与不存在同样是 workspace_invalid / workspace_outside_roots（与建会话同一套）。
	if _, err := svc.ListWorkspaceFiles(outside, false); aclCode(t, err) != "workspace_outside_roots" {
		t.Fatalf("越界目录应 workspace_outside_roots: %v", err)
	}
	if _, err := svc.ListWorkspaceFiles(filepath.Join(root, "nope"), false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("不存在目录应 workspace_invalid: %v", err)
	}
	// 文件不是目录：看内容也要给目录（工作空间三条件之一）。
	file := filepath.Join(root, "a.pdf")
	if _, err := svc.ListWorkspaceFiles(file, false); aclCode(t, err) != "workspace_invalid" {
		t.Fatalf("对文件列内容应 workspace_invalid: %v", err)
	}
	// 符号链接指向的目录仍按真身判定边界：根内的链接可看，根外的不可。
	linkIn := filepath.Join(root, "selflink")
	if err := os.Symlink(sub, linkIn); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := svc.ListWorkspaceFiles(linkIn, false); err != nil {
		t.Fatalf("根内符号链接目录应可读: %v", err)
	}
}
