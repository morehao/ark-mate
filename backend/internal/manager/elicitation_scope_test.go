package manager

import (
	"context"
	"errors"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/gerror"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/service/svcruntime"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objid"
)

// 本文件是从 `elicitation_test.go` 拆出来的：那三个用例测的是 **Orchestrator / Service 侧**
// 的提问归属解析（`resolveElicitationScope` / `noteScopeAmbiguous` / `ListPendingElicitations`），
// 不是 `interaction.ElicitationManager` 自己——所以它们留在 manager，随 `permission_flow.go` 走。

// TestResolveElicitationScope C3 归属判定三分支：wire（`_meta`）优先 → inferred_inflight（唯一在途
// run）→ ambiguous（都没有，返回空 id 由调用方按取消回应并留痕）。cli=nil 时无在途 run，
// 正好覆盖后两个分支（wire 分支不碰 cli）。
//
// v15（D-4 修复）之后 wire 分支的口径变了：`_meta` 里的 id 是**底座原文**，必须先按
// `runtime_session_id` 反查平台会话；查不到就退 ambiguous —— 这正是本用例现在要钉的东西
// （改之前它会把 `wire-conv` 直接当成平台会话 id 用，把提问卡挂到一个不存在的会话上）。
func TestResolveElicitationScope(t *testing.T) {
	_, orch, _ := newTestRig(t)

	// 先登记一条会话，让它的 `runtime_session_id` 是 `rt_wire-sess`。
	orch.svc.registerSession(&SessionRec{
		ID: "ses_platform", Cwd: "/tmp",
		RuntimeSessionID: objid.RuntimeShell + "wire-sess",
	})

	if sid, src, note := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		SessionID: "wire-sess", ScopeSource: acl.ScopeWire,
	}); sid != "ses_platform" || src != acl.ScopeWire || note != "" {
		t.Fatalf("wire 归属应按 runtime_session_id 反查成平台会话: sid=%q src=%q note=%q", sid, src, note)
	}
	// **查不到**的 wire id（底座发了个我们不认识的会话）→ ambiguous + 留痕，**不猜**。
	if sid, src, note := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		SessionID: "wire-unknown", ScopeSource: acl.ScopeWire,
	}); sid != "" || src != acl.ScopeAmbiguous || note == "" {
		t.Fatalf("查不到的 wire 归属应落 ambiguous 且留痕: sid=%q src=%q note=%q", sid, src, note)
	}
	// ScopeSource=wire 但 SessionID 为空（脏数据）→ 不认，退到推断/歧义。
	if sid, src, _ := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		ScopeSource: acl.ScopeWire,
	}); sid != "" || src != acl.ScopeAmbiguous {
		t.Fatalf("空 wire 归属应落 ambiguous: sid=%q src=%q", sid, src)
	}
	if sid, src, _ := orch.resolveElicitationScope(nil, acl.ElicitationRequest{
		Mode: "form", Message: "q",
	}); sid != "" || src != acl.ScopeAmbiguous {
		t.Fatalf("无 wire 且无在途 run 应落 ambiguous: sid=%q src=%q", sid, src)
	}
}

// TestServiceResolveElicitationScope C3：REST 面（service）跨会话决策按「不存在」处理——
// 与既有「未知 elicitation_id」完全同码同文案（40001，不泄露他会话挂起项的存在）；
// 归属会话决策成功；列出挂起项时带 session_id/scope_source。
func TestServiceResolveElicitationScope(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	svc.registerSession(&SessionRec{ID: "owner"})
	svc.registerSession(&SessionRec{ID: "intruder"})

	published := make(chan interaction.ElicitationItem, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		orch.elic.RegisterAndWait(context.Background(), "owner", acl.ElicitationRequest{
			SessionID: "owner", Mode: "form", Message: "q",
			Schema: &acl.ElicitationSchema{Properties: map[string]any{}},
		}, func(it interaction.ElicitationItem) { published <- it })
	}()
	it := <-published

	list, err := detailSvc(svc).ListPendingElicitations(context.Background(), "owner")
	if err != nil {
		t.Fatalf("ListPendingElicitations: %v", err)
	}
	if len(list) != 1 || list[0].SessionID != "owner" || list[0].ScopeSource == "" {
		t.Fatalf("挂起项应带 scope 归属: %+v", list)
	}
	if other, err := detailSvc(svc).ListPendingElicitations(context.Background(), "intruder"); err != nil || len(other) != 0 {
		t.Fatalf("他会话不应看到挂起项: %+v err=%v", other, err)
	}

	err = interactionSvc(svc).ResolveElicitation(context.Background(), "intruder", it.ID, "accept", map[string]any{"k": "v"})
	if err == nil {
		t.Fatal("跨会话决策应报错")
	}
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 40001 {
		t.Fatalf("跨会话决策期望 40001（与未知 id 同码），got %v", err)
	}
	if !strings.Contains(ge.Msg, "不存在或已过期") {
		t.Fatalf("跨会话文案应与未知 id 一致: %q", ge.Msg)
	}
	if _, _, ok := orch.elic.Owner(it.ID); !ok {
		t.Fatal("跨会话决策不得清除挂起项")
	}

	if err := interactionSvc(svc).ResolveElicitation(context.Background(), "owner", it.ID, "accept", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("归属会话决策应成功: %v", err)
	}
	<-done // 等待 RegisterAndWait 消费决策并清表（异步删除）
	if _, _, ok := orch.elic.Owner(it.ID); ok {
		t.Fatal("决策后应清除挂起项")
	}
	// 未知会话仍是 40401（会话不存在，先于 elicitation 校验）。
	if err := interactionSvc(svc).ResolveElicitation(context.Background(), "ghost", it.ID, "accept", nil); err == nil {
		t.Fatal("未知会话应报错")
	}
}

