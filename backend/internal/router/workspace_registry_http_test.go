package router_test

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/sessionruntime"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// 本文件是 v14 工作空间登记表的**进程内集成判据**（真路由 + 真 manager + 真 sqlite 库）。
//
// 它与服务端语义测试（internal/manager 的 workspace_registry_test.go）分工不同：
// 那边验领域规则，这边验**用户硬要求那条线是真的通了**——
// 「选择的和创建的都要落入 db」在 HTTP 面上走一遍：POST /workspaces 创建 → GET /workspaces
// 能读回来 → 重启（重新打开同一个库）后**仍在**且顺序一致 → DELETE 只删登记不删数据。
//
// 刻意不走 `POST /sessions`（那需要拉起一个真的 ACP 子进程）：建会话与登记的接线由
// manager 的 TestCreateSession* 系列覆盖，这里只钉"登记这条线"。把两者混在一个测试里
// 会让"底座拉不起来"这种环境问题把登记面的判据一起弄红。

// registryApp 装配真路由 + 真 manager + 独立 sqlite 库（工作区白名单：不受限）。
//
// 返回 service 是为了让用例能直接调领域方法做前置（例如建出默认工作空间）。
func registryApp(t *testing.T) (*sessionruntime.Service, string, string) {
	t.Helper()
	dbPath := testutil.TempDBPath(t)
	st := testutil.OpenStore(t, dbPath)
	t.Cleanup(func() { _ = st.Close() })

	svc := sessionruntime.NewService(runtime.NewRegistry(), eventbus.New())
	svc.EnableStore(st)

	// 工作空间根放在**私有临时目录**下（本机隔离纪律：绝不碰 ~/.ark-mate）。
	provision := filepath.Join(t.TempDir(), "workspaces")
	if err := svc.Planes().Workspaces.SetWorkspacePolicy(provision, nil); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}
	if err := svc.Planes().Workspaces.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}

	return svc, dbPath, provision
}

func callJSON(t *testing.T, svc *sessionruntime.Service, method, path, body string) (int, json.RawMessage) {
	t.Helper()
	env := doRaw(t, svc, method, path, body)
	var parsed struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(env, &parsed); err != nil {
		t.Fatalf("解析信封(%s %s): %v\n%s", method, path, err, env)
	}
	if parsed.Code != 0 {
		t.Fatalf("%s %s 失败: code=%d msg=%s", method, path, parsed.Code, parsed.Msg)
	}
	return parsed.Code, parsed.Data
}

