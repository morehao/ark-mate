package manager

import (
	"context"
	"encoding/json"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— K2：`mate doctor` 的 provider / 模型清单只读体检 ———

// providerDoctorFixture 一个"有 DB、有 provider 目录、有 mate home"的最小现场。
type providerDoctorFixture struct {
	home        string
	providerDir string
	st          *store.Store
}

func newProviderDoctorFixture(t *testing.T) *providerDoctorFixture {
	t.Helper()
	home := t.TempDir()
	providerDir := filepath.Join(home, "providers")
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, filepath.Join(home, "mate.db"))
	t.Cleanup(func() { _ = st.Close() })
	return &providerDoctorFixture{home: home, providerDir: providerDir, st: st}
}

func (f *providerDoctorFixture) facts(t *testing.T) ProviderDoctorFacts {
	t.Helper()
	return CollectProviderDoctorFacts(context.Background(), f.st, ProviderDoctorOptions{
		Home: f.home, ProviderDir: f.providerDir, DBPath: filepath.Join(f.home, "mate.db"),
	})
}

// TestCollectProviderDoctorFactsReadsEverything 一次把五件事都放上：没有密钥的档案、
// 导入对账、镜像与库的三方差、孤儿 MCP 密钥、旧机制遗留物、模型清单的物化状态。
//
// 断言的是**事实本身**（数、集合、顺序），不是措辞——措辞归 command 层。
func TestCollectProviderDoctorFactsReadsEverything(t *testing.T) {
	ctx := context.Background()
	f := newProviderDoctorFixture(t)

	// 三条档案：① 库那一列有值 ②③ 没有（v20 里"有没有密钥"= `providers.key` 是否非空，
	// 结构上不存在"声明了引用却没有密钥"这种中间态）。
	for _, id := range []string{"ok-key", "other-key", "no-key"} {
		if _, err := f.st.ImportProviderDefinition(ctx, store.ProviderColumns{
			ID: id, Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
		}); err != nil {
			t.Fatalf("导入档案 %s: %v", id, err)
		}
	}
	if written, err := f.st.SetProviderKey(ctx, "ok-key", "sk-ok"); err != nil || !written {
		t.Fatalf("给 ok-key 写密钥: written=%v err=%v", written, err)
	}
	// MCP 密钥：一条**被定义引用**、一条孤儿（定义没了，值留着——删除补偿或用户清定义时的正常残留）。
	if err := f.st.SetMcpSecret(ctx, "store:mcp/used", "sk-used"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMcpSecret(ctx, "store:mcp/orphan", "sk-orphan"); err != nil {
		t.Fatal(err)
	}
	used, err := json.Marshal(capability.McpServer{
		ID: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"TOKEN": "store:mcp/used"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpsertMcpServerDefinition(ctx, "fs", string(used), 0, "t"); err != nil {
		t.Fatalf("造 MCP 定义: %v", err)
	}
	// 导入对账标记：一条"真不一致"（文件里有、库里没有）。
	if err := f.st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch,
		"legacy: 文件里有、DB 里没有\nok-key: DB 里有、文件里没有（写面新建？跑 `mate provider export` 同步文件）"); err != nil {
		t.Fatal(err)
	}
	// 托管文档：库里有 2 条 provider 密钥面（ok-key 有值）+ 2 条 MCP 密钥；镜像里缺一条、
	// 多一条（两边的差异都要如实报出来）。
	writeMirrorFile(t, f.home, 0o600, map[string]string{
		"providers/ok-key": "sk-ok",
		"store:mcp/used":   "sk-used",
		"providers/ghost":  "sk-ghost",
	})
	// 旧机制遗留物：目录 + 主密钥文件都在（**只读不删**，这一条永远只是 warn）。
	legacyDir, legacyKey := LegacySecretPaths(LegacySecretAnchor(f.home, f.providerDir))
	if err := os.MkdirAll(filepath.Join(legacyDir, "providers"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "providers", "ok-key.enc"), []byte("frozen"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyKey, []byte("frozen-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := f.facts(t)
	if got.Carrier != "db" || got.ProviderRows != 3 {
		t.Fatalf("载体/行数不对：carrier=%s rows=%d（%+v）", got.Carrier, got.ProviderRows, got)
	}
	if strings.Join(got.ProviderIDs, ",") != "no-key,ok-key,other-key" {
		t.Fatalf("档案 id 应升序：%v", got.ProviderIDs)
	}
	if strings.Join(got.ProviderKeysWithKey, ",") != "ok-key" {
		t.Fatalf("有密钥的档案应恰好是 ok-key：%v", got.ProviderKeysWithKey)
	}
	if strings.Join(got.NoKeyProviders, ",") != "no-key,other-key" {
		t.Fatalf("没有密钥的档案应恰好是 no-key,other-key：%v", got.NoKeyProviders)
	}
	if strings.Join(got.McpSecretRefs, ",") != "store:mcp/orphan,store:mcp/used" {
		t.Fatalf("库里的 MCP 密钥应升序列出：%v", got.McpSecretRefs)
	}
	if strings.Join(got.OrphanMcpSecrets, ",") != "store:mcp/orphan" {
		t.Fatalf("孤儿密钥应恰好是 store:mcp/orphan：%v", got.OrphanMcpSecrets)
	}
	if strings.Join(got.MissingInCredentials, ",") != "store:mcp/orphan" {
		t.Fatalf("库里有的而镜像里缺的应报出来：%v", got.MissingInCredentials)
	}
	if strings.Join(got.MissingInStore, ",") != "providers/ghost" {
		t.Fatalf("镜像里多的应报出来：%v", got.MissingInStore)
	}
	if !got.CredentialsExists || !got.CredentialsPermOK || got.CredentialsPerm != "0600" || got.CredentialsRefs != 3 {
		t.Fatalf("托管文档的事实不对：%+v", got)
	}
	if len(got.ImportMismatch) != 2 || got.ImportMismatchBenignOnly {
		t.Fatalf("对账不一致应逐条报出且**不是**纯快照滞后：%v benign=%t", got.ImportMismatch, got.ImportMismatchBenignOnly)
	}
	if !got.LegacySecretDir || !got.LegacySecretKeyFile || got.LegacySecretCount != 1 {
		t.Fatalf("旧机制遗留物应如实报出：%+v", got)
	}
	if got.LegacySecretAnchor != f.home {
		t.Fatalf("遗留物锚点应是 ProviderDir 的父目录（%s），得到 %s", f.home, got.LegacySecretAnchor)
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

// writeMirrorFile 直接写托管文档（不走 Service：这里要造的是**任意形状**的现场）。
func writeMirrorFile(t *testing.T, home string, perm os.FileMode, values map[string]string) {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential.CredentialsPath(home), raw, perm); err != nil {
		t.Fatal(err)
	}
}

// TestCollectProviderDoctorFactsMemoryMode 没有持久层（memory 模式）时：DB 派生的事实
// 全部记 unknown，**不许**把 0 条当成事实（那会假绿），也不许因为读不到就判失败。
func TestCollectProviderDoctorFactsMemoryMode(t *testing.T) {
	home := t.TempDir()
	providerDir := filepath.Join(home, "providers")
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerDir, provider.ProvidersFileName), []byte(`{"p1":{"type":"deepseek"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := CollectProviderDoctorFacts(context.Background(), nil, ProviderDoctorOptions{Home: home, ProviderDir: providerDir})
	if got.Carrier != "unknown" || got.CarrierNote == "" {
		t.Fatalf("没有持久层时载体应记 unknown 并给出原因：%+v", got)
	}
	if !got.ProvidersFile || got.ProvidersBackup {
		t.Fatalf("文件侧快照应如实报：%+v", got)
	}
	if got.ProviderRows != 0 || len(got.ProviderIDs) != 0 || len(got.Runtimes) != 0 {
		t.Fatalf("没有 DB 时不该凭空造出 DB 事实：%+v", got)
	}
	// 载体无关的那几条仍然要报：托管文档还没有（可重建，不是故障）、没有旧机制遗留物。
	if got.CredentialsExists || got.CredentialsPermOK || got.CredentialsPath != credential.CredentialsPath(home) {
		t.Fatalf("托管文档面应照常探测并如实报「还没有」：%+v", got)
	}
	if got.LegacySecretDir || got.LegacySecretKeyFile || got.LegacySecretCount != 0 {
		t.Fatalf("没有遗留物时不该报有：%+v", got)
	}
}

// TestProviderMismatchBenignOnly 对账分档：只有"库里有、文件里没有"才算快照滞后。
//
// 这条分档的判据就是写入侧拼串里的那个标记（`provider.ProviderMismatchExtraMarker`），所以这里
// 直接用它造串——**两侧共用同一个常量**，改文案不会让分档悄悄失效。
func TestProviderMismatchBenignOnly(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    bool
	}{
		{"空", nil, false},
		{"只有快照滞后", []string{"a: " + provider.ProviderMismatchExtraMarker + "（写面新建？）"}, true},
		{"文件里有库里没有", []string{"a: 文件里有、DB 里没有"}, false},
		{"type 不一致", []string{"a: type 不一致（文件 \"x\" / DB \"y\"）"}, false},
		{"混合", []string{
			"a: " + provider.ProviderMismatchExtraMarker + "（写面新建？）",
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
	// 对账借读的判据是构建期底座（v19：没有"哪个 agent 跑在哪个底座"这一维了），
	// 所以这里把它设成要体检的那个（`newModelRig` 也注册了合成底座 rtx 的声明）。
	setBuildRuntime(t, runtimetest.ID)
	state := r.svc.RuntimeConfigStates(ctx)[runtimetest.ID]

	// ① 平台声明一条、底座却什么都没广告（这里用"有目录条目但零候选"造出 advertised=0 的事实）：
	//    这正是"写了完全没生效"的形态 → 高危。
	m := r.createModel(t, "p1", "m-1")
	// 运行时目录里的取值是**不透明串**，由平台的同一份生成规则折出来（v12 起清单行不再回带它）。
	value, err := modelValueFor(r.svc.reg, runtimetest.ID, m.ProviderID, m.Model)
	if err != nil {
		t.Fatal(err)
	}
	seedCatalog(r.svc, runtimetest.ID)
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
	seedCatalog(r.svc, runtimetest.ID, aclValue(value))
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
	r.svc.catalog = newCatalogStore()
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

// TestLegacySecretAnchor 旧机制遗留物的锚点：**ProviderDir 的父目录**优先（与搬运扫描同源）。
//
// 两个文件名不在这里断言（它们由 `internal/store` 的历史面拥有，见 v20 词规），这里只钉锚点。
func TestLegacySecretAnchor(t *testing.T) {
	if got := LegacySecretAnchor("/h", "/h/providers"); got != "/h" {
		t.Fatalf("应取 ProviderDir 的父目录：%q", got)
	}
	if got := LegacySecretAnchor("/h", "/data/providers/"); got != filepath.Clean("/data") {
		t.Fatalf("尾斜杠应被 Clean 掉：%q", got)
	}
	if got := LegacySecretAnchor("/h", ""); got != "/h" {
		t.Fatalf("ProviderDir 为空时回落到 mate home：%q", got)
	}
	if dir, key := LegacySecretPaths(""); dir != "" || key != "" {
		t.Fatalf("锚点为空 → 两个路径都为空（不给出会指向当前目录的假路径）：%q %q", dir, key)
	}
}

// TestReadCredentialsMirror 托管文档的**只读**探测：不存在 / 权限合规 / 权限过宽 / 形状坏掉，
// 四条路各自给出结论，且**不建文件、不改权限**（医生没有落笔权）。
func TestReadCredentialsMirror(t *testing.T) {
	home := t.TempDir()

	// ① 还没有：可重建，不是故障。
	got := readCredentialsMirror(home)
	if got.Exists || got.PermOK || !strings.Contains(got.Note, "尚未生成") {
		t.Fatalf("还没有文档时应如实报「尚未生成」：%+v", got)
	}
	if _, err := os.Lstat(credential.CredentialsPath(home)); !os.IsNotExist(err) {
		t.Fatalf("探测不许创建文档（err=%v）", err)
	}

	// ② 合规：0600 + 合法映射。
	writeMirrorFile(t, home, 0o600, map[string]string{"providers/p1": "sk-1"})
	got = readCredentialsMirror(home)
	if !got.Exists || !got.PermOK || got.Perm != "0600" || len(got.Raw) != 1 {
		t.Fatalf("合规文档应读得出来：%+v", got)
	}

	// ③ 权限过宽：**硬失败**那一档，且探测不许顺手 chmod。
	if err := os.Chmod(credential.CredentialsPath(home), 0o644); err != nil {
		t.Fatal(err)
	}
	got = readCredentialsMirror(home)
	if got.PermOK || !strings.Contains(got.Note, "chmod 600") {
		t.Fatalf("权限过宽应报出来并给出路：%+v", got)
	}
	if fi, err := os.Stat(credential.CredentialsPath(home)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("探测不许改权限（mode=%v err=%v）", fi.Mode().Perm(), err)
	}
	_ = os.Chmod(credential.CredentialsPath(home), 0o600)

	// ④ 形状坏掉（不是 `引用 → 值` 对象）：可重建，但要说清。
	if err := os.WriteFile(credential.CredentialsPath(home), []byte("[1,2,3]"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = readCredentialsMirror(home)
	if got.Exists && len(got.Raw) != 0 {
		t.Fatalf("坏形状不该被解析成值表：%+v", got)
	}
	if !strings.Contains(got.Note, "JSON 对象") {
		t.Fatalf("坏形状应说清是什么问题：%+v", got)
	}

	// ⑤ home 解析不出来：给出结论而不是 panic。
	if got := readCredentialsMirror(""); got.Path != "" || got.Note == "" {
		t.Fatalf("空 home 应老实报出来：%+v", got)
	}
}

// TestCollectProviderDoctorFactsNoStoreNoHome 极端输入：什么都没有时不许 panic。
func TestCollectProviderDoctorFactsNoStoreNoHome(t *testing.T) {
	got := CollectProviderDoctorFacts(context.Background(), nil, ProviderDoctorOptions{})
	if got.Carrier != "unknown" || got.LegacySecretAnchor != "" {
		t.Fatalf("空输入应老实报 unknown 且不给假锚点：%+v", got)
	}
	if got.CredentialsPath != "" || got.CredentialsNote == "" {
		t.Fatalf("托管文档面也要给出结论：%+v", got)
	}
	if got.ProviderIDs == nil || got.OrphanMcpSecrets == nil || got.Runtimes == nil ||
		got.McpSecretRefs == nil || got.MissingInCredentials == nil || got.MissingInStore == nil {
		t.Fatal("列表字段必须非 nil（JSON 里空 = []，不出现 null）")
	}
}

// seedCatalog 往内存目录缓存里塞一份**伪造的运行时事实**（对账的另一半）。
//
// 这是把"运行时广告了什么"变成可构造输入的唯一干净办法：目录缓存本来就是内存态，
// 而 `ModelCatalog` 在无活会话时读它（source=cache）。用真会话造这一侧会让单测依赖真底座，
// 而本用例要钉的是**对账算法**本身。缓存键是 runtime id（v19）。
func seedCatalog(svc *Service, runtimeID string, values ...acl.ConfigOptionValue) {
	svc.catalog.put(runtimeID, []acl.ConfigOption{{
		ID: "model", Name: "Model", Category: acl.CategoryModel, Type: "select",
		Values: values,
	}})
}
