package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/gerror"
)

// ——— M6-3 写面换载体的验收（sqlite 模式：DB 行 + revision CAS + 变更审计） ———
//
// memory 模式的写面由 skills_test.go / mcps_test.go 的既有用例逐字覆盖（本提交**未改**它们
// 的断言，证明文件载体零回归）；本文件补的是 DB 载体独有的那几条：行与其 revision、审计、
// CAS 拒绝、以及"闸门拒绝时零写入"。

// storeCapRig sqlite 模式的能力写面夹具：**不配 MATE_CAPABILITIES 目录**（DB 模式不需要），
// 登记一处物化目标（启停/写入后应立刻重校）。
func storeCapRig(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) { c.store = true })
	root := t.TempDir()
	r.Svc.SetSkillProjections([]SkillProjection{skillProj(root, "opencode")})
	return r, root
}

// rawObj 拼一个写面请求体（键的出现与否有语义，故用 RawMessage）。
func rawObj(t *testing.T, kv ...string) map[string]json.RawMessage {
	t.Helper()
	out := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = json.RawMessage(kv[i+1])
	}
	return out
}

func TestStoreSkillWriteFaceRoundTrip(t *testing.T) {
	r, root := storeCapRig(t)
	ctx := context.Background()
	id := "db-skill"

	// —— 新建：落行 + 入表 + 审计 + 物化 ——
	resp, err := r.Svc.CreateSkill(ctx, dtocapability.SkillWriteReq(rawObj(t,
		"id", `"db-skill"`, "name", `"DB Skill"`, "body", `"# DB"`, "runtimes", `["opencode"]`,
		"my_note", `"手写字段"`)))
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if resp.ID != id || !resp.Created || resp.Path != "db:skill/"+id {
		t.Fatalf("新建响应不符: %+v", resp)
	}
	row, found, err := r.St.GetSkillDefinition(ctx, id)
	if err != nil || !found {
		t.Fatalf("DB 里应有该行: found=%v err=%v", found, err)
	}
	if row.Revision != 1 {
		t.Fatalf("新建后 revision 应为 1，got %d", row.Revision)
	}
	if !strings.Contains(row.Body, `"my_note": "手写字段"`) {
		t.Fatalf("未知键应原样进 body: %s", row.Body)
	}
	if _, ok := r.Svc.caps.SkillByID(id); !ok {
		t.Fatal("注册表应有该条目")
	}
	// 物化：sqlite 写面同样要立刻把副本写到目标里（否则"生效了"只是内存里的话）。
	if !materializedAt(t, root, id) {
		t.Fatalf("应物化 %s/SKILL.md", id)
	}
	revs := mustRevisions(t, r, model.CapabilityKindSkill, id)
	if len(revs) != 1 || revs[0].Action != model.CapabilityActionCreate || revs[0].Actor != "rest" {
		t.Fatalf("新建审计不符: %+v", revs)
	}
	if revs[0].Digest == "" {
		t.Fatal("新建审计应带正文摘要")
	}

	// —— 覆盖写：只改出现的键，未知键与其余键保留；revision 递增；审计追加 ——
	resp, err = r.Svc.UpdateSkill(ctx, id, dtocapability.SkillWriteReq(rawObj(t, "name", `"改名了"`)))
	if err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}
	if resp.Created || resp.Path != "db:skill/"+id {
		t.Fatalf("覆盖写响应不符: %+v", resp)
	}
	row, _, _ = r.St.GetSkillDefinition(ctx, id)
	if row.Revision != 2 {
		t.Fatalf("覆盖写后 revision 应为 2，got %d", row.Revision)
	}
	for _, want := range []string{`"name": "改名了"`, `"body": "# DB"`, `"my_note": "手写字段"`} {
		if !strings.Contains(row.Body, want) {
			t.Fatalf("覆盖写应保留 %s，got %s", want, row.Body)
		}
	}
	if revs := mustRevisions(t, r, model.CapabilityKindSkill, id); len(revs) != 2 ||
		revs[1].Action != model.CapabilityActionUpdate {
		t.Fatalf("覆盖写审计不符: %+v", mustRevisions(t, r, model.CapabilityKindSkill, id))
	}

	// —— 删除：行没了、内存条目没了、物化副本撤回、审计仍在（删了也留痕） ——
	del, err := r.Svc.DeleteSkill(ctx, id)
	if err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	if !del.Deleted || del.Path != "db:skill/"+id || del.Removed != 1 {
		t.Fatalf("删除响应不符: %+v", del)
	}
	if _, found, _ := r.St.GetSkillDefinition(ctx, id); found {
		t.Fatal("删除后 DB 里不该还有该行")
	}
	if _, ok := r.Svc.caps.SkillByID(id); ok {
		t.Fatal("删除后注册表不该还有该条目")
	}
	if materializedAt(t, root, id) {
		t.Fatalf("删除后应撤回物化副本（%s/SKILL.md 仍在）", id)
	}
	revs = mustRevisions(t, r, model.CapabilityKindSkill, id)
	if len(revs) != 3 || revs[2].Action != model.CapabilityActionDelete {
		t.Fatalf("删除审计不符: %+v", revs)
	}
	if revs[2].Digest != "" {
		t.Fatalf("删除审计的正文摘要应为空，got %q", revs[2].Digest)
	}

	// —— 幂等删除：第二次是 noop，且**不追加审计**（没发生的变更不该留痕） ——
	del2, err := r.Svc.DeleteSkill(ctx, id)
	if err != nil {
		t.Fatalf("重复删除: %v", err)
	}
	if del2.Deleted {
		t.Fatalf("重复删除应 deleted=false: %+v", del2)
	}
	if revs := mustRevisions(t, r, model.CapabilityKindSkill, id); len(revs) != 3 {
		t.Fatalf("noop 删除不该追加审计: %+v", revs)
	}
}

