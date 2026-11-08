package dsh

import (
	"reflect"
	"testing"
)

// TestDeclaredBaseline 钉住 dsh adapter 声明（数据契约防漂移）。
func TestDeclaredBaseline(t *testing.T) {
	if ID != "dsh" || Name != "dsh" || Version != "dsh --profile mate-acp" {
		t.Fatalf("元数据不符: %s %s %s", ID, Name, Version)
	}
	if Command != "dsh" || !reflect.DeepEqual(Args, []string{"--profile", ProfileName}) {
		t.Fatalf("启动形态不符: %q %v", Command, Args)
	}
	// 能力基线按 docs/dsh-acp-adaptation.md 源码核实：
	// 持久会话（list/resume/close）为真；无 load/fork；image/embeddedContext 为假。
	if !Caps.SessionsPersistent || !Caps.MultiSessionConcurrent {
		t.Fatalf("持久会话基线不符: %+v", Caps)
	}
	if Caps.Images || Caps.EmbeddedContext {
		t.Fatalf("不应声明 image/embeddedContext: %+v", Caps)
	}
	if Notes == "" {
		t.Fatal("Notes 不应为空")
	}
	if !reflect.DeepEqual(Args, []string{"--profile", ProfileName}) {
		t.Fatal("Args 共享切片被改动")
	}
}
