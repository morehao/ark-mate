package archtest

// 本文件钉住**命令门禁真的接在裁决点上**（A2 / O20，2026-10-17）。
//
// 这一族最典型的失效是"有实现、没接线"：`command_policy.go` 的规则、加载期自校验与
// `DecideCommand` 全都写好了，但 `requestPermission` 从未调用它——所有测试照旧绿，
// 而门禁一行都没生效。第二典型的失效是**接在记忆之后**：用户说过"这类工具始终允许"，
// 规则说 forbidden 的命令于是被记忆放行（两个裁决器各管一摊，谁在前谁说了算）。
//
// 判据四段（fail-closed）：
//
//	① `permission_flow.go` 里 `commandGateDecision(` 出现在 `AutoDecision(` **之前**；
//	② 同一文件用 `toolCallFor(` 回查工具详情（权限请求只带 toolCallId，命令行在同 id 的
//	   tool_call 更新里）；
//	③ `app.go` 真的加载规则（`LoadCommandPolicyFile(`）并交给 Service（`SetCommandPolicy(`）；
//	④ `domain/session/eventsink.go` 写入/删除工具调用记忆（P4a 第五十五刀搬入）（`rememberToolCall(` / `forgetToolCall(`）。
//
// 反证（实跑）：把 `AutoDecision` 挪到门禁之前 → ① 红；抽掉 app 的加载调用 → ③ 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommandGateIsWiredToPermissionDecision 见文件头。
func TestCommandGateIsWiredToPermissionDecision(t *testing.T) {
	root := backendRoot(t)
	read := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("读 %s 失败: %v（判据失去对象）", rel, err)
		}
		return string(raw)
	}

	// ①② 裁决点顺序 + 回查
	flow := stripGoComments(read("internal/sessionruntime/permission_flow.go"))
	// 必须落在 requestPermission 的**函数体**里：本文件末尾也有 commandGateDecision 的
	// 定义，按整文件找会被定义那一行喂饱（本仓已经踩过"判据被定义喂饱"的坑）。
	body, ok := goFuncBody(flow, "func (o *Orchestrator) requestPermission(")
	if !ok {
		t.Fatalf("permission_flow.go 里找不到 requestPermission——裁决点改名/搬家时回来改这条判据")
	}
	gateIdx := strings.Index(body, "commandGateDecision(")
	memIdx := strings.Index(body, "AutoDecision(")
	if gateIdx < 0 {
		t.Fatalf("permission_flow.go 没有调用 commandGateDecision——门禁有实现、没接线")
	}
	if memIdx < 0 {
		t.Fatalf("permission_flow.go 找不到 AutoDecision(——判据失去对象")
	}
	if gateIdx > memIdx {
		t.Errorf("命令门禁排在会话级记忆**之后**——规则说 forbidden 的命令会被『这类工具始终允许』放行")
	}
	if !strings.Contains(body, "o.tools.Get(") {
		t.Errorf("permission_flow.go 没有 o.tools.Get(——权限请求只带 toolCallId，不回查就拿不到命令行")
	}

	// ③ 装配：加载 + 交给 service（P5a 起这条动作在 app/assembly.go 的声明表里）
	app := stripGoComments(read("internal/app/assembly.go"))
	if !strings.Contains(app, "LoadCommandPolicyFile(") {
		t.Errorf("assembly.go 没有 LoadCommandPolicyFile(——规则文件写了但没人读")
	}
	if !strings.Contains(app, "SetCommandPolicy(") {
		t.Errorf("assembly.go 没有 SetCommandPolicy(——规则加载了但没交给裁决点")
	}

	// ④ 工具详情记忆的写入/删除
	sink := stripGoComments(read("internal/domain/session/eventsink.go"))
	if !strings.Contains(sink, "rememberToolCall(") {
		t.Errorf("eventsink.go 没有 rememberToolCall(——权限请求到达时查不到命令行")
	}
	if !strings.Contains(sink, "forgetToolCall(") {
		t.Errorf("eventsink.go 没有 forgetToolCall(——工具详情会随进程只增不减")
	}

	// 配置键：MATE_COMMAND_POLICY 是唯一入口。
	cfg := read("pkg/config/config.go")
	if !strings.Contains(cfg, "MATE_COMMAND_POLICY") || !strings.Contains(cfg, "CommandPolicyPath") {
		t.Errorf("config 没有 MATE_COMMAND_POLICY / CommandPolicyPath——规则文件没有配置入口")
	}
}