// TestWorkspaceRegistryHTTPFlow 创建 → 列出 → 重排 → 改名 → 删除，全部经真实 HTTP 路由。
func TestWorkspaceRegistryHTTPFlow(t *testing.T) {
	svc, _, _ := registryApp(t)

	// ① 首次装配已建出默认工作空间（"未分组的会话"有落点）。
	_, data := callJSON(t, svc, "GET", "/v1/mate/workspaces", "")
	var list dtoworkspace.WorkspaceListResp
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析列表: %v", err)
	}
	if len(list.Items) != 1 || list.DefaultID == "" || !list.Items[0].IsDefault {
		t.Fatalf("装配后应恰有一条默认工作空间: %+v", list)
	}
	if !list.Writable {
		t.Fatal("sqlite 持久层应可写")
	}
	defID := list.DefaultID

	// ② 创建两个（POST /workspaces）——用户硬要求「创建的也要落入 db」。
	dirA, dirB := t.TempDir(), t.TempDir()
	_, dataA := callJSON(t, svc, "POST", "/v1/mate/workspaces", `{"path":"`+dirA+`","title":"项目 A"}`)
	var wsA dtoworkspace.WorkspaceResp
	if err := json.Unmarshal(dataA, &wsA); err != nil {
		t.Fatalf("解析创建结果: %v", err)
	}
	if wsA.ID == "" || wsA.ID == defID || wsA.Title != "项目 A" {
		t.Fatalf("创建结果不符: %+v", wsA)
	}
	_, dataB := callJSON(t, svc, "POST", "/v1/mate/workspaces", `{"path":"`+dirB+`"}`)
	var wsB dtoworkspace.WorkspaceResp
	if err := json.Unmarshal(dataB, &wsB); err != nil {
		t.Fatalf("解析创建结果: %v", err)
	}
	if wsB.Title != "" {
		t.Fatalf("未传标题时应是未命名（前端按末级目录名渲染）: %+v", wsB)
	}

	// ③ get-or-create：同一路径再登记一次返回**同一行**，不报错（前端重复点击不该像失败）。
	_, dataAgain := callJSON(t, svc, "POST", "/v1/mate/workspaces", `{"path":"`+dirA+`"}`)
	var again dtoworkspace.WorkspaceResp
	_ = json.Unmarshal(dataAgain, &again)
	if again.ID != wsA.ID {
		t.Fatalf("同路径重复登记应返回既有行: %s ≠ %s", again.ID, wsA.ID)
	}

	// ④ 列出：默认 + A + B，顺序 = 创建顺序（默认排第一，它由装配期建、list_order=0）。
	_, data = callJSON(t, svc, "GET", "/v1/mate/workspaces", "")
	list = dtoworkspace.WorkspaceListResp{}
	_ = json.Unmarshal(data, &list)
	if len(list.Items) != 3 {
		t.Fatalf("应有 3 条登记，实际 %d: %+v", len(list.Items), list.Items)
	}
	if list.Items[0].ID != defID || list.Items[1].ID != wsA.ID || list.Items[2].ID != wsB.ID {
		t.Fatalf("顺序应为 默认 → A → B: %+v", idsOf(list.Items))
	}

	// ⑤ 重排：把 B 移到最前 → 返回**完整**顺序，且再读一次与之一致（不是只在该响应里对）。
	_, dataMove := callJSON(t, svc, "POST", "/v1/mate/workspaces/"+wsB.ID+"/move", `{"before_id":"`+defID+`"}`)
	var mv dtoworkspace.WorkspaceMoveResp
	_ = json.Unmarshal(dataMove, &mv)
	if len(mv.IDs) != 3 || mv.IDs[0] != wsB.ID {
		t.Fatalf("重排应返回完整顺序且 B 在首位: %+v", mv.IDs)
	}
	_, data = callJSON(t, svc, "GET", "/v1/mate/workspaces", "")
	list = dtoworkspace.WorkspaceListResp{}
	_ = json.Unmarshal(data, &list)
	if list.Items[0].ID != wsB.ID || list.Items[1].ID != defID || list.Items[2].ID != wsA.ID {
		t.Fatalf("重排后重新读到的顺序应与返回一致: %+v", idsOf(list.Items))
	}
	for i, it := range list.Items {
		if it.ListOrder != int64(i) {
			t.Fatalf("list_order 应是 0..n-1 的排列: %+v", idsOf(list.Items))
		}
	}

	// ⑥ 改名（PATCH）：改的是展示名，**身份不动**。
	_, dataRen := callJSON(t, svc, "PATCH", "/v1/mate/workspaces/"+wsB.ID, `{"title":"换了个名字"}`)
	var renamed dtoworkspace.WorkspaceResp
	_ = json.Unmarshal(dataRen, &renamed)
	if renamed.ID != wsB.ID || renamed.Path != wsB.Path || renamed.Title != "换了个名字" {
		t.Fatalf("改名不该动身份: 旧 %+v 新 %+v", wsB, renamed)
	}

	// ⑦ 删除（DELETE）：只删登记，目录仍在（数据保留）。
	if _, err := svc.Planes().Workspaces.CreateRegisteredWorkspace(dirA, "", false); err != nil {
		t.Fatalf("前置登记: %v", err)
	}
	_, dataDel := callJSON(t, svc, "DELETE", "/v1/mate/workspaces/"+wsA.ID, "")
	var del dtoworkspace.WorkspaceDeleteResp
	_ = json.Unmarshal(dataDel, &del)
	if del.ID != wsA.ID {
		t.Fatalf("删除响应不符: %+v", del)
	}
	_, data = callJSON(t, svc, "GET", "/v1/mate/workspaces", "")
	list = dtoworkspace.WorkspaceListResp{}
	_ = json.Unmarshal(data, &list)
	if len(list.Items) != 2 {
		t.Fatalf("删除后应剩 2 条: %+v", idsOf(list.Items))
	}
	if list.DefaultID != defID {
		t.Fatalf("删掉普通条目不该影响默认: %+v", list)
	}
}