// TestRuntimeDiagnosticsShowsScopeAmbiguous verify 归属歧义不再无痕：留痕随底座诊断的
// remediation 外露，给出事实 + 可执行动作（否则用户只觉得「提问卡没出现」）。
func TestRuntimeDiagnosticsShowsScopeAmbiguous(t *testing.T) {
	svc, _ := newSearchRig(t)
	svc.noteScopeAmbiguous("elicitation 无法归属会话（wire `_meta` 未给 sessionId，且连接上无唯一在途 run）")

	resp, err := svcruntime.New(svc.runtimePlane).RuntimeDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("RuntimeDiagnostics: %v", err)
	}
	joined := strings.Join(resp.Remediation, "\n")
	if !strings.Contains(joined, "无法归属会话") {
		t.Fatalf("归属歧义应出现在 remediation: %+v", resp.Remediation)
	}
	if !strings.Contains(joined, "_meta") {
		t.Fatalf("remediation 应给出可执行动作（_meta 带 sessionId）: %+v", resp.Remediation)
	}
}

// TestNativePermissionEnabledSwitch B5：Service 开关（默认开 / 可关）。
//
// v19：agent 面撤销后平台**没有**权限策略来源（策略曾随 agent 定义一起加载），所以这个开关
// 现在不改变任何注入结果——它保留是因为它是对外装配面（`MATE_NATIVE_PERMISSION=off`）与
// `interaction.nativePermissionEnv` 这条**端口契约**的存在证明：接入方仍可自行调用它。见 service.go 的注释。
func TestNativePermissionEnabledSwitch(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), nil)
	if !svc.NativePermissionEnabled() {
		t.Fatal("默认应开启")
	}
	svc.SetNativePermission(false)
	if svc.NativePermissionEnabled() {
		t.Fatal("关闭后应为 false")
	}
}

// TestConnKeyIsolatesNativePermission B5：连接缓存键含原生权限签名——配置不同不得共享子进程。
func TestConnKeyIsolatesNativePermission(t *testing.T) {
	ro := []string{"DSH_PERMISSION_MODE=read-only"}
	full := []string{"DSH_PERMISSION_MODE=danger-full-access"}
	if connKey("dsh", ro) == connKey("dsh", full) {
		t.Fatal("不同原生权限配置应产生不同连接键")
	}
	if connKey("dsh", ro) != connKey("dsh", ro) {
		t.Fatal("相同配置应产生相同连接键（可共享暖进程）")
	}
	if connKey("stub", nil) != "stub" {
		t.Fatalf("无原生配置时应退化为 runtime ID: %q", connKey("stub", nil))
	}
	// 同一策略两次计算结果一致（env 顺序稳定）。这里直接调 `interaction.NativePermissionEnv`
	// ——它是那条**端口契约**（原生权限 → 环境变量）的唯一实现，行为侧与实现侧各调一次，
	// 正是这条用例要钉的"两侧同源"。
	pol := &interaction.PermissionPolicy{Default: "deny"}
	envOf := func() []string {
		e, _ := interaction.NativePermissionEnv(runtime.AllBuiltinRegistry(), "dsh", pol)
		return e
	}
	if connKey("dsh", envOf()) != connKey("dsh", envOf()) {
		t.Fatal("同一策略的连接键应稳定")
	}
}
