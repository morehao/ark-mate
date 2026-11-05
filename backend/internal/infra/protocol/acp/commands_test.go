package acp

import (
	"context"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// TestCommandsFromSDK 归一化 ACP 命令列表：名字/说明/入参提示映射，无名条目与空入参不伪造。
func TestCommandsFromSDK(t *testing.T) {
	in := []acpsdk.AvailableCommand{
		{
			Name:        "review",
			Description: "审查改动",
			Input:       &acpsdk.AvailableCommandInput{Unstructured: &acpsdk.UnstructuredCommandInput{Hint: "文件路径"}},
		},
		{Name: "init", Description: "初始化"},
		// 名字是唯一可执行标识：空名字既无法补全也无法发送，跳过而不是造一个占位名。
		{Name: "", Description: "匿名"},
		// Input 非 nil 但 Unstructured 为 nil（其它入参变体尚未定义）→ 不 panic、Hint 留空。
		{Name: "plan", Input: &acpsdk.AvailableCommandInput{}},
	}
	got := commandsFromSDK(in)
	if len(got) != 3 {
		t.Fatalf("无名条目应被跳过，得到 %d 条: %+v", len(got), got)
	}
	if got[0].Name != "review" || got[0].Hint != "文件路径" || got[0].Description != "审查改动" {
		t.Fatalf("带参命令映射不符: %+v", got[0])
	}
	if got[1].Name != "init" || got[1].Hint != "" {
		t.Fatalf("无参命令映射不符: %+v", got[1])
	}
	if got[2].Name != "plan" || got[2].Hint != "" {
		t.Fatalf("空入参不应 panic 且 Hint 留空: %+v", got[2])
	}
}

// TestAvailableCommandsUpdatePush 运行时推送 available_commands_update →
// 归一化为 commands 事件，载荷是**全量**列表且保序（平台不重排底座的展示意图）。
func TestAvailableCommandsUpdatePush(t *testing.T) {
	agent := &fakeAgent{
		init:       fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), nil),
		newCfgOpts: []acpsdk.SessionConfigOption{modelOptionFlat("test/test-model")},
		promptUpdates: []acpsdk.SessionUpdate{{AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
			SessionUpdate: "available_commands_update",
			AvailableCommands: []acpsdk.AvailableCommand{
				{Name: "review", Description: "审查改动"},
				{Name: "init"},
			},
		}}},
	}
	events := make(chan Event, 8)
	cl := dialFake(t, agent, ClientOptions{OnEvent: func(e Event) { events <- e }})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, _, err := sess.Prompt(context.Background(), protocol.TextParts("hi")); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != KindCommands {
			t.Fatalf("事件 kind 不符: %s", ev.Kind)
		}
		info, ok := ev.Payload.(CommandsInfo)
		if !ok {
			t.Fatalf("载荷类型不符: %+v", ev.Payload)
		}
		if info.SessionID != sess.ID() {
			t.Fatalf("载荷会话 id 不符: %+v", info)
		}
		if len(info.Commands) != 2 || info.Commands[0].Name != "review" || info.Commands[1].Name != "init" {
			t.Fatalf("载荷应含全量命令且保序: %+v", info.Commands)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 commands 事件")
	}
}

// TestAvailableCommandsUpdateEmptyStillEmitted 空列表也要上抛：
// "底座通告了当前没有命令"是让前端清空既有候选的事实，不能当成"没发生"而静默丢弃。
func TestAvailableCommandsUpdateEmptyStillEmitted(t *testing.T) {
	agent := &fakeAgent{
		init:       fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), nil),
		newCfgOpts: []acpsdk.SessionConfigOption{modelOptionFlat("test/test-model")},
		promptUpdates: []acpsdk.SessionUpdate{{AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
			SessionUpdate:     "available_commands_update",
			AvailableCommands: []acpsdk.AvailableCommand{},
		}}},
	}
	events := make(chan Event, 8)
	cl := dialFake(t, agent, ClientOptions{OnEvent: func(e Event) { events <- e }})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, _, err := sess.Prompt(context.Background(), protocol.TextParts("hi")); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	select {
	case ev := <-events:
		info, ok := ev.Payload.(CommandsInfo)
		if !ok {
			t.Fatalf("载荷类型不符: %+v", ev.Payload)
		}
		if info.Commands == nil {
			t.Fatal("空列表应是非 nil 空切片（nil 会被 JSON 序列化成 null，前端数组语义会崩）")
		}
		if len(info.Commands) != 0 {
			t.Fatalf("空列表通告不应携带命令: %+v", info.Commands)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("空列表通告未上抛")
	}
}

// TestAvailableCommandsUpdateNilOnEvent nil 回调不 panic（未注入 OnEvent 的连接照常工作）。
func TestAvailableCommandsUpdateNilOnEvent(t *testing.T) {
	h := &hostClient{}
	err := h.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: "s1",
		Update: acpsdk.SessionUpdate{AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
			AvailableCommands: []acpsdk.AvailableCommand{{Name: "review"}},
		}},
	})
	if err != nil {
		t.Fatalf("nil OnEvent 不应报错: %v", err)
	}
}
