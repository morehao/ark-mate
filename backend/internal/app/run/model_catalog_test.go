package run

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/domain/provider"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— 运行时派生模型目录（manager 层）———

// catalogRig 组装带 echo runtime 的 svc+orch（不拨真实连接；会话由桩注入）。
func catalogRig(t *testing.T) (*Service, *Orchestrator) {
	t.Helper()
	reg := testutil.StubRegistry()
	bus := eventbus.New()
	svc := newServiceForTest(reg, bus)
	setBuildRuntime(t, "stub") // 夹具注册表只有 stub
	orch := newOrchestratorForTest(svc, reg)
	t.Cleanup(orch.Close)
	return svc, orch
}

// fakeProjection Runtime 桩：只实现本用例消费的 ApplyModel（其余方法走内嵌 nil 接口）。
type fakeProjection struct {
	runtime.Runtime
	applied []runtime.ModelConfig
	state   []runtime.ConfigOption
	err     error
}

func (f *fakeProjection) ConfigOptions() []runtime.ConfigOption { return f.state }

func (f *fakeProjection) ApplyModel(_ context.Context, m runtime.ModelConfig) ([]runtime.ConfigOption, error) {
	f.applied = append(f.applied, m)
	if f.err != nil {
		return nil, f.err
	}
	return f.state, nil
}

