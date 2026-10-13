package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— K2：`mate doctor` 的 provider / 模型清单只读体检 ———

// providerDoctorFixture 一个"有 DB、有 vault 锚点、有 agent 定义"的最小现场。
type providerDoctorFixture struct {
	home     string
	agentDir string
	st       *store.Store
}

func newProviderDoctorFixture(t *testing.T) *providerDoctorFixture {
	t.Helper()
	home := t.TempDir()
	agentDir := filepath.Join(home, "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, filepath.Join(home, "mate.db"))
	t.Cleanup(func() { _ = st.Close() })
	return &providerDoctorFixture{home: home, agentDir: agentDir, st: st}
}

func (f *providerDoctorFixture) facts(t *testing.T) ProviderDoctorFacts {
	t.Helper()
	return CollectProviderDoctorFacts(context.Background(), f.st, ProviderDoctorOptions{
		Home: f.home, AgentDir: f.agentDir, DBPath: filepath.Join(f.home, "mate.db"),
	})
}

// TestCollectProviderDoctorFactsReadsEverything 一次把五件事都放上：悬空引用、导入对账、
// 孤儿密文、悬空的密钥引用、模型清单的物化状态。
//
// 断言的是**事实本身**（数、集合、顺序），不是措辞——措辞归 command 层。
func TestCollectProviderDoctorFactsReadsEverything(t *testing.T) {
	ctx := context.Background()
	f := newProviderDoctorFixture(t)

	// 三条档案：① 有真实密文 ②③ 没有密钥（v13 里"有没有密文"只能问 vault：
	// 表里没有 `key_ref` 列，所以"声明了引用却没有密文"这个状态**结构上不存在**）。
	vst, err := vault.Open(f.home)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	if err := vst.Put(providerRef("ok-key"), vault.KindProviderKey, []byte("sk-ok")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ok-key", "other-key", "no-key"} {
		if _, err := f.st.ImportProviderDefinition(ctx, store.ProviderColumns{
			ID: id, Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
		}); err != nil {
			t.Fatalf("导入档案 %s: %v", id, err)
		}
	}
	// 孤儿密文：没有任何行引用它（删除补偿失败的残留）。
	if err := vst.Put(providerRef("deleted-row"), vault.KindProviderKey, []byte("sk-orphan")); err != nil {
		t.Fatal(err)
	}
	// agent 行：一条悬空引用（`zz` 不存在）+ 一条正常引用。
	if _, err := f.st.UpsertAgentDefinition(ctx, "a-broken",
		`{"id":"a-broken","name":"broken","provider":"zz"}`, 0, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpsertAgentDefinition(ctx, "a-ok",
		`{"id":"a-ok","name":"ok","provider":"ok-key"}`, 0, "t"); err != nil {
		t.Fatal(err)
	}
	// 导入对账标记：一条"真不一致"（文件里有、库里没有）。
	if err := f.st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch,
		"legacy: 文件里有、DB 里没有\nok-key: DB 里有、文件里没有（写面新建？跑 `mate provider export` 同步文件）"); err != nil {
		t.Fatal(err)
	}

	got := f.facts(t)
	if got.Carrier != "db" || got.ProviderRows != 3 {
		t.Fatalf("载体/行数不对：carrier=%s rows=%d（%+v）", got.Carrier, got.ProviderRows, got)
	}
	if strings.Join(got.ProviderIDs, ",") != "no-key,ok-key,other-key" {
		t.Fatalf("档案 id 应升序：%v", got.ProviderIDs)
	}
	if strings.Join(got.CiphertextFiles, ",") != "deleted-row,ok-key" {
		t.Fatalf("vault 里实际的密文：%v（scan=%s）", got.CiphertextFiles, got.VaultDirScan)
	}
	if strings.Join(got.NoKeyProviders, ",") != "no-key,other-key" {
		t.Fatalf("没有密文的档案应恰好是 no-key,other-key：%v", got.NoKeyProviders)
	}
	if strings.Join(got.OrphanCiphertexts, ",") != "deleted-row" {
		t.Fatalf("孤儿密文应恰好是 deleted-row：%v", got.OrphanCiphertexts)
	}
	if len(got.DanglingRefs) != 1 || got.DanglingRefs[0].AgentID != "a-broken" || got.DanglingRefs[0].ProviderID != "zz" {
		t.Fatalf("悬空引用应恰好是 a-broken→zz：%+v", got.DanglingRefs)
	}
	if len(got.ImportMismatch) != 2 || got.ImportMismatchBenignOnly {
		t.Fatalf("对账不一致应逐条报出且**不是**纯快照滞后：%v benign=%t", got.ImportMismatch, got.ImportMismatchBenignOnly)
	}
	if !got.VaultUsable || got.VaultSource != "keyfile" {
		t.Fatalf("有真实 keyfile 时应报可用：usable=%t source=%s note=%s", got.VaultUsable, got.VaultSource, got.VaultNote)
	}
	// 模型清单段：**每个内置 runtime** 各一条物化状态（按 `AllBuiltinSpecs()` 现算，而不是写死
	// 一个数字——写死 2 的那一版在第二个内置底座下线后就变成了"数量恰好对上"的空断言），
	// 且**离线**（无编排器）→ advertised 必须记 unknown。
	wantRuntimes := len(runtime.AllBuiltinSpecs())
	if len(got.Runtimes) != wantRuntimes {
		t.Fatalf("应对每个内置 runtime 各报一条物化状态（%d 条）：%+v", wantRuntimes, got.Runtimes)
	}
	for _, rt := range got.Runtimes {
		if rt.AdvertisedKnown || rt.HighRisk {
			t.Fatalf("离线体检不许声称取到了运行时广告：%+v", rt)
		}
		if !strings.Contains(rt.DiffNote, "无法校验") {
			t.Fatalf("取不到 advertised 时必须如实记无法校验：%+v", rt)
		}
	}
	if got.RuntimeConfigDigestWritten {
		t.Fatal("没写过 meta.runtime_config_digest，不该报已写过")
	}
}

// TestCollectProviderDoctorFactsMemoryMode 没有持久层（memory 模式）时：DB 派生的事实
// 全部记 unknown，**不许**把 0 条当成事实（那会假绿），也不许因为读不到就判失败。
func TestCollectProviderDoctorFactsMemoryMode(t *testing.T) {
	home := t.TempDir()
	agentDir := filepath.Join(home, "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, providersFileName), []byte(`{"p1":{"type":"deepseek"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := CollectProviderDoctorFacts(context.Background(), nil, ProviderDoctorOptions{Home: home, AgentDir: agentDir})
	if got.Carrier != "unknown" || got.CarrierNote == "" {
		t.Fatalf("没有持久层时载体应记 unknown 并给出原因：%+v", got)
	}
	if !got.ProvidersFile || got.ProvidersBackup {
		t.Fatalf("文件侧快照应如实报：%+v", got)
	}
	if got.ProviderRows != 0 || len(got.ProviderIDs) != 0 || len(got.Runtimes) != 0 {
		t.Fatalf("没有 DB 时不该凭空造出 DB 事实：%+v", got)
	}
	// 载体无关的那一条仍然要报：vault 尚未创建（没有任何密文 → 不丢东西）。
	if got.VaultUsable || got.VaultSource != "none" || got.VaultDirScan != "missing" {
		t.Fatalf("vault 探测应照常做：%+v", got)
	}
}

// TestProviderMismatchBenignOnly 对账分档：只有"库里有、文件里没有"才算快照滞后。
//
// 这条分档的判据就是写入侧拼串里的那个标记（`providerMismatchExtraMarker`），所以这里
// 直接用它造串——**两侧共用同一个常量**，改文案不会让分档悄悄失效。
func TestProviderMismatchBenignOnly(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    bool
	}{
		{"空", nil, false},
		{"只有快照滞后", []string{"a: " + providerMismatchExtraMarker + "（写面新建？）"}, true},
		{"文件里有库里没有", []string{"a: 文件里有、DB 里没有"}, false},
		{"type 不一致", []string{"a: type 不一致（文件 \"x\" / DB \"y\"）"}, false},
		{"混合", []string{
			"a: " + providerMismatchExtraMarker + "（写面新建？）",
			"b: 文件里有、DB 里没有",
		}, false},
	}
	for _, tc := range cases {
		if got := providerMismatchBenignOnly(tc.entries); got != tc.want {
			t.Errorf("%s：got %t want %t", tc.name, got, tc.want)
		}
	}
}

// TestProviderDoctorHighRisk 高危：**声明了模型，底座一条都没广告**（"写了完全没生效"）。
//
// 这一条只有带编排器的真实夹具才跑得到（`CollectProviderDoctorFacts` 里的 Service 没有编排器，
// 永远走 unknown 分支），所以它单独钉在 `runtimeModelFacts` 上——那也正是 doctor 走的那个方法。
func TestProviderDoctorHighRisk(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	// 对账借读的判据是"这个 agent 跑在哪个底座"——底座是构建期常量，所以这里把它设成要体检的
	// 那个（`newModelRig` 把合成底座 rtx 的声明也注册进来了，它就是"第二个底座"）。
	setBuildRuntime(t, runtimetest.ID)
	agentID := r.svc.agentForRuntime(runtimetest.ID)
	if agentID == "" {
		t.Fatalf("夹具里应有一个服务 %s 的 agent", runtimetest.ID)
	}
	state := r.svc.RuntimeConfigStates(ctx)[runtimetest.ID]

	// ① 平台声明一条、底座却什么都没广告（这里用"有目录条目但零候选"造出 advertised=0 的事实）：
	//    这正是"写了完全没生效"的形态 → 高危。
	m := r.createModel(t, "p1", "m-1")
	// 运行时目录里的取值是**不透明串**，由平台的同一份生成规则折出来（v12 起清单行不再回带它）。
	value, err := modelValueFor(r.svc.reg, runtimetest.ID, m.ProviderID, m.ModelID)
	if err != nil {
		t.Fatal(err)
	}
	seedCatalog(r.svc, agentID, runtimetest.ID)
	facts := r.svc.runtimeModelFacts(ctx, runtimetest.ID, state)
	if !facts.AdvertisedKnown || facts.AdvertisedCount != 0 {
		t.Fatalf("应取到「目录为空」这个事实：%+v", facts)
	}
	if !facts.HighRisk || facts.ModelCount != 1 {
		t.Fatalf("声明 1 条而广告 0 条必须判高危：%+v", facts)
	}
	if len(facts.Missing) != 1 {
		t.Fatalf("差集应恰好是那一条：%+v", facts)
	}

	// ② 底座广告了它 → 不高危、差集为空。
	seedCatalog(r.svc, agentID, runtimetest.ID, aclValue(value))
	facts = r.svc.runtimeModelFacts(ctx, runtimetest.ID, state)
	if !facts.AdvertisedKnown || facts.HighRisk || facts.AdvertisedCount != 1 {
		t.Fatalf("两侧一致时不该高危：%+v", facts)
	}
	if len(facts.Missing) != 0 {
		t.Fatalf("两侧一致时 Missing 应为空：%+v", facts)
	}

	// ③ 完全没有取样来源 → **无法校验**，不是高危、也不是一致。
	//
	// 这里把编排器摘掉来制造"没有来源"（`source=none`），而不是清缓存：清掉缓存会让
	// `ModelCatalog` 去 resume 历史会话——那是一条会**真的拉起底座**的路（测试不得碰用户装的那份）。
	// 摘掉编排器走的正是 CLI 离线那条路。
	r.svc.orch = nil
	r.svc.catalog.drop(agentID)
	facts = r.svc.runtimeModelFacts(ctx, runtimetest.ID, state)
	if facts.AdvertisedKnown || facts.HighRisk {
		t.Fatalf("取不到广告时不许判定：%+v", facts)
	}
	if !strings.Contains(facts.DiffNote, "无法校验") {
		t.Fatalf("取不到广告时必须写明无法校验：%+v", facts)
	}
}

// aclValue 造一个 `category=model` 的目录取值。
func aclValue(value string) acl.ConfigOptionValue {
	return acl.ConfigOptionValue{Value: value}
}

// TestProviderVaultHome vault 锚点：**AgentDir 的父目录**优先（与 providerVault 同源）。
func TestProviderVaultHome(t *testing.T) {
	if got := providerVaultHome("/h", "/h/agents"); got != "/h" {
		t.Fatalf("应取 AgentDir 的父目录：%q", got)
	}
	if got := providerVaultHome("/h", "/data/agents/"); got != filepath.Clean("/data") {
		t.Fatalf("尾斜杠应被 Clean 掉：%q", got)
	}
	if got := providerVaultHome("/h", ""); got != "/h" {
		t.Fatalf("AgentDir 为空时回落到 mate home：%q", got)
	}
}

// TestProbeVault 只读探测的三条路：env 覆盖 / keyfile / 尚未创建。
func TestProbeVault(t *testing.T) {
	home := t.TempDir()

	// ① 尚未创建：不判失败（第一次写入密文时才会创建），来源标 none。
	usable, source, note := probeVault(home)
	if usable || source != "none" || !strings.Contains(note, "尚未创建") {
		t.Fatalf("未创建时应报 none：usable=%t source=%s note=%s", usable, source, note)
	}
	// 且**不该**因为探测而创建 keyfile。
	if _, err := os.Lstat(filepath.Join(home, vault.KeyFileName)); !os.IsNotExist(err) {
		t.Fatalf("探测不许创建 keyfile（err=%v）", err)
	}

	// ② keyfile：真建一个（这里显式用 vault.Open，属测试夹具的行为，不是探测的行为）。
	if _, err := vault.Open(home); err != nil {
		t.Fatal(err)
	}
	usable, source, _ = probeVault(home)
	if !usable || source != "keyfile" {
		t.Fatalf("有 keyfile 时应报可用：usable=%t source=%s", usable, source)
	}

	// ③ env 覆盖优先，且**不读也不建** keyfile。
	t.Setenv(vault.EnvKey, "not-a-key")
	if usable, source, _ := probeVault(home); usable || source != "env" {
		t.Fatalf("非法 MATE_VAULT_KEY 应报 env 不可用：usable=%t source=%s", usable, source)
	}
	// 32 字节 base64（合法）。
	t.Setenv(vault.EnvKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if usable, source, _ := probeVault(home); !usable || source != "env" {
		t.Fatalf("合法 MATE_VAULT_KEY 应报 env 可用：usable=%t source=%s", usable, source)
	}
}

// TestCollectProviderDoctorFactsNoStoreNoHome 极端输入：什么都没有时不许 panic。
func TestCollectProviderDoctorFactsNoStoreNoHome(t *testing.T) {
	got := CollectProviderDoctorFacts(context.Background(), nil, ProviderDoctorOptions{})
	if got.Carrier != "unknown" || got.VaultHome != "" {
		t.Fatalf("空输入应老实报 unknown：%+v", got)
	}
	if got.VaultUsable || got.VaultDirScan == "" {
		t.Fatalf("vault 也要给出结论：%+v", got)
	}
	if got.ProviderIDs == nil || got.DanglingRefs == nil || got.OrphanCiphertexts == nil || got.Runtimes == nil {
		t.Fatal("列表字段必须非 nil（JSON 里空 = []，不出现 null）")
	}
}

// seedCatalog 往内存目录缓存里塞一份**伪造的运行时事实**（对账的另一半）。
//
// 这是把"运行时广告了什么"变成可构造输入的唯一干净办法：目录缓存本来就是内存态，
// 而 `ModelCatalog` 在无活会话时读它（source=cache）。用真会话造这一侧会让单测依赖真底座，
// 而本用例要钉的是**对账算法**本身。
func seedCatalog(svc *Service, agentID, runtimeID string, values ...acl.ConfigOptionValue) {
	svc.catalog.put(agentID, runtimeID, []acl.ConfigOption{{
		ID: "model", Name: "Model", Category: acl.CategoryModel, Type: "select",
		Values: values,
	}})
}
