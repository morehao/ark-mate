package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/mcp"
)

// ——— M6-8 / P-13：`mcps` 白名单的写面 + 「下一轮生效」 ———
//
// P-13 的原话是"入 DB 只解决*读到的是新的*，不解决*何时生效*"。核这件事时发现前半句也没成立：
// `mcps` 在 M6-6 之前只从定义文件读、M6-6 之后只从 DB 行读，而**两个写面都没有这个字段**
// （`AgentCreateReq`/`AgentUpdateReq`/`AgentResp` 里都没有它），`mate agent import` 又刻意
// 不覆盖已有行 ⇒ 白名单事实上不可改。所以本任务的落地是两半：**给写面**（含三态）+ **发通知**。

// TestAgentMcpsWriteFaceThreeStates 写面三态：缺键不改 / null 清空 / [] 禁用全部 / 数组白名单，
// 且每一态都落到**载体**（sqlite 行）与内存两侧；类型不对是请求形态问题（40000），零副作用。
func TestAgentMcpsWriteFaceThreeStates(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t)
	if err := r.svc.createAgentDefinition(ctx, agentDefinitionFile{
		ID: "a1", Name: "A1", Mcps: ptrOf([]string{"fs"}),
	}); err != nil {
		t.Fatalf("落一条定义: %v", err)
	}
	r.svc.agents["a1"].Mcps = ptrOf([]string{"fs"})

	// ① 数组 → 白名单（内存 + 载体 + 响应三处一致）
	resp, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`["git","fs"]`)})
	if err != nil {
		t.Fatalf("PATCH mcps: %v", err)
	}
	if resp.Mcps == nil || !sameStringSet(*resp.Mcps, []string{"fs", "git"}) {
		t.Fatalf("响应应回带白名单: %+v", resp.Mcps)
	}
	if !strings.Contains(agentRowBody(t, r, "a1"), `"mcps"`) {
		t.Fatalf("载体里应写出 mcps: %s", agentRowBody(t, r, "a1"))
	}

	// ② [] → 禁用全部（**不是**"没改"、也**不是**"清空回全量"）
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`[]`)}); err != nil {
		t.Fatalf("PATCH mcps=[]: %v", err)
	}
	got, _ := r.svc.AgentByID("a1")
	if got.Mcps == nil || len(*got.Mcps) != 0 {
		t.Fatalf("[] 应表达为「非 nil 的空白名单」（禁用全部）: %+v", got.Mcps)
	}
	if !strings.Contains(agentRowBody(t, r, "a1"), `"mcps": []`) {
		t.Fatalf("空白名单应原样写进载体（与「未声明」在文件里长得不同）: %s", agentRowBody(t, r, "a1"))
	}

	// ③ 缺键 → 不改（`[]` 必须留着）
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Name: ptrOf("A1-改名")}); err != nil {
		t.Fatalf("PATCH name: %v", err)
	}
	got, _ = r.svc.AgentByID("a1")
	if got.Mcps == nil || len(*got.Mcps) != 0 {
		t.Fatalf("缺键不得动白名单: %+v", got.Mcps)
	}

	// ④ null → 清空 = 回落"未声明"（全量）
	resp, err = r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`null`)})
	if err != nil {
		t.Fatalf("PATCH mcps=null: %v", err)
	}
	if resp.Mcps != nil {
		t.Fatalf("null 应清成「未声明」（响应里省略）: %+v", resp.Mcps)
	}
	got, _ = r.svc.AgentByID("a1")
	if got.Mcps != nil {
		t.Fatalf("null 应把白名单清成 nil: %+v", got.Mcps)
	}
	if strings.Contains(agentRowBody(t, r, "a1"), `"mcps"`) {
		t.Fatalf("清空后载体里不该留 mcps 键（omitempty）: %s", agentRowBody(t, r, "a1"))
	}

	// ⑤ 类型不对 → 40000 且零副作用（内存/载体都没动）
	before := agentRowBody(t, r, "a1")
	for _, raw := range []string{`"fs"`, `{"a":1}`, `[""]`, `[1]`} {
		_, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(raw)})
		if numericCode(err) != 40000 {
			t.Fatalf("mcps=%s 应 40000，got %v（%d）", raw, err, numericCode(err))
		}
	}
	if after := agentRowBody(t, r, "a1"); after != before {
		t.Fatalf("被拒的 PATCH 不得改载体: %s → %s", before, after)
	}
}

