package runtimeconn

import (
	"context"
	"reflect"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/session/event"
)

// TestClientOptionsOfTranslatesInteraction 平台交互回调 → ACP 回调的**两个方向**翻译。
//
// 为什么单独钉：翻译在适配器里、消费在用例层，中间隔着 `session.DialOptions.Hooks` 的
// `any` 断言。漏译一个字段的表现是"权限卡少一个选项"或"提问卡丢了 schema"，都不会报错；
// 回执方向漏字段则是"用户点了拒绝，底座却收到允许"。
func TestClientOptionsOfTranslatesInteraction(t *testing.T) {
	ctx := context.Background()

	t.Run("permission_request_in", func(t *testing.T) {
		var got interaction.PermissionRequest
		out := clientOptionsOf(interaction.Callbacks{
			OnPermission: func(_ context.Context, req interaction.PermissionRequest) interaction.PermissionDecision {
				got = req
				return interaction.PermissionDecision{Allowed: false, Rejected: true, OptionID: "reject-once", Always: false}
			},
		})
		dec := out.RequestPermission(ctx, acp.PermissionRequest{
			SessionID: "s1",
			RequestID: "r1",
			ToolCall:  acp.ToolCallInfo{ID: "c1", Title: "bash", Status: "pending", Kind: "execute", ContentText: "out"},
			Options: []acp.PermissionOption{
				{OptionID: "allow-once", Name: "允许一次", Kind: "allow_once"},
				{OptionID: "reject-once", Name: "拒绝", Kind: "reject_once"},
			},
		})
		want := interaction.PermissionRequest{
			SessionID: "s1",
			RequestID: "r1",
			ToolCall:  event.ToolCall{ID: "c1", Title: "bash", Status: "pending", Kind: "execute", ContentText: "out"},
			Options: []interaction.PermissionOption{
				{OptionID: "allow-once", Name: "允许一次", Kind: "allow_once"},
				{OptionID: "reject-once", Name: "拒绝", Kind: "reject_once"},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("请求翻译不符:\n got %+v\nwant %+v", got, want)
		}
		// 回执方向：领域的拒绝必须原样回到协议结果（不能悄悄变成"允许"）。
		if wantDec := (acp.PermissionDecision{Rejected: true, OptionID: "reject-once"}); dec != wantDec {
			t.Fatalf("回执翻译不符: got %+v want %+v", dec, wantDec)
		}
	})

	t.Run("elicitation_request_in", func(t *testing.T) {
		var got interaction.ElicitationRequest
		out := clientOptionsOf(interaction.Callbacks{
			OnElicitation: func(_ context.Context, req interaction.ElicitationRequest) interaction.ElicitationOutcome {
				got = req
				return interaction.ElicitationOutcome{Action: "accept", Content: map[string]any{"k": "v"}}
			},
		})
		res := out.CreateElicitation(ctx, acp.ElicitationRequest{
			SessionID:   "s1",
			ScopeSource: "wire",
			Mode:        "form",
			Message:     "填一下",
			URL:         "https://x",
			URLID:       "u1",
			Schema: &acp.ElicitationSchema{
				Title:       "表单",
				Description: "说明",
				Properties:  map[string]any{"k": "v"},
				Required:    []string{"k"},
			},
		})
		want := interaction.ElicitationRequest{
			SessionID:   "s1",
			ScopeSource: "wire",
			Mode:        "form",
			Message:     "填一下",
			URL:         "https://x",
			URLID:       "u1",
			Schema: &interaction.ElicitationSchema{
				Title:       "表单",
				Description: "说明",
				Properties:  map[string]any{"k": "v"},
				Required:    []string{"k"},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("提问翻译不符:\n got %+v\nwant %+v", got, want)
		}
		if wantOut := (acp.ElicitationOutcome{Action: "accept", Content: map[string]any{"k": "v"}}); !reflect.DeepEqual(res, wantOut) {
			t.Fatalf("回执翻译不符: got %+v want %+v", res, wantOut)
		}
	})

	// 无 schema 的提问（URL 模式）：Schema 保持 nil，不造一个空壳。
	t.Run("elicitation_without_schema", func(t *testing.T) {
		var got interaction.ElicitationRequest
		out := clientOptionsOf(interaction.Callbacks{
			OnElicitation: func(_ context.Context, req interaction.ElicitationRequest) interaction.ElicitationOutcome {
				got = req
				return interaction.ElicitationOutcome{Action: "decline"}
			},
		})
		out.CreateElicitation(ctx, acp.ElicitationRequest{SessionID: "s1", Mode: "url", URL: "https://x"})
		if got.Schema != nil {
			t.Fatalf("无 schema 时不应造空壳: %+v", got.Schema)
		}
	})

	// 零值安全：适配器不会因为装配侧少接线而 panic；对应回调保持 ACP 的零值实现。
	t.Run("nil_callbacks", func(t *testing.T) {
		out := clientOptionsOf(interaction.Callbacks{})
		if out.RequestPermission != nil || out.CreateElicitation != nil || out.CompleteElicitation != nil {
			t.Fatalf("nil 回调不应被替换成包装: %+v", out)
		}
	})
}