// TestStoreSkillWriteFaceConflictIsNotLastWriteWins CAS 没中时必须**拒绝**并保住别人的那一版。
//
// 写面自身的读→写窗口极短（open 刚读完就 commit），无法在测试里插进另一个写者，所以这里
// 直接对载体（repo）做：这正是"绝不 last-write-wins"这条承诺的落点。
func TestStoreSkillWriteFaceConflictIsNotLastWriteWins(t *testing.T) {
	r, _ := storeCapRig(t)
	ctx := context.Background()

	if _, err := r.Svc.CreateSkill(ctx, dtocapability.SkillWriteReq(rawObj(t, "id", `"cas-skill"`, "name", `"orig"`))); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	repo, err := r.Svc.definitionRepoFor(capSubSkills)
	if err != nil {
		t.Fatal(err)
	}
	doc, rev, existed, err := repo.open(ctx, "cas-skill", map[string]any{"name": "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 || !existed {
		t.Fatalf("open 应读到 revision=1 且存在: rev=%d existed=%v", rev, existed)
	}

	// 别人抢先改了一版（等价于面板保存与手工修库/另一个进程撞上）。
	other := `{"id":"cas-skill","name":"theirs"}` + "\n"
	if ok, err := r.St.UpsertSkillDefinition(ctx, "cas-skill", other, 1, "other"); err != nil || !ok {
		t.Fatalf("外部写入应成功: ok=%v err=%v", ok, err)
	}

	created, conflict, err := repo.commit(ctx, "cas-skill", doc, rev, existed)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !conflict || created {
		t.Fatalf("CAS 应判冲突: created=%v conflict=%v", created, conflict)
	}
	row, _, _ := r.St.GetSkillDefinition(ctx, "cas-skill")
	if !strings.Contains(row.Body, "theirs") || strings.Contains(row.Body, "mine") || row.Revision != 2 {
		t.Fatalf("冲突时不该覆盖别人的那一版: rev=%d body=%s", row.Revision, row.Body)
	}

	// 「我以为是新建、别人抢先建出来」也走同一条冲突判定（不是 last-write-wins）。
	doc2, rev2, existed2, err := repo.open(ctx, "race-skill", map[string]any{"name": "mine"})
	if err != nil || existed2 {
		t.Fatalf("race-skill 应不存在: existed=%v err=%v", existed2, err)
	}
	if doc2 == nil || rev2 != 0 {
		t.Fatalf("不存在的定义 rev 应为 0: rev=%d", rev2)
	}
	if ok, err := r.St.UpsertSkillDefinition(ctx, "race-skill", `{"id":"race-skill"}`, 0, "other"); err != nil || !ok {
		t.Fatalf("抢先新建应成功: ok=%v err=%v", ok, err)
	}
	if _, conflict, err := repo.commit(ctx, "race-skill", doc2, rev2, existed2); err != nil || !conflict {
		t.Fatalf("抢先新建后应判冲突: conflict=%v err=%v", conflict, err)
	}

	// 冲突到对外信封的映射（与文件载体时代同码同义）：40901 + definition_changed。
	// 写面里 `conflict → definitionChangedErr` 是一行直连，所以这里钉住那一行的产物即可。
	rawErr := definitionChangedErr(repo.locator("cas-skill"))
	if !strings.Contains(rawErr.Error(), "definition_changed") ||
		!strings.Contains(rawErr.Error(), "db:skill/cas-skill") {
		t.Fatalf("冲突错误应带稳定码与定位串: %v", rawErr)
	}
	// 注意 `code.FromError` 的文案是**码的默认文案**（acl 细节不进信封），所以稳定码只在
	// acl 错误自身上可见——面板按 40901 分流，用户看默认文案。
	if ge, ok := code.FromError(rawErr).(gerror.Error); !ok || ge.Code != code.Conflict {
		t.Fatalf("CAS 冲突应落 40901，got %v", code.FromError(rawErr))
	}
}

// TestStoreWriteFaceGateRejectsWithoutWrite 闸门拒绝（40001）时**载体里一个字节都没写**。
//
// 这正是"先校验、后提交"换来的性质：M6-3 之前是"先入表、提交失败再回滚"，回滚漏一条就留下
// 幻影条目；现在校验在提交之前，坏定义连行都不会出现。
func TestStoreWriteFaceGateRejectsWithoutWrite(t *testing.T) {
	r, _ := storeCapRig(t)
	ctx := context.Background()

	// skill：非法 scope。
	_, err := r.Svc.CreateSkill(ctx, dtocapability.SkillWriteReq(rawObj(t, "id", `"bad-scope"`, "scope", `"bogus"`)))
	if err == nil || !strings.Contains(err.Error(), "scope") || !strings.Contains(err.Error(), "definition_invalid") {
		t.Fatalf("非法 scope 应被闸门以 definition_invalid 拒绝: %v", err)
	}
	if ge := envelopeCode(t, code.FromError(err)); ge != code.InvalidRequest {
		t.Fatalf("闸门拒绝应落 40001，got %d (%v)", ge, err)
	}
	if _, found, _ := r.St.GetSkillDefinition(ctx, "bad-scope"); found {
		t.Fatal("被拒的定义不该在 DB 里留行")
	}
	if _, ok := r.Svc.caps.SkillByID("bad-scope"); ok {
		t.Fatal("被拒的定义不该进注册表")
	}
	if revs := mustRevisions(t, r, model.CapabilityKindSkill, "bad-scope"); len(revs) != 0 {
		t.Fatalf("被拒的写入不该留审计: %+v", revs)
	}

	// mcp：stdio 命令不是绝对路径（同一道闸门，MCP 侧最常踩的一条）。
	_, err = r.Svc.CreateMcpServer(ctx, dtocapability.McpWriteReq(rawObj(t,
		"id", `"bad-mcp"`, "transport", `"stdio"`, "command", `"npx"`)))
	if err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("相对命令应被闸门拒绝: %v", err)
	}
	if _, found, _ := r.St.GetMcpServerDefinition(ctx, "bad-mcp"); found {
		t.Fatal("被拒的 mcp 定义不该在 DB 里留行")
	}
	if _, ok := r.Svc.caps.GetMcpServer("bad-mcp"); ok {
		t.Fatal("被拒的 mcp 定义不该进注册表")
	}
}