// TestAgentMcpsCreateWritesWhitelist 创建面也带 mcps（否则"新 agent 一开始就限定 MCP"只能靠手改库）。
func TestAgentMcpsCreateWritesWhitelist(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t)
	resp, err := r.svc.CreateAgent(ctx, dtoagent.AgentCreateReq{
		ID: "b1", Name: "B1", Mcps: ptrOf([]string{"fs"}),
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if resp.Mcps == nil || len(*resp.Mcps) != 1 || (*resp.Mcps)[0] != "fs" {
		t.Fatalf("创建响应应回带白名单: %+v", resp.Mcps)
	}
	if body := agentRowBody(t, r, "b1"); !strings.Contains(body, `"fs"`) {
		t.Fatalf("载体里应写出白名单: %s", body)
	}
}

// TestAgentMcpsFileCarrier 同一套三态在 **memory 载体**（定义文件）上逐字成立：
// 两条载体的对外承诺相同（M6-6 的合成点就是这条）。
func TestAgentMcpsFileCarrier(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r := newRig(t, func(c *rigConfig) {
		c.stub = true
		c.agents = []*AgentRec{{ID: "f1", Name: "F1", State: AgentStateStopped}}
	})
	r.Svc.agentDir = dir
	if _, err := r.Svc.CreateAgent(ctx, dtoagent.AgentCreateReq{
		ID: "f2", Name: "F2", Mcps: ptrOf([]string{"fs"}),
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "f2.json"))
	if err != nil {
		t.Fatalf("读定义文件: %v", err)
	}
	var d agentDefinitionFile
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Mcps == nil || len(*d.Mcps) != 1 || (*d.Mcps)[0] != "fs" {
		t.Fatalf("文件里的白名单不符: %+v", d.Mcps)
	}
	// null 清空后文件里不该留键（省略 = 未声明 = 全量）。
	if _, err := r.Svc.UpdateAgent(ctx, "f2", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "f2.json"))
	if strings.Contains(string(raw), `"mcps"`) {
		t.Fatalf("清空后文件里不该留 mcps 键: %s", raw)
	}
}

// TestAgentMcpsChangeTakesEffectNextRun P-13 的正面用例：**缺省路径**（未声明能力选择的会话）
// 在写面改白名单之后，**下一轮**用新的注入集。
//
// 三条断言缺一不可（对应"读到的是新的 / 何时生效 / 代价多大"）：
//
//  1. 空闲句柄被作废（`session(id) == nil`）——不换这一下，下一轮只会复用旧句柄；
//  2. 重建后底座真的收到新的 `mcpServers`（管理侧算得再对，下发的若是旧集合就等于没生效）；
//  3. 缺省路径的指纹**仍是空**、连接数**仍是 1**——生效动作没有偷换代际键（R11），也没有多起进程。
func TestAgentMcpsChangeTakesEffectNextRun(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t)
	log := sessionNewLog(t)
	r.registerMcp(t, fsServer("v1"))
	r.registerMcp(t, capability.McpServer{ID: "git", Name: "git", Transport: capability.TransportStdio, Command: "/bin/echo"})
	r.svc.agents["a1"].Mcps = ptrOf([]string{"fs"})

	convID, as1 := r.start(t, nil) // nil = 缺省路径（P-13 说的正是这一条）
	if as1.Fingerprint != "" {
		t.Fatalf("缺省路径的指纹必须为空: %q", as1.Fingerprint)
	}
	if first := mcpNamesOf(t, log(), 0); !sameStringSet(first, []string{"fs"}) {
		t.Fatalf("首轮注入集应只有 fs（白名单），got %v", first)
	}

	// 写面改白名单（真实入口：PATCH /agents/:id）。
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`["fs","git"]`)}); err != nil {
		t.Fatalf("PATCH mcps: %v", err)
	}
	if r.orch.session(convID) != nil {
		t.Fatal("空闲会话的句柄应在写面之后立刻作废（否则下一轮还会用旧注入集）")
	}
	if n := len(log()); n != 1 {
		t.Fatalf("作废句柄本身不该开会话（重建发生在下一轮）: 日志 %d 条", n)
	}

	// 下一轮走真实入口取句柄 → 重建 → 下发新注入集。
	as2 := r.boundary(t, convID)
	if as2 == as1 {
		t.Fatal("句柄应已重建")
	}
	if as2.Fingerprint != "" {
		t.Fatalf("缺省路径重建后指纹仍须为空（R11：不因定义变化偷换代际）: %q", as2.Fingerprint)
	}
	if got := mcpNamesOf(t, log(), 1); !sameStringSet(got, []string{"fs", "git"}) {
		t.Fatalf("下一轮应下发新注入集，got %v", got)
	}
	if n := r.conns(); n != 1 {
		t.Fatalf("作废句柄不等于关进程：同配置仍应共享一个暖进程，got %d 条连接", n)
	}

	// 语义相同的改动（同集合、换顺序）不该再动会话：白名单是过滤器，重排不改变任何注入结果。
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`["git","fs"]`)}); err != nil {
		t.Fatal(err)
	}
	if r.orch.session(convID) != as2 {
		t.Fatal("等价集合（仅顺序不同）不得作废句柄")
	}
	if n := len(log()); n != 2 {
		t.Fatalf("等价改动不该触发重建: 日志 %d 条", n)
	}
}

