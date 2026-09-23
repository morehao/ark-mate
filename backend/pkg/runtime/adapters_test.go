package runtime

import (
	"testing"
)

// TestSpecsHaveNoModelEnv 各投影都**不再**经启动 env 注入模型（决策记录 D1）：
// 模型选择是 ACP 会话级语义（session/set_config_option，值是运行时目录里的不透明串），
// 且 Spec 层不再声明任何模型相关字段（option id 各家不同，平台只认 protocol 的 category）。
// 进程级模型配置归底座自身的配置文件/环境（子进程本就继承 daemon 环境，无需平台 passthrough）。
func TestSpecsHaveNoModelEnv(t *testing.T) {
	t.Setenv("OPENCODE_MODEL", "yygu-v2/deepseek-v4-flash")
	t.Setenv("OPENCODE_OTHER", "should-not-pass")
	for _, spec := range []Spec{opencodeSpec(), deepseekharnessSpec()} {
		if len(spec.Launch.Env) != 0 {
			t.Fatalf("%s 不应注入启动 env: %v", spec.ID, spec.Launch.Env)
		}
	}
}
