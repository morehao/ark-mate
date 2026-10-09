package manager

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— 运行时派生模型目录（manager 层）———

// catalogRig 组装带 echo runtime 的 svc+orch（不拨真实连接；会话由桩注入）。
func catalogRig(t *testing.T) (*Service, *Orchestrator) {
	t.Helper()
	reg := testutil.StubRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	svc.agents["agent-stub"] = &AgentRec{ID: "agent-stub", Name: "Stub", Runtimes: []string{"stub"}, State: AgentStateStopped}
	return svc, orch
}

// fakeProjection Runtime 桩：只实现本用例消费的 ApplyModel（其余方法走内嵌 nil 接口）。
type fakeProjection struct {
	runtime.Runtime
	applied []runtime.ModelConfig
	state   []acl.ConfigOption
	err     error
}

func (f *fakeProjection) ConfigOptions() []acl.ConfigOption { return f.state }

func (f *fakeProjection) ApplyModel(_ context.Context, m runtime.ModelConfig) ([]acl.ConfigOption, error) {
	f.applied = append(f.applied, m)
	if f.err != nil {
		return nil, f.err
	}
	return f.state, nil
}

// TestModelCatalogLiveSession 活会话是首选来源：直接读运行时会话自报的配置项（分组展平）。
func TestModelCatalogLiveSession(t *testing.T) {
	svc, orch := catalogRig(t)
	opts := []acl.ConfigOption{{
		ID: "model", Name: "Model", Category: acl.CategoryModel, Type: acl.OptionTypeSelect,
		CurrentValue: `["deepseek","deepseek-chat"]`, Grouped: true,
		Values: []acl.ConfigOptionValue{
			{Value: `["deepseek","deepseek-chat"]`, Name: "DeepSeek Chat", Group: "deepseek", GroupName: "DeepSeek"},
			{Value: `["anthropic","claude-sonnet"]`, Name: "Claude Sonnet", Group: "anthropic", GroupName: "Anthropic"},
		},
	}}
	orch.conversations["c1"] = &ActiveConversation{ID: "c1", AgentID: "agent-stub", Runtime: "stub",
		sess: &stubConversation{options: opts}}

	resp, err := svc.ModelCatalog(context.Background(), "agent-stub")
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if resp.Source != catalogFromSession || resp.Runtime != "stub" {
		t.Fatalf("来源标注不符: %+v", resp)
	}
	if len(resp.Options) != 1 || resp.Options[0].Category != "model" || !resp.Options[0].Grouped {
		t.Fatalf("选项投影不符: %+v", resp.Options)
	}
	if len(resp.Options[0].Values) != 2 || resp.Options[0].Values[1].GroupName != "Anthropic" {
		t.Fatalf("分组值投影不符: %+v", resp.Options[0].Values)
	}
	if resp.Options[0].CurrentValue != `["deepseek","deepseek-chat"]` {
		t.Fatalf("不透明当前值应原样保留: %q", resp.Options[0].CurrentValue)
	}
	if resp.FetchedAt == "" {
		t.Fatal("取样时间应回带")
	}

	// 活会话消失后仍可从缓存取（避免每次查询都 resume）。
	delete(orch.conversations, "c1")
	cached, err := svc.ModelCatalog(context.Background(), "agent-stub")
	if err != nil {
		t.Fatalf("ModelCatalog(cache): %v", err)
	}
	if cached.Source != catalogFromCache {
		t.Fatalf("应命中缓存: %+v", cached)
	}
	if len(cached.Options) != 1 || cached.Options[0].CurrentValue != resp.Options[0].CurrentValue {
		t.Fatalf("缓存内容应与取样一致: %+v", cached.Options)
	}
}

// TestModelCatalogNoSource 无活会话、无缓存、无可恢复会话 → 空目录 + 说明
// （**不新建探测会话**：此处若为拿到目录而 session/new，会留下无法删除的持久记录）。
func TestModelCatalogNoSource(t *testing.T) {
	svc, _ := catalogRig(t)
	resp, err := svc.ModelCatalog(context.Background(), "agent-stub")
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if resp.Source != catalogFromNone {
		t.Fatalf("应 source=none: %+v", resp)
	}
	if len(resp.Options) != 0 {
		t.Fatalf("空目录不得有选项: %+v", resp.Options)
	}
	if resp.Note == "" {
		t.Fatal("空目录必须带说明")
	}
}

// TestModelCatalogUnknownAgent 未知 agent / 未注册 runtime → 请求类错误。
func TestModelCatalogUnknownAgent(t *testing.T) {
	svc, _ := catalogRig(t)
	if _, err := svc.ModelCatalog(context.Background(), "nope"); err == nil {
		t.Fatal("未知 agent 应报错")
	}
	svc.agents["agent-ghost"] = &AgentRec{ID: "agent-ghost", Runtimes: []string{"ghost"}}
	if _, err := svc.ModelCatalog(context.Background(), "agent-ghost"); err == nil {
		t.Fatal("未注册 runtime 应报错")
	}
}

