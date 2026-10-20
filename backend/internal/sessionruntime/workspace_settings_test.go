package sessionruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/workspace"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/runtime"
)

// fakeRootsStore 记录落盘调用并支持注入失败/锁定态。
// manager 只依赖端口（WorkspaceRootsStore），所以它能用一个假实现把"落盘"这一层隔离掉：
// 真正要验的是**顺序与拒绝路径**（先落盘后改内存、锁定时不落盘），不是文件 IO。
type fakeRootsStore struct {
	saved   [][]string
	saveErr error
	src     string
	locked  string
}

func (f *fakeRootsStore) SaveWorkspaceRoots(roots []string) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, append([]string(nil), roots...))
	return nil
}

func (f *fakeRootsStore) Status() (string, string) { return f.src, f.locked }

// TestWorkspaceSettingsWithoutStore 无持久化端口（acp-serve/demo 这类没有配置目录的进程）：
// GET 可读但必须自报"不可写"，PUT 报 settings_readonly——不能假装接受然后什么都不做。
func TestWorkspaceSettingsWithoutStore(t *testing.T) {
	root := t.TempDir()
	svc := newServiceForTest(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc, "", []string{mustEval(t, root)})

	resp := planesOf(svc).Workspaces.Settings()
	if len(resp.Configured) != 1 || !resp.PreferRoots || resp.Writable {
		t.Fatalf("快照不符: %+v", resp)
	}
	if resp.Source != "unavailable" {
		t.Fatalf("无端口时来源应标 unavailable，得到 %q", resp.Source)
	}
	if _, err := planesOf(svc).Workspaces.UpdateSettings([]string{root}); acpCode(t, err) != "settings_readonly" {
		t.Fatalf("无端口写入应 settings_readonly: %v", err)
	}
}

// TestUpdateWorkspaceSettings 设置面主路径：校验 → 落盘 → 热更新，且 Configured 与 Roots 分工清楚
// （前者是面板编辑的那一份，后者含隐式并入的代建根，用户删不掉）。
func TestUpdateWorkspaceSettings(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "docs")
	rootB := filepath.Join(base, "repos")
	provision := filepath.Join(base, "tmp")
	for _, d := range []string{rootA, rootB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	svc := newServiceForTest(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc, provision, nil) // 未配置偏好起点
	store := &fakeRootsStore{src: "config.json"}
	planesOf(svc).Workspaces.SetRootsStore(store)

	// 起步快照：没有偏好起点（prefer_roots=false，无名单可言，故 provision_included 为 false）。
	resp := planesOf(svc).Workspaces.Settings()
	if resp.PreferRoots || len(resp.Configured) != 0 || len(resp.Roots) != 0 || resp.ProvisionIncluded {
		t.Fatalf("未配置时不该有偏好起点: %+v", resp)
	}
	if !resp.Writable || resp.LockedBy != "" || resp.Source != "config.json" {
		t.Fatalf("可写态不符: %+v", resp)
	}

	// 落盘内容必须是**显式**名单（不含代建根）：派生项写进配置文件就变成了运维的配置。
	var err error
	resp, err = planesOf(svc).Workspaces.UpdateSettings([]string{rootA, rootB})
	if err != nil {
		t.Fatalf("UpdateWorkspaceSettings: %v", err)
	}
	if len(store.saved) != 1 || len(store.saved[0]) != 2 {
		t.Fatalf("落盘内容不符（应为显式名单）: %+v", store.saved)
	}
	if !resp.PreferRoots || !resp.ProvisionIncluded || len(resp.Configured) != 2 {
		t.Fatalf("更新后快照不符: %+v", resp)
	}
	if len(resp.Roots) != 3 || resp.Roots[0] != mustEval(t, rootA) || resp.Roots[2] != filepath.Join(mustEval(t, base), "tmp") {
		t.Fatalf("生效名单应为「配置的根 + 隐式代建根」: %+v", resp.Roots)
	}
	// 热更新立即生效：偏好起点变了，但**它不再是边界**——名录外目录照样可浏览。
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := planesOf(svc).Workspaces.BrowseWorkspace(outside, false); err != nil {
		t.Fatalf("更新偏好后名录外目录仍应可浏览（名录不是边界）: %v", err)
	}
	// 代建根也要放行——默认工作空间就在它下面（未指定 cwd 的落点）。
	defDir := filepath.Join(provision, "default")
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatalf("mkdir default: %v", err)
	}
	if _, err := workspace.ResolveWorkspace("", &workspace.DefaultWorkspaceRef{ID: "wsp-default", Path: defDir}); err != nil {
		t.Fatalf("默认工作空间应始终可选（代建根隐式并入）: %v", err)
	}
	if _, err := planesOf(svc).Workspaces.BrowseWorkspace(provision, false); err != nil {
		t.Fatalf("代建根应始终可浏览: %v", err)
	}

	// 影响面：偏好起点只影响"浏览从哪儿起步"；已建会话 cwd 创建即定稿、不回头否决，
	// 而且**它仍然可以**作为"最近目录"候选被推回来（v14.5：名录外不再意味着"点了必被拒"）。
	svc.sessionPlane.RegisterSession(&session.SessionRec{
		ID: "c-old", Cwd: mustEval(t, outside), UpdatedAt: time.Now().UTC(),
	})
	recent, err := planesOf(svc).Workspaces.RecentWorkspaces(10)
	if err != nil {
		t.Fatalf("RecentWorkspaces: %v", err)
	}
	found := false
	for _, r := range recent {
		if r.Path == mustEval(t, outside) {
			found = true
		}
	}
	if !found {
		t.Fatalf("名录外的旧会话应仍可作为候选被推回来: %+v", recent)
	}
	_, kept := svc.sessionPlane.Lookup("c-old")
	if !kept {
		t.Fatal("旧会话记录必须保留（可续聊性只看会话是否存在）")
	}

	// 空名单 = 恢复不限制（面板"取消限定"的那一步）。
	resp, err = planesOf(svc).Workspaces.UpdateSettings(nil)
	if err != nil {
		t.Fatalf("清空: %v", err)
	}
	if resp.PreferRoots || len(resp.Roots) != 0 || len(store.saved) != 2 || len(store.saved[1]) != 0 {
		t.Fatalf("清空后应为不限制并落盘空名单: resp=%+v saved=%+v", resp, store.saved)
	}
}