// TestModelCatalogLiveSession 活会话是首选来源：直接读运行时会话自报的配置项（分组展平）。
func TestModelCatalogLiveSession(t *testing.T) {
	svc, orch := catalogRig(t)
	opts := []runtime.ConfigOption{{
		ID: "model", Name: "Model", Category: acp.CategoryModel, Type: acp.OptionTypeSelect,
		CurrentValue: `["deepseek","deepseek-chat"]`, Grouped: true,
		Values: []runtime.ConfigOptionValue{
			{Value: `["deepseek","deepseek-chat"]`, Name: "DeepSeek Chat", Group: "deepseek", GroupName: "DeepSeek"},
			{Value: `["anthropic","claude-sonnet"]`, Name: "Claude Sonnet", Group: "anthropic", GroupName: "Anthropic"},
		},
	}}
	orch.handles.Put(session.NewActiveSession(session.HandleParams{ID: "c1", Runtime: "stub",
		Session: &stubSession{options: opts}}))

	resp, err := planesOf(svc).Providers.ModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if resp.Source != provider.CatalogFromSession || resp.Runtime != "stub" {
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
	orch.handles.Remove("c1")
	cached, err := planesOf(svc).Providers.ModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("ModelCatalog(cache): %v", err)
	}
	if cached.Source != provider.CatalogFromCache {
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
	resp, err := planesOf(svc).Providers.ModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if resp.Source != provider.CatalogFromNone {
		t.Fatalf("应 source=none: %+v", resp)
	}
	if len(resp.Options) != 0 {
		t.Fatalf("空目录不得有选项: %+v", resp.Options)
	}
	if resp.Note == "" {
		t.Fatal("空目录必须带说明")
	}
}

// TestModelCatalogRuntimeNotRegistered 构建期底座未装配 → runtime_not_registered
// （历史的「未知 agent → agent_not_found」随 agent 面一起下线：目录本来就只问底座）。
func TestModelCatalogRuntimeNotRegistered(t *testing.T) {
	svc, _ := catalogRig(t)
	setBuildRuntime(t, "ghost") // 换一个不在注册表里的构建期底座
	// 注意：这条错误在 ModelCatalog 里已被 `code.FromError` 归一（`runtime_not_registered` 只留在
	// 文案里），所以判据是信封码 + 文案，而不是 acp 的稳定 code。
	if _, err := planesOf(svc).Providers.ModelCatalog(context.Background()); err == nil {
		t.Fatal("构建期底座未装配应报错")
	} else if numericCode(err) != 40001 || !strings.Contains(err.Error(), "runtime_not_registered") {
		t.Fatalf("应是 40001 runtime_not_registered，got %d（%v）", numericCode(err), err)
	}
}

// TestApplySessionModelSemantics 会话内改模型的三种结局：
// 成功（刷新目录缓存）/ 运行时拒绝（记不可用并上抛）/ 未广告模型项（model_not_supported）。
func TestApplySessionModelSemantics(t *testing.T) {
	svc, orch := catalogRig(t)

	// 1) 成功：全量状态回填目录缓存，且曾失败的标记被清除。
	proj := &fakeProjection{state: []runtime.ConfigOption{{
		ID: "model", Name: "Model", Category: acp.CategoryModel, Type: acp.OptionTypeSelect,
		CurrentValue: "test/other",
		Values:       []runtime.ConfigOptionValue{{Value: "test/other", Name: "Other"}},
	}}}
	orch.handles.Put(session.NewActiveSession(session.HandleParams{ID: "c1", Runtime: "stub",
		Session: &stubSession{}, Projection: proj}))
	planesOf(svc).Providers.MarkModelRejected("stub", "test/other", "旧失败")
	applied, err := orch.ApplySessionModel(context.Background(), "c1", runtime.ModelConfig{Model: "test/other"})
	if err != nil || !applied {
		t.Fatalf("应用应成功: applied=%v err=%v", applied, err)
	}
	if len(proj.applied) != 1 || proj.applied[0].Model != "test/other" {
		t.Fatalf("下发值应原样透传: %+v", proj.applied)
	}
	if reason, bad := planesOf(svc).Providers.ModelRejection("stub", "test/other"); bad {
		t.Fatalf("成功后应清除不可用标记: %s", reason)
	}
	// 缓存被成功状态刷新（活会话优先级更高，故直接查缓存）。
	entry, ok := planesOf(svc).Providers.ConfigOptions("stub", provider.DefaultCatalogTTL)
	if !ok || runtime.CurrentConfigValue(entry.Options, runtime.CategoryModel) != "test/other" {
		t.Fatalf("目录缓存应被成功状态刷新: %+v", entry)
	}

	// 2) 运行时拒绝：上抛 + 标记不可用（目录里标灰）。
	proj.err = code.NewError(code.ClassRequest, "invalid_params", "unknown model option: nope", nil)
	if _, err := orch.ApplySessionModel(context.Background(), "c1", runtime.ModelConfig{Model: "nope"}); err == nil {
		t.Fatal("运行时拒绝应上抛")
	}
	if _, bad := planesOf(svc).Providers.ModelRejection("stub", "nope"); !bad {
		t.Fatal("拒绝的取值应记入不可用反馈")
	}

	// 3) 运行时未广告模型项（echo 语义）：显式指定模型 → model_not_supported；
	//    未指定（空）→ 无操作。
	unsupported := &fakeProjection{err: code.NewError(code.ClassUnsupported, "unsupported_model", "未广告模型配置项", nil)}
	orch.handles.Put(session.NewActiveSession(session.HandleParams{ID: "c2", Runtime: "stub",
		Session: &stubSession{}, Projection: unsupported}))
	_, err = orch.ApplySessionModel(context.Background(), "c2", runtime.ModelConfig{Model: "x"})
	re, ok := code.RuntimeErrorOf(err)
	if !ok || re.Code != "model_not_supported" {
		t.Fatalf("未广告模型项应报 model_not_supported: %v", err)
	}
	if _, err := orch.ApplySessionModel(context.Background(), "c2", runtime.ModelConfig{}); err != nil {
		t.Fatalf("空模型应无操作: %v", err)
	}
	if len(unsupported.applied) != 1 {
		t.Fatalf("空模型不应触发应用: %+v", unsupported.applied)
	}

	// 4) 会话不活跃 → (false, nil)：调用方只更新元数据。
	appliedNow, err := orch.ApplySessionModel(context.Background(), "ghost", runtime.ModelConfig{Model: "x"})
	if err != nil || appliedNow {
		t.Fatalf("非活跃会话应 (false,nil): applied=%v err=%v", appliedNow, err)
	}
}

// TestApplySessionModelFailsFastOnMissingCredential 按凭据过滤目录的底座：缺密钥时在平台侧
// 给出可执行错误，不把底座的 invalidParams 原样甩给用户，也不下发到运行时。
func TestApplySessionModelFailsFastOnMissingCredential(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "stub", Name: "Stub", ModelConfigWriter: runtime.StaticModelConfig{NeedsCredentialInCat: true}})
	svc := newServiceForTest(reg, eventbus.New())
	setBuildRuntime(t, "stub")
	orch := newOrchestratorForTest(svc, reg)
	t.Cleanup(orch.Close)

	proj := &fakeProjection{}
	orch.handles.Put(session.NewActiveSession(session.HandleParams{ID: "c1", Runtime: "stub",
		Session: &stubSession{}, Projection: proj}))

	_, err := orch.ApplySessionModel(context.Background(), "c1", runtime.ModelConfig{Provider: "ghost", Model: "x"})
	re, ok := code.RuntimeErrorOf(err)
	if !ok || re.Code != "model_credential_missing" {
		t.Fatalf("缺密钥应报 model_credential_missing，got %v", err)
	}
	if len(proj.applied) != 0 {
		t.Fatalf("缺密钥不应把值下发到运行时: %+v", proj.applied)
	}
}

