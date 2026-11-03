package cliops

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestMcpMaterialCLI `mate mcp material` 的进程级用例（P4）：
//   - stdin 送 initialize / tools/list → stdout 逐行是 JSON-RPC 帧（C10 纯度）；
//   - tools/list 返回**恰三个文件级工具**（library_list_nodes / library_node_info / library_upload）；
//   - **不含 library_search**（本期只做资料库、不做知识库）；
//   - stdin EOF → 进程 0 退出（父进程结束的正常收尾）。
//
// 隔离：MATE_HOME / MATE_DB / MATE_CONFIG_DIR 都在 t.TempDir()，空库自动迁移（不碰用户环境）。
func TestMcpMaterialCLI(t *testing.T) {
	bin := buildMateCLI(t)
	isolated, _ := isolatedCLIEnvHome(t)

	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cli","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	cmd := exec.Command(bin, "mcp", "material")
	cmd.Env = isolated
	cmd.Stdin = strings.NewReader(strings.Join(frames, "\n") + "\n")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("material 服务端应正常退出（stdin EOF → 0）: %v\nstderr:\n%s", err, stderr.String())
	}

	// stdout 逐行必须是 JSON-RPC（一行日志就毁掉整条会话）。
	var toolNames []string
	sawTools := false
	for i, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg struct {
			JSONRPC string           `json:"jsonrpc"`
			ID      *json.RawMessage `json:"id"`
			Result  json.RawMessage  `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout 第 %d 行不是 JSON-RPC（stdout 被污染）：%q", i+1, line)
		}
		if msg.JSONRPC != "2.0" {
			t.Fatalf("第 %d 行 jsonrpc≠2.0：%q", i+1, line)
		}
		// tools/list 结果（id=2）里抓工具名。
		if msg.ID != nil && strings.TrimSpace(string(*msg.ID)) == "2" {
			var r struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(msg.Result, &r); err != nil {
				t.Fatalf("解析 tools/list 结果: %v\n%s", err, string(msg.Result))
			}
			sawTools = true
			for _, tl := range r.Tools {
				toolNames = append(toolNames, tl.Name)
			}
		}
	}
	if !sawTools {
		t.Fatalf("没有收到 tools/list 响应：%s", stdout.String())
	}
	want := map[string]bool{"library_list_nodes": true, "library_node_info": true, "library_upload": true}
	got := map[string]bool{}
	for _, n := range toolNames {
		got[n] = true
	}
	for n := range want {
		if !got[n] {
			t.Fatalf("工具面缺 %q：got %v", n, toolNames)
		}
	}
	for n := range got {
		if n == "library_search" {
			t.Fatalf("工具面**不得**含 library_search（本期只做资料库、不做知识库）")
		}
	}
	if len(toolNames) != 3 {
		t.Fatalf("工具面应恰 3 个文件级工具，got %v", toolNames)
	}
}