// TestApplyConversationModelSemantics 会话内改模型的三种结局：
// 成功（刷新目录缓存）/ 运行时拒绝（记不可用并上抛）/ 未广告模型项（model_not_supported）。
func TestApplyConversationModelSemantics(t *testing.T) {
	svc, orch := catalogRig(t)

	// 1) 成功：全量状态回填目录缓存，且曾失败的标记被清除。
	proj := &fakeProjection{state: []acl.ConfigOption{{
		ID: "model", Name: "Model", Category: acl.CategoryModel, Type: acl.OptionTypeSelect,
		CurrentValue: "test/other",
		Values:       []acl.ConfigOptionValue{{Value: "test/other", Name: "Other"}},
	}}}
	orch.conversations["c1"] = &ActiveConversation{ID: "c1", AgentID: "agent-stub", Runtime: "stub",
		sess: &stubConversation{}, proj: proj}
	svc.catalog.markRejected("agent-stub", "test/other", "旧失败")
	applied, err := orch.ApplyConversationModel(context.Background(), "c1", runtime.ModelConfig{Model: "test/other"})
	if err != nil || !applied {
		t.Fatalf("应用应成功: applied=%v err=%v", applied, err)
	}
	if len(proj.applied) != 1 || proj.applied[0].Model != "test/other" {
		t.Fatalf("下发值应原样透传: %+v", proj.applied)
	}
	if reason, bad := svc.catalog.rejection("agent-stub", "test/other"); bad {
		t.Fatalf("成功后应清除不可用标记: %s", reason)
	}
	// 缓存被成功状态刷新（活会话优先级更高，故直接查缓存）。
	entry, ok := svc.catalog.get("agent-stub", defaultCatalogTTL)
	if !ok || acl.CurrentConfigValue(entry.options, acl.CategoryModel) != "test/other" {
		t.Fatalf("目录缓存应被成功状态刷新: %+v", entry)
	}

	// 2) 运行时拒绝：上抛 + 标记不可用（目录里标灰）。
	proj.err = acl.NewError(acl.ClassRequest, "invalid_params", "unknown model option: nope", nil)
	if _, err := orch.ApplyConversationModel(context.Background(), "c1", runtime.ModelConfig{Model: "nope"}); err == nil {
		t.Fatal("运行时拒绝应上抛")
	}
	if _, bad := svc.catalog.rejection("agent-stub", "nope"); !bad {
		t.Fatal("拒绝的取值应记入不可用反馈")
	}

	// 3) 运行时未广告模型项（echo 语义）：显式指定模型 → model_not_supported；
	//    未指定（空）→ 无操作。
	unsupported := &fakeProjection{err: acl.NewError(acl.ClassUnsupported, "unsupported_model", "未广告模型配置项", nil)}
	orch.conversations["c2"] = &ActiveConversation{ID: "c2", AgentID: "agent-stub", Runtime: "stub",
		sess: &stubConversation{}, proj: unsupported}
	_, err = orch.ApplyConversationModel(context.Background(), "c2", runtime.ModelConfig{Model: "x"})
	ae, ok := acl.As(err)
	if !ok || ae.Code != "model_not_supported" {
		t.Fatalf("未广告模型项应报 model_not_supported: %v", err)
	}
	if _, err := orch.ApplyConversationModel(context.Background(), "c2", runtime.ModelConfig{}); err != nil {
		t.Fatalf("空模型应无操作: %v", err)
	}
	if len(unsupported.applied) != 1 {
		t.Fatalf("空模型不应触发应用: %+v", unsupported.applied)
	}

	// 4) 会话不活跃 → (false, nil)：调用方只更新元数据。
	appliedNow, err := orch.ApplyConversationModel(context.Background(), "ghost", runtime.ModelConfig{Model: "x"})
	if err != nil || appliedNow {
		t.Fatalf("非活跃会话应 (false,nil): applied=%v err=%v", appliedNow, err)
	}
}

// TestPickListedSession session/list 兜底：优先带绝对 cwd 的条目（resume 需要 cwd），
// 无绝对 cwd 的条目一律跳过（宁可退回「无来源」也不做无 cwd 的恢复猜测）。
func TestPickListedSession(t *testing.T) {
	if _, ok := pickListedSession(nil); ok {
		t.Fatal("空列表不应命中")
	}
	if _, ok := pickListedSession([]acl.SessionInfo{{ID: "s1", Cwd: "relative/dir"}}); ok {
		t.Fatal("相对 cwd 不应命中")
	}
	got, ok := pickListedSession([]acl.SessionInfo{
		{ID: "s1", Cwd: "relative"}, {ID: "s2", Cwd: "/work/b"},
	})
	if !ok || got.ID != "s2" || got.Cwd != "/work/b" {
		t.Fatalf("应挑首个带绝对 cwd 的条目: %+v ok=%v", got, ok)
	}
}
