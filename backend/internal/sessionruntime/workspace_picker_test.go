package sessionruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	mateRuntime "runtime"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 宿主原生目录选择器的**装配面**用例 ———
//
// 判定与配方的纯函数用例已随实现搬进 `internal/domain/workspace/picker_test.go`；这里留的是
// "装配起来的那份 `Service`"（转发层 + 真 `NewService` + 真进程环境 + 真 spawn）的用例：
// 它验的是**装配期注入的监听地址到底有没有参与判定**、以及端点在最坏情况下会不会去 spawn。
// 两边不互相替代，所以不合并。

// TestPickDirectoryHostUnsupportedWhenNotHost 宿主不是 native 形态时，端点**明确拒绝**而不是去 spawn。
//
// 这条挡的是"能力声明与执行面不一致"时的最坏形态：在远程/SSH 场景下弹出一个没人看得见的系统面板。
func TestPickDirectoryHostUnsupportedWhenNotHost(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.Planes().Picker.SetBindAddr("0.0.0.0:10001") // 对外监听 → browse 形态
	if got := svc.Planes().Picker.NativePickerMode(); got != string(workspace.NativePickerBrowse) {
		t.Fatalf("对外监听应为 browse 形态: %s", got)
	}
	_, _, err := svc.Planes().Picker.PickDirectoryHost(context.Background())
	if err == nil || !errors.Is(err, workspace.ErrNativePickUnsupported) {
		t.Fatalf("非 native 形态应 native_pick_unsupported: %v", err)
	}
	if code := aclCode(t, err); code != "native_pick_unsupported" {
		t.Fatalf("错误码应为 native_pick_unsupported: %s", code)
	}
}

// TestPickDirectoryHostReportsUnsupportedOnThisPlatform 本机（测试进程所在平台）若不支持原生选择器，
// 端点同样给可判定错误；支持时**不去真开面板**（那会把测试挂住）——只断言判定与错误分类。
func TestPickDirectoryHostReportsUnsupportedOnThisPlatform(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.Planes().Picker.SetBindAddr("127.0.0.1:10001")

	switch mateRuntime.GOOS {
	case "darwin":
		// 本机是 macOS：形态必须是 native（回环 + 非 SSH + 有显示会话）。
		if got := svc.Planes().Picker.NativePickerMode(); got != string(workspace.NativePickerHost) {
			t.Fatalf("macOS 本机应为 native 形态: %s", got)
		}
		// 刻意**不调** PickDirectoryHost：它会真的弹出系统面板并挂住测试。
	default:
		// 其它平台（含本仓未实现的 win32）：形态至少要是可判定的两值之一，
		// 且"不支持"必须以 ErrNativePickUnsupported 表达，而不是笼统 50000。
		mode := svc.Planes().Picker.NativePickerMode()
		if mode != string(workspace.NativePickerHost) && mode != string(workspace.NativePickerBrowse) {
			t.Fatalf("形态只有 native|browse 两个取值: %q", mode)
		}
	}
}

// TestSetBindAddrFeedsPickerMode 装配期注入的监听地址确实参与判定（这是装配顺序的唯一接口）。
func TestSetBindAddrFeedsPickerMode(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	// 未注入地址（""）→ 解析不出来 → 保守判 browse。
	if got := svc.Planes().Picker.NativePickerMode(); got != string(workspace.NativePickerBrowse) {
		t.Fatalf("未注入监听地址时应保守判 browse: %s", got)
	}
	svc.Planes().Picker.SetBindAddr("127.0.0.1:10001")
	got := svc.Planes().Picker.NativePickerMode()
	if mateRuntime.GOOS == "darwin" && got != string(workspace.NativePickerHost) {
		t.Fatalf("macOS 注入回环地址后应为 native: %s", got)
	}
	if mateRuntime.GOOS == "darwin" {
		return
	}
	if got != string(workspace.NativePickerBrowse) {
		t.Fatalf("非 darwin 平台应判 browse: %s", got)
	}
}

// TestPickEnvReadsSSHFromProcess 进程环境里的 SSH 键会真的影响判定（不是只认注入的字段）。
func TestPickEnvReadsSSHFromProcess(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 1 10.0.0.2 2")
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.Planes().Picker.SetBindAddr("127.0.0.1:10001")
	if got := svc.Planes().Picker.NativePickerMode(); got != string(workspace.NativePickerBrowse) {
		t.Fatalf("SSH 会话应判 browse（面板会开在没人看的屏幕上）: %s", got)
	}
}

// TestPickDirectoryHostSpawnsAndParsesHostPicker 端到端：核心**真的 spawn**宿主命令，
// 并把 stdout 的路径原样回给调用方；取消（退出码 1 + stderr 含 cancel）回 `( "", true)`。
//
// 为什么可以用假 osascript 而不算"假验"：被测面是**宿主命令的 spawn 与解析**（配方的正确性由
// `domain/workspace` 的 `TestPlanNativePick` 与真机探针各自覆盖）。真机探针在本机跑过一次
// （核心确实停在等 NSOpenPanel），但那条不能进自动化（需要真人回答面板）。
//
// 平台闸门：非 darwin 直接 skip——在那种平台上传 `osascript` 假货只会走到另一个配方（zenity/kdialog），
// 断言就变成在测别的东西。
func TestPickDirectoryHostSpawnsAndParsesHostPicker(t *testing.T) {
	if mateRuntime.GOOS != "darwin" {
		t.Skip("本用例钉的是 macOS 的 osascript 配方；其它平台的配方由 domain/workspace 的用例覆盖")
	}

	// ① 选定：假 osascript 往 stdout 打一行路径（末尾带换行，模拟真实行为）。
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "osascript")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$FAKE_PICK_PATH\"\n"), 0o755); err != nil {
		t.Fatalf("写假 osascript: %v", err)
	}
	t.Setenv("FAKE_PICK_PATH", "/tmp/mate-picked")
	t.Setenv("PATH", binDir) // 只留假货：确保打的是它

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.Planes().Picker.SetBindAddr("127.0.0.1:10001") // 回环 + 非 SSH + 有显示会话 → native
	if got := svc.Planes().Picker.NativePickerMode(); got != string(workspace.NativePickerHost) {
		t.Fatalf("本机 macOS 应为 native 形态: %s", got)
	}
	path, cancelled, err := svc.Planes().Picker.PickDirectoryHost(context.Background())
	if err != nil {
		t.Fatalf("PickDirectoryHost: %v", err)
	}
	if cancelled || path != "/tmp/mate-picked" {
		t.Fatalf("应回选中的路径（且已去掉行尾换行）: path=%q cancelled=%v", path, cancelled)
	}

	// ② 取消：退出码 1 + stderr 含 "User canceled" → ( "", true)，**不是错误**。
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'execution error: User canceled. (-128)' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("改写假 osascript: %v", err)
	}
	path, cancelled, err = svc.Planes().Picker.PickDirectoryHost(context.Background())
	if err != nil {
		t.Fatalf("取消不该是错误: %v", err)
	}
	if !cancelled || path != "" {
		t.Fatalf("取消应回 (\"\", true): path=%q cancelled=%v", path, cancelled)
	}

	// ③ 其它失败（退出码非取消码）→ 服务侧错误，且带上命令名（否则只剩一句"退出码 N"）。
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'boom' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatalf("改写假 osascript: %v", err)
	}
	if _, _, err := svc.Planes().Picker.PickDirectoryHost(context.Background()); err == nil || aclCode(t, err) != "native_pick_failed" {
		t.Fatalf("非取消失败应 native_pick_failed: %v", err)
	}
}