// TestAgentMcpsChangeDefersBusySession 忙则顺延（ADR-13 纪律③的同一条判据）：
// 有在途轮次、或有未应答的权限卡时**不动**句柄——这一轮继续用旧面跑完，下一次重建才换。
func TestAgentMcpsChangeDefersBusySession(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	r.svc.agents["a1"].Mcps = ptrOf([]string{"fs"})
	convID, as1 := r.start(t, nil)

	// ① 有在途轮次。`as.busy` 由 executeRun 置位、unmarkBusy 清除；这里直接置位是有意的：
	//    本用例要钉的是"裁决读到 busy 会怎么办"，而不是"busy 怎么被置上"（后者由轮次用例管）。
	as1.mu.Lock()
	as1.busy = true
	as1.mu.Unlock()
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`["fs","git"]`)}); err != nil {
		t.Fatal(err)
	}
	if r.orch.session(convID) != as1 {
		t.Fatal("有在途轮次时必须顺延（绝不打断跑着的一轮）")
	}
	as1.mu.Lock()
	as1.busy = false
	as1.mu.Unlock()

	// ② 有未应答的权限卡：同样是"别动用户正盯着的会话"。
	r.orch.perm.mu.Lock()
	r.orch.perm.pending["perm-test"] = &pendingPermission{
		callID: "perm-test", sessionID: convID, ch: make(chan acl.PermissionDecision, 1),
	}
	r.orch.perm.mu.Unlock()
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`["git"]`)}); err != nil {
		t.Fatal(err)
	}
	if r.orch.session(convID) != as1 {
		t.Fatal("有未应答权限卡时必须顺延")
	}

	// ③ 权限卡收敛之后，再改一次就一定生效（顺延不是"永远不生效"）。换一个**真变了**的集合：
	// 语义没变的 PATCH 本来就不该发通知（上面那两条已经把它写进内存了）。
	r.orch.perm.CancelSession(convID)
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Mcps: json.RawMessage(`["fs"]`)}); err != nil {
		t.Fatal(err)
	}
	if r.orch.session(convID) != nil {
		t.Fatal("权限卡收敛后应立刻作废句柄")
	}
}

// TestAgentWriteWithoutMcpsChangeKeepsHandles 不涉及能力面的写（改名）不得动会话：
// "改个显示名 = 所有会话重建"是不可接受的代价。
func TestAgentWriteWithoutMcpsChangeKeepsHandles(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t)
	r.registerMcp(t, fsServer("v1"))
	convID, as1 := r.start(t, nil)
	if _, err := r.svc.UpdateAgent(ctx, "a1", dtoagent.AgentUpdateReq{Name: ptrOf("A1 新名字")}); err != nil {
		t.Fatal(err)
	}
	if r.orch.session(convID) != as1 {
		t.Fatal("只改名字不得作废会话句柄")
	}
}

// agentRowBody 读该 agent 在载体里的正文（sqlite 模式 = 行；本文件用它断言"写面真的落载体了"）。
func agentRowBody(t *testing.T, r *genRig, id string) string {
	t.Helper()
	row, found, err := r.svc.st.GetAgentDefinition(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("读定义行: found=%v err=%v", found, err)
	}
	return row.Body
}

// mcpNamesOf 取第 i 次 `session/new` 下发的 MCP name 集合（底座收到什么才是"生效"的唯一判据）。
func mcpNamesOf(t *testing.T, entries []map[string]any, i int) []string {
	t.Helper()
	if len(entries) <= i {
		t.Fatalf("session/new 日志不足：want 第 %d 条，got %d 条", i+1, len(entries))
	}
	raw, _ := json.Marshal(entries[i]["mcpServers"])
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("mcpServers 不是数组: %v（%s）", err, raw)
	}
	out := make([]string, 0, len(list))
	for _, m := range list {
		if name, _ := m["name"].(string); name != "" && name != mcp.GatewayServerName {
			// 恒定的平台网关条目另计（M7-3）：本文件的断言都是"MCP 白名单 = 过滤器"，
			// 网关条目与白名单无关，混进来只会让每条断言都要减一。
			out = append(out, name)
		}
	}
	return out
}
