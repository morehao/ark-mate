package opencode

import (
	"reflect"
	"testing"
)

// TestDeclaredBaseline 钉住 opencode adapter 声明（数据契约防漂移）。
func TestDeclaredBaseline(t *testing.T) {
	if ID != "opencode" || Name != "opencode" || Version != "opencode acp" {
		t.Fatalf("元数据不符: %s %s %s", ID, Name, Version)
	}
	if Command != "opencode" || !reflect.DeepEqual(Args, []string{"acp"}) {
		t.Fatalf("启动形态不符: %q %v", Command, Args)
	}
	if !Caps.SessionsPersistent || !Caps.MultiSessionConcurrent || !Caps.Images || !Caps.EmbeddedContext {
		t.Fatalf("能力基线不符: %+v", Caps)
	}
	if Notes == "" {
		t.Fatal("Notes 不应为空")
	}
	// Args 不可变语义（调用方不应原地改共享切片）。
	if !reflect.DeepEqual(Args, []string{"acp"}) {
		t.Fatal("Args 共享切片被改动")
	}
}
