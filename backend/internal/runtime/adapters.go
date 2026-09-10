package runtime

import (
	"github.com/morehao/ark-forge/internal/runtime/deepseekharness"
	"github.com/morehao/ark-forge/internal/runtime/echo"
	"github.com/morehao/ark-forge/internal/runtime/opencode"
)

// opencodeSpec 汇入 opencode adapter 的启动/能力描述。
//
// 模型面（决策记录 D1，源码级结论）：opencode 的 ACP server **实现**会话级模型选择——
// packages/opencode/src/acp/service.ts 处理 configId=="model"（parseSelectedModel → session.setModel），
// 且其自带 live 测试断言 currentValue 随之变化（test/cli/acp/config-options.test.ts）。
// 因此不再用 OPENCODE_MODEL 注入 env（那样会「一个模型一个进程」），改为走 ACP 会话配置项；
// 其 ACP 目录本身就是「已配置凭证/显式声明的 provider」的模型集合。
func opencodeSpec() Spec {
	return Spec{
		ID:      opencode.ID,
		Name:    opencode.Name,
		Version: opencode.Version,
		Launch: LaunchSpec{
			Command: opencode.Command,
			Args:    opencode.Args,
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
		// SetupSteps 该底座未就绪时的可执行修复步骤（诊断面 /agents/:id/diagnostics 外露为 remediation）。
		SetupSteps: []string{
			"安装 opencode CLI（ACP 子命令由它提供），确认 `opencode --version` 可执行且 opencode 在 PATH 中",
			"在底座内完成 provider 登录，或给该 agent 绑定 provider 档案以注入密钥 env",
		},
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
		// 模型走 ACP 会话配置项（session/set_config_option，会话级、可共享连接）：
		// 目录与取值全部由 dsh 自报（option id "model"、值 JSON.stringify([provider,model])），
		// 平台只按 category 消费，不在此声明任何 id/格式。
		Notes: deepseekharness.Notes,
		// SetupSteps 该底座未就绪时的可执行修复步骤（诊断面 remediation 外露）。
		SetupSteps: []string{
			"安装含 acp profile 模板的 dsh（npm @next 0.1.2-rc.1+；latest 0.1.1-rc.2 缺该模板），确认 `dsh --profile acp` 可启动且 dsh 在 PATH 中",
			"准备 DEEPSEEK_API_KEY（子进程环境透传）：给该 agent 绑定 provider 档案，或让 daemon 进程环境带上该变量",
		},
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
		// SetupSteps 演示底座随 forge 内置：没有可安装步骤（便于自证「命令不可执行」类问题与它无关）。
		SetupSteps: []string{
			"echo 是进程内自包含演示底座（以当前 forge 可执行文件的 acp-demo 子命令启动），无需安装与密钥",
		},
	}
}
