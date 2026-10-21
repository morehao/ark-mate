package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

// TestSessionIDFromMeta `_meta` 归属读取：键按序回退、只认非空字符串（数字/对象/空串一律不认）。
func TestSessionIDFromMeta(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"sessionId", map[string]any{"sessionId": "s1"}, "s1"},
		{"session_id 回退", map[string]any{"session_id": "s2"}, "s2"},
		{"session_id 回退", map[string]any{"session_id": "s3"}, "s3"},
		{"顺序优先 sessionId", map[string]any{"session_id": "s9", "sessionId": "s1"}, "s1"},
		{"空串跳过", map[string]any{"sessionId": "", "session_id": "s2"}, "s2"},
		{"非字符串不认", map[string]any{"sessionId": 42}, ""},
		{"对象不认", map[string]any{"sessionId": map[string]any{"id": "s1"}}, ""},
		{"无命中", map[string]any{"other": "x"}, ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		if got := sessionIDFromMeta(tc.meta); got != tc.want {
			t.Fatalf("%s: want %q got %q", tc.name, tc.want, got)
		}
	}
}

// TestRequestIDFromMeta 权限侧请求键（requestId/request_id）的容忍读取。
func TestRequestIDFromMeta(t *testing.T) {
	if got := requestIDFromMeta(map[string]any{"requestId": "r1"}); got != "r1" {
		t.Fatalf("requestId: %q", got)
	}
	if got := requestIDFromMeta(map[string]any{"request_id": "r2"}); got != "r2" {
		t.Fatalf("request_id: %q", got)
	}
	// 裸 id 不参与回退：语义太泛，误认比不认更坏。
	if got := requestIDFromMeta(map[string]any{"id": "r3"}); got != "" {
		t.Fatalf("裸 id 不应被认作请求归属: %q", got)
	}
}

// TestElicitationScopeFromMetaSDK verify elicitation/create 的 Form/Url `_meta` 命中即走 wire：
// SDK v0.13.5 会丢弃顶层 scope，但变体里的 `_meta` 完整保留 → 零 fork 拿到归属。
func TestElicitationScopeFromMetaSDK(t *testing.T) {
	form, ok := elicitationRequestFromSDK(acpsdk.UnstableCreateElicitationRequest{
		Form: &acpsdk.UnstableCreateElicitationForm{
			Meta:    map[string]any{"session_id": "s-form"},
			Message: "请选择一个环境",
		},
	})
	if !ok {
		t.Fatal("form 载荷应可识别")
	}
	if form.SessionID != "s-form" || form.ScopeSource != ScopeWire {
		t.Fatalf("form `_meta` 归属应为 wire/s-form: %+v", form)
	}

	url, ok := elicitationRequestFromSDK(acpsdk.UnstableCreateElicitationRequest{
		Url: &acpsdk.UnstableCreateElicitationUrl{
			Meta:          map[string]any{"sessionId": "s-url"},
			Message:       "请打开链接",
			Url:           "https://example.com",
			ElicitationId: "acp-el-9",
		},
	})
	if !ok {
		t.Fatal("url 载荷应可识别")
	}
	if url.SessionID != "s-url" || url.ScopeSource != ScopeWire || url.URLID != "acp-el-9" {
		t.Fatalf("url `_meta` 归属应为 wire/s-url 且透传 elicitationId: %+v", url)
	}

	// 无 `_meta`：本层不猜——留空由宿主注入（ScopeSource 由注入方填）。
	none, ok := elicitationRequestFromSDK(acpsdk.UnstableCreateElicitationRequest{
		Form: &acpsdk.UnstableCreateElicitationForm{Message: "无 meta"},
	})
	if !ok {
		t.Fatal("form 载荷应可识别")
	}
	if none.SessionID != "" || none.ScopeSource != "" {
		t.Fatalf("无 `_meta` 时本层不应填充归属: %+v", none)
	}
}
