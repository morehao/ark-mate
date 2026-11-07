package cliops

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestMcpPlatformCLI `mate mcp platform` 的进程级用例（与 mcp_automation_test 同款）：
//   - stdin 送 initialize / tools/list → stdout 逐行是 JSON-RPC 帧（C10 纯度）；
//   - tools/list 返回**恰十二个工具**：automation_* 六个 + expert_* 六个（聚合器把两域合起来）；
//   - schema 里**不含 cron**（ADR v35 对 schedule 的约束仍成立）；
//   - expert schema 带 4096 字节上限与 id 正则（与 ValidateExpert 同口径）；
//   - stdin EOF → 进程 0 退出。
//
// 隔离：MATE_HOME / MATE_DB / MATE_CONFIG_DIR 都在 t.TempDir()，空库自动迁移（不碰用户环境）。
func TestMcpPlatformCLI(t *testing.T) {
	bin := buildMateCLI(t)
	isolated, _ := isolatedCLIEnvHome(t)

	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cli","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	cmd := exec.Command(bin, "mcp", "platform")
	cmd.Env = isolated
	cmd.Stdin = strings.NewReader(strings.Join(frames, "\n") + "\n")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("platform 服务端应正常退出（stdin EOF → 0）: %v\nstderr:\n%s", err, stderr.String())
	}

	toolNames := map[string]json.RawMessage{}
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
		if msg.ID != nil && strings.TrimSpace(string(*msg.ID)) == "2" {
			var r struct {
				Tools []struct {
					Name        string          `json:"name"`
					InputSchema json.RawMessage `json:"inputSchema"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(msg.Result, &r); err != nil {
				t.Fatalf("解析 tools/list 结果: %v\n%s", err, string(msg.Result))
			}
			sawTools = true
			for _, tl := range r.Tools {
				toolNames[tl.Name] = tl.InputSchema
				if strings.Contains(strings.ToLower(string(tl.InputSchema)), "cron") {
					t.Fatalf("工具 %q 的 schema 不该出现 cron", tl.Name)
				}
			}
		}
	}
	if !sawTools {
		t.Fatalf("没有收到 tools/list 响应：%s", stdout.String())
	}

	want := []string{
		"automation_create", "automation_update", "automation_list",
		"automation_get", "automation_delete", "automation_preview",
		"expert_create", "expert_update", "expert_list",
		"expert_get", "expert_delete", "expert_set_enabled",
	}
	for _, n := range want {
		if _, ok := toolNames[n]; !ok {
			t.Fatalf("工具面缺 %q：got %v", n, keys(toolNames))
		}
	}
	if len(toolNames) != len(want) {
		t.Fatalf("工具面应恰 %d 个工具，got %d：%v", len(want), len(toolNames), keys(toolNames))
	}

	// expert_create schema 的关键约束（与 capability.ValidateExpert 同口径）。
	createSchema := string(toolNames["expert_create"])
	for _, needle := range []string{`^[a-z0-9]+(-[a-z0-9]+)*$`, `"maxLength":4096`, `"pattern":"^mdl_"`} {
		if !strings.Contains(createSchema, needle) {
			t.Fatalf("expert_create schema 缺少 %q\n%s", needle, createSchema)
		}
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
