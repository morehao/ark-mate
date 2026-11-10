package assembly

// ——— WP-C6 端到端：install → enable → tools/list → invokeTool → disable → 工具归零 → uninstall ———
//
// 真·子进程：把当前测试二进制以 helper 模式重新执行当 hostkit 服务端（与 infra/apphost/proc_test.go
// 同款），验证「运行期应用包的工具经隔离子进程真正可列可调」「disable 后精确撤销、工具归零」「uninstall
// 清目录」这条二期 C 的闭环。用 t.TempDir()，绝不碰用户 ~/.dsh、绝不真装不可信代码。
//
// 覆盖点：
//   - 状态机：off（装完未启用）→ active（enable）→ off（disable）→（uninstall 后定义也没了）。
//   - RuntimeAppToolScope 生产实现：enable 后 RuntimeAppServers 返回 `mate mcp app --apps=` 桥条目；
//     disable 后返回空（该会话下次 tools/list 归零）。
//   - 工具面真执行：apptool 聚合运行期 host → ListTools 出现该包工具 → InvokeTool 回显成功。
//   - 精确撤销：disable 后 host 关闭、工具面归零。
//   - uninstall：本地包目录被删、定义被删。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/infra/apphost"
	"github.com/morehao/ark-mate/internal/infra/apphost/hostkit"
	"github.com/morehao/ark-mate/internal/infra/apptool"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcapp"
	_ "github.com/morehao/golib/glog/driver/zap"
)

const appLifecycleHelperEnv = "MATE_APPHOST_LIFECYCLE_HELPER"

// TestMain 在 helper 模式下充当 app-host 子进程入口（跑 hostkit.Serve 的样例 echo handler）；
// 正常模式下跑本包全部测试。
func TestMain(m *testing.M) {
	if os.Getenv(appLifecycleHelperEnv) == "1" {
		runLifecycleHelperHost()
		return
	}
	os.Exit(m.Run())
}

// runLifecycleHelperHost 是本测试二进制作为子进程 host 时的 body：hostkit.Serve 一个 echo handler。
func runLifecycleHelperHost() {
	srv := hostkit.NewServer(&sampleEchoHandlers{})
	_ = srv.Serve(os.Stdin, os.Stdout)
}

// sampleEchoHandlers 样例包宿主：工具 myapp_echo / myapp_ping，执行回显入参（证明 invoke 链路真通）。
type sampleEchoHandlers struct{}

func (sampleEchoHandlers) Describe() hostkit.DescribeInfo {
	return hostkit.DescribeInfo{PackageID: "myapp", PackageVersion: "1.0.0", APIVersion: "^1",
		Impls: []string{"myapp.sync"}, Tools: []string{"myapp_echo", "myapp_ping"}}
}
func (sampleEchoHandlers) ListImpls() []string { return []string{"myapp.sync"} }
func (sampleEchoHandlers) ListTools() []apphost.ToolSpec {
	return []apphost.ToolSpec{{Name: "myapp_echo"}, {Name: "myapp_ping"}}
}
func (sampleEchoHandlers) InvokeTool(name string, args json.RawMessage) (json.RawMessage, bool, error) {
	out, _ := json.Marshal([]map[string]any{{"type": "text", "text": "echo:" + name + ":" + string(args)}})
	return json.RawMessage(out), false, nil
}
func (sampleEchoHandlers) ApplySlot(string, map[string]string) error { return nil }
func (sampleEchoHandlers) DisposeSlot(string) error                  { return nil }

// lifecycleSpawn 返回一个把当前测试二进制以 helper 模式拉起的 SpawnFunc（真子进程）。
func lifecycleSpawn(t *testing.T) apphost.SpawnFunc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试可执行文件失败：%v", err)
	}
	return func(ctx context.Context, _ string) (io.WriteCloser, io.ReadCloser, func() error, func() error, error) {
		cmd := exec.CommandContext(ctx, exe, "-test.run=^$")
		cmd.Env = append(os.Environ(), appLifecycleHelperEnv+"=1")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, nil, nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, nil, err
		}
		return stdin, stdout, cmd.Wait, func() error { return cmd.Process.Kill() }, nil
	}
}

