package workspace

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// ——— 宿主原生目录选择器（v14.5，对齐 dsh 的 directory-picker-auto / -native）———
//
// 这些用例随实现一起搬进本域（`interaction` 那一批之后的做法：**测试跟着所有者走**）。
// 留在 manager 的是"装配起来的那份 Service"的用例（转发层 + 真 `NewService`），两批不互相替代。

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
	// 采样器读的是**进程环境**（不是注入的 map）——这一条与上面的注入版配对：
	// 只测注入版会漏掉"真去读了 os.Getenv 吗"。
	t.Setenv("SSH_CONNECTION", "10.0.0.1 1 10.0.0.2 2")
	if !sshSession(os.Getenv) {
		t.Fatal("sshSession 应认进程环境里的 SSH_CONNECTION")
	}
}
