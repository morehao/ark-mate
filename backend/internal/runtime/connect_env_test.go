package runtime

import (
	"strings"
	"testing"
)

// TestConnectEnvPlatformDeclarationWins 平台声明（含配置隔离）不被调用方增量改写。
//
// 为什么值得单测：这是"provider 档案里顺手写一个 XDG_CONFIG_HOME 就把隔离盖掉"的唯一拦截点，
// 而症状在真跑会话时表现为"桌面态莫名读了用户 ~/.config/opencode"——很难反查到 env 合并顺序。
func TestConnectEnvPlatformDeclarationWins(t *testing.T) {
	spec := Spec{
		ID: "rt-alt",
		Launch: LaunchSpec{
			Env: []string{"XDG_CONFIG_HOME=/home/mate/runtime/rt-alt/config", "RT_ALT_DB=/home/mate/x.db"},
		},
	}
	got := connectEnv(spec, []string{"XDG_CONFIG_HOME=/home/user/.config", "OPENAI_API_KEY=sk-1"})

	counts := map[string]int{}
	for _, kv := range got {
		counts[envKeyName(kv)]++
	}
	for key, n := range counts {
		if n != 1 {
			t.Fatalf("环境里 %s 出现了 %d 次（同名两条在 POSIX getenv 与 Node process.env 下语义相反）：%v", key, n, got)
		}
	}
	if !containsEnv(got, "XDG_CONFIG_HOME=/home/mate/runtime/rt-alt/config") {
		t.Fatalf("隔离值被调用方增量覆盖了：%v", got)
	}
	if containsEnv(got, "XDG_CONFIG_HOME=/home/user/.config") {
		t.Fatalf("用户值不该留在结果里：%v", got)
	}
	// 非冲突的增量必须原样保留（provider 密钥是这条路径的正事）。
	if !containsEnv(got, "OPENAI_API_KEY=sk-1") {
		t.Fatalf("调用方增量丢失：%v", got)
	}
}

// TestConnectEnvKeepsSpecOnly 调用方没有增量时不应凭空多出内容（也别改写顺序）。
// containsEnv 判定环境数组里有没有恰好等于 want 的那一条（同名不同值不算命中）。
func containsEnv(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestConnectEnvKeepsSpecOnly(t *testing.T) {
	env := []string{"A=1", "B=2"}
	spec := Spec{Launch: LaunchSpec{Env: env}}
	got := connectEnv(spec, nil)
	if strings.Join(got, ",") != strings.Join(env, ",") {
		t.Fatalf("期望原样返回 %v，得到 %v", env, got)
	}
}