// TestStoreSkillEnabledTogglesOnlyOneKey 启停在 DB 载体上只改 `enabled` 一个键（未知键保留）。
func TestStoreSkillEnabledTogglesOnlyOneKey(t *testing.T) {
	r, root := storeCapRig(t)
	ctx := context.Background()

	if _, err := r.Svc.CreateSkill(ctx, dtocapability.SkillWriteReq(rawObj(t,
		"id", `"toggle"`, "name", `"T"`, "body", `"# T"`, "my_note", `"留着"`))); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if !materializedAt(t, root, "toggle") {
		t.Fatal("新建后应物化 toggle/SKILL.md")
	}

	resp, err := r.Svc.SetSkillEnabled(ctx, "toggle", false)
	if err != nil {
		t.Fatalf("SetSkillEnabled: %v", err)
	}
	if resp.Enabled || resp.Path != "db:skill/toggle" || resp.Removed != 1 {
		t.Fatalf("禁用响应不符: %+v", resp)
	}
	if materializedAt(t, root, "toggle") {
		t.Fatal("禁用后应撤回物化副本")
	}
	row, _, _ := r.St.GetSkillDefinition(ctx, "toggle")
	if !strings.Contains(row.Body, `"enabled": false`) {
		t.Fatalf("启停应写回 enabled:false: %s", row.Body)
	}
	for _, want := range []string{`"my_note": "留着"`, `"body": "# T"`} {
		if !strings.Contains(row.Body, want) {
			t.Fatalf("启停不该动别的键（缺 %s）: %s", want, row.Body)
		}
	}
	if row.Revision != 2 {
		t.Fatalf("启停应让 revision 递增到 2，got %d", row.Revision)
	}
	if revs := mustRevisions(t, r, model.CapabilityKindSkill, "toggle"); len(revs) != 2 {
		t.Fatalf("启停应留一条审计: %+v", revs)
	}

	// 再启用：写回 enabled:true 且副本回来。
	if resp, err := r.Svc.SetSkillEnabled(ctx, "toggle", true); err != nil || !resp.Enabled || resp.Written != 1 {
		t.Fatalf("启用不符: %+v err=%v", resp, err)
	}
	row, _, _ = r.St.GetSkillDefinition(ctx, "toggle")
	if !strings.Contains(row.Body, `"enabled": true`) || row.Revision != 3 {
		t.Fatalf("启用应写回 true 且 revision=3: rev=%d body=%s", row.Revision, row.Body)
	}
}

