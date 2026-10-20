package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/drivers/mechanism/doctor"
)

// ——— v18 存量体检（「不在册底座」的痕迹） ———
//
// 这一段的判据是「**在不在册**」，所以本文件里**不许**出现任何具体底座的 id 字面量：
// 夹具里的 id 一律是自造的临时名（`gone-rt` / `in-service`）。这不是洁癖——`internal/**`
// 里出现内置适配 id 的 `case`/`==` 会被 `TestInternalHasNoRuntimeIDSpecialCase` 判红，
// 而那条守卫的意图正是"内核不许认识具体底座"（本文件断言的也正是这套判据本身可用）。

// retiredFixture 一个"磁盘与库里都有存量痕迹"的最小现场。
type retiredFixture struct {
	*providerDoctorFixture
	// inService / gone 两个自造 id：前者在册、后者已下线（判据由测试显式传入）。
	inService string
	gone      string
}

func newRetiredFixture(t *testing.T) *retiredFixture {
	t.Helper()
	f := &retiredFixture{
		providerDoctorFixture: newProviderDoctorFixture(t),
		inService:             "in-service",
		gone:                  "gone-rt",
	}
	// 磁盘：在册那个目录 + 不在册那个目录（含一个"原生凭据文档"形态的文件）。
	for _, id := range []string{f.inService, f.gone} {
		if err := os.MkdirAll(filepath.Join(f.home, "runtime", id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.home, "runtime", f.gone, "native-cred.yaml"), []byte("k: v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 库里：一条指向不在册底座的会话 + 一条正常的。
	at := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	for id, rt := range map[string]string{"ses_gone": f.gone, "ses_live": f.inService} {
		if err := f.st.UpsertSession(context.Background(), sessionrow.Item{
			ID: id, Cwd: f.home, ModelID: "m1",
			RuntimeID: rt, RuntimeSessionID: "rt_native", Title: "t", Status: "idle",
			Origin: "manual", CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatalf("UpsertSession(%s): %v", id, err)
		}
	}
	// skill：一条同时声明了在册与不在册两个候选，一条只声明在册的。
	for id, body := range map[string]string{
		"s-mixed": `{"id":"s-mixed","name":"M","runtimes":["` + f.gone + `","` + f.inService + `"]}`,
		"s-live":  `{"id":"s-live","name":"L","runtimes":["` + f.inService + `"]}`,
	} {
		if _, err := f.st.UpsertSkillDefinition(context.Background(), id, body, 0, "test"); err != nil {
			t.Fatalf("UpsertSkillDefinition(%s): %v", id, err)
		}
	}
	return f
}

// facts 按"只有 inService 在册"的口径采集（= 一个底座下线之后的现场）。
func (f *retiredFixture) facts(t *testing.T) doctor.RetiredRuntimeFacts {
	t.Helper()
	return doctor.CollectRetiredRuntimeFacts(context.Background(), f.st, doctor.RetiredRuntimeOptions{
		Home:            f.home,
		KnownRuntimeIDs: []string{f.inService},
		ProjectionDirs: map[string]string{
			f.inService + ":skill": filepath.Join(f.home, "proj-live"),
			f.gone + ":skill":      filepath.Join(f.home, "proj-gone"),
		},
	})
}

// TestCollectRetiredRuntimeFactsFindsEveryFace 三面（磁盘 / 会话 / 定义）的存量都要被找出来。
//
// 断言的是**事实本身**（集合、条数、顺序），不是措辞——措辞归 command 层（那里另有一条测试）。
func TestCollectRetiredRuntimeFactsFindsEveryFace(t *testing.T) {
	f := newRetiredFixture(t)
	facts := f.facts(t)

	// 判据要自证：在册集合印在事实里（报告说"没有遗留"时，看报告的人应当能核对判据）。
	if len(facts.KnownRuntimeIDs) != 1 || facts.KnownRuntimeIDs[0] != f.inService {
		t.Fatalf("在册集合没有如实回带: %v", facts.KnownRuntimeIDs)
	}
	if facts.HomeScan != "ok" {
		t.Fatalf("HomeScan = %q，期望 ok", facts.HomeScan)
	}

	// ① 磁盘：只报不在册那个目录，且在册那个**不许**出现（否则正常部署会假红）。
	if len(facts.HomeDirs) != 1 {
		t.Fatalf("遗留目录数 = %d（%+v），期望 1（只有在册之外的先 %s 那一个）", len(facts.HomeDirs), facts.HomeDirs, f.gone)
	}
	dir := facts.HomeDirs[0]
	if dir.Runtime != f.gone {
		t.Fatalf("报出来的目录是 %q，期望 %q", dir.Runtime, f.gone)
	}
	if want := filepath.Join(f.home, "runtime", f.gone); dir.Path != want {
		t.Fatalf("目录路径 = %q，期望 %q", dir.Path, want)
	}
	// 顶层条目列出来是为了让人一眼看出"里面是什么"（这里正是那个原生凭据文档）。
	if len(dir.Entries) != 1 || dir.Entries[0] != "native-cred.yaml" {
		t.Fatalf("目录条目 = %v，期望 [native-cred.yaml]", dir.Entries)
	}

	// ② 会话：只有指向不在册底座的那条被数进来（样本给 id，供人直接去查）。
	if facts.SessionRefCount != 1 {
		t.Fatalf("存量会话数 = %d，期望 1", facts.SessionRefCount)
	}
	if len(facts.SessionSamples) != 1 || facts.SessionSamples[0].ID != "ses_gone" ||
		facts.SessionSamples[0].Kind != "session" || facts.SessionSamples[0].Runtime != f.gone {
		t.Fatalf("会话样本 = %+v，期望一条 ses_gone→%s", facts.SessionSamples, f.gone)
	}

	// ③ skill：逐 (skill, runtime) 一条，只报不在册那个候选。
	if len(facts.SkillRefs) != 1 || facts.SkillRefs[0].ID != "s-mixed" || facts.SkillRefs[0].Runtime != f.gone {
		t.Fatalf("skill 引用 = %+v，期望一条 s-mixed→%s", facts.SkillRefs, f.gone)
	}

	// ④ 投影覆盖：形状合法所以解析不报错，但它永远匹配不上。
	if len(facts.ProjectionRefs) != 1 || facts.ProjectionRefs[0].Runtime != f.gone {
		t.Fatalf("投影覆盖引用 = %+v，期望一条指向 %s", facts.ProjectionRefs, f.gone)
	}
	if len(facts.Notes) != 0 {
		t.Fatalf("三面都读到了，不该有「无法校验」的说明: %v", facts.Notes)
	}
}

// TestCollectRetiredRuntimeFactsFollowsTheCallersKnownSet 判据必须**跟着调用方给的在册集合走**。
//
// 这条是本文件最要紧的测试，它挡的是一种"看起来在工作"的假实现：**把在册集合写死**。
// 写死之后，接第二次下线（或换一个构建）时那一段会静默地报"没有遗留"——而且它会一路绿，
// 因为写死的那份恰好等于本次的真值。反证形态就是本测试：同一份现场，只换入参，结论必须翻过来。
func TestCollectRetiredRuntimeFactsFollowsTheCallersKnownSet(t *testing.T) {
	f := newRetiredFixture(t)
	ctx := context.Background()

	// 一、只认 inService → gone 是遗留。
	before := doctor.CollectRetiredRuntimeFacts(ctx, f.st, doctor.RetiredRuntimeOptions{
		Home: f.home, KnownRuntimeIDs: []string{f.inService},
	})
	if len(before.HomeDirs) != 1 || before.SessionRefCount != 1 || len(before.SkillRefs) != 1 {
		t.Fatalf("判据=只在册 %s 时应当报出存量: %+v", f.inService, before)
	}

	// 二、把 gone 也算在册 → **同一个现场**必须一条都不报。
	after := doctor.CollectRetiredRuntimeFacts(ctx, f.st, doctor.RetiredRuntimeOptions{
		Home: f.home, KnownRuntimeIDs: []string{f.inService, f.gone},
	})
	if len(after.HomeDirs) != 0 || after.SessionRefCount != 0 || len(after.SkillRefs) != 0 {
		t.Fatalf("判据把 %s 也算在册之后不该再报存量（在册集合被写死时会在这里露出来）: %+v", f.gone, after)
	}
	if len(after.KnownRuntimeIDs) != 2 {
		t.Fatalf("在册集合 = %v，期望两条", after.KnownRuntimeIDs)
	}
}

// TestCollectRetiredRuntimeFactsNeverTreatsUnreadableAsEmpty 「读不到」与「没有」必须分开。
//
// 这条挡的是本段最坏的假绿形态：库读不出来 / 目录读不动时**报「一切正常」**——用户拿着这份
// 报告去删数据或去排障，两个方向都会被带错。
func TestCollectRetiredRuntimeFactsNeverTreatsUnreadableAsEmpty(t *testing.T) {
	f := newRetiredFixture(t)
	ctx := context.Background()

	// ① 没有可读的持久层：磁盘那半照报，DB 那半**不许**报 0 条，而要留下 note。
	noDB := doctor.CollectRetiredRuntimeFacts(ctx, nil, doctor.RetiredRuntimeOptions{
		Home: f.home, KnownRuntimeIDs: []string{f.inService},
	})
	if len(noDB.HomeDirs) != 1 {
		t.Fatalf("没有库不该影响磁盘面: %+v", noDB.HomeDirs)
	}
	if len(noDB.Notes) == 0 {
		t.Fatal("没有可读的持久层时必须留下「无从校验」的说明，不能静默报 0 条")
	}
	// **逐面**给结论（不是一句"库读得动吗"）：单面读失败时另一面照旧要能判。
	if noDB.SessionScan != "no_store" || noDB.SkillScan != "no_store" {
		t.Fatalf("没有持久层时两面都要记 no_store：session=%q skill=%q", noDB.SessionScan, noDB.SkillScan)
	}

	// ② 隔离根读不动（权限去掉）：必须记 unreadable + note，**不**能报成"扫过、没有遗留"。
	// 注意判据是**三重**的：`ok` + 0 个目录 + 0 条 note 才是"没有遗留"，读不动时必须至少破一处。
	locked := filepath.Join(f.home, "runtime")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	unreadable := doctor.CollectRetiredRuntimeFacts(ctx, f.st, doctor.RetiredRuntimeOptions{
		Home: f.home, KnownRuntimeIDs: []string{f.inService},
	})
	if unreadable.HomeScan == "ok" && len(unreadable.HomeDirs) == 1 {
		// 以 root 跑的 CI 上 chmod 000 不生效（照样读得到）——那时如实按"读到了"判。
		t.Skip("当前用户不受 chmod 000 限制（多半是 root）：unreadable 分支在本环境不可覆盖")
	}
	if !strings.HasPrefix(unreadable.HomeScan, "unreadable") {
		t.Fatalf("隔离根读不动时 HomeScan = %q：必须如实记 unreadable", unreadable.HomeScan)
	}
	if len(unreadable.HomeDirs) != 0 {
		t.Fatalf("隔离根读不动时报出了目录: %+v", unreadable.HomeDirs)
	}
	if len(unreadable.Notes) == 0 {
		t.Fatal("隔离根读不动时必须留下说明（否则「没有遗留」与「读不到」外观相同）")
	}
}

// TestCollectRetiredRuntimeFactsHandlesMissingRoot 没有隔离根是**合法状态**（本机从没让平台
// 拉起过底座），必须与"扫过但没有遗留"区分开——前者的结论是"不可能有遗留"。
func TestCollectRetiredRuntimeFactsHandlesMissingRoot(t *testing.T) {
	f := newRetiredFixture(t)
	if err := os.RemoveAll(filepath.Join(f.home, "runtime")); err != nil {
		t.Fatal(err)
	}
	facts := doctor.CollectRetiredRuntimeFacts(context.Background(), f.st, doctor.RetiredRuntimeOptions{
		Home: f.home, KnownRuntimeIDs: []string{f.inService},
	})
	if facts.HomeScan != "missing" {
		t.Fatalf("HomeScan = %q，期望 missing", facts.HomeScan)
	}
	if len(facts.HomeDirs) != 0 {
		t.Fatalf("没有隔离根时不该报出目录: %+v", facts.HomeDirs)
	}
	// 未配置 home 与"配置了但目录不存在"是两件事，结论不同（无法校验 vs 不可能有遗留）。
	noHome := doctor.CollectRetiredRuntimeFacts(context.Background(), f.st, doctor.RetiredRuntimeOptions{
		KnownRuntimeIDs: []string{f.inService},
	})
	if noHome.HomeScan != "no_home" {
		t.Fatalf("未配置 home 时 HomeScan = %q，期望 no_home", noHome.HomeScan)
	}
}
