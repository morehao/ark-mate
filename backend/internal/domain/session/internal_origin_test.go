package session

import (
	"testing"
)

// TestInternalOriginSessionHiddenFromList 内部（模型网关）会话不出现在用户会话列表里；
// 普通会话照常列出。钉住"内部会话不进用户视图"这条投影纪律。
func TestInternalOriginSessionHiddenFromList(t *testing.T) {
	pl := NewPlane()
	// ListSessions 会读归属展示（读库）；内存最小装配给它一个 no-op。
	pl.SetWorkspaceDisplay(func(string) (string, string) { return "", "" })
	pl.RegisterSession(&SessionRec{ID: "ses_user", Status: SessionStatusCompleted, Title: "用户会话"})
	pl.RegisterSession(&SessionRec{ID: "ses_internal", Status: SessionStatusCompleted, Origin: "internal:llm"})
	pl.RegisterSession(&SessionRec{ID: "ses_auto", Status: SessionStatusCompleted, Origin: "automation:nightly"})

	items, err := pl.ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	if !ids["ses_user"] {
		t.Fatal("用户会话应出现")
	}
	if ids["ses_internal"] {
		t.Fatal("内部会话不应出现在用户会话列表")
	}
	if !ids["ses_auto"] {
		t.Fatal("automation 会话仍应出现（只有 internal: 前缀被过滤）")
	}
}

// TestIsInternalOrigin 前缀判定只认 `internal:`（automation 与空都算外部）。
func TestIsInternalOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"", false},
		{"automation:x", false},
		{"internal:llm", true},
		{"internal:other", true},
	} {
		if got := IsInternalOrigin(tc.origin); got != tc.want {
			t.Fatalf("IsInternalOrigin(%q)=%v want %v", tc.origin, got, tc.want)
		}
	}
}
