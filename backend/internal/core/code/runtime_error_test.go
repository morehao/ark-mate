package code

import (
	"errors"
	"io"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/acp"
)

// TestClassifyRuntimeKeepsDiagnostics 协议错误折成平台视图时，诊断面要用的四样一个都不能丢：
// 分类、稳定 code、文案、stderr 尾巴；原始错误还要能 `errors.Is` 穿透（判连接类故障用）。
//
// 失效方式：丢一样不会让任何调用点报错——诊断页少一句原因、`run.error` 缺 class、
// 或者连接类判据认不出 `exec.ExitError`（"进程起不来"被当成普通失败，用户看到的出路是错的）。
func TestClassifyRuntimeKeepsDiagnostics(t *testing.T) {
	ae := acp.NewError(acp.ClassConnection, "process_exited", "agent 进程异常退出", io.ErrUnexpectedEOF)
	ae.StderrTail = []string{"boot line 1", "boot line 2"}

	re := ClassifyRuntime(ae)
	if re.Class != ClassConnection || re.Code != "process_exited" || re.Details != "agent 进程异常退出" {
		t.Fatalf("分类/code/文案未保留: %+v", re)
	}
	if len(re.StderrTail) != 2 || re.StderrTail[0] != "boot line 1" {
		t.Fatalf("stderr 尾巴未保留: %+v", re.StderrTail)
	}
	if !errors.Is(re, io.ErrUnexpectedEOF) {
		t.Fatal("原始错误应能 errors.Is 穿透（Unwrap 断了，连接类判据就认不出底层故障）")
	}

	// 已经是平台视图：原样返回同一个对象（不重复包装——重复包装会让 tail 与 cause 分叉）。
	if got := ClassifyRuntime(re); got != re {
		t.Fatalf("平台视图应原样返回，实际另包了一层: %+v", got)
	}

	// nil 进 nil 出（诊断路径在 err == nil 时不该造出一条"空失败"）。
	if got := ClassifyRuntime(nil); got != nil {
		t.Fatalf("nil 应回 nil，实际 %+v", got)
	}

	// 普通错误：有分类（unknown）、有原文，不能返回 nil。
	plain := errors.New("boom")
	if got := ClassifyRuntime(plain); got == nil || got.Class != ClassUnknown || got.Details != "boom" {
		t.Fatalf("普通错误应折成 unknown + 原文，实际 %+v", got)
	}

	// RuntimeErrorOf 两种输入都要认（判"是不是某类故障"的调用点面对的是两边来的错误）。
	if _, ok := RuntimeErrorOf(ae); !ok {
		t.Fatal("RuntimeErrorOf 应认出协议错误")
	}
	if _, ok := RuntimeErrorOf(re); !ok {
		t.Fatal("RuntimeErrorOf 应认出平台视图")
	}
	if _, ok := RuntimeErrorOf(plain); ok {
		t.Fatal("普通错误不该被认成上游错误视图")
	}
}
