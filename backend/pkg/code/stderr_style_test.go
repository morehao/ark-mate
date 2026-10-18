package code

import (
	"strings"
	"testing"
)

// 真实形态（dsh 0.2.0-rc.2 从含保留键 .env 的目录启动时，核心日志里的 stderr 顺序；0.2.1-rc.2 复测同形）：
// 先一段 Node 的源码片段 + `Error: <真因>`，再是栈帧，最后是 Node 版本。
func TestStderrMsgSuffixNodeStyle(t *testing.T) {
	tail := []string{
		"\t\tthrow new Error(`${binName}: ${path} sets \"${name}\"...`);",
		"\t\t      ^",
		`Error: dsh: /repo/.env sets "DSH_VERSION", which only the launching environment may set`,
		"    at readEnvLayer (file:///dsh-app-boot/lib/index.js:3415:9)",
		"    at loadLayeredEnv (file:///dsh-app-boot/lib/index.js:3436:18)",
		"    at runCli (file:///dsh/lib/bin.js:216:19)",
		"    at async runDesktopCli (file:///dsh-desktop-host/lib/cli.js:93:2)",
		"",
		"Node.js v24.18.1",
	}
	got := stderrMsgSuffix(tail)
	t.Logf("got = %s", got)
	if !strings.Contains(got, "Error: dsh: /repo/.env sets") {
		t.Fatalf("必须给出真因那一行（而不是一串栈帧）: %s", got)
	}
	if strings.Contains(got, "Node.js v24.18.1") {
		t.Fatalf("窗口应从原因行开始，不该退化到结尾的 Node 版本行: %s", got)
	}
}

// Python 形态：原因在**最后一行**，窗口要往前回退把它包含进来。
func TestStderrMsgSuffixPythonStyle(t *testing.T) {
	tail := []string{
		"Traceback (most recent call last):",
		`  File "boot.py", line 3, in <module>`,
		"    raise RuntimeError('provider 未配置')",
		"RuntimeError: provider 未配置",
	}
	got := stderrMsgSuffix(tail)
	t.Logf("got = %s", got)
	if !strings.Contains(got, "RuntimeError: provider 未配置") {
		t.Fatalf("最后一行原因必须在窗口里: %s", got)
	}
}
