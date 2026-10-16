package router_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// 本文件是 v14.3 目录浏览对话框的**进程内集成判据**（真路由 + 真 manager + 真文件系统）。
//
// 它证的是"界面依赖的每一条事实真的经 HTTP 面拿得到"：
//   - browse 的 `crumbs` 是从文件系统根到当前目录的链、且被白名单裁到最近的根；
//   - `entries[].hidden` 让「显示隐藏文件」成为一个客户端开关（hidden=true 时下发并带标记）；
//   - `POST /workspaces/dirs` 真的在磁盘上建出目录、并回**规范化后**的路径；
//   - 白名单外建目录被拒（40001）——写面与读面共用同一条边界。
//
// 与 manager 层单测的分工：那边验四条规则本身，这边验**它们确实经过了控制器与路由**
// （新增固定字面量路径最典型的坏法就是被 `/workspaces/:id` 抢走，那种坏法只有走真路由才看得见）。

// browseApp 装配真路由 + 真 manager，白名单可指定（nil = 不限制）。
func browseApp(t *testing.T, roots []string) *manager.Service {
	t.Helper()
	svc := manager.NewService(runtime.NewRegistry(), eventbus.New())
	provision := filepath.Join(t.TempDir(), "workspaces")
	if err := svc.SetWorkspacePolicy(provision, roots); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}
	return svc
}

// doRawEnvelope 与 doRaw 同一套装配，但**不**把非零 code 当失败：拒绝路径本身就是要验的东西。
func doRawEnvelope(t *testing.T, svc *manager.Service, method, path, body string) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(doRaw(t, svc, method, path, body), &env); err != nil {
		t.Fatalf("解析信封(%s %s): %v", method, path, err)
	}
	return env
}

// TestDirectoryBrowseHTTPCrumbsAndHidden browse 的 crumbs / hidden / truncated 三条事实。
func TestDirectoryBrowseHTTPCrumbsAndHidden(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "proj", "src")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(deep, ".hidden"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	svc := browseApp(t, nil)

	// hidden=false：隐藏目录不下发（它由开关决定，而不是靠前端过滤不存在的行）。
	_, data := callJSON(t, svc, "GET", "/v1/mate/workspaces/browse?path="+deep, "")
	var resp dtoworkspace.WorkspaceBrowseResp
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("解析 browse: %v", err)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("该层只有隐藏目录，hidden=false 应回空: %+v", resp.Entries)
	}
	if len(resp.Crumbs) < 3 {
		t.Fatalf("crumbs 应含根 → … → 当前: %+v", resp.Crumbs)
	}
	if resp.Crumbs[0].Path != filepath.Dir(resp.Crumbs[0].Path) || resp.Crumbs[0].Name != resp.Crumbs[0].Path {
		t.Fatalf("crumbs 首项应为文件系统根（名字=自身路径）: %+v", resp.Crumbs[0])
	}
	if last := resp.Crumbs[len(resp.Crumbs)-1]; last.Path != resp.Path {
		t.Fatalf("crumbs 尾项应为当前目录: %+v want %q", last, resp.Path)
	}

	// hidden=true：下发并带标记（面板据此做纯客户端开关）。
	_, data = callJSON(t, svc, "GET", "/v1/mate/workspaces/browse?path="+deep+"&hidden=true", "")
	resp = dtoworkspace.WorkspaceBrowseResp{}
	_ = json.Unmarshal(data, &resp)
	if len(resp.Entries) != 1 || resp.Entries[0].Name != ".hidden" || !resp.Entries[0].Hidden {
		t.Fatalf("hidden=true 应下发隐藏行并带标记: %+v", resp.Entries)
	}
}

