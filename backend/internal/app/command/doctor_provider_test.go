package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/config"
)

// ——— K2：`mate doctor` 的 provider / 模型清单段 ———
//
// 这一组钉的是**分档**（哪一条该 fail、哪一条只能 warn），不是措辞——分档错了就是退出码错了。

// providerDoctorRig 一个真实落盘的现场：真库（跑过迁移）+ 真 vault + agent 行 + 真密文。
type providerDoctorRig struct {
	home     string
	agentDir string
	dbPath   string
}

func newProviderDoctorRig(t *testing.T) *providerDoctorRig {
	t.Helper()
	home := t.TempDir()
	agentDir := filepath.Join(home, "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &providerDoctorRig{home: home, agentDir: agentDir, dbPath: filepath.Join(home, "mate.db")}
}

// withStore 打开（必要时创建）库并跑一段夹具动作，结束后关闭——模拟"库已经存在"的形态。
func (r *providerDoctorRig) withStore(t *testing.T, fn func(st *store.Store)) {
	t.Helper()
	st, err := store.Open(r.dbPath)
	if err != nil {
		t.Fatalf("建库失败：%v", err)
	}
	defer func() { _ = st.Close() }()
	fn(st)
}

func (r *providerDoctorRig) opts() doctorOptions {
	return doctorOptions{Home: r.home, AgentDir: r.agentDir, DBPath: r.dbPath, StoreMode: config.StoreModeSQLite}
}

// checkByName 把检查列表索引成 map（同名只可能有一条：名字就是判据的一部分）。
func checkByName(t *testing.T, checks []doctorCheck) map[string]doctorCheck {
	t.Helper()
	out := make(map[string]doctorCheck, len(checks))
	for _, c := range checks {
		if _, dup := out[c.Name]; dup {
			t.Fatalf("检查项重名（体检报告会对同一件事报两次）：%q", c.Name)
		}
		out[c.Name] = c
	}
	return out
}

// TestDoctorProviderSectionClean 干净现场：**provider 段一项都不该 fail**。
//
// "干净"的判据按 v11 的真实形态造：一条带真密文的档案、一条引用它的 agent、没有孤儿、没有对账标记。
// models 那两条仍是 warn（离线取不到运行时广告；dsh 落点未实测只读）——这正是"无法校验记警告"。
func TestDoctorProviderSectionClean(t *testing.T) {
	r := newProviderDoctorRig(t)
	vst, err := vault.Open(r.home)
	if err != nil {
		t.Fatal(err)
	}
	if err := vst.Put("vault:providers/p1", vault.KindProviderKey, []byte("sk-p1")); err != nil {
		t.Fatal(err)
	}
	r.withStore(t, func(st *store.Store) {
		if _, err := st.ImportProviderDefinition(context.Background(), "p1",
			`{"protocol":"openai","base_url":"https://api.deepseek.com/v1","key_ref":"vault:providers/p1"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertAgentDefinition(context.Background(), "a1",
			`{"id":"a1","name":"A1","runtimes":["opencode"],"provider":"p1"}`, 0, "t"); err != nil {
			t.Fatal(err)
		}
	})

	facts, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	for _, name := range []string{"providers", "vault", "provider keys", "provider refs", "providers import", "vault orphans", "models"} {
		c, ok := by[name]
		if !ok {
			t.Fatalf("缺检查项 %q：%+v", name, checks)
		}
		if c.Status == doctorFail {
			t.Fatalf("干净现场不该有失败项：%s — %s", c.Name, c.Detail)
		}
	}
	if by["providers"].Status != doctorOK || !strings.Contains(by["providers"].Detail, "1 条档案") {
		t.Fatalf("providers 应报 1 条档案：%+v", by["providers"])
	}
	if by["vault"].Status != doctorOK {
		t.Fatalf("有真实 keyfile 时 vault 应 ok：%+v", by["vault"])
	}
	if by["provider refs"].Status != doctorOK || by["provider keys"].Status != doctorOK {
		t.Fatalf("无悬空引用、密文齐全时应 ok：refs=%+v keys=%+v", by["provider refs"], by["provider keys"])
	}
	if len(facts.DanglingRefs) != 0 || len(facts.MissingCiphertexts) != 0 || len(facts.OrphanCiphertexts) != 0 {
		t.Fatalf("干净现场的事实面不该有条目：%+v", facts)
	}
}

// TestDoctorProviderSectionFailures 四种"事实与声明不符 / 残留"各自落到该落的档。
func TestDoctorProviderSectionFailures(t *testing.T) {
	r := newProviderDoctorRig(t)
	vst, err := vault.Open(r.home)
	if err != nil {
		t.Fatal(err)
	}
	// 孤儿密文：没有任何档案行引用它。
	if err := vst.Put("vault:providers/deleted-row", vault.KindProviderKey, []byte("sk-orphan")); err != nil {
		t.Fatal(err)
	}
	r.withStore(t, func(st *store.Store) {
		ctx := context.Background()
		// 声明了 key_ref 但密文不存在（悬空的密钥引用）。
		if _, err := st.ImportProviderDefinition(ctx, "p-missing",
			`{"type":"openai","key_ref":"vault:providers/p-missing"}`); err != nil {
			t.Fatal(err)
		}
		// 悬空引用：agent 指向不存在的档案。
		if _, err := st.UpsertAgentDefinition(ctx, "a-broken",
			`{"id":"a-broken","name":"broken","runtimes":["opencode"],"provider":"zz"}`, 0, "t"); err != nil {
			t.Fatal(err)
		}
		// 导入对账：真不一致（文件里有、库里没有）→ fail。
		if err := st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch, "legacy: 文件里有、DB 里没有"); err != nil {
			t.Fatal(err)
		}
	})

	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["provider refs"].Status != doctorFail || !strings.Contains(by["provider refs"].Detail, "a-broken→zz") {
		t.Fatalf("悬空引用必须 fail 且点名：%+v", by["provider refs"])
	}
	if by["provider keys"].Status != doctorFail || !strings.Contains(by["provider keys"].Detail, "p-missing") {
		t.Fatalf("悬空的密钥引用必须 fail 且点名：%+v", by["provider keys"])
	}
	if by["providers import"].Status != doctorFail || !strings.Contains(by["providers import"].Detail, "文件里有、DB 里没有") {
		t.Fatalf("真不一致必须 fail：%+v", by["providers import"])
	}
	if by["vault orphans"].Status != doctorWarn || !strings.Contains(by["vault orphans"].Detail, "deleted-row") {
		t.Fatalf("孤儿密文应 warn 且列出（不自动删）：%+v", by["vault orphans"])
	}
	// 主密钥那一条：本夹具里 vault.Open 已把 keyfile 建出来 → 可用即 ok；
	// **不可 ok 的唯一形态**是"密文在而主密钥丢了"（fail）——两档都由 checkProviderVault 决定。
	if by["vault"].Status != doctorOK {
		t.Fatalf("本夹具里主密钥可用，vault 应 ok：%+v", by["vault"])
	}
}

// TestDoctorProviderSectionImportBenignOnly 对账里只有"库里有、文件里没有"（写面新建后的快照滞后）
// → 只警告，**不判失败**（否则每次用过写面 doctor 都会红）。
func TestDoctorProviderSectionImportBenignOnly(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) {
		if err := st.SetMeta(context.Background(), model.MetaKeyProvidersImportMismatch,
			"p1: DB 里有、文件里没有（写面新建？跑 `mate provider export` 同步文件）"); err != nil {
			t.Fatal(err)
		}
	})
	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["providers import"].Status != doctorWarn {
		t.Fatalf("纯快照滞后应只警告：%+v", by["providers import"])
	}
	if !strings.Contains(by["providers import"].Detail, "mate provider export") {
		t.Fatalf("警告里要给出出路：%+v", by["providers import"])
	}
}

// TestDoctorProviderSectionNoStore 三种"没有 DB 事实"的处境分开说，且**都不判失败**
// （那都是"无法校验"，不是"事实与声明不符"）——memory 模式 / 库文件不存在 / DB_PATH 未配置。
func TestDoctorProviderSectionNoStore(t *testing.T) {
	r := newProviderDoctorRig(t)

	t.Run("memory 模式", func(t *testing.T) {
		opts := r.opts()
		opts.StoreMode = config.StoreModeMemory
		_, checks := checkProviderSection(opts)
		by := checkByName(t, checks)
		if by["providers"].Status != doctorWarn || !strings.Contains(by["providers"].Detail, "memory") {
			t.Fatalf("memory 模式应如实说明载体不是 DB：%+v", by["providers"])
		}
		for _, c := range checks {
			if c.Status == doctorFail {
				t.Fatalf("memory 模式下 provider 段不该有失败项：%s — %s", c.Name, c.Detail)
			}
		}
	})

	t.Run("库文件不存在", func(t *testing.T) {
		opts := r.opts()
		opts.DBPath = filepath.Join(r.home, "nope.db")
		_, checks := checkProviderSection(opts)
		by := checkByName(t, checks)
		if by["providers"].Status != doctorWarn || !strings.Contains(by["providers"].Detail, "不存在") {
			t.Fatalf("库文件不存在应记 warn（还没建库）：%+v", by["providers"])
		}
	})

	t.Run("未配置 DB 路径", func(t *testing.T) {
		opts := r.opts()
		opts.DBPath = ""
		_, checks := checkProviderSection(opts)
		by := checkByName(t, checks)
		if by["providers"].Status != doctorWarn {
			t.Fatalf("未配置库路径应记 warn：%+v", by["providers"])
		}
	})
}

// TestDoctorProviderSectionReadOnly 体检**不许**写库、**不许**创建 vault.key。
//
// 这是本段最硬的一条：doctor 是只读命令，一次运行不能改变被体检对象的状态——
// 包括"顺手把 keyfile 建出来"这种看起来无害的落笔。
func TestDoctorProviderSectionReadOnly(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) {
		if _, err := st.ImportProviderDefinition(context.Background(), "p1", `{"protocol":"openai","base_url":"https://api.deepseek.com/v1"}`); err != nil {
			t.Fatal(err)
		}
	})
	before, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}

	checkProviderSection(r.opts())

	after, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("体检改了库文件：size %d→%d mtime %s→%s", before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
	if _, err := os.Lstat(filepath.Join(r.home, vault.KeyFileName)); !os.IsNotExist(err) {
		t.Fatalf("体检创建了 vault.key（err=%v）——只读命令没有这个权限", err)
	}
}

// TestRunDoctorProviderSectionNoFailures 端到端：跑真正的 `runDoctor`，一个干净的 provider 现场
// 不该往失败数里加任何一项（退出码语义 = 既有口径不变）。
func TestRunDoctorProviderSectionNoFailures(t *testing.T) {
	r := newProviderDoctorRig(t)
	vst, err := vault.Open(r.home)
	if err != nil {
		t.Fatal(err)
	}
	if err := vst.Put("vault:providers/p1", vault.KindProviderKey, []byte("sk-p1")); err != nil {
		t.Fatal(err)
	}
	r.withStore(t, func(st *store.Store) {
		if _, err := st.ImportProviderDefinition(context.Background(), "p1",
			`{"protocol":"openai","base_url":"https://api.deepseek.com/v1","key_ref":"vault:providers/p1"}`); err != nil {
			t.Fatal(err)
		}
	})
	web := filepath.Join(r.home, "web")
	writeFiles(t, web, map[string]string{"index.html": "<html></html>", "build-id.txt": "abcdef\n"})

	report := runDoctor(doctorOptions{
		Home: r.home, WebDir: web, Addr: "127.0.0.1:0",
		AgentDir: r.agentDir, DBPath: r.dbPath, StoreMode: config.StoreModeSQLite,
	})
	if report.Failed != 0 {
		t.Fatalf("干净的 provider 现场不该产生失败项（failed=%d）：\n%s", report.Failed, renderDoctorReport(report))
	}
	if report.Providers.ProviderRows != 1 || report.Providers.ProviderIDs[0] != "p1" {
		t.Fatalf("--json 的 providers 段应带上事实：%+v", report.Providers)
	}
	if report.Passed != (report.Failed == 0) {
		t.Fatal("Passed 应与 Failed==0 一致")
	}
	// 事实段必须能被机器读（键名稳定）：序列化后能解析回同一个形状。
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Providers struct {
			ProviderRows int      `json:"providerRows"`
			ProviderIDs  []string `json:"providerIds"`
			VaultUsable  bool     `json:"vaultUsable"`
			VaultSource  string   `json:"vaultSource"`
			Runtimes     []struct {
				Runtime         string `json:"runtime"`
				AdvertisedKnown bool   `json:"advertisedKnown"`
				DiffNote        string `json:"diffNote"`
			} `json:"runtimes"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatalf("providers 段的键名不是稳定的机器可读形状：%v\n%s", err, b)
	}
	if probe.Providers.ProviderRows != 1 || !probe.Providers.VaultUsable || probe.Providers.VaultSource != "keyfile" {
		t.Fatalf("键名/取值不对：%+v", probe.Providers)
	}
	if len(probe.Providers.Runtimes) == 0 {
		t.Fatalf("runtime 段不应为空：%+v", probe.Providers)
	}
	for _, rt := range probe.Providers.Runtimes {
		if rt.AdvertisedKnown {
			t.Fatalf("离线命令不许声称取到了运行时广告：%+v", rt)
		}
	}
}

// TestDoctorProviderVaultFail 主密钥那一条**真的会 fail** 的形态：密文还在、主密钥没了。
//
// 这是 doctor 必须报出来的最坏一种现场（密文永久不可恢复），所以单独钉一条——
// 判据是"有实际存在的密文"而不是"档案声明了 key_ref"（后者可能只是引用悬空，
// 由 `provider keys` 那一条报）。
func TestDoctorProviderVaultFail(t *testing.T) {
	r := newProviderDoctorRig(t)
	vst, err := vault.Open(r.home)
	if err != nil {
		t.Fatal(err)
	}
	if err := vst.Put("vault:providers/p1", vault.KindProviderKey, []byte("sk-p1")); err != nil {
		t.Fatal(err)
	}
	r.withStore(t, func(st *store.Store) {
		if _, err := st.ImportProviderDefinition(context.Background(), "p1",
			`{"protocol":"openai","base_url":"https://api.deepseek.com/v1","key_ref":"vault:providers/p1"}`); err != nil {
			t.Fatal(err)
		}
	})
	// 主密钥被删/被换（用户侧的事故）——密文文件仍在。
	if err := os.Remove(filepath.Join(r.home, vault.KeyFileName)); err != nil {
		t.Fatal(err)
	}

	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["vault"].Status != doctorFail {
		t.Fatalf("密文在而主密钥没了必须 fail：%+v", by["vault"])
	}
	if by["provider keys"].Status != doctorOK {
		t.Fatalf("密文文件在，密钥引用面本身没悬空（ok）：%+v", by["provider keys"])
	}
}

// TestDoctorProviderVaultNotYetCreated 还没有任何密文时，主密钥不存在只该 warn（不丢东西）。
func TestDoctorProviderVaultNotYetCreated(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) {
		if _, err := st.ImportProviderDefinition(context.Background(), "p1", `{"protocol":"openai","base_url":"https://api.deepseek.com/v1"}`); err != nil {
			t.Fatal(err)
		}
	})
	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["vault"].Status != doctorWarn || !strings.Contains(by["vault"].Detail, "尚未创建") {
		t.Fatalf("还没写过密文时应 warn 并说明：%+v", by["vault"])
	}
	if by["vault orphans"].Status != doctorOK {
		t.Fatalf("没有密文目录时应报无孤儿：%+v", by["vault orphans"])
	}
}
