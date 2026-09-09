package runtime

import (
	"os"

	"github.com/morehao/ark-forge/internal/runtime/deepseekharness"
	"github.com/morehao/ark-forge/internal/runtime/echo"
	"github.com/morehao/ark-forge/internal/runtime/opencode"
)

// passthroughEnv 把 daemon 环境中命名的变量透传给 runtime 子进程（叠加于 os.Environ）。
// 典型：OPENCODE_MODEL——opencode ACP 忽略 ACP 客户端传入的模型选择（issue #13644），
// 模型只能经启动环境注入；配合用户 opencode.json 的 "model":"{env:OPENCODE_MODEL}" 引用生效。
func passthroughEnv(names ...string) []string {
	var out []string
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			out = append(out, n+"="+v)
		}
	}
	return out
}

// opencodeSpec 汇入 opencode adapter 的启动/能力描述。
func opencodeSpec() Spec {
	return Spec{
		ID:      opencode.ID,
		Name:    opencode.Name,
		Version: opencode.Version,
		Launch: LaunchSpec{
			Command: opencode.Command,
			Args:    opencode.Args,
			Env:     passthroughEnv("OPENCODE_MODEL"),
		},
		Caps: Capabilities{
			SessionsPersistent:     opencode.Caps.SessionsPersistent,
			MultiSessionConcurrent: opencode.Caps.MultiSessionConcurrent,
			Images:                 opencode.Caps.Images,
			EmbeddedContext:        opencode.Caps.EmbeddedContext,
		},
		Description:       "opencode（terminal AI）ACP 执行后端",
		McpTransports:     []string{"stdio", "sse", "http"}, // §6.2：opencode 支持 http/sse/stdio
		ManagesOwnContext: true,                             // §6.2：后端自管上下文/工具
		Notes:             opencode.Notes,
	}
}

// deepseekharnessSpec 汇入 deepseek-harness adapter 的启动/能力描述。
func deepseekharnessSpec() Spec {
	return Spec{
		ID:      deepseekharness.ID,
		Name:    deepseekharness.Name,
		Version: deepseekharness.Version,
		Launch: LaunchSpec{
			Command: deepseekharness.Command,
			Args:    deepseekharness.Args,
		},
		Caps: Capabilities{
			SessionsPersistent:     deepseekharness.Caps.SessionsPersistent,
			MultiSessionConcurrent: deepseekharness.Caps.MultiSessionConcurrent,
			Images:                 deepseekharness.Caps.Images,
			EmbeddedContext:        deepseekharness.Caps.EmbeddedContext,
		},
		Description:       "deepseek-harness ACP 执行后端",
		McpTransports:     []string{"stdio", "streamable_http"}, // §6.2：dsh 拒 sse
		ManagesOwnContext: true,                                 // §6.2：后端自管上下文/工具
		Notes:             deepseekharness.Notes,
	}
}

// echoSpec 汇入 echo 演示底座的启动/能力描述。它自派生：以当前可执行文件（forge）的
// acp-demo 子命令作为 ACP server，与 opencode/dsh 同构走 stdio；无需外部安装/模型 key。
func echoSpec() Spec {
	cmd := "forge" // 兜底；正常取 os.Executable()
	if exe, err := echo.Command(); err == nil && exe != "" {
		cmd = exe
	}
	return Spec{
		ID:      echo.ID,
		Name:    echo.Name,
		Version: echo.Version,
		Launch: LaunchSpec{
			Command: cmd,
			Args:    echo.Args,
		},
		Description:       "echo 本地自包含 ACP 演示底座（权限/elicitation 演示，免模型 key）",
		McpTransports:     []string{"stdio"}, // 演示底座仅 stdio
		ManagesOwnContext: true,              // §6.2：后端自管上下文/工具
		Notes:             echo.Notes,
	}
}
