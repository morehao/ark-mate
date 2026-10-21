package installed

import (
	"github.com/morehao/ark-mate/internal/runtime"

	"os"
	"testing"
)

// TestInstalled 命令可执行判定：PATH 查找与路径 stat 两分支 + 空命令 + 远程端点分支。
//
// 它是诊断面 remediation 的判据来源（「runtime 未装配」vs「已装配但命令不可执行」是两种不同的
// 修复动作），所以两分支与三种边界都要钉住。
func TestInstalled(t *testing.T) {
	exe, _ := os.Executable()
	if !Installed(runtime.Spec{Launch: runtime.LaunchSpec{Command: exe}}) {
		t.Fatal("绝对路径可执行文件应判 installed")
	}
	if Installed(runtime.Spec{Launch: runtime.LaunchSpec{Command: "ghost-cmd-not-exist-zzz-9f3a"}}) {
		t.Fatal("PATH 找不到应判未安装")
	}
	if Installed(runtime.Spec{}) {
		t.Fatal("空命令应判未安装")
	}
	// 远程 runtime（A5）：无本地命令，端点非空即视为可用（可达性由拨号判定）。
	if !Installed(runtime.Spec{Launch: runtime.LaunchSpec{Transport: "ws", Endpoint: "ws://127.0.0.1:1/acp"}}) {
		t.Fatal("远程 runtime 端点非空应判 installed")
	}
	if !Installed(runtime.Spec{Launch: runtime.LaunchSpec{Transport: "http", Endpoint: "http://127.0.0.1:1/acp"}}) {
		t.Fatal("远程 http runtime 端点非空应判 installed")
	}
	if Installed(runtime.Spec{Launch: runtime.LaunchSpec{Transport: "ws"}}) {
		t.Fatal("远程 runtime 端点为空应判未安装")
	}
}

// TestCommandExecutable 直接判定的两个分支（含"是目录"这种最容易漏的假阳）。
func TestCommandExecutable(t *testing.T) {
	if CommandExecutable("") {
		t.Fatal("空命令应为 false")
	}
	if CommandExecutable(t.TempDir()) {
		t.Fatal("目录应为 false（不是可执行文件）")
	}
	exe, _ := os.Executable()
	if !CommandExecutable(exe) {
		t.Fatal("当前测试二进制应为 true")
	}
}