// TestStoreSkillEnabledNotPersistable 代码注入的技能（注册表有、DB 无行）→ 显式失败，且**不建行**。
func TestStoreSkillEnabledNotPersistable(t *testing.T) {
	r, _ := storeCapRig(t)
	ctx := context.Background()
	if err := r.Svc.caps.RegisterSkill(&capability.Skill{ID: "injected", Body: "# x"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Svc.SetSkillEnabled(ctx, "injected", false)
	if err == nil || !strings.Contains(err.Error(), "没有定义行") {
		t.Fatalf("应显式失败并给出路: %v", err)
	}
	if ge := envelopeCode(t, code.FromError(err)); ge != code.InvalidRequest {
		t.Fatalf("应落 40001，got %d (%v)", ge, err)
	}
	if _, found, _ := r.St.GetSkillDefinition(ctx, "injected"); found {
		t.Fatal("启停不该顺手为代码注入的技能建行")
	}
}

// TestStoreMcpWriteFaceRoundTrip mcp 写面的 DB 载体一条龙，含"删 server 连带摘工具"。
func TestStoreMcpWriteFaceRoundTrip(t *testing.T) {
	r, _ := storeCapRig(t)
	ctx := context.Background()

	resp, err := r.Svc.CreateMcpServer(ctx, dtocapability.McpWriteReq(rawObj(t,
		"id", `"fs"`, "transport", `"stdio"`, "command", `"/usr/bin/env"`, "args", `["node"]`,
		"secret_env", `{"TOKEN":"vault:fs/token"}`)))
	if err != nil {
		t.Fatalf("CreateMcpServer: %v", err)
	}
	if resp.ID != "fs" || !resp.Created || resp.Path != "db:mcp/fs" {
		t.Fatalf("新建响应不符: %+v", resp)
	}
	if len(resp.UnresolvedSecretKeys) != 1 || resp.UnresolvedSecretKeys[0] != "TOKEN" {
		t.Fatalf("应报出未解析的密钥键: %+v", resp.UnresolvedSecretKeys)
	}
	row, found, err := r.St.GetMcpServerDefinition(ctx, "fs")
	if err != nil || !found || row.Revision != 1 {
		t.Fatalf("DB 行不符: found=%v rev=%d err=%v", found, row.Revision, err)
	}
	if revs := mustRevisions(t, r, model.CapabilityKindMcp, "fs"); len(revs) != 1 ||
		revs[0].Action != model.CapabilityActionCreate {
		t.Fatalf("mcp 新建审计不符: %+v", revs)
	}
	// skill 与 mcp 的审计**按 kind 隔离**（同一张表、两种能力，别互相串）。
	if revs := mustRevisions(t, r, model.CapabilityKindSkill, "fs"); len(revs) != 0 {
		t.Fatalf("kind 隔离失效: %+v", revs)
	}

	// 归属该 server 的工具条目必须随删除一起摘掉。
	if err := r.Svc.RegisterTool(capability.Tool{Name: "fs__read", Source: capability.ToolSourceMCP, McpServer: "fs"}); err != nil {
		t.Fatal(err)
	}
	del, err := r.Svc.DeleteMcpServer(ctx, "fs")
	if err != nil {
		t.Fatalf("DeleteMcpServer: %v", err)
	}
	if !del.Deleted || del.ToolsRemoved != 1 || del.Path != "db:mcp/fs" {
		t.Fatalf("删除响应不符: %+v", del)
	}
	if _, found, _ := r.St.GetMcpServerDefinition(ctx, "fs"); found {
		t.Fatal("删除后 DB 里不该还有该行")
	}
	if _, ok := r.Svc.caps.GetMcpServer("fs"); ok {
		t.Fatal("删除后注册表不该还有该 server")
	}
	for _, tool := range r.Svc.caps.ListTools() {
		if tool.McpServer == "fs" {
			t.Fatalf("已删 server 的工具条目应一并摘除: %+v", tool)
		}
	}
}

// TestStoreWriteFaceRequiresNoCapabilityDir DB 模式**不需要** MATE_CAPABILITIES 目录。
//
// 这条是 M6-3 的语义变化：memory 模式下没目录就只能内存生效（报 `capability_dir_unset`），
// DB 模式下定义存在行里，目录只是可选的导入/导出位置。
func TestStoreWriteFaceRequiresNoCapabilityDir(t *testing.T) {
	r, _ := storeCapRig(t)
	if dir := r.Svc.capabilityDirSnapshot(); dir != "" {
		t.Fatalf("本夹具刻意不配能力目录，got %q", dir)
	}
	if _, err := r.Svc.CreateSkill(context.Background(), dtocapability.SkillWriteReq(rawObj(t, "id", `"no-dir"`))); err != nil {
		t.Fatalf("DB 模式没配目录也该能写: %v", err)
	}
}

// mustRevisions 读某条能力的变更记录（按 seq 升序）。
func mustRevisions(t *testing.T, r *rig, kind, id string) model.CapabilityRevisionList {
	t.Helper()
	revs, err := r.St.ListCapabilityRevisions(context.Background(), kind, id)
	if err != nil {
		t.Fatalf("读变更记录: %v", err)
	}
	return revs
}

// materializedAt 报告某 skill 的物化副本是否在（判据与 skills_test.go 同口径：
// `<root>/<id>/SKILL.md`，两个 adapter 当前的原生约定）。
func materializedAt(t *testing.T, root, id string) bool {
	t.Helper()
	fi, err := os.Stat(filepath.Join(root, id, "SKILL.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		t.Fatalf("stat 物化副本: %v", err)
	}
	return !fi.IsDir()
}
