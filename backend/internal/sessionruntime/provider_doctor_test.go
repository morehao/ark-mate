package sessionruntime

import (
	"context"
	"encoding/json"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/credential"
	"github.com/morehao/ark-mate/internal/infra/dsh"

	"github.com/morehao/ark-mate/internal/domain/provider"
	doctorfacts "github.com/morehao/ark-mate/internal/drivers/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/drivers/registry"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/drivers/transfer"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
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

func (f *providerDoctorFixture) facts(t *testing.T) doctorfacts.ProviderDoctorFacts {
	t.Helper()
	return doctorfacts.CollectProviderDoctorFacts(context.Background(), f.st, doctorfacts.ProviderDoctorOptions{
		Home: f.home, ProviderDir: f.providerDir, DBPath: filepath.Join(f.home, "mate.db"),
		ProvidersFileName:   provider.ProvidersFileName,
		BackupName:          transfer.ProvidersBackupName,
		MismatchExtraMarker: transfer.ProviderMismatchExtraMarker,
	}, func(ctx context.Context, st *store.Store, home string) []doctorfacts.ProviderRuntimeFacts {
		return registry.CollectRuntimeModelFacts(ctx, st, home, dsh.Registry())
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
	// 直接写托管文档（不走 Service）：这里要造的是**任意形状**的现场——包括镜像里多一条、
	// 少一条这种"库与镜像对不上"的形态。
	mirrorRaw, err := json.Marshal(map[string]string{
		"providers/ok-key": "sk-ok",
		"store:mcp/used":   "sk-used",
		"providers/ghost":  "sk-ghost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential.CredentialsPath(f.home), mirrorRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	// 旧机制遗留物：目录 + 主密钥文件都在（**只读不删**，这一条永远只是 warn）。
	legacyDir, legacyKey := doctorfacts.LegacySecretPaths(doctorfacts.LegacySecretAnchor(f.home, f.providerDir))
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
	wantRuntimes := len(dsh.Specs())
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
	providerDir := filepath.Join(home, "providers")
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerDir, provider.ProvidersFileName), []byte(`{"p1":{"type":"deepseek"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := doctorfacts.CollectProviderDoctorFacts(context.Background(), nil, doctorfacts.ProviderDoctorOptions{
		Home: home, ProviderDir: providerDir, ProvidersFileName: provider.ProvidersFileName,
	}, func(ctx context.Context, st *store.Store, home string) []doctorfacts.ProviderRuntimeFacts {
		return registry.CollectRuntimeModelFacts(ctx, st, home, dsh.Registry())
	})
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
	state := r.svc.Planes().Resources.RuntimeConfigStates(ctx)[runtimetest.ID]

	// ① 平台声明一条、底座却什么都没广告（这里用"有目录条目但零候选"造出 advertised=0 的事实）：
	//    这正是"写了完全没生效"的形态 → 高危。
	m := r.createModel(t, "p1", "m-1")
	// 运行时目录里的取值是**不透明串**，由平台的同一份生成规则折出来（v12 起清单行不再回带它）。
	value, err := provider.ModelValueFor(r.svc.planes.Registry, runtimetest.ID, m.ProviderID, m.Model)
	if err != nil {
		t.Fatal(err)
	}
	seedCatalog(r.svc, runtimetest.ID)
	facts := doctorfacts.RuntimeModelFacts(ctx, registry.NewModelFacts(r.svc.Planes()), dsh.Specs(), runtimetest.ID, state)
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
	seedCatalog(r.svc, runtimetest.ID, acpValue(value))
	facts = doctorfacts.RuntimeModelFacts(ctx, registry.NewModelFacts(r.svc.Planes()), dsh.Specs(), runtimetest.ID, state)
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
	r.svc.planes.Providers.ResetConfigCatalog()
	facts = doctorfacts.RuntimeModelFacts(ctx, registry.NewModelFacts(r.svc.Planes()), dsh.Specs(), runtimetest.ID, state)
	if facts.AdvertisedKnown || facts.HighRisk {
		t.Fatalf("取不到广告时不许判定：%+v", facts)
	}
	if !strings.Contains(facts.DiffNote, "无法校验") {
		t.Fatalf("取不到广告时必须写明无法校验：%+v", facts)
	}
}

// acpValue 造一个 `category=model` 的目录取值。
func acpValue(value string) protocol.ConfigOptionValue {
	return protocol.ConfigOptionValue{Value: value}
}

// TestCollectProviderDoctorFactsNoStoreNoHome 极端输入：什么都没有时不许 panic。
func TestCollectProviderDoctorFactsNoStoreNoHome(t *testing.T) {
	got := doctorfacts.CollectProviderDoctorFacts(context.Background(), nil, doctorfacts.ProviderDoctorOptions{},
		func(ctx context.Context, st *store.Store, home string) []doctorfacts.ProviderRuntimeFacts {
			return registry.CollectRuntimeModelFacts(ctx, st, home, dsh.Registry())
		})
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
func seedCatalog(svc *Service, runtimeID string, values ...protocol.ConfigOptionValue) {
	svc.planes.Providers.PutConfigOptions(runtimeID, []protocol.ConfigOption{{
		ID: "model", Name: "Model", Category: protocol.CategoryModel, Type: "select",
		Values: values,
	}})
}
