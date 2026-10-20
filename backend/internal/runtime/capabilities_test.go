package runtime

import (
	"reflect"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/acp"
)

// TestCapabilityFactsOfACPKeepsEveryField 连接期能力事实折成平台值时**一个字段都不能丢**，
// 且切片必须是副本（缓存/调用方改输入不能反噬平台值）。
//
// 为什么值得一条用例：这些布尔决定"能不能原生恢复会话""要不要重放模型""目录取样可不可行"。
// 少搬一个不会让任何调用点报错——只是那条路悄悄走兜底（上下文丢失 / 多起一个进程 / 目录永远空），
// 而两侧各自的测试都绿。
func TestCapabilityFactsOfACPKeepsEveryField(t *testing.T) {
	in := acp.Capabilities{
		SessionsPersistent:     true,
		MultiSessionConcurrent: true,
		Images:                 true,
		EmbeddedContext:        true,
		AuthMethods:            []string{"env_var", "terminal"},
		LoadSession:            true,
		SessionClose:           true,
		SessionList:            true,
		SessionResume:          true,
		SessionDelete:          false,
		SessionFork:            true,
		AuthGuidance: []acp.AuthGuidance{{
			MethodID: "dsh-login", Name: "终端登录", Description: "Run dsh auth login",
			Terminal: true, Command: []string{"dsh", "auth", "login"},
		}},
	}
	got := CapabilityFactsOfACP(in)

	if !got.SessionsPersistent || !got.MultiSessionConcurrent || !got.Images || !got.EmbeddedContext {
		t.Fatalf("基础能力未保留: %+v", got)
	}
	if !got.LoadSession || !got.SessionClose || !got.SessionList || !got.SessionResume || !got.SessionFork {
		t.Fatalf("会话生命周期能力未保留: %+v", got)
	}
	if got.SessionDelete {
		t.Fatalf("SessionDelete 应为 false（搬错方向 = 上层以为能删会话）: %+v", got)
	}
	if !reflect.DeepEqual(got.AuthMethods, in.AuthMethods) {
		t.Fatalf("鉴权方式标签未保留: %+v", got.AuthMethods)
	}
	if len(got.AuthGuidance) != 1 || !reflect.DeepEqual(got.AuthGuidance[0].Command, in.AuthGuidance[0].Command) {
		t.Fatalf("鉴权指引未保留: %+v", got.AuthGuidance)
	}

	// 副本语义：改输入切片，平台值不得跟着变。
	in.AuthMethods[0] = "mutated"
	in.AuthGuidance[0].Command[0] = "mutated"
	if got.AuthMethods[0] == "mutated" {
		t.Fatal("AuthMethods 与输入共享底层数组（缓存/装配侧改输入会反噬平台值）")
	}
	if got.AuthGuidance[0].Command[0] == "mutated" {
		t.Fatal("AuthGuidance.Command 与输入共享底层数组")
	}

	// 空输入不得造出 nil 以外的形态（调用方按 len 判"没声明"）。
	if empty := CapabilityFactsOfACP(acp.Capabilities{}); empty.AuthMethods != nil || empty.AuthGuidance != nil {
		t.Fatalf("空输入应得 nil 切片: %+v", empty)
	}
}
