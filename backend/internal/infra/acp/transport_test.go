package acl

import (
	"bufio"
	"context"
	"strings"
	"testing"
)

// TestStdioTransportEcho 用 `cat` 做管道回环：验证 Start/Stdin/Stdout/Close 全链路。
func TestStdioTransportEcho(t *testing.T) {
	tp := NewStdioTransport("cat", nil, nil)
	ctx := context.Background()
	if err := tp.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := tp.Stdin().Write([]byte("hello mate\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	line, err := bufio.NewReader(tp.Stdout()).ReadString('\n')
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.TrimSpace(line) != "hello mate" {
		t.Fatalf("回环内容不符: %q", line)
	}
	if err := tp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close 幂等
	if err := tp.Close(); err != nil {
		t.Fatalf("二次 Close: %v", err)
	}
}

// TestStdioTransportSpawnFailed 启动失败应归类为 connection/spawn_failed。
func TestStdioTransportSpawnFailed(t *testing.T) {
	tp := NewStdioTransport("definitely-not-a-real-binary-xyz", nil, nil)
	err := tp.Start(context.Background())
	if err == nil {
		t.Fatal("期望启动失败")
	}
	ae, ok := As(err)
	if !ok {
		t.Fatalf("期望 *acl.Error，got %T", err)
	}
	if ae.Class != ClassConnection || ae.Code != "spawn_failed" {
		t.Fatalf("class/code 不符: %s/%s", ae.Class, ae.Code)
	}
}

// TestClassifyRequestError SDK JSON-RPC 错误应归为 service/request 等可操作分类。
func TestClassifyRequestError(t *testing.T) {
	// 无法直接构造 sdk RequestError（字段可导出），验证包装链路可保留分类。
	inner := NewError(ClassConnection, "process_exited", "boom", nil)
	ae := Classify(inner)
	if ae.Code != "process_exited" || ae.Class != ClassConnection {
		t.Fatalf("原样透传失败: %v", ae)
	}
	if ae := Classify(context.Canceled); ae.Class != ClassConnection {
		t.Fatalf("ctx 取消应归 connection: %v", ae)
	}
}

// TestPidInfo 子进程标识外露（进程注册表 / 孤儿回收的登记来源）：
// Start 后 PID 为正、PGID == PID（Setpgid 自建进程组）、Command 为启动命令；
// 未 Start 时为零值（调用方据此跳过登记）。
func TestPidInfo(t *testing.T) {
	tp := NewStdioTransport("sh", []string{"-c", "sleep 30"}, nil)
	if got := tp.PidInfo(); got.PID != 0 || got.PGID != 0 || got.Command != "" {
		t.Fatalf("未 Start 应返回零值: %+v", got)
	}
	if err := tp.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	info := tp.PidInfo()
	if info.PID <= 0 || info.Command != "sh" {
		t.Fatalf("PID/Command 不符: %+v", info)
	}
	if info.PGID != info.PID {
		t.Fatalf("Setpgid 下 PGID 应等于 PID: %+v", info)
	}
	if err := tp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
