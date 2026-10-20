package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/credential"

	doctorfacts "github.com/morehao/ark-mate/internal/drivers/mechanism/doctor"

	"github.com/morehao/ark-mate/internal/config"
	provrow "github.com/morehao/ark-mate/internal/domain/provider/row"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
)

// ——— K2：`mate doctor` 的 provider / 模型清单段（v20 凭据面） ———
//
// 这一组钉的是**分档**（哪一条该 fail、哪一条只能 warn），不是措辞——分档错了就是退出码错了。
// v20 的三条分档口径（推翻 v19 的"密文在而主密钥丢了 = 不可恢复"那一档）：
//
//	credentials    文档权限不合 / 形状坏 → **fail**（平台拒读它 = 用户填的密钥不被采用）；
//	               不存在 / 有条目没写进去 → warn（**权威在库**，镜像可由库重建）；
//	provider keys  库那一列为空的档案 → warn（尚未设置，档案可以先建后补）；
//	legacy secrets 旧机制遗留物 → 永远 warn（**已迁移，可删**；医生不替用户删）。
type providerDoctorRig struct {
	home        string
	providerDir string
	dbPath      string
}

func newProviderDoctorRig(t *testing.T) *providerDoctorRig {
	t.Helper()
	home := t.TempDir()
	providerDir := filepath.Join(home, "providers")
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &providerDoctorRig{home: home, providerDir: providerDir, dbPath: filepath.Join(home, "mate.db")}
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
	return doctorOptions{Home: r.home, ProviderDir: r.providerDir, DBPath: r.dbPath, StoreMode: config.StoreModeSQLite}
}

// writeMirror 在 `<home>/credentials.json` 写一份镜像（perm 由调用方决定：0600 之外的取值
// 正是"权限不合"那一档的判据）。
func (r *providerDoctorRig) writeMirror(t *testing.T, perm os.FileMode, values map[string]string) {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential.CredentialsPath(r.home), raw, perm); err != nil {
		t.Fatal(err)
	}
}

