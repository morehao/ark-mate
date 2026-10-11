package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	mateRuntime "runtime"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— 宿主原生目录选择器（v14.5，对齐 dsh 的 directory-picker-auto / -native）———

// TestPlanNativePick 命令配方按平台给出，且"没有可用选择器"是**可判定**的错误。
//
// 为什么这些配方要钉住：它们是"原生面板"这半的全部内容，而错法是**静默**的——
// 配方写错（参数顺序、缺 title）只会在用户点下去的那一刻表现成"什么都没发生"或
// "面板里选不了目录"，测试里看不见。
func TestPlanNativePick(t *testing.T) {
	has := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}

	// macOS：osascript 的 choose folder（且必须把 POSIX path 打出来）。
	plan, err := planNativePick("darwin", has("osascript"))
	if err != nil {
		t.Fatalf("darwin 应可用: %v", err)
	}
	if plan.Exe != "osascript" {
		t.Fatalf("darwin 应使用 osascript: %+v", plan)
	}
	joined := strings.Join(plan.Args, " ")
	if !strings.Contains(joined, "choose folder") || !strings.Contains(joined, "POSIX path") {
		t.Fatalf("osascript 配方必须既 choose folder 又打印 POSIX path: %v", plan.Args)
	}
	if plan.CancelExit != 1 || !strings.Contains(strings.ToLower(plan.CancelStderr), "cancel") {
		t.Fatalf("取消判据应是「退出码 1 + stderr 含 cancel」: %+v", plan)
	}

	// Linux：优先 zenity，缺了退 kdialog；两个都没有 → 不可用（可判定）。
	z, err := planNativePick("linux", has("zenity", "kdialog"))
	if err != nil || z.Exe != "zenity" || !containsArg(z.Args, "--directory") {
		t.Fatalf("linux 应优先 zenity 且带 --directory: %+v err=%v", z, err)
	}
	k, err := planNativePick("linux", has("kdialog"))
	if err != nil || k.Exe != "kdialog" {
		t.Fatalf("缺 zenity 时应退 kdialog: %+v err=%v", k, err)
	}
	if _, err := planNativePick("linux", has()); !errors.Is(err, ErrNativePickUnsupported) {
		t.Fatalf("两个都没有时应 native_pick_unsupported: %v", err)
	}

	// Windows / 其它平台：**明确不支持**（本仓没有 COM/koffi 那条路，退回应用内浏览比写一个
	// 未经验证的 PowerShell 版更诚实）。
	for _, p := range []string{"win32", "freebsd"} {
		if _, err := planNativePick(p, has("zenity", "kdialog", "osascript")); !errors.Is(err, ErrNativePickUnsupported) {
			t.Fatalf("%s 应明确不支持: %v", p, err)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestNativePickerMode 三条宿主判据（绑回环 / 非 SSH / 有显示会话）——与参照实现逐条对齐。
//
// **这条钉的是"本机浏览器也能用系统面板"**：判定只看宿主事实，不看客户端是不是桌面壳。
func TestNativePickerMode(t *testing.T) {
	all := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	darwinOnly := all("osascript")

	cases := []struct {
		name string
		env  pickEnv
		want NativePickerMode
	}{
		{"本机 macOS（回环 + 非 SSH）→ 原生", pickEnv{
			platform: "darwin", bindAddr: "127.0.0.1:10001", lookPath: darwinOnly,
		}, NativePickerHost},
		{"桌面态内核分配端口（127.0.0.1:52xxx）→ 原生", pickEnv{
			platform: "darwin", bindAddr: "127.0.0.1:52620", lookPath: darwinOnly,
		}, NativePickerHost},
		{"对外监听 → 应用内浏览（对面的人看不见宿主屏幕）", pickEnv{
			platform: "darwin", bindAddr: "0.0.0.0:10001", lookPath: darwinOnly,
		}, NativePickerBrowse},
		{"SSH 会话 → 应用内浏览（面板会开在没人的屏幕前）", pickEnv{
			platform: "darwin", bindAddr: "127.0.0.1:10001", ssh: true, lookPath: darwinOnly,
		}, NativePickerBrowse},
		{"Linux 无显示会话 → 应用内浏览", pickEnv{
			platform: "linux", bindAddr: "127.0.0.1:10001", display: "", lookPath: all("zenity"),
		}, NativePickerBrowse},
		{"Linux 有显示会话且有 zenity → 原生", pickEnv{
			platform: "linux", bindAddr: "127.0.0.1:10001", display: ":0", lookPath: all("zenity"),
		}, NativePickerHost},
		{"Linux 无选择器二进制 → 应用内浏览", pickEnv{
			platform: "linux", bindAddr: "127.0.0.1:10001", display: ":0", lookPath: all(),
		}, NativePickerBrowse},
		{"Windows → 应用内浏览（本仓那条路未实现，明确退化）", pickEnv{
			platform: "win32", bindAddr: "127.0.0.1:10001", lookPath: all(),
		}, NativePickerBrowse},
		{"地址解析不出来 → 应用内浏览（拿不准就当对外）", pickEnv{
			platform: "darwin", bindAddr: "garbage", lookPath: darwinOnly,
		}, NativePickerBrowse},
	}
	for _, tc := range cases {
		if got := nativePickerMode(tc.env); got != tc.want {
			t.Fatalf("%s: nativePickerMode=%s want %s", tc.name, got, tc.want)
		}
	}
}

// TestSSHSessionEnvKeys SSH 判据只看 sshd 注入的那三个键（**不读配置文件**：`.env` 里的值
// 表达不了"这次是怎么起的"）。
func TestSSHSessionEnvKeys(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if sshSession(env(map[string]string{})) {
		t.Fatal("没有 SSH_* 键时不该判成 SSH 会话")
	}
	for _, k := range []string{"SSH_CONNECTION", "SSH_TTY", "SSH_CLIENT"} {
		if !sshSession(env(map[string]string{k: "x"})) {
			t.Fatalf("%s 存在时应判成 SSH 会话", k)
		}
		if sshSession(env(map[string]string{k: "   "})) {
			t.Fatalf("%s 只有空白时不该判成 SSH 会话", k)
		}
	}
}

// TestPickDirectoryHostUnsupportedWhenNotHost 宿主不是 native 形态时，端点**明确拒绝**而不是去 spawn。
//
// 这条挡的是"能力声明与执行面不一致"时的最坏形态：在远程/SSH 场景下弹出一个没人看得见的系统面板。
func TestPickDirectoryHostUnsupportedWhenNotHost(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetBindAddr("0.0.0.0:10001") // 对外监听 → browse 形态
	if got := svc.NativePickerMode(); got != string(NativePickerBrowse) {
		t.Fatalf("对外监听应为 browse 形态: %s", got)
	}
	_, _, err := svc.PickDirectoryHost(context.Background())
	if err == nil || !errors.Is(err, ErrNativePickUnsupported) {
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
	svc.SetBindAddr("127.0.0.1:10001")

	switch mateRuntime.GOOS {
	case "darwin":
		// 本机是 macOS：形态必须是 native（回环 + 非 SSH + 有显示会话）。
		if got := svc.NativePickerMode(); got != string(NativePickerHost) {
			t.Fatalf("macOS 本机应为 native 形态: %s", got)
		}
		// 刻意**不调** PickDirectoryHost：它会真的弹出系统面板并挂住测试。
	default:
		// 其它平台（含本仓未实现的 win32）：形态至少要是可判定的两值之一，
		// 且"不支持"必须以 ErrNativePickUnsupported 表达，而不是笼统 50000。
		mode := svc.NativePickerMode()
		if mode != string(NativePickerHost) && mode != string(NativePickerBrowse) {
			t.Fatalf("形态只有 native|browse 两个取值: %q", mode)
		}
	}
}

// TestSetBindAddrFeedsPickerMode 装配期注入的监听地址确实参与判定（这是装配顺序的唯一接口）。
func TestSetBindAddrFeedsPickerMode(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	// 未注入地址（""）→ 解析不出来 → 保守判 browse。
	if got := svc.NativePickerMode(); got != string(NativePickerBrowse) {
		t.Fatalf("未注入监听地址时应保守判 browse: %s", got)
	}
	svc.SetBindAddr("127.0.0.1:10001")
	got := svc.NativePickerMode()
	if mateRuntime.GOOS == "darwin" && got != string(NativePickerHost) {
		t.Fatalf("macOS 注入回环地址后应为 native: %s", got)
	}
	if mateRuntime.GOOS == "darwin" {
		return
	}
	if got != string(NativePickerBrowse) {
		t.Fatalf("非 darwin 平台应判 browse: %s", got)
	}
}

// TestPickEnvReadsSSHFromProcess 进程环境里的 SSH 键会真的影响判定（不是只认注入的字段）。
func TestPickEnvReadsSSHFromProcess(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 1 10.0.0.2 2")
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetBindAddr("127.0.0.1:10001")
	if got := svc.NativePickerMode(); got != string(NativePickerBrowse) {
		t.Fatalf("SSH 会话应判 browse（面板会开在没人看的屏幕上）: %s", got)
	}
	if !sshSession(os.Getenv) {
		t.Fatal("sshSession 应认 SSH_CONNECTION")
	}
}

// TestPickDirectoryHostSpawnsAndParsesHostPicker 端到端：核心**真的 spawn**宿主命令，
// 并把 stdout 的路径原样回给调用方；取消（退出码 1 + stderr 含 cancel）回 `( "", true)`。
//
// 为什么可以用假 osascript 而不算"假验"：被测面是**宿主命令的 spawn 与解析**（配方的正确性由
// `TestPlanNativePick` 与真机探针各自覆盖）。真机探针在本机跑过一次（核心确实停在等 NSOpenPanel），
// 但那条不能进自动化（需要真人回答面板）。
//
// 平台闸门：非 darwin 直接 skip——在那种平台上传 `osascript` 假货只会走到另一个配方（zenity/kdialog），
// 断言就变成在测别的东西。
func TestPickDirectoryHostSpawnsAndParsesHostPicker(t *testing.T) {
	if mateRuntime.GOOS != "darwin" {
		t.Skip("本用例钉的是 macOS 的 osascript 配方；其它平台的配方由 TestPlanNativePick 覆盖")
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
	svc.SetBindAddr("127.0.0.1:10001") // 回环 + 非 SSH + 有显示会话 → native
	if got := svc.NativePickerMode(); got != string(NativePickerHost) {
		t.Fatalf("本机 macOS 应为 native 形态: %s", got)
	}
	path, cancelled, err := svc.PickDirectoryHost(context.Background())
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
	path, cancelled, err = svc.PickDirectoryHost(context.Background())
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
	if _, _, err := svc.PickDirectoryHost(context.Background()); err == nil || aclCode(t, err) != "native_pick_failed" {
		t.Fatalf("非取消失败应 native_pick_failed: %v", err)
	}
}
