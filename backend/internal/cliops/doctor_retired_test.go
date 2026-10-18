package cliops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	doctorfacts "github.com/morehao/ark-mate/internal/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/config"
)

// ——— v18：`mate doctor` 的存量体检段（「不在册底座」的痕迹） ———
//
// 这一组钉两件事，它们是这一整段的**存在理由**：
//  1. 存量**必须被看见**（在此之前它整段消失：逐底座采事实的两段都按注册表枚举，
//     适配一删那个底座就从报告里没了——报告一切正常，盘上东西一个没少）；
//  2. 存量**不许判 fail**（决策 v18：读面宽容、不做破坏性迁移。判红会把用户逼向"删数据"
//     这一个动作，而那正是明确不要的）。
//
// 夹具里的 id 一律自造（`in-service` / `gone-rt`）：这一段判的是"在不在册"，不是"是不是某某"。

// retiredRig 一个"库里与盘上都有存量痕迹"的现场（复用 provider 段的真库夹具）。
type retiredRig struct {
	*providerDoctorRig
	inService string
	gone      string
}

func newRetiredRig(t *testing.T) *retiredRig {
	t.Helper()
	r := &retiredRig{providerDoctorRig: newProviderDoctorRig(t), inService: "in-service", gone: "gone-rt"}
	if err := os.MkdirAll(filepath.Join(r.home, "runtime", r.inService), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.home, "runtime", r.gone), 0o755); err != nil {
		t.Fatal(err)
	}
	// 那个底座的原生凭据文档：报告要让人一眼看出"这里留着明文密钥"。
	if err := os.WriteFile(filepath.Join(r.home, "runtime", r.gone, "native-cred.yaml"), []byte("k: v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	r.withStore(t, func(st *store.Store) {
		if err := st.UpsertSession(context.Background(), model.SessionEntity{
			ID: "ses_gone", Cwd: r.home, ModelID: "m1",
			RuntimeID: r.gone, RuntimeSessionID: "rt_native", Title: "t", Status: "idle",
			Origin: "manual", CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertSkillDefinition(context.Background(), "s-mixed",
			`{"id":"s-mixed","name":"M","runtimes":["`+r.gone+`"]}`, 0, "test"); err != nil {
			t.Fatal(err)
		}
	})
	return r
}

// opts 存量段的输入：在册只有 inService（= 一个底座下线之后的现场）+ 一条配给已下线底座的投影覆盖。
func (r *retiredRig) opts() doctorOptions {
	o := r.providerDoctorRig.opts()
	o.KnownRuntimeIDs = []string{r.inService}
	o.ProjectionDirs = map[string]string{
		r.inService + ":skill": filepath.Join(r.home, "proj-live"),
		r.gone + ":skill":      filepath.Join(r.home, "proj-gone"),
	}
	return o
}

// TestDoctorRetiredSectionReportsLegacy 四面的存量都要被点名，且**一项都不判 fail**。
func TestDoctorRetiredSectionReportsLegacy(t *testing.T) {
	r := newRetiredRig(t)
	checks := checkRetiredSection(r.opts())
	by := checkByName(t, checks)

	// ① 磁盘：给出目录路径**与顶层条目**（"里面是什么"决定该不该删）。
	dirs := by["retired runtime dirs"]
	if dirs.Status != doctorWarn {
		t.Fatalf("盘上留有不在册底座的目录必须 warn：%+v", dirs)
	}
	if !strings.Contains(dirs.Detail, filepath.Join(r.home, "runtime", r.gone)) ||
		!strings.Contains(dirs.Detail, "native-cred.yaml") {
		t.Fatalf("要点名目录与其中的条目（里面是明文凭据时尤其要说出来）：%s", dirs.Detail)
	}
	// 在册那个目录**不许**被列进来（否则正常部署假红）。
	if strings.Contains(dirs.Detail, filepath.Join(r.home, "runtime", r.inService)) {
		t.Fatalf("在册底座的目录被当成了遗留：%s", dirs.Detail)
	}
	if !strings.Contains(dirs.Detail, "不自动删") {
		t.Fatalf("要写明平台不自动删（决策 v18：不做破坏性迁移）：%s", dirs.Detail)
	}

	// ② 会话：点出 id 与**后果**（读得到、发消息报 runtime_not_found）。
	sessions := by["retired sessions"]
	if sessions.Status != doctorWarn || !strings.Contains(sessions.Detail, "ses_gone") {
		t.Fatalf("存量会话必须 warn 且点名：%+v", sessions)
	}
	if !strings.Contains(sessions.Detail, "runtime_not_found") {
		t.Fatalf("要说清后果（读得到、发消息才报错）：%s", sessions.Detail)
	}

	// ③ skill：候选集为空 ⇒ 永不投放。
	skills := by["retired skills"]
	if skills.Status != doctorWarn || !strings.Contains(skills.Detail, "s-mixed") {
		t.Fatalf("skill 指向不在册底座必须 warn 且点名：%+v", skills)
	}

	// ④ 投影覆盖：形状合法所以解析不报错，但它永远匹配不上。
	proj := by["retired projection dirs"]
	if proj.Status != doctorWarn || !strings.Contains(proj.Detail, r.gone+":skill") {
		t.Fatalf("投影覆盖指向不在册底座必须 warn 且点名：%+v", proj)
	}

	// 本段的**硬口径**：存量一律 warn。判 fail 会让退出码变 1，而唯一的变绿动作是删数据。
	if _, has := by["retired scan"]; has {
		t.Fatalf("这一组现场三面都读到了，不该有「无法校验」那一条：%+v", by["retired scan"])
	}
	for _, c := range checks {
		if c.Status == doctorFail {
			t.Fatalf("存量体检不许判失败（存量是事实，不是「事实与声明不符」）：%+v", c)
		}
	}
}

// TestDoctorRetiredSectionCleanMachineIsOk 干净机器：四条**全部 ok**，且不输出"无法校验"那条。
//
// 与上一条配对：这一段的两种失效方向都要挡住——把正常现场报成遗留（假红），
// 和把遗留报成正常（假绿）。
func TestDoctorRetiredSectionCleanMachineIsOk(t *testing.T) {
	r := newRetiredRig(t)
	// 拆掉全部痕迹：已下线那个目录、库里的会话与 skill。
	if err := os.RemoveAll(filepath.Join(r.home, "runtime", r.gone)); err != nil {
		t.Fatal(err)
	}
	r.withStore(t, func(st *store.Store) {
		// 把两条引用都改回"在册"的那个 id（store 门面没有删会话的接口——体检也不该依赖删除）。
		at := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
		if err := st.UpsertSession(context.Background(), model.SessionEntity{
			ID: "ses_gone", Cwd: r.home, ModelID: "m1",
			RuntimeID: r.inService, RuntimeSessionID: "rt_native", Title: "t", Status: "idle",
			Origin: "manual", CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.UpsertSkillDefinition(context.Background(), "s-mixed",
			`{"id":"s-mixed","name":"M","runtimes":["`+r.inService+`"]}`, 1, "test"); err != nil || !ok {
			t.Fatalf("改 skill 的 runtimes 失败（ok=%v）：%v", ok, err)
		}
	})
	opts := r.opts()
	opts.ProjectionDirs = map[string]string{r.inService + ":skill": filepath.Join(r.home, "proj-live")}

	checks := checkRetiredSection(opts)
	by := checkByName(t, checks)
	for _, name := range []string{"retired runtime dirs", "retired sessions", "retired skills", "retired projection dirs"} {
		c, ok := by[name]
		if !ok {
			t.Fatalf("缺少检查项 %q（体检报告的形状是契约的一部分）", name)
		}
		if c.Status != doctorOK {
			t.Fatalf("干净现场 %q 应 ok：%+v", name, c)
		}
	}
	if _, has := by["retired scan"]; has {
		t.Fatalf("干净现场不该有「无法校验」那一条：%+v", by["retired scan"])
	}
}

// TestDoctorRetiredSectionEmptyKnownSetIsLoud 在册集合为空 = **判据失效**，必须响亮地说出来。
//
// 这一条挡的是"假绿"的最坏形态：判据失效时把每个目录都算成遗留（假红）还算好，
// 真正的危险是它**静默变空**——那时报告会说"没有遗留"，而它与"判据没生效"外观完全相同。
func TestDoctorRetiredSectionEmptyKnownSetIsLoud(t *testing.T) {
	r := newRetiredRig(t)
	opts := r.opts()
	opts.KnownRuntimeIDs = nil
	checks := checkRetiredSection(opts)
	by := checkByName(t, checks)

	scan, ok := by["retired scan"]
	if !ok {
		t.Fatalf("判据失效必须单列一条（不能只在别处顺带提一句）：%+v", checks)
	}
	if scan.Status != doctorWarn || !strings.Contains(scan.Detail, "判据失效") {
		t.Fatalf("判据失效要如实命名并提示当缺陷报：%+v", scan)
	}
}

// TestDoctorRetiredSectionWithoutStoreNeverClaimsClean 没有可读的持久层时**不许**报"没有存量"。
//
// 判据是「读不到」与「没有」必须分开：前者必须至少破掉"ok + 空"这个组合。
func TestDoctorRetiredSectionWithoutStoreNeverClaimsClean(t *testing.T) {
	r := newRetiredRig(t)
	opts := r.opts()
	opts.StoreMode = config.StoreModeMemory // → openDoctorStore 返回 nil
	checks := checkRetiredSection(opts)
	by := checkByName(t, checks)

	for _, name := range []string{"retired sessions", "retired skills"} {
		c := by[name]
		if c.Status != doctorWarn || !strings.Contains(c.Detail, "无法校验") {
			t.Fatalf("没有持久层时 %q 必须记「无法校验」而不是「没有」：%+v", name, c)
		}
	}
	// 磁盘那半与载体无关，照报。
	if by["retired runtime dirs"].Status != doctorWarn {
		t.Fatalf("磁盘面不依赖 DB，仍应报出遗留：%+v", by["retired runtime dirs"])
	}
	if _, has := by["retired scan"]; !has {
		t.Fatalf("没有持久层时必须留下说明：%+v", checks)
	}
}

// TestRenderRetiredChecksSingleFaceFailureIsNeverClean 单面读失败**不许**被报成"没有存量"。
//
// 这条挡的是一个很具体的假绿：把"库读得动吗"做成**全局**判据时，只要 skill 表读得动，
// 会话表读不动那一面就会被报成"没有会话指向不在册底座"——而它只在**出问题的那台机器**上出现。
// 所以判据是逐面的（`SessionScan` / `SkillScan`），本用例直接喂事实把两面掰开。
func TestRenderRetiredChecksSingleFaceFailureIsNeverClean(t *testing.T) {
	cases := []struct {
		name    string
		facts   doctorfacts.RetiredRuntimeFacts
		broken  string
		healthy string
	}{
		{
			name: "会话面读失败、定义面正常",
			facts: doctorfacts.RetiredRuntimeFacts{KnownRuntimeIDs: []string{"in-service"},
				SessionScan: "unreadable", SkillScan: "ok", SessionRefCount: 0},
			broken:  "retired sessions",
			healthy: "retired skills",
		},
		{
			name: "定义面读失败、会话面正常",
			facts: doctorfacts.RetiredRuntimeFacts{KnownRuntimeIDs: []string{"in-service"},
				SessionScan: "ok", SkillScan: "unreadable"},
			broken:  "retired skills",
			healthy: "retired sessions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			by := checkByName(t, renderRetiredChecks(tc.facts))
			broken := by[tc.broken]
			if broken.Status != doctorWarn || !strings.Contains(broken.Detail, "无法校验") {
				t.Fatalf("读不动的面必须记「无法校验」，不许报成「没有」：%+v", broken)
			}
			if by[tc.healthy].Status != doctorOK {
				t.Fatalf("正常那一面应当照常给结论：%+v", by[tc.healthy])
			}
			if _, has := by["retired scan"]; has {
				t.Fatalf("本用例的 Notes 为空，不该有说明那一条：%+v", by["retired scan"])
			}
		})
	}
}
