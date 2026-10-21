package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/eventbus"
)

// 本文件是会话域**transcript 测试夹具的公共底座**：形状常量（会话 id / 事件 id）与落盘助手。
//
// 这些字面量此前在 `manager/transcript_load_test.go` 里，P3b-2b 随 transcript 面搬进本域；
// **manager 侧仍有一份同名同值的副本**（`manager/transcript_load_test.go` 的旧形状会话 id
// 等），因为那边还有据此造的端到端夹具。两份都从同一套形状派生，**刻意不互相 import**
// （manager 的测试若 import 域的测试文件，包依赖会绕成环）。

// testPlane 造一个只带总线与 transcript 目录的会话域（内存模式）。
//
// 归属展示端口接上**最小实现**：`ListSessions` 会走 `workspaceDisplayOf`，而它是**函数字段**
// ——没接就是 nil 调用，直接 panic（这不是"接口端口的 nil 接收者"那类问题，函数值没有接收者）。
// 最小实现保持"id 原样、无标题"（域内测试不验归属展示那一面）。
//
// 能力选定回显 P3c 起不再需要端口（`SessionCapabilitiesRespOf` 在域内）。
func testPlane(t *testing.T, dir string) (*Plane, *eventbus.Bus) {
	t.Helper()
	bus := eventbus.New()
	pl := NewPlane()
	pl.SetBus(bus)
	pl.SetWorkspaceDisplay(func(workspaceID string) (string, string) { return workspaceID, "" })
	if dir != "" {
		pl.SetTranscriptDir(dir)
	}
	return pl, bus
}

// 测试用平台会话 id：v15 起平台 id 只由 objid 铸（`ses_<26 位 Crockford Base32>`）。
// 夹具用**合法形状的固定字面量**（而不是 objid.New），是为了让 JSONL 字面量保持可读、可 grep。
const (
	sesLegacyA  = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PA"
	sesLegacyB  = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PB"
	sesLegacyC  = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PC"
	sesWsTemp   = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PT"
	sesWsLegacy = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PS"
)

// 事件身份夹具（`evt_` + 26 位主体）。v17 起 transcript 事件行必须带它——装载面缺 id/seq
// 会把**整份文件**跳过（不代铸：身份必须与用户手里那份文件一致）。
const (
	evtA1 = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1E1"
	evtA2 = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1E2"
	evtA3 = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1E3"
	evtB1 = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1F1"
	evtB2 = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1F2"
	evtC1 = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1G1"
)

// writeTranscriptJSONL 直接落一个合法 transcript 文件（header + 事件行），用于回读测试。
func writeTranscriptJSONL(t *testing.T, dir, filename string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
