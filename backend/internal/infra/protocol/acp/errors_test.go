package acp

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

// TestErrorBasics 验证 *Error 的构造/解包/断言语义。
func TestErrorBasics(t *testing.T) {
	cause := errors.New("boom")
	e := NewError(ClassService, "svc", "详情", cause)
	if e.Class != ClassService || e.Code != "svc" || e.Details != "详情" {
		t.Fatalf("字段不符: %+v", e)
	}
	if !errors.Is(e, cause) {
		t.Fatal("应能 Is 到 cause")
	}
	if !strings.Contains(e.Error(), "class=service code=svc 详情: boom") {
		t.Fatalf("Error() 文本不符: %s", e.Error())
	}
	if ae, ok := As(e); !ok || ae != e {
		t.Fatalf("As 应原样返回 *Error")
	}
	// As 对普通错误返回 false。
	if _, ok := As(errors.New("plain")); ok {
		t.Fatal("普通错误不应 As 成功")
	}
	// NewError 允许 nil cause。
	nilCause := NewError(ClassLimit, "quota", "x", nil)
	if nilCause.Err != nil {
		t.Fatalf("nil cause 被替换: %+v", nilCause)
	}
}

// TestClassifyPassthrough 已分类错误原样透传。
func TestClassifyPassthrough(t *testing.T) {
	in := NewError(ClassAccess, "auth_required", "login", nil)
	if got := Classify(in); got != in {
		t.Fatalf("应透传原指针，got %v", got)
	}
	if Classify(nil) != nil {
		t.Fatal("Classify(nil) 应为 nil")
	}
}

// TestClassifySDKRequestError JSON-RPC 错误码 → 可操作分类映射。
func TestClassifySDKRequestError(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		msg      string
		wantCls  Class
		wantCode string
	}{
		{"method_not_found", -32601, "method not found", ClassUnsupported, "method_not_found"},
		{"invalid_params", -32602, "bad params", ClassRequest, "invalid_params"},
		{"invalid_request", -32600, "bad req", ClassRequest, "invalid_request"},
		{"protocol_low", -32000, "proto", ClassService, "protocol_error"},
		{"protocol_high", -32099, "proto", ClassService, "protocol_error"},
		{"runtime_business", 1001, "user-level error", ClassService, "runtime_error_1001"},
		{"other_negative", -12345, "?", ClassUnknown, "jsonrpc_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			re := &acpsdk.RequestError{Code: tc.code, Message: tc.msg}
			ae := Classify(re)
			if ae.Class != tc.wantCls || ae.Code != tc.wantCode {
				t.Fatalf("got %s/%s want %s/%s", ae.Class, ae.Code, tc.wantCls, tc.wantCode)
			}
			if !errors.Is(ae, re) {
				t.Fatal("应保留原始 RequestError 链")
			}
		})
	}
}

// TestClassifyProcessAndCtx 进程退出/上下文取消/超时 → connection 分类。
func TestClassifyProcessAndCtx(t *testing.T) {
	ee := &exec.ExitError{}
	ae := Classify(ee)
	if ae.Class != ClassConnection || ae.Code != "process_exited" {
		t.Fatalf("ExitError 映射不符: %s/%s", ae.Class, ae.Code)
	}
	if ae := Classify(context.Canceled); ae.Class != ClassConnection || ae.Code != "cancelled" {
		t.Fatalf("Canceled 映射不符: %s/%s", ae.Class, ae.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	if ae := Classify(ctx.Err()); ae.Class != ClassConnection || ae.Code != "timeout" {
		t.Fatalf("DeadlineExceeded 映射不符: %s/%s", ae.Class, ae.Code)
	}
	// 兜底 unknown。
	if ae := Classify(errors.New("plain")); ae.Class != ClassUnknown || ae.Code != "unknown" {
		t.Fatalf("plain 映射不符: %s/%s", ae.Class, ae.Code)
	}
}

// TestClassifyWrappedRequestError 包装在普通 error 内的 RequestError 也能被识别。
func TestClassifyWrappedRequestError(t *testing.T) {
	inner := &acpsdk.RequestError{Code: -32602, Message: "nested"}
	wrapped := errors.Join(errors.New("outer"), inner)
	ae := Classify(wrapped)
	if ae.Code != "invalid_params" {
		t.Fatalf("包装链内 RequestError 未识别: %v", ae)
	}
}

// TestClassifySurfacesRequestErrorDetails 回归：-32603 的 `message` 通常是无信息量的
// "Internal error"，真正死因在 `data.details` 里（实测 dsh session/new：`no adapter registered
// for provider "mate-magpie"`）。丢掉它用户只看到 `50001 Internal error`——正是本次建会话失败的观感。
//
// 三条判据：① details 被拼进文案；② 有 details 时不再是裸 "Internal error"；
// ③ 无 data 时逐字保持旧行为（只回 message），不劣化既有路径。
func TestClassifySurfacesRequestErrorDetails(t *testing.T) {
	re := &acpsdk.RequestError{
		Code:    -32603,
		Message: "Internal error",
		Data:    map[string]any{"details": `no adapter registered for provider "mate-magpie"`},
	}
	ae := Classify(re)
	if !strings.Contains(ae.Details, `no adapter registered for provider "mate-magpie"`) {
		t.Fatalf("data.details 未被拼进文案: %q", ae.Details)
	}
	if ae.Details == "Internal error" {
		t.Fatalf("有 details 时不该只剩裸通用文案: %q", ae.Details)
	}
	if !strings.Contains(ae.Details, "Internal error") {
		t.Fatalf("协议 message 应保留在最前: %q", ae.Details)
	}

	// 无 data：逐字保持旧行为。
	bare := Classify(&acpsdk.RequestError{Code: -32603, Message: "Internal error"})
	if bare.Details != "Internal error" {
		t.Fatalf("无 data 时应逐字等于 message，got %q", bare.Details)
	}

	// 非约定形状的 data：用紧凑 JSON 兜底，且不为空。
	odd := Classify(&acpsdk.RequestError{Code: -32603, Message: "Internal error",
		Data: map[string]any{"code": "E_X"}})
	if !strings.Contains(odd.Details, "E_X") {
		t.Fatalf("非约定 data 应 JSON 兜底带出信息: %q", odd.Details)
	}
}

// TestTailLinesRedactsSecrets stderr 尾巴的唯一出口即脱敏闸门：
// 子进程把 key 打进 stderr（如底座启动日志回显 env）时，诊断/事件/错误三处消费者都拿到脱敏后的行。
func TestTailLinesRedactsSecrets(t *testing.T) {
	const secret = "sk-abcdefghijklmnop1234"
	got := TailLines([]string{"OPENAI_API_KEY=" + secret, "listening on :8080", "Bearer abcdefghijklmnop"}, 5)
	if len(got) != 3 {
		t.Fatalf("行数不符: %v", got)
	}
	for _, l := range got {
		if strings.Contains(l, secret) || strings.Contains(l, "abcdefghijklmnop") {
			t.Fatalf("stderr 尾巴未脱敏: %q", l)
		}
	}
	if got[1] != "listening on :8080" {
		t.Fatalf("正常行被改写: %q", got[1])
	}
	if got[2] != "Bearer ***" {
		t.Fatalf("Bearer 应保留前缀: %q", got[2])
	}
	// 截断语义不变（只取最近 n 行）。
	if tail := TailLines([]string{"a", "b", "c"}, 2); len(tail) != 2 || tail[1] != "c" {
		t.Fatalf("截断语义被破坏: %v", tail)
	}
}