// TestUpdateWorkspaceSettingsValidation 交互式设置的严格度：绝对 / 存在 / 是目录，且**不落盘**。
// 与配置文件入口（只校验绝对路径，容忍"挂载还没到"）的差异是有意的，见 workspace_settings.go。
func TestUpdateWorkspaceSettingsValidation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "docs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(base, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc := newServiceForTest(runtime.NewRegistry(), eventbus.New())
	store := &fakeRootsStore{}
	planesOf(svc).Workspaces.SetRootsStore(store)

	cases := []struct {
		name  string
		roots []string
	}{
		{"相对路径", []string{"relative/root"}},
		{"不存在", []string{filepath.Join(base, "nope")}},
		{"不是目录", []string{file}},
		{"好项 + 坏项（逐项校验，不能被前面的合法项带过）", []string{root, "relative"}},
	}
	for _, tc := range cases {
		if _, err := planesOf(svc).Workspaces.UpdateSettings(tc.roots); acpCode(t, err) != "workspace_invalid" {
			t.Fatalf("%s: 应 workspace_invalid，得到 %v", tc.name, err)
		}
	}
	if len(store.saved) != 0 {
		t.Fatalf("校验失败不该落盘: %+v", store.saved)
	}
	if got := planesOf(svc).Workspaces.WorkspaceRoots(); len(got) != 0 {
		t.Fatalf("校验失败不该改动内存策略: %v", got)
	}

	// 空白项被跳过；重复项（含符号链接指向同一目录的真身）去重。
	link := filepath.Join(base, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := planesOf(svc).Workspaces.UpdateSettings([]string{"  ", root, link, root}); err != nil {
		t.Fatalf("重复项应去重而不是报错: %v", err)
	}
	if len(store.saved) != 1 || len(store.saved[0]) != 1 {
		t.Fatalf("应按真身去重: %+v", store.saved)
	}
}

// TestUpdateWorkspaceSettingsLocked 被更高优先级来源锁定（进程 env / env 文件 / flag 设过该键）
// 时**拒绝写**：写 config.json 必然被覆盖，报成功等于骗人。
func TestUpdateWorkspaceSettingsLocked(t *testing.T) {
	root := t.TempDir()
	svc := newServiceForTest(runtime.NewRegistry(), eventbus.New())
	store := &fakeRootsStore{src: "env(process)", locked: "MATE_WORKSPACE_ROOTS"}
	planesOf(svc).Workspaces.SetRootsStore(store)

	resp := planesOf(svc).Workspaces.Settings()
	if resp.Writable || resp.LockedBy != "MATE_WORKSPACE_ROOTS" {
		t.Fatalf("锁定态应透出给面板（它据此转只读）: %+v", resp)
	}
	err := func() error { _, e := planesOf(svc).Workspaces.UpdateSettings([]string{root}); return e }()
	if acpCode(t, err) != "settings_locked" {
		t.Fatalf("应 settings_locked: %v", err)
	}
	// 报错要点名去哪改（"被锁定"本身不是可执行信息）。
	if msg := err.Error(); !strings.Contains(msg, "MATE_WORKSPACE_ROOTS") || !strings.Contains(msg, "[settings_locked]") {
		t.Fatalf("锁定错误应点明来源与语义码: %v", msg)
	}
	if len(store.saved) != 0 {
		t.Fatalf("锁定时不该落盘: %+v", store.saved)
	}
}

// TestUpdateWorkspaceSettingsSaveFailure 落盘失败时内存**不被改动**（顺序：先落盘、成功后才热更新）。
// 反过来会留下"面板生效、重启回滚"的假成功，比直接失败更难查。
func TestUpdateWorkspaceSettingsSaveFailure(t *testing.T) {
	root := t.TempDir()
	svc := newServiceForTest(runtime.NewRegistry(), eventbus.New())
	mustWorkspacePolicy(t, svc, "", []string{mustEval(t, t.TempDir())})
	before := planesOf(svc).Workspaces.WorkspaceRoots()
	planesOf(svc).Workspaces.SetRootsStore(&fakeRootsStore{saveErr: errors.New("磁盘只读")})

	if _, err := planesOf(svc).Workspaces.UpdateSettings([]string{root}); acpCode(t, err) != "settings_save_failed" {
		t.Fatalf("应 settings_save_failed: %v", err)
	}
	after := planesOf(svc).Workspaces.WorkspaceRoots()
	if len(after) != len(before) || after[0] != before[0] {
		t.Fatalf("落盘失败不该改动内存策略: before=%v after=%v", before, after)
	}
}