// TestPickListedSession session/list 兜底：优先带绝对 cwd 的条目（resume 需要 cwd），
// 无绝对 cwd 的条目一律跳过（宁可退回「无来源」也不做无 cwd 的恢复猜测）。
func TestPickListedSession(t *testing.T) {
	if _, ok := pickListedSession(nil); ok {
		t.Fatal("空列表不应命中")
	}
	if _, ok := pickListedSession([]runtime.SessionRef{{ID: "s1", Cwd: "relative/dir"}}); ok {
		t.Fatal("相对 cwd 不应命中")
	}
	got, ok := pickListedSession([]runtime.SessionRef{
		{ID: "s1", Cwd: "relative"}, {ID: "s2", Cwd: "/work/b"},
	})
	if !ok || got.ID != "s2" || got.Cwd != "/work/b" {
		t.Fatalf("应挑首个带绝对 cwd 的条目: %+v ok=%v", got, ok)
	}
}

// ——— v26.1：空目录必须说得出**哪一种空**（取样逐条退避 + 成因分开的说明）———

// TestSampleCandidatesFallsBackPastBrokenCandidate 取样逐条退避。
//
// 挡的失败模式（2026-10-15 实测）：最近一条历史会话是**坏数据**（旧会话引用了底座已不再提供的
// 路由），只试它一条 → 整个目录为空，而"下拉里什么都没有"看起来与"平台没配模型"一模一样。
//
// 判据五态：① 退避到第二条；② "读通了但零候选"不结束退避；③ 全失败报**最后**一个错；
// ④ 全读通但都零候选 = 取样成功（**不是**失败）；⑤ 空候选列表不伪装成"读到了但没有"。
func TestSampleCandidatesFallsBackPastBrokenCandidate(t *testing.T) {
	broken := errors.New(`no adapter registered for provider "old"`)
	opts := []runtime.ConfigOption{{
		ID: "model", Category: acp.CategoryModel,
		Values: []runtime.ConfigOptionValue{{Value: "v1"}},
	}}
	names := []string{"newest", "middle", "oldest"}

	// ① 第一条报错、第二条有候选 → 用第二条的，且 sampled=true、无错。
	got, sampled, err := sampleCandidates(names, func(n string) ([]runtime.ConfigOption, error) {
		if n == "newest" {
			return nil, broken
		}
		if n == "middle" {
			return opts, nil
		}
		t.Fatal("第一条成功后不该再试第三条（顺序即优先级）")
		return nil, nil
	})
	if err != nil || !sampled || len(got) != 1 || got[0].Values[0].Value != "v1" {
		t.Fatalf("应退避到第二条并成功: opts=%+v sampled=%v err=%v", got, sampled, err)
	}

	// ② 第一条**读通但零候选**（底座没有选中的模型）、第二条有候选 → 仍然用第二条。
	got, sampled, err = sampleCandidates(names, func(n string) ([]runtime.ConfigOption, error) {
		if n == "newest" {
			return nil, nil
		}
		if n == "middle" {
			return opts, nil
		}
		t.Fatal("第二条成功后不该再试第三条")
		return nil, nil
	})
	if err != nil || !sampled || len(got) != 1 {
		t.Fatalf("零候选不该结束退避: opts=%+v sampled=%v err=%v", got, sampled, err)
	}

	// ③ 全部报错 → sampled=false，err 是**最后一个**（它最接近"此刻为什么没有目录"）。
	last := errors.New("oldest failed")
	got, sampled, err = sampleCandidates(names, func(n string) ([]runtime.ConfigOption, error) {
		if n == "oldest" {
			return nil, last
		}
		return nil, broken
	})
	if sampled || !errors.Is(err, last) || got != nil {
		t.Fatalf("全失败应回最后一个错: opts=%+v sampled=%v err=%v", got, sampled, err)
	}

	// ④ 全部读通、都零候选 → 取样成功（底座确实没给），不是"我们没读到"。
	if got, sampled, err := sampleCandidates(names, func(string) ([]runtime.ConfigOption, error) {
		return nil, nil
	}); !sampled || err != nil || got != nil {
		t.Fatalf("全读通且零候选应记为取样成功: opts=%+v sampled=%v err=%v", got, sampled, err)
	}

	// ⑤ 没有候选：不能伪装成"读到了但没有"。
	if _, sampled, err := sampleCandidates[string](nil, func(string) ([]runtime.ConfigOption, error) {
		t.Fatal("没有候选时不该调用取样")
		return nil, nil
	}); sampled || err == nil {
		t.Fatalf("空候选列表应是失败态: sampled=%v err=%v", sampled, err)
	}
}

