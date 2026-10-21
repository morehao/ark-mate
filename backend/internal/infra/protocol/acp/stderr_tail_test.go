package acp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDialErrorCarriesStderrTail 进程起不来/握手失败时，错误上应带 stderr 诊断尾巴（A3）。
func TestDialErrorCarriesStderrTail(t *testing.T) {
	tr := NewStdioTransport("sh", []string{"-c", "echo '[warn] runtime 启动即失败' >&2; echo 'second line' >&2; exit 7"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, tr, ClientOptions{DialTimeout: 3 * time.Second})
	if err == nil {
		t.Fatal("期望拨号失败（进程立即退出）")
	}
	ae, ok := As(err)
	if !ok || ae == nil {
		t.Fatalf("期望 acp.Error，got %T %v", err, err)
	}
	if len(ae.StderrTail) != 2 {
		t.Fatalf("期望 2 行 stderr 尾巴，got %v", ae.StderrTail)
	}
	if !strings.Contains(ae.StderrTail[0], "runtime 启动即失败") || !strings.Contains(ae.StderrTail[1], "second line") {
		t.Fatalf("尾巴内容不符: %v", ae.StderrTail)
	}
	// 顺序应为「先写的在前」（最近输出在末尾）。
	if strings.Contains(ae.StderrTail[0], "second") {
		t.Fatalf("尾巴顺序应保持写入顺序: %v", ae.StderrTail)
	}
}

// TestTailLines 截断语义：超上限保留最近 n 行；n<=0 或空输入返回 nil；返回副本（不共享底层数组）。
func TestTailLines(t *testing.T) {
	in := []string{"a", "b", "c", "d"}
	if got := TailLines(in, 2); len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Fatalf("期望最近 2 行 [c d]，got %v", got)
	}
	if got := TailLines(in, 0); got != nil {
		t.Fatalf("n=0 应返回 nil，got %v", got)
	}
	if got := TailLines(nil, 3); got != nil {
		t.Fatalf("空输入应返回 nil，got %v", got)
	}
	if got := TailLines(in, 10); len(got) != 4 {
		t.Fatalf("不足上限应全量返回，got %v", got)
	}
	// 副本语义：改动返回值不应影响入参。
	got := TailLines(in, 2)
	got[0] = "mutated"
	if in[2] != "c" {
		t.Fatalf("返回值应为基础数组副本: %v", in)
	}
}

// TestAttachStderr 已有尾巴不覆盖；nil transport 安全。
func TestAttachStderr(t *testing.T) {
	ae := NewError(ClassConnection, "x", "d", nil)
	if err := AttachStderr(ae, nil); err != ae {
		t.Fatalf("nil transport 应原样返回")
	}
	ae.StderrTail = []string{"keep"}
	tr := NewStdioTransport("sh", []string{"-c", "echo new >&2"}, nil)
	if err := AttachStderr(ae, tr); err != ae {
		t.Fatalf("应原样返回同一错误")
	}
	if len(ae.StderrTail) != 1 || ae.StderrTail[0] != "keep" {
		t.Fatalf("已有尾巴不应被覆盖: %v", ae.StderrTail)
	}
}
