package svcapp_test

// ——— WP-C6：生命周期状态机 + install/enable/disable/uninstall（用假运行期端口驱动）———
//
// 这里用**假 AppRuntimePort**（记录起停、可控状态）验证 svcapp 侧的生命周期语义：
//   - install 装包 + 建定义（默认 off）；
//   - enable 经端口起运行期 → state=active；disable → state=off；
//   - state 派生逐态（off/pending/active/failed/disposed）；
//   - 未接端口 → Install/Uninstall 拒绝（app_runtime_unavailable）。
// 真 host 端到端在 bootstrap/assembly 的 app_lifecycle_test.go（真子进程）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/service/svcapp"
)

// fakeRuntime 记录起停、可控状态、可控 HasPack。
type fakeRuntime struct {
	enabled  map[string]bool
	packs    map[string]bool
	state    map[string]svcapp.RuntimeState
	enabledC int
	disablC  int
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{enabled: map[string]bool{}, packs: map[string]bool{}, state: map[string]svcapp.RuntimeState{}}
}

func (f *fakeRuntime) EnableApp(_ context.Context, app svcapp.AppRuntimeRef) error {
	f.enabled[app.ID] = true
	f.enabledC++
	f.state[app.ID] = svcapp.RuntimeState{State: svcapp.AppStateActive, HasHost: true}
	return nil
}
func (f *fakeRuntime) DisableApp(_ context.Context, app svcapp.AppRuntimeRef) error {
	delete(f.enabled, app.ID)
	f.disablC++
	f.state[app.ID] = svcapp.RuntimeState{State: svcapp.AppStateOff}
	return nil
}
func (f *fakeRuntime) RuntimeState(_ context.Context, app svcapp.AppRuntimeRef) svcapp.RuntimeState {
	if s, ok := f.state[app.ID]; ok {
		return s
	}
	return svcapp.RuntimeState{State: svcapp.AppStateOff}
}
func (f *fakeRuntime) HasPack(id string) bool { return f.packs[id] }
func (f *fakeRuntime) UninstallPack(_ context.Context, id string) (bool, error) {
	delete(f.packs, id)
	delete(f.enabled, id)
	return true, nil
}

// writeSrcPack 造一个可装载样例包（无 min_core_version，避免 dev 门槛 fail-closed）。integrity 由 apppkg 校验。
func writeSrcPack(t *testing.T, srcRoot, id string) string {
	t.Helper()
	dir := filepath.Join(srcRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := []byte("#!/bin/sh\nexec mate app-host --pkg=.\n")
	if err := os.WriteFile(filepath.Join(dir, "entrypoint.sh"), entry, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256hex(entry)
	manifest := `{"id":"` + id + `","version":"1.0.0","api_version":"^1","provides":{"impls":[{"impl_id":"` + id + `.sync","kind":"slot"}],"tools":[{"domain":"` + id + `","tools":["echo"]}]},"entrypoint":"entrypoint.sh","integrity":{"sha256":"` + sum + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInstallEnableDisableUninstall(t *testing.T) {
	_, st := newSvc(t)
	appsRoot := t.TempDir()
	rt := newFakeRuntime()
	svc2 := svcapp.NewApp(st, svcapp.WithRuntimePort(rt), svcapp.WithAppsRoot(appsRoot))
	ctx := context.Background()
	src := writeSrcPack(t, t.TempDir(), "myapp")

	// install（不启用）：落地 + 定义 off。
	inst, err := svc2.InstallApp(ctx, dtoapp.AppInstallReq{Dir: src})
	if err != nil {
		t.Fatalf("install 失败：%v", err)
	}
	if !inst.Installed {
		t.Fatalf("应安装成功：%+v", inst)
	}
	if _, err := os.Stat(filepath.Join(appsRoot, "myapp", "manifest.json")); err != nil {
		t.Fatalf("install 后本地应用目录应有包：%v", err)
	}
	// 假运行期的 packs 由真实现维护；这里装完手动置 hasPack 供 state 派生（模拟装到了本地）。
	rt.packs["myapp"] = true
	got, err := svc2.GetApp(ctx, "myapp")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.Enabled {
		t.Fatal("install(enable=false) 后应为未启用")
	}
	if got.State != svcapp.AppStateOff {
		t.Fatalf("未启用 state 应 off，got %q", got.State)
	}

	// enable → active。
	if _, err := svc2.SetAppEnabled(ctx, "myapp", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !rt.enabled["myapp"] || rt.enabledC != 1 {
		t.Fatalf("enable 应驱动端口 EnableApp，got enabled=%v calls=%d", rt.enabled["myapp"], rt.enabledC)
	}
	if got, _ := svc2.GetApp(ctx, "myapp"); got.State != svcapp.AppStateActive {
		t.Fatalf("enable 后 state 应 active，got %q", got.State)
	}

	// disable → off + 端口 DisableApp（清零计数：install 期为强制 enabled=0 已驱动过一次 disable）。
	rt.disablC = 0
	if _, err := svc2.SetAppEnabled(ctx, "myapp", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if rt.enabled["myapp"] || rt.disablC != 1 {
		t.Fatalf("disable 应驱动端口 DisableApp，got enabled=%v disables=%d", rt.enabled["myapp"], rt.disablC)
	}
	if got, _ := svc2.GetApp(ctx, "myapp"); got.State != svcapp.AppStateOff {
		t.Fatalf("disable 后 state 应 off，got %q", got.State)
	}

	// uninstall → 包目录删 + 定义删。
	un, err := svc2.UninstallApp(ctx, "myapp")
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !un.Uninstalled || !un.PackRemoved || !un.DefinitionDeleted {
		t.Fatalf("uninstall 结果不对：%+v", un)
	}
	if rt.packs["myapp"] {
		t.Fatal("uninstall 后运行期端口应清掉该包（UninstallPack 被调用）")
	}
	if _, err := svc2.GetApp(ctx, "myapp"); err == nil {
		t.Fatal("uninstall 后定义应删除")
	}
}

func TestInstallUninstallRequireRuntimePort(t *testing.T) {
	svc, _ := newSvc(t) // 无运行期端口
	ctx := context.Background()
	if _, err := svc.InstallApp(ctx, dtoapp.AppInstallReq{Dir: t.TempDir()}); err == nil {
		t.Fatal("未接运行期端口时 InstallApp 应拒绝（app_runtime_unavailable）")
	}
	if _, err := svc.UninstallApp(ctx, "x"); err == nil {
		t.Fatal("未接运行期端口时 UninstallApp 应拒绝（app_runtime_unavailable）")
	}
}

// sha256hex 计算内容的十六进制 sha256（样例包 integrity 用）。
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