// TestWorkspaceRegistryPersistsAcrossReopen 「落入 db」的判据：**换一个句柄读同一个库**仍在。
//
// 只断言"HTTP 能读回来"是不够的——那只证明进程内有状态。这条把库关掉再开一次（等价于重启），
// 顺序与标题都要原样回来；这也是用户硬要求 ③ 的直接机器判据。
func TestWorkspaceRegistryPersistsAcrossReopen(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	st := testutil.OpenStore(t, dbPath)
	svc := sessionruntime.NewService(runtime.NewRegistry(), eventbus.New())
	svc.EnableStore(st)
	provision := filepath.Join(t.TempDir(), "workspaces")
	if err := svc.Planes().Workspaces.SetWorkspacePolicy(provision, nil); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}
	if err := svc.Planes().Workspaces.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}
	dir1, dir2 := t.TempDir(), t.TempDir()
	if _, err := svc.Planes().Workspaces.CreateRegisteredWorkspace(dir1, "第一个", false); err != nil {
		t.Fatalf("登记 1: %v", err)
	}
	ws2, err := svc.Planes().Workspaces.CreateRegisteredWorkspace(dir2, "第二个", false)
	if err != nil {
		t.Fatalf("登记 2: %v", err)
	}
	// 把第二个移到最前，落一个**非创建顺序**的顺序进库。
	if _, err := svc.Planes().Workspaces.MoveRegisteredWorkspace(ws2.ID, ""); err != nil {
		t.Fatalf("重排: %v", err)
	}
	before, err := svc.Planes().Workspaces.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry: %v", err)
	}
	_ = st.Close()

	// 重新打开同一个库（等价重启）：登记与顺序都必须在。
	st2 := testutil.OpenStore(t, dbPath)
	t.Cleanup(func() { _ = st2.Close() })
	svc2 := sessionruntime.NewService(runtime.NewRegistry(), eventbus.New())
	svc2.EnableStore(st2)
	if err := svc2.Planes().Workspaces.SetWorkspacePolicy(provision, nil); err != nil {
		t.Fatalf("SetWorkspacePolicy(重启): %v", err)
	}
	// 幂等：默认工作空间已存在 → 不重建、不改顺序。
	if err := svc2.Planes().Workspaces.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace(重启): %v", err)
	}
	after, err := svc2.Planes().Workspaces.WorkspaceRegistry()
	if err != nil {
		t.Fatalf("WorkspaceRegistry(重启): %v", err)
	}

	if len(after.Items) != len(before.Items) {
		t.Fatalf("重启后条数变了: %d → %d", len(before.Items), len(after.Items))
	}
	for i := range before.Items {
		if after.Items[i].ID != before.Items[i].ID ||
			after.Items[i].Title != before.Items[i].Title ||
			after.Items[i].ListOrder != before.Items[i].ListOrder {
			t.Fatalf("重启后第 %d 条不一致:\n before %+v\n after  %+v",
				i, before.Items[i], after.Items[i])
		}
	}
	if after.DefaultID != before.DefaultID {
		t.Fatalf("默认工作空间 id 不该变: %q → %q", before.DefaultID, after.DefaultID)
	}
}

// TestWorkspaceRegistryMemoryModeIsReadOnlyHTTP memory 持久层：读面空 + 不可写，写面 40501。
func TestWorkspaceRegistryMemoryModeIsReadOnlyHTTP(t *testing.T) {
	svc := sessionruntime.NewService(runtime.NewRegistry(), eventbus.New()) // 无库 = memory
	env := doRaw(t, svc, "GET", "/v1/mate/workspaces", "")
	var parsed struct {
		Code int                            `json:"code"`
		Data dtoworkspace.WorkspaceListResp `json:"data"`
	}
	if err := json.Unmarshal(env, &parsed); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if parsed.Code != 0 {
		t.Fatalf("memory 下读面不该报错: %+v", parsed)
	}
	if len(parsed.Data.Items) != 0 || parsed.Data.Writable {
		t.Fatalf("memory 下应是空列表 + 不可写: %+v", parsed.Data)
	}

	// 写面：明确 40501（不是 50000——"换个持久层就好"与"平台坏了"必须分得开）。
	env = doRaw(t, svc, "POST", "/v1/mate/workspaces", `{"path":"/tmp"}`)
	var failed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(env, &failed); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if failed.Code != 40501 || !strings.Contains(failed.Msg, "workspace_schema_read_only") {
		t.Fatalf("memory 下创建应 40501 workspace_schema_read_only: %+v", failed)
	}
}

// doRaw 走真实路由表发一次请求，返回原始信封字节。
func doRaw(t *testing.T, svc *sessionruntime.Service, method, path, body string) []byte {
	t.Helper()
	engine := router.New(testRouterDeps(t, testRouterOpts{service: svc, bus: eventbus.New()}))
	var rd *strings.Reader
	rd = strings.NewReader(body)
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("%s %s HTTP %d: %s", method, path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func idsOf(items []dtoworkspace.WorkspaceResp) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}