// TestDirectoryBrowseHTTPPreferenceRoots 偏好名录下：crumbs 给完整链、名录外照常可浏览。
func TestDirectoryBrowseHTTPClampedByRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	inside := filepath.Join(root, "proj")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	svc := browseApp(t, []string{real})

	_, data := callJSON(t, svc, "GET", "/v1/mate/workspaces/browse?path="+inside, "")
	var resp dtoworkspace.WorkspaceBrowseResp
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("解析 browse: %v", err)
	}
	if len(resp.Crumbs) < 3 || resp.Crumbs[0].Path != "/" {
		t.Fatalf("crumbs 应给完整祖先链（v14.5 不再按名录裁剪）: %+v", resp.Crumbs)
	}
	if resp.Parent != real {
		t.Fatalf("根内子目录的上级应为根: %q", resp.Parent)
	}

	// 名录外：照常可浏览（白名单已降级为起始点偏好，越界不再是错）。
	env := doRawEnvelope(t, svc, "GET", "/v1/mate/workspaces/browse?path="+base, "")
	if env.Code != 0 {
		t.Fatalf("名录外目录应可浏览: %+v", env)
	}
}

// TestWorkspaceDirCreateHTTPFlow 「新建文件夹」走真路由：建出来、可再次浏览到、越界被拒。
func TestWorkspaceDirCreateHTTPFlow(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	svc := browseApp(t, []string{real})

	// 建在根内：回规范化后的绝对路径，目录真的存在。
	_, data := callJSON(t, svc, "POST", "/v1/mate/workspaces/dirs",
		`{"parent":"`+real+`","name":"新项目"}`)
	var created dtoworkspace.WorkspaceDirCreateResp
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建结果: %v", err)
	}
	if created.Path != filepath.Join(real, "新项目") {
		t.Fatalf("应回规范化后的新目录路径: %q", created.Path)
	}
	if st, serr := os.Stat(created.Path); serr != nil || !st.IsDir() {
		t.Fatalf("目录应真的建出来了: %v", serr)
	}

	// 建完就能浏览到（界面"落到该层并选中新目录"依赖这一条）。
	_, data = callJSON(t, svc, "GET", "/v1/mate/workspaces/browse?path="+real, "")
	var resp dtoworkspace.WorkspaceBrowseResp
	_ = json.Unmarshal(data, &resp)
	found := false
	for _, e := range resp.Entries {
		if e.Name == "新项目" {
			found = true
		}
	}
	if !found {
		t.Fatalf("新建目录应出现在父层的 entries 里: %+v", resp.Entries)
	}

	// 同名再建 → 40901（不覆盖、不静默复用）。文案是"目录已存在: <path>"（这条错误的 Details
	// 由 manager 自己给出，不含稳定 code——与越界那条的写法有意不同：改名的引导靠 40901 本身）。
	env := doRawEnvelope(t, svc, "POST", "/v1/mate/workspaces/dirs",
		`{"parent":"`+real+`","name":"新项目"}`)
	if env.Code != 40901 {
		t.Fatalf("同名目录应 40901: %+v", env)
	}

	// 名字非法 → 40001（acl.ClassRequest 的既有投影，界面靠 msg 分辨该改什么）。
	if env := doRawEnvelope(t, svc, "POST", "/v1/mate/workspaces/dirs",
		`{"parent":"`+real+`","name":"a/b"}`); env.Code != 40001 || !strings.Contains(env.Msg, "路径分隔符") {
		t.Fatalf("多段名应 40001 且说明含分隔符: %+v", env)
	}
	if env := doRawEnvelope(t, svc, "POST", "/v1/mate/workspaces/dirs",
		`{"parent":"`+base+`","name":"x"}`); env.Code != 0 {
		t.Fatalf("名录外的父目录应可建目录: %+v", env)
	}
}

// 宿主原生选择器端点的三个结局（选定 / 取消 / 不支持）改在**控制器级**用替身钉：
// `TestWorkspacePickDirectoryThreeOutcomes`（internal/controller/ctrworkspace/pick_test.go）。
// 原因：搬走 manager 那条转发之后控制器直接持域平面 `*workspace.Picker`，而"选定 / 取消"
// 要真的 spawn 宿主命令（osascript/zenity），路由级拿真对象跑不出来——**替身缝在哪一层，
// 契约测试就在哪一层**（P4a 正文第十七刀）。