// writeSamplePack 在 srcRoot 下造一个可装载样例包目录 `<srcRoot>/myapp/`（manifest + entrypoint，integrity 正确）。
// 不含 requires.min_core_version（避免 dev 版本门槛 fail-closed），供 install 复制。
func writeSamplePack(t *testing.T, srcRoot string) string {
	t.Helper()
	pkgDir := filepath.Join(srcRoot, "myapp")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := []byte("#!/bin/sh\nexec mate app-host --pkg=.\n")
	if err := os.WriteFile(filepath.Join(pkgDir, "entrypoint.sh"), entry, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(entry)
	manifest := map[string]any{
		"id":          "myapp",
		"version":     "1.0.0",
		"api_version": "^1",
		"provides": map[string]any{
			"impls": []map[string]any{{"impl_id": "myapp.sync", "kind": "slot"}},
			"tools": []map[string]any{{"domain": "myapp", "tools": []string{"echo", "ping"}}},
		},
		"entrypoint": "entrypoint.sh",
		"integrity":  map[string]string{"sha256": hex.EncodeToString(sum[:])},
	}
	b, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pkgDir, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return pkgDir
}

// TestAppLifecycleEndToEnd 全链路：install → enable（状态 active，工具面可见可调）→ disable（工具归零）→ uninstall（清目录+删定义）。
func TestAppLifecycleEndToEnd(t *testing.T) {
	ctx := context.Background()
	appsRoot := t.TempDir()
	srcRoot := t.TempDir()
	pkgDir := writeSamplePack(t, srcRoot)

	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	lc := newAppLifecycle(appsRoot, lifecycleSpawn(t))
	svc := svcapp.NewApp(appStore(st), svcapp.WithRuntimePort(lc), svcapp.WithAppsRoot(appsRoot))

	// ——— install（不启用）———
	inst, err := svc.InstallApp(ctx, dtoapp.AppInstallReq{Dir: pkgDir})
	if err != nil {
		t.Fatalf("install 失败：%v", err)
	}
	if !inst.Installed || inst.ID != "myapp" {
		t.Fatalf("install 结果不对：%+v", inst)
	}
	if _, err := os.Stat(filepath.Join(appsRoot, "myapp", "manifest.json")); err != nil {
		t.Fatalf("install 后本地应用目录应有包：%v", err)
	}
	// 定义已派生，state=off（有包未启用）。
	got, err := svc.GetApp(ctx, "myapp")
	if err != nil {
		t.Fatalf("GetApp 失败：%v", err)
	}
	if got.State != svcapp.AppStateOff {
		t.Fatalf("装完未启用 state 应为 off，got %q", got.State)
	}
	// 未启用：无运行期工具面。
	if servers := lc.RuntimeAppServers([]string{"myapp"}); len(servers) != 0 {
		t.Fatalf("未启用不该有运行期工具面，got %+v", servers)
	}

	// ——— enable（起 host + apply slot）———
	if _, err := svc.SetAppEnabled(ctx, "myapp", true); err != nil {
		t.Fatalf("enable 失败：%v", err)
	}
	got, _ = svc.GetApp(ctx, "myapp")
	if got.State != svcapp.AppStateActive {
		t.Fatalf("enable 后 state 应 active，got %q why=%q", got.State, got.Why)
	}
	// RuntimeAppToolScope 生产实现：返回 `mate mcp app --apps=myapp` 桥条目（会话注入面拿到它）。
	servers := lc.RuntimeAppServers([]string{"myapp"})
	if len(servers) != 1 {
		t.Fatalf("enable 后应有 1 条运行期工具面桥条目，got %+v", servers)
	}
	if servers[0].Args[len(servers[0].Args)-1] != "--apps=myapp" {
		t.Fatalf("桥条目参数应含 --apps=myapp，got %v", servers[0].Args)
	}

	// ——— 工具面真执行：直接用 host 聚合，ListTools 出现该包工具，InvokeTool 回显成功 ———
	lhost := lc.hosts["myapp"]
	if lhost == nil {
		t.Fatal("enable 后应持有一个 host")
	}
	runner := apphost.NewImplRunner(lhost)
	hs, rerr := apptool.ResolveRuntimeDomains("myapp", mcpHandlerAdapter{runner})
	if rerr != nil {
		t.Fatalf("读取运行期工具自述失败：%v", rerr)
	}
	domains, derr := apptool.SelectRuntimeDomains(hs, []string{"myapp"})
	if derr != nil {
		t.Fatalf("收窄运行期域失败：%v", derr)
	}
	agg, aerr := apptool.AggregateCompiledAndRuntime(nil, domains)
	if aerr != nil {
		t.Fatalf("聚合运行期工具面失败：%v", aerr)
	}
	tools, terr := agg.Handler().ListTools(ctx)
	if terr != nil {
		t.Fatalf("ListTools 失败：%v", terr)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if !containsStr(names, "myapp_echo") || !containsStr(names, "myapp_ping") {
		t.Fatalf("工具面应含 myapp_echo/myapp_ping，got %v", names)
	}
	res, cerr := agg.Handler().CallTool(ctx, "myapp_echo", json.RawMessage(`{"x":1}`))
	if cerr != nil {
		t.Fatalf("InvokeTool 失败：%v", cerr)
	}
	if len(res.Content) == 0 {
		t.Fatal("InvokeTool 应返回内容")
	}

	// ——— disable（dispose + 停 host）→ 工具归零 ———
	if _, err := svc.SetAppEnabled(ctx, "myapp", false); err != nil {
		t.Fatalf("disable 失败：%v", err)
	}
	got, _ = svc.GetApp(ctx, "myapp")
	if got.State != svcapp.AppStateOff {
		t.Fatalf("disable 后 state 应 off，got %q", got.State)
	}
	if servers := lc.RuntimeAppServers([]string{"myapp"}); len(servers) != 0 {
		t.Fatalf("disable 后运行期工具面必须归零（关得干净），got %+v", servers)
	}
	if h := lc.hosts["myapp"]; h != nil {
		t.Fatalf("disable 后不该还持有 host（应已关闭），got %v", h.State())
	}

	// ——— uninstall（清目录 + 删定义）———
	un, err := svc.UninstallApp(ctx, "myapp")
	if err != nil {
		t.Fatalf("uninstall 失败：%v", err)
	}
	if !un.Uninstalled || !un.PackRemoved || !un.DefinitionDeleted {
		t.Fatalf("uninstall 结果不对：%+v", un)
	}
	if _, err := os.Stat(filepath.Join(appsRoot, "myapp")); !os.IsNotExist(err) {
		t.Fatalf("uninstall 后本地包目录应被删除，stat err=%v", err)
	}
	if _, err := svc.GetApp(ctx, "myapp"); err == nil {
		t.Fatal("uninstall 后定义应已删除（GetApp 应报 not_found）")
	}
}

// TestAppLifecycleInstallRejectsTampered 篡改包（entrypoint 与 manifest sha256 不符）→ 安装被拒（app_invalid）。
func TestAppLifecycleInstallRejectsTampered(t *testing.T) {
	ctx := context.Background()
	appsRoot := t.TempDir()
	srcRoot := t.TempDir()
	pkgDir := writeSamplePack(t, srcRoot)
	// 篡改 entrypoint（改内容但不动 manifest 的 sha256）。
	if err := os.WriteFile(filepath.Join(pkgDir, "entrypoint.sh"), []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lc := newAppLifecycle(appsRoot, lifecycleSpawn(t))
	svc := svcapp.NewApp(appStore(st), svcapp.WithRuntimePort(lc), svcapp.WithAppsRoot(appsRoot))
	if _, err := svc.InstallApp(ctx, dtoapp.AppInstallReq{Dir: pkgDir}); err == nil {
		t.Fatal("被篡改的包必须安装失败（完整性校验 fail-closed）")
	}
	if _, err := os.Stat(filepath.Join(appsRoot, "myapp")); !os.IsNotExist(err) {
		t.Fatal("安装被拒后本地应用目录不该落地")
	}
}

// ——— 辅助 ———

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// mcpHandlerAdapter 把一个 ImplRunner 包成 apptool.ToolInvoker（ToolSpec→ToolDesc）。
type mcpHandlerAdapter struct{ runner *apphost.ImplRunner }

func (a mcpHandlerAdapter) ListTools() ([]apptool.ToolDesc, error) {
	specs, err := a.runner.ListTools()
	if err != nil {
		return nil, err
	}
	out := make([]apptool.ToolDesc, 0, len(specs))
	for _, s := range specs {
		out = append(out, apptool.ToolDesc{Name: s.Name, Description: s.Description, InputSchema: []byte(s.InputSchema)})
	}
	return out, nil
}
func (a mcpHandlerAdapter) InvokeTool(name string, args []byte) ([]byte, bool, error) {
	return a.runner.InvokeTool(name, args)
}

// TestInstallEnableStartsHost 回归：install 时 enable=true（新建定义）必须**真正拉起运行期 host** 并到 active。
//
// 真起 mate serve 验证暴露的第二个 bug：InstallApp 用 `cur.Enabled != req.Enable` 决定是否驱动运行期，
// 但新建定义经 gorm `default:1` 读回 enabled=true，于是 install.enable=true 时 true!=true=false → 跳过
// SetAppEnabled → host 从未被拉起，install 返回 enabled=true 但 state 停在 pending（无 host 子进程）。
// 本测试用 lifecycleSpawn(t) 真子进程断言：install(enable=true) 后 state=active 且持有 ready 的 host。
func TestInstallEnableStartsHost(t *testing.T) {
	ctx := context.Background()
	appsRoot := t.TempDir()
	srcRoot := t.TempDir()
	pkgDir := writeSamplePack(t, srcRoot)

	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	lc := newAppLifecycle(appsRoot, lifecycleSpawn(t))
	t.Cleanup(lc.closeAll)
	svc := svcapp.NewApp(appStore(st), svcapp.WithRuntimePort(lc), svcapp.WithAppsRoot(appsRoot))

	// install 时直接 enable=true。
	inst, err := svc.InstallApp(ctx, dtoapp.AppInstallReq{Dir: pkgDir, Enable: true})
	if err != nil {
		t.Fatalf("install(enable=true) 失败：%v", err)
	}
	if !inst.Installed || !inst.Enabled {
		t.Fatalf("install 结果应为 installed 且 enabled，got %+v", inst)
	}
	// 关键：state 必须 active（不是 pending），且真持有 ready 的 host 子进程。
	got, err := svc.GetApp(ctx, "myapp")
	if err != nil {
		t.Fatalf("GetApp 失败：%v", err)
	}
	if got.State != svcapp.AppStateActive {
		t.Fatalf("install(enable=true) 后 state 应 active（并真正起了 host），got %q why=%q", got.State, got.Why)
	}
	h := lc.hosts["myapp"]
	if h == nil || h.State() != apphost.StateReady {
		t.Fatalf("install(enable=true) 后应持有 ready 的 host，got host=%v", h)
	}
}

// TestEnableAppSurvivesRequestCtxCancel 回归：enable 的入参 ctx（=HTTP 请求 ctx）被 cancel 后，
// 长驻 app-host 子进程**必须存活**、Host 保持 active——因为 host 绑的是进程级 baseCtx 而非请求 ctx。
//
// 真起 mate serve 验证暴露的 bug：EnableApp 曾把入参 ctx 传给 h.Start → ExecSpawn 的 exec.CommandContext
// 绑请求 ctx → handler 返回、请求 ctx cancel 时 host 子进程被杀 → enable 立刻 state=failed。
// 本测试用 lifecycleSpawn(t)（真子进程 helper host）复现：若回退到「绑入参 ctx」，cancel 后 host 会变 failed。
func TestEnableAppSurvivesRequestCtxCancel(t *testing.T) {
	srcRoot := t.TempDir()
	_ = writeSamplePack(t, srcRoot)                   // 造 <srcRoot>/myapp/
	lc := newAppLifecycle(srcRoot, lifecycleSpawn(t)) // 直接以 srcRoot 为 appsRoot，findPackage 能扫到 myapp
	t.Cleanup(lc.closeAll)

	ref := svcapp.AppRuntimeRef{ID: "myapp", ImplIDs: []string{"myapp.sync"}}

	// 模拟一次 HTTP 请求：用可 cancel 的 ctx 调 EnableApp，成功后 cancel（= handler 返回）。
	reqCtx, cancel := context.WithCancel(context.Background())
	if err := lc.EnableApp(reqCtx, ref); err != nil {
		cancel()
		t.Fatalf("EnableApp 失败：%v", err)
	}
	h := lc.hosts["myapp"]
	if h == nil {
		cancel()
		t.Fatal("enable 后应持有 host")
	}
	if h.State() != apphost.StateReady {
		cancel()
		t.Fatalf("enable 后 host 应 ready，got %v", h.State())
	}
	// 关键：cancel 请求 ctx（模拟 handler 返回），host 必须**不受影响**。
	cancel()
	// 给 supervise 一个窗口观测子进程是否被误杀。
	time.Sleep(400 * time.Millisecond)
	if st := h.State(); st != apphost.StateReady {
		t.Fatalf("请求 ctx cancel 后 host 必须仍 ready（绑 baseCtx），got %v reason=%q", st, h.Reason())
	}
	// 经 Host 的工具面仍可列可调（真链路未断）。
	runner := apphost.NewImplRunner(h)
	specs, err := runner.ListTools()
	if err != nil {
		t.Fatalf("cancel 后 ListTools 失败：%v", err)
	}
	found := false
	for _, s := range specs {
		if s.Name == "myapp_echo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cancel 后工具面应仍含 myapp_echo，got %+v", specs)
	}

	// 收尾：closeAll 应能干净关掉（baseCtx cancel + Close）。
	lc.closeAll()
	if st := h.State(); st == apphost.StateReady {
		t.Fatalf("closeAll 后 host 不该还是 ready，got %v", st)
	}
}