// TestResumeTargetsFromOrdersAndSkipsMalformed 候选枚举的两条判据：
// 最近的在前；壳不合法的记录**跳过**（不为一条残留把整个目录打成空）。
// 平台记录一条都折不出来时才退回 `session/list` 的**底座原文**。
func TestResumeTargetsFromOrdersAndSkipsMalformed(t *testing.T) {
	ctx := context.Background()
	// 夹具直接写 `rt_` 壳的**字面量**而不是调 `objid.WrapRuntime`：包装/解包的调用点是一张白名单
	// （守卫②），测试夹具不该往那张表里加一行——表里每一行都该是一个真实的边界点。
	now := time.Now().UTC()
	recs := []session.SessionRec{
		{ID: "ses_old", RuntimeSessionID: "rt_raw-old", Cwd: "/w/old", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "ses_bad", RuntimeSessionID: "raw-unwrapped", Cwd: "/w/bad", UpdatedAt: now.Add(time.Hour)},
		{ID: "ses_new", RuntimeSessionID: "rt_raw-new", Cwd: "/w/new", UpdatedAt: now},
		// 同一个原生会话的两条平台记录（重复登记）：只留第一条。
		{ID: "ses_dup", RuntimeSessionID: "rt_raw-new", Cwd: "/w/new", UpdatedAt: now.Add(-time.Minute)},
	}
	listed := []runtime.SessionRef{{ID: "listed", Cwd: "/w/listed"}}

	got := resumeTargetsFrom(ctx, recs, listed)
	want := []resumeTarget{{RawID: "raw-new", Cwd: "/w/new"}, {RawID: "raw-old", Cwd: "/w/old"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("候选顺序/去重不符：\nwant %+v\ngot  %+v", want, got)
	}

	// 平台记录**全部**壳不合法 → 退回 session/list（底座原文，不需要解包）。
	got = resumeTargetsFrom(ctx, recs[1:2], listed)
	want = []resumeTarget{{RawID: "listed", Cwd: "/w/listed"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("平台记录不可用时应用 session/list 兜底：\nwant %+v\ngot  %+v", want, got)
	}

	// 两处都没有可用条目 → 空（"没有来源"是合法结论，不是错误）。
	if got := resumeTargetsFrom(ctx, nil, []runtime.SessionRef{{ID: "x", Cwd: "relative"}}); len(got) != 0 {
		t.Fatalf("没有可用条目应是空候选: %+v", got)
	}
}

// TestCatalogEmptyNoteTellsTheCauseApart 空目录的说明必须**分辨成因**——它们对用户的下一步完全不同。
//
// 2026-10-15 的真实事故：三种成因共用一句"还没有会话可供读取"，而当时的真实成因是第三种
// （平台声明了模型、底座也读通了，但底座没有"选中的模型"），于是那句文案把用户和照着它排查的
// agent 一起引向了"平台没把模型交给底座"这条完全错误的方向。
func TestCatalogEmptyNoteTellsTheCauseApart(t *testing.T) {
	ctx := context.Background()
	r := sessionModelRig(t)

	// ① 平台清单还是空的：出路是"先去添加模型"。
	noModels := planesOf(r.svc).Providers.CatalogEmptyNote(ctx, true, nil)
	if !strings.Contains(noModels, "设置 · 模型") || !strings.Contains(noModels, "添加") {
		t.Fatalf("清单为空时应指向「先去添加模型」，实际：%s", noModels)
	}

	createProviderAndModel(t, r.svc, "p1", "m-1")

	// ② 读通了但运行时零候选（真实成因）：出路是"选一条默认模型"。
	noSelection := planesOf(r.svc).Providers.CatalogEmptyNote(ctx, true, nil)
	if !strings.Contains(noSelection, "平台已声明 1 条") || !strings.Contains(noSelection, "默认模型") {
		t.Fatalf("读通了但零候选时应指向「选一条默认模型」，实际：%s", noSelection)
	}

	// ③ 取样失败：如实说"这次没读到"，并且**不得**冒充"还没有会话可供读取"。
	failed := planesOf(r.svc).Providers.CatalogEmptyNote(ctx, false,
		code.NewError(code.ClassRequest, "invalid_params", `no adapter registered for provider "old"`, nil))
	if !strings.Contains(failed, "取样失败") {
		t.Fatalf("取样失败应如实说失败，实际：%s", failed)
	}
	if strings.Contains(failed, "还没有会话可供读取") {
		t.Fatalf("取样失败不得冒充「还没有会话可供读取」——那正是把这次排查引偏的那句话：%s", failed)
	}

	// ④ "该底座不支持在不新建会话的前提下读目录"是**能力事实、不是故障**：
	//    退回"发一条消息就会补全"这条正常出路。
	unsupported := planesOf(r.svc).Providers.CatalogEmptyNote(ctx, false,
		code.NewError(code.ClassUnsupported, "resume_session_unsupported", "不支持 resume", nil))
	if !strings.Contains(unsupported, "发送一条消息") {
		t.Fatalf("底座不支持读时应给「发消息」这条路，实际：%s", unsupported)
	}

	// ⑤ **压根没有候选**（一条会话都没有）是正常态，不是"取样失败"：说明也走"发消息"那条路。
	//    这两种混起来的症状是"明明只是还没发过消息，界面却说取样失败"。
	//
	//    这里的错误值**必须从产出它的那一处取**（`sampleCandidates`），不是直接引用那个哨兵：
	//    隔离环境实测时 `fetchCatalogByResume` 自己又早退造了一个同码错误，于是用户看到"取样失败"
	//    ——而单测只要引用哨兵就照样绿。⑤ 的取法现在钉住了"产出 → 说明"这条边。
	_, _, noCandErr := sampleCandidates[string](nil, func(string) ([]runtime.ConfigOption, error) {
		return nil, nil
	})
	noCandidate := planesOf(r.svc).Providers.CatalogEmptyNote(ctx, false, noCandErr)
	if !strings.Contains(noCandidate, "发送一条消息") || strings.Contains(noCandidate, "取样失败") {
		t.Fatalf("没有候选时应说「发消息就会补全」而不是取样失败，实际：%s", noCandidate)
	}
	// ④ 与 ⑤ 共用同一句话是**有意的**：这两种成因对用户的出路是同一条（还没有会话 / 读不了会话，
	//    都只能靠"发一条消息"把目录带出来）。差别在别处——`samplingUnsupported` 决定要不要打 WARN。
	if unsupported != noCandidate {
		t.Fatalf("「底座不支持读」与「还没有候选」应共用「发消息」那句说明：\n不支持 %s\n没候选 %s",
			unsupported, noCandidate)
	}

	// fail-closed：三种**出路不同**的成因必须是三句可分辨的话（合并回一句就是这条判据失效）。
	seen := map[string]string{}
	for name, note := range map[string]string{
		"没有模型": noModels, "零候选": noSelection, "取样失败": failed,
	} {
		if note == "" {
			t.Fatalf("%s 的说明不得为空", name)
		}
		if prev, dup := seen[note]; dup {
			t.Fatalf("「%s」与「%s」的说明逐字相同：%s", name, prev, note)
		}
		seen[note] = name
	}
}

// TestModelCatalogLiveSessionWithoutModelKeepsNote 活会话在、但运行时没有选中任何模型 →
// `source=session` + 空 options + **说明**。
//
// 回归：此前这条路只返回空 options、note 为空串，面板于是显示那句通用的"运行时未提供模型目录"
// ——把"底座没有当前选中的模型"这个唯一可执行的结论藏了起来（v26.1 修）。
func TestModelCatalogLiveSessionWithoutModelKeepsNote(t *testing.T) {
	svc, orch := catalogRig(t)
	orch.handles.Put(session.NewActiveSession(session.HandleParams{ID: "c1", Runtime: "stub", Session: &stubSession{}}))

	resp, err := planesOf(svc).Providers.ModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if resp.Source != provider.CatalogFromSession {
		t.Fatalf("活会话在时应标 source=session: %+v", resp)
	}
	if len(resp.Options) != 0 {
		t.Fatalf("桩会话没有配置项，不该有候选: %+v", resp.Options)
	}
	if resp.Note == "" {
		t.Fatal("活会话但零候选也必须带说明（否则界面只剩「运行时未提供模型目录」这类通用话）")
	}
}
