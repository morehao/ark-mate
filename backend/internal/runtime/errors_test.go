package runtime

import (
	"errors"
	"io"
	"testing"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/golib/gerror"
)

// TestFoldErrorKeepsClassCodeTail 协议错误折成平台视图时，四样一个都不能丢：
// 分类、稳定 code、文案、stderr 尾巴；原始错误还要能 `errors.Is` 穿透。
//
// 失效方式（B4 的核心风险）：漏掉一条出口或折歪一处，**编译器帮不上**——本该 40401/50201 的
// 错误会在控制器出口退化成 50000，用户看到"服务内部错误"而真因是"资源不存在 / 连不上底座"。
func TestFoldErrorKeepsClassCodeTail(t *testing.T) {
	ae := acp.NewError(acp.ClassConnection, "process_exited", "agent 进程异常退出", io.ErrUnexpectedEOF)
	ae.StderrTail = []string{"boot line 1", "boot line 2"}

	err := FoldError(ae)
	re, ok := code.RuntimeErrorOf(err)
	if !ok {
		t.Fatal("折叠后的错误必须是平台视图")
	}
	if re.Class != code.ClassConnection || re.Code != "process_exited" || re.Details != "agent 进程异常退出" {
		t.Fatalf("分类/code/文案未保留: %+v", re)
	}
	if len(re.StderrTail) != 2 || re.StderrTail[0] != "boot line 1" {
		t.Fatalf("stderr 尾巴未保留: %+v", re.StderrTail)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("原始错误应能 errors.Is 穿透（Unwrap 断了，连接类判据就认不出底层故障）")
	}
}

// TestFoldErrorDrivesEnvelopeCode 折叠后的错误必须在信封出口给出**正确的业务码**。
//
// 这条是"退化成 50000"这条静默失效的机器判据：资源不存在（40401）、连接失败（50201）、
// 不支持（40501）各一条，取的都是真实出口会遇到的分类。
func TestFoldErrorDrivesEnvelopeCode(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want int
	}{
		{"not_found", FoldError(acp.NewError(acp.ClassRequest, "runtime_not_found", "未装配", nil)), code.NotFound},
		{"connection", FoldError(acp.NewError(acp.ClassConnection, "connection_lost", "连接断开", nil)), code.BadGateway},
		{"unsupported", FoldError(acp.NewError(acp.ClassUnsupported, "method_not_found", "没有这个方法", nil)), code.NotSupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ge gerror.Error
			if !errors.As(code.FromError(tc.in), &ge) {
				t.Fatalf("FromError 未给出带码错误: %v", tc.in)
			}
			if ge.Code != tc.want {
				t.Fatalf("期望 %d，got %d（%q）", tc.want, ge.Code, ge.Msg)
			}
		})
	}
}

// TestFoldErrorIsIdempotent 适配器可以在每一层出口都折一次：已是平台错误就原样返回。
//
// 不幂等的表现是"折两遍"——第二次把平台视图当成未知错误，分类与 code 一起丢掉。
func TestFoldErrorIsIdempotent(t *testing.T) {
	coded := code.NewError(code.ClassRequest, "runtime_not_found", "未装配", nil)
	if got := FoldError(coded); got != coded {
		t.Fatalf("平台带码错误不该被重新包装: %v", got)
	}
	view := code.NewRuntimeError(code.ClassLimit, "generation_limit_reached", "代际上限", nil)
	if got := FoldError(view); got != view {
		t.Fatalf("平台视图不该被重新包装: %v", got)
	}
	if got := FoldError(nil); got != nil {
		t.Fatalf("nil 应原样返回 nil，got %v", got)
	}
}