// seedProvider 造一条供应商行（可选：连密钥一起写进同一行）。
func seedProvider(t *testing.T, st *store.Store, id string, key string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.ImportProviderDefinition(ctx, store.ProviderColumns{
		ID: id, Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if key != "" {
		if written, err := st.SetProviderKey(ctx, id, key); err != nil || !written {
			t.Fatalf("写密钥: written=%v err=%v", written, err)
		}
	}
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
// "干净"的判据按 v20 的真实形态造：一条带密钥的档案（密钥就在那一行上）+ 一条挂靠它的模型条目
// + 一份权限合规、与库一致的镜像 + 没有遗留物 + 没有对账标记。
// models 那两条仍是 warn（离线取不到运行时广告；dsh 落点未实测只读）——这正是"无法校验记警告"。
func TestDoctorProviderSectionClean(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) {
		seedProvider(t, st, "p1", "sk-p1")
		// 引用者是模型条目（v19：供应商的唯一引用者；默认模型决定注入哪条档案）。
		if created, err := st.CreateModelDefinition(context.Background(), provrow.ModelColumns{
			ID: "mdl_a1", ProviderID: "p1", Model: "m-1", InputModalities: "text",
		}, "t"); err != nil || !created {
			t.Fatalf("造模型行: created=%v err=%v", created, err)
		}
	})
	// 镜像与库一致（装配期会写成这样）。
	r.writeMirror(t, 0o600, map[string]string{"providers/p1": "sk-p1"})

	facts, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	for _, name := range []string{"providers", "credentials", "provider keys", "mcp secrets", "legacy secrets", "providers import", "models"} {
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
	if by["credentials"].Status != doctorOK {
		t.Fatalf("镜像与库一致且权限 0600 时应 ok：%+v", by["credentials"])
	}
	if by["provider keys"].Status != doctorOK {
		t.Fatalf("库那一列有值时 keys 应 ok：%+v", by["provider keys"])
	}
	if by["legacy secrets"].Status != doctorOK {
		t.Fatalf("没有遗留物时应 ok：%+v", by["legacy secrets"])
	}
	if len(facts.NoKeyProviders) != 0 || len(facts.MissingInCredentials) != 0 || len(facts.MissingInStore) != 0 {
		t.Fatalf("干净现场的事实面不该有条目：%+v", facts)
	}
	if len(facts.ProviderKeysWithKey) != 1 || facts.ProviderKeysWithKey[0] != "p1" {
		t.Fatalf("providerKeysWithKey 应列出 p1：%+v", facts.ProviderKeysWithKey)
	}
}

// TestDoctorProviderSectionFailures "事实与声明不符 / 残留"各自落到该落的档：
//
//	provider keys   库里没有密钥 → warn（尚未设置，不是"引用悬空"）
//	mcp secrets     孤儿密钥（没有任何定义引用）→ warn，且**不自动删**
//	providers import 真不一致（文件里有、库里没有）→ fail
func TestDoctorProviderSectionFailures(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) {
		ctx := context.Background()
		seedProvider(t, st, "p-missing", "")
		// 孤儿密钥：库里有一条，但没有任何 MCP 定义引用它。
		if err := st.SetMcpSecret(ctx, "store:default/orphan", "sk-orphan"); err != nil {
			t.Fatal(err)
		}
		// 导入对账：真不一致（文件里有、库里没有）→ fail。
		if err := st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch, "legacy: 文件里有、DB 里没有"); err != nil {
			t.Fatal(err)
		}
	})

	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["provider keys"].Status != doctorWarn || !strings.Contains(by["provider keys"].Detail, "p-missing") {
		t.Fatalf("没有密钥的档案应 warn 且点名（它是「尚未设置密钥」）：%+v", by["provider keys"])
	}
	if by["providers import"].Status != doctorFail || !strings.Contains(by["providers import"].Detail, "文件里有、DB 里没有") {
		t.Fatalf("真不一致必须 fail：%+v", by["providers import"])
	}
	if by["mcp secrets"].Status != doctorWarn || !strings.Contains(by["mcp secrets"].Detail, "orphan") {
		t.Fatalf("孤儿密钥应 warn 且列出（不自动删）：%+v", by["mcp secrets"])
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

// TestDoctorProviderSectionReadOnly 体检**不许**写库、**不许**建托管文档、**不许**动遗留物。
//
// 这是本段最硬的一条：doctor 是只读命令，一次运行不能改变被体检对象的状态——包括"顺手把
// 托管文档建出来"与"顺手把看起来没用的旧密文删掉"这两种看起来无害的落笔。
func TestDoctorProviderSectionReadOnly(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) { seedProvider(t, st, "p1", "sk-p1") })
	// 旧机制遗留物（用例通过**生产侧**给出的路径构造，不自己拼那套目录名）。
	legacyDir, legacyKeyFile := doctorfacts.LegacySecretPaths(doctorfacts.LegacySecretAnchor(r.home, r.providerDir))
	if err := os.MkdirAll(filepath.Join(legacyDir, "providers"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyCipher := filepath.Join(legacyDir, "providers", "p1.enc")
	if err := os.WriteFile(legacyCipher, []byte("frozen-ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyKeyFile, []byte("frozen-master-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}

	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["legacy secrets"].Status != doctorWarn {
		t.Fatalf("有遗留物时应如实报 warn：%+v", by["legacy secrets"])
	}

	after, err := os.Stat(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("体检改了库文件：size %d→%d mtime %s→%s", before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
	if _, err := os.Lstat(credential.CredentialsPath(r.home)); !os.IsNotExist(err) {
		t.Fatalf("体检创建了托管文档（err=%v）——只读命令没有这个权限", err)
	}
	if _, err := os.Lstat(legacyCipher); err != nil {
		t.Fatalf("体检动了旧密文（%v）——那是用户唯一一份旧密钥本体", err)
	}
	if _, err := os.Lstat(legacyKeyFile); err != nil {
		t.Fatalf("体检动了旧主密钥文件（%v）", err)
	}
}

// TestRunDoctorProviderSectionNoFailures 端到端：跑真正的 `runDoctor`，一个干净的 provider 现场
// 不该往失败数里加任何一项（退出码语义 = 既有口径不变）。
func TestRunDoctorProviderSectionNoFailures(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) { seedProvider(t, st, "p1", "sk-p1") })
	r.writeMirror(t, 0o600, map[string]string{"providers/p1": "sk-p1"})
	web := filepath.Join(r.home, "web")
	writeFiles(t, web, map[string]string{"index.html": "<html></html>", "build-id.txt": "abcdef\n"})

	report := runDoctor(doctorOptions{
		Home: r.home, WebDir: web, Addr: "127.0.0.1:0",
		ProviderDir: r.providerDir, DBPath: r.dbPath, StoreMode: config.StoreModeSQLite,
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
	// 这里**逐字钉住 v20 的字段名**——它们是 `--json` 的对外契约（诊断包与 CI 消费）。
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Providers struct {
			ProviderRows        int      `json:"providerRows"`
			ProviderIDs         []string `json:"providerIds"`
			ProviderKeysWithKey []string `json:"providerKeysWithKey"`
			NoKeyProviders      []string `json:"noKeyProviders"`
			CredentialsPath     string   `json:"credentialsPath"`
			CredentialsExists   bool     `json:"credentialsExists"`
			CredentialsPerm     string   `json:"credentialsPerm"`
			CredentialsPermOK   bool     `json:"credentialsPermOk"`
			CredentialsRefs     int      `json:"credentialsRefs"`
			McpSecretRefs       []string `json:"mcpSecretRefs"`
			OrphanMcpSecrets    []string `json:"orphanMcpSecrets"`
			LegacySecretAnchor  string   `json:"legacySecretAnchor"`
			LegacySecretDir     bool     `json:"legacySecretDir"`
			LegacySecretKeyFile bool     `json:"legacySecretKeyFile"`
			Runtimes            []struct {
				Runtime         string `json:"runtime"`
				AdvertisedKnown bool   `json:"advertisedKnown"`
				DiffNote        string `json:"diffNote"`
			} `json:"runtimes"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatalf("providers 段的键名不是稳定的机器可读形状：%v\n%s", err, b)
	}
	p := probe.Providers
	if p.ProviderRows != 1 || p.CredentialsPath != credential.CredentialsPath(r.home) {
		t.Fatalf("键名/取值不对：%+v", p)
	}
	if !p.CredentialsExists || p.CredentialsPerm != "0600" || !p.CredentialsPermOK || p.CredentialsRefs != 1 {
		t.Fatalf("托管文档面的事实不对：%+v", p)
	}
	if len(p.ProviderKeysWithKey) != 1 || p.ProviderKeysWithKey[0] != "p1" || len(p.NoKeyProviders) != 0 {
		t.Fatalf("密钥面的事实不对：%+v", p)
	}
	if p.LegacySecretDir || p.LegacySecretKeyFile {
		t.Fatalf("干净现场不该报遗留物：%+v", p)
	}
	if len(p.Runtimes) == 0 {
		t.Fatalf("runtime 段不应为空：%+v", p)
	}
	for _, rt := range p.Runtimes {
		if rt.AdvertisedKnown {
			t.Fatalf("离线命令不许声称取到了运行时广告：%+v", rt)
		}
	}
}

// TestDoctorCredentialsPermFail 托管文档**真的会 fail** 的形态：权限过宽。
//
// 这是 v20 新增的最重要一档：文档是可自由复制的明文，而权限是它唯一的边界。平台读它时**硬失败**
// （`pkg/credentials` 的纪律），于是"面板里保存的密钥不被采用"——事实与声明不符，必须判失败。
// 同一现场里库那一列照样有值，所以 `provider keys` 仍是 ok：两条判据回答的是两个问题。
func TestDoctorCredentialsPermFail(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) { seedProvider(t, st, "p1", "sk-p1") })
	r.writeMirror(t, 0o644, map[string]string{"providers/p1": "sk-p1"})

	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["credentials"].Status != doctorFail {
		t.Fatalf("权限过宽必须 fail（平台拒读它）：%+v", by["credentials"])
	}
	if by["provider keys"].Status != doctorOK {
		t.Fatalf("库那一列有值 → keys 仍 ok：%+v", by["provider keys"])
	}
}

// TestDoctorCredentialsNotYetCreated 还没有写出托管文档时只该 warn（**权威在库**，它可重建）。
func TestDoctorCredentialsNotYetCreated(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) { seedProvider(t, st, "p1", "sk-p1") })

	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if by["credentials"].Status != doctorWarn || !strings.Contains(by["credentials"].Detail, "尚未生成") {
		t.Fatalf("还没有托管文档时应 warn 并说明：%+v", by["credentials"])
	}
	if by["legacy secrets"].Status != doctorOK {
		t.Fatalf("没有遗留物时应报 ok：%+v", by["legacy secrets"])
	}
}

// TestDoctorCredentialsMirrorOnlyInStore 镜像里有、库里没有 → warn 且**不自动回灌**。
//
// 这一档的失效方式是静默的：如果医生（或装配期）顺手把镜像那份写回库，用户刚清掉的密钥就会被
// 一份陈旧文件悄悄撤销。所以要如实报出引用名与出路，而不是"修好它"。
func TestDoctorCredentialsMirrorOnlyInStore(t *testing.T) {
	r := newProviderDoctorRig(t)
	r.withStore(t, func(st *store.Store) { seedProvider(t, st, "p1", "sk-p1") })
	// 镜像里多出两条库里没有的（用户手写、或某次删除没写到底）：一条 provider、一条 MCP。
	r.writeMirror(t, 0o600, map[string]string{
		"providers/p1":              "sk-p1",
		"providers/p-mirror-only":   "sk-handwritten",
		"store:default/handwritten": "sk-handwritten",
	})

	facts, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	if len(facts.MissingInStore) != 2 {
		t.Fatalf("镜像多出来的两条都应被列出：%+v", facts.MissingInStore)
	}
	if by["credentials"].Status != doctorWarn {
		t.Fatalf("镜像与库不一致应 warn：%+v", by["credentials"])
	}
	if !strings.Contains(by["credentials"].Detail, "不自动回灌") {
		t.Fatalf("警告里必须写清「不采用镜像那一份」：%+v", by["credentials"])
	}
	// 出路**逐类说**，且每类只说用户今天真能做到的那条：
	//   · provider 密钥 → 设置 · 模型（那里确实有写入口）；
	//   · MCP 密钥     → 改引用为 env:（**写值的那条路这一批还没有**，写了也不该让用户去找）。
	detail := by["credentials"].Detail
	if !strings.Contains(detail, "供应商密钥") || !strings.Contains(detail, "设置 · 模型") {
		t.Fatalf("provider 那一类应指向「设置 · 模型」：%s", detail)
	}
	if !strings.Contains(detail, "MCP 密钥") || !strings.Contains(detail, "env:<ENV_NAME>") {
		t.Fatalf("MCP 那一类应指向 env: 引用：%s", detail)
	}
	for _, banned := range []string{"把值写进库", "mcp_secrets", "MCP 面板里重新填一次"} {
		if strings.Contains(detail, banned) {
			t.Fatalf("出路不得指向不存在的入口（%q）：%s", banned, detail)
		}
	}
	// 库里那一份**没被动过**（只读体检）。
	ro, err := store.OpenReadOnly(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, ok, err := ro.McpSecret(context.Background(), "store:default/handwritten"); err != nil || ok {
		t.Fatalf("体检把镜像那份回灌进库了：ok=%v err=%v", ok, err)
	}
}

// TestDoctorLegacyMigrationBackupLayout 老布局遗留（v19 之前留在数据目录根下的 `mate.db.bak-v*`）：
// **只报不处置**，且判 warn 而不是 fail。
//
// 三件事一起钉，因为它们的失效方式都是静默的：① 不报 = 用户看不见"根下还堆着 7 个文件"；
// ② 判 fail = 退出码红，而这既不丢东西也不阻塞任何功能（把"可以收拾一下"报成故障）；
// ③ 顺手搬移/删除 = 医生越权改动回退窗口（恢复说明指向的路径被悄悄改掉）。
func TestDoctorLegacyMigrationBackupLayout(t *testing.T) {
	r := newProviderDoctorRig(t)
	legacy := r.dbPath + ".bak-v15-20260101T000000Z"
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.withStore(t, func(*store.Store) {})

	_, checks := checkProviderSection(r.opts())
	by := checkByName(t, checks)
	c, ok := by["备份布局"]
	if !ok {
		t.Fatalf("缺检查项「备份布局」：%+v", checks)
	}
	if c.Status != doctorWarn {
		t.Fatalf("老布局遗留应记 warn（有效退路，只是位置旧）：%+v", c)
	}
	if !strings.Contains(c.Detail, "backups/") || !strings.Contains(c.Detail, filepath.Base(legacy)) {
		t.Fatalf("警告要给出新位置与具体文件名：%+v", c)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("医生不得搬移或删除遗留备份：%v", err)
	}

	// 没有遗留时这一条是 ok（不是"无法校验"，也不是 warn）——否则每台机器都会长期挂着一条警告。
	clean := newProviderDoctorRig(t)
	clean.withStore(t, func(*store.Store) {})
	_, checks = checkProviderSection(clean.opts())
	if c := checkByName(t, checks)["备份布局"]; c.Status != doctorOK {
		t.Fatalf("没有遗留应报 ok：%+v", c)
	}
}
