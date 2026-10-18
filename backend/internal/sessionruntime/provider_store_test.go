package sessionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"github.com/morehao/ark-mate/internal/mechanism/definition"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— v11 provider 载体（B1/B4）与存量导入（B2，**闸门 3 / H2**） ———

// ptr 取字符串字面量的地址（PATCH DTO 的指针语义：nil = 不改）。
func ptr[T any](v T) *T { return &v }

// TestProviderImportFileEquivalence 闸门 3（H2）：以**真实形态**的 `providers.json`
// （一条只留派生引用的 + 一条历史明文，且带历史 `model` 与一个未知键）为夹具，
// 迁移前后逐字段等价；备份存在、文件**一个字节都不变**、对账干净、导入幂等。
//
// v20 起 `key_ref` 是**派生量**：文件里那条引用（哪怕是旧前缀）不再指向任何东西，
// "这条档案有没有密钥"只看 `providers.key` 那一列。所以夹具里两种形态分别落到
// has_key=false（只有引用、没有任何值）与 has_key=true（历史明文被搬进那一列）。
//
// 这一条是 v11 迁移正确性的唯一判据：档案的字段少（id/type/key_ref），所以**全量逐条比**，
// 没有抽样一说。历史 `model` 与未知键必须在 body 里活下来（P6：文件载体会丢未知键，
// 而迁移这一步绝不能丢——丢了就再也回不去）。
func TestProviderImportFileEquivalence(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	const legacySecret = "sk-legacy-plaintext-abc123"
	existingRef := credential.ProviderRef("deepseek-main")

	content := `{
  "deepseek-main": {
    "id": "deepseek-main",
    "type": "deepseek",
    "model": "deepseek-v4-flash",
    "key_ref": "` + existingRef + `",
    "note": "主账号"
  },
  "legacy-openai": {
    "id": "legacy-openai",
    "type": "openai",
    "model": "gpt-4o",
    "key": "` + legacySecret + `"
  }
}`
	writeLegacyProviders(t, dir, content)
	before := readProvidersFile(t, dir)
	beforeStat, err := os.Stat(filepath.Join(dir, provider.ProvidersFileName))
	if err != nil {
		t.Fatalf("stat providers.json: %v", err)
	}

	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()

	stats, err := provider.ImportProviderFile(ctx, st, dir, definition.DefinitionImportOptions{Auto: true}, nil)
	if err != nil {
		t.Fatalf("provider.ImportProviderFile: %v", err)
	}
	if stats.Already || stats.Providers != 2 || stats.Inserted != 2 || stats.Existing != 0 {
		t.Fatalf("导入计数不符: %+v", stats)
	}
	if len(stats.Mismatch) != 0 {
		t.Fatalf("对账不该有不一致: %v", stats.Mismatch)
	}
	if len(stats.KeyProblems) != 0 {
		t.Fatalf("这条现场里不该有搬不动的密钥: %v", stats.KeyProblems)
	}

	// ① 备份存在，且内容是**迁移前**那份（含明文）。
	backupPath := filepath.Join(dir, provider.ProvidersBackupName)
	bb, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("迁移前备份未写出: %v", err)
	}
	if string(bb) != before {
		t.Fatalf("备份内容应逐字节等于迁移前的文件")
	}

	// ② 原文件**一个字节都不变**（不删不回写），mtime 也不动。
	after := readProvidersFile(t, dir)
	afterStat, _ := os.Stat(filepath.Join(dir, provider.ProvidersFileName))
	if after != before {
		t.Fatalf("providers.json 被改写了:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if !afterStat.ModTime().Equal(beforeStat.ModTime()) {
		t.Fatalf("providers.json 的 mtime 被改动了: %s → %s", beforeStat.ModTime(), afterStat.ModTime())
	}

	// ③ 逐字段等价：id / protocol / base_url / note 都要对（v13 起是**列**，没有 body 可比）。
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("providers 行数不符: n=%d err=%v", len(rows), err)
	}
	got := map[string]model.ProviderEntity{}
	for i := range rows {
		got[rows[i].ID] = rows[i]
	}
	// 只留引用的那条：**v11 → v12 的字段映射**逐条成立
	//   `type: "deepseek"` → `protocol: "openai"` + `base_url: https://api.deepseek.com/v1`
	//   （按冻结的已知端点表推导；这是 T6.1 的"导入保留对 v11 type 的识别"）
	//   `note`（平台拥有的列）原样活下来。
	//   `model` 是 v11 的历史静默陷阱键（写了不生效），映射**主动丢掉它**。
	dm, ok := got["deepseek-main"]
	if !ok {
		t.Fatal("deepseek-main 未导入")
	}
	if dm.Protocol != "openai" || dm.BaseURL != "https://api.deepseek.com/v1" || dm.Note != "主账号" {
		t.Fatalf("deepseek-main 的列值不符: %+v", dm)
	}
	// 明文那条：历史明文进 `providers.key` 那一列（v20 起它才有落点），文件一个字节不改。
	lb, ok := got["legacy-openai"]
	if !ok {
		t.Fatal("legacy-openai 未导入")
	}
	// v11 → v12 映射：`type: "openai"` → `protocol: "openai"` + 冻结表推导的 `base_url`。
	if lb.Protocol != "openai" || lb.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("legacy-openai 的列值不符: %+v", lb)
	}
	// 明文就落在那一列上（唯一落点），且引用是**派生**的（文件里写什么都没用）。
	if lb.Key != legacySecret {
		t.Fatalf("历史明文应被搬进 key 列: %q", lb.Key)
	}
	if pt, found, kErr := st.ProviderKey(ctx, "legacy-openai"); kErr != nil || !found || pt != legacySecret {
		t.Fatalf("库里的密钥列不符: found=%v len=%d err=%v", found, len(pt), kErr)
	}
	// 反过来：只有引用、没有任何值的那条，库里那一列必须是空的（引用不构成"有密钥"）。
	if v, found, kErr := st.ProviderKey(ctx, "deepseek-main"); kErr != nil || !found || v != "" {
		t.Fatalf("只留引用的那条不该有密钥值: v=%q found=%v err=%v", v, found, kErr)
	}

	// ④ marker 落库（幂等依据）；审计是 action=import。
	if v, ok, _ := st.GetMeta(ctx, model.MetaKeyImportedProviders); !ok || v != stats.Marker {
		t.Fatalf("meta.imported_providers 不符: ok=%v v=%q want=%q", ok, v, stats.Marker)
	}
	for _, id := range []string{"deepseek-main", "legacy-openai"} {
		revs, err := st.ListDefinitionRevisions(ctx, model.DefinitionKindProvider, id)
		if err != nil || len(revs) != 1 || revs[0].Action != model.DefinitionActionImport {
			t.Fatalf("%s 的导入审计不符: %+v err=%v", id, revs, err)
		}
	}

	// ⑤ 幂等：内容没变 → 整体跳过（一个字节都不写）。
	again, err := provider.ImportProviderFile(ctx, st, dir, definition.DefinitionImportOptions{Auto: true}, nil)
	if err != nil || !again.Already || again.Inserted != 0 {
		t.Fatalf("重复导入应整体跳过: %+v err=%v", again, err)
	}

	// ⑥ 保存并重开（模拟重启）：装载出来的内存档案与文件时代逐字段一致。
	svc := NewService(publisherRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.planes.Credentials.SetLayered(newTestCredentials(t, home))
	svc.planes.Providers.LoadProviderDefinitionsFromStore(ctx, dir)
	items, _ := svc.planes.Providers.ListProviders(context.Background())
	if len(items) != 2 {
		t.Fatalf("装载后档案数不符: %+v", items)
	}
	byID := map[string]dtoprovider.ProviderResp{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if it := byID["deepseek-main"]; it.Protocol != "openai" || it.HasKey {
		t.Fatalf("只有引用、库里没有值 → 不该报有密钥: %+v", it)
	}
	if it := byID["legacy-openai"]; it.Protocol != "openai" || !it.HasKey {
		t.Fatalf("legacy-openai 装载形态不符: %+v", it)
	}
	// 注入值就是迁移前那份（明文解出来了）。
	if got := svc.planes.Providers.ProviderEnvFor(runtimetest.ID, "legacy-openai"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_LEGACY_OPENAI_") {
		t.Fatalf("迁移后注入不符: %v", got)
	}
	// 引用它的默认模型不再被报悬空（B3 的装载顺序判据）。
	//
	// v19：注入的档案由**默认模型**那条清单决定（agent 上的 provider 字段已随 agent 面撤销）。
	// v20：挂到**有密钥**的那条上（`legacy-openai`）——只有引用、没有值的那条会把
	// `credential_missing` 如实报出来（那是正确行为，见下面的反证）。
	created, err := st.CreateModelDefinition(ctx, model.ModelColumns{
		ID: "mdl_a2probe", ProviderID: "legacy-openai", Model: "m-a2probe", InputModalities: "text",
	}, "t")
	if err != nil || !created {
		t.Fatalf("造默认模型行: created=%v err=%v", created, err)
	}
	if err := st.SetDefaultModelID(ctx, "mdl_a2probe"); err != nil {
		t.Fatalf("置默认模型: %v", err)
	}
	if err := svc.CredentialProblem(ctx); err != nil {
		t.Fatalf("导入后凭据应可用: %v", err)
	}
	// 反证：挂到"只有引用、库里没有值"的那条上必须报出来——否则"引用即有效"这个错觉会在
	// v20 里复活（文件里的 `key_ref` 不携带任何东西）。
	created, err = st.CreateModelDefinition(ctx, model.ModelColumns{
		ID: "mdl_nokey", ProviderID: "deepseek-main", Model: "m-nokey", InputModalities: "text",
	}, "t")
	if err != nil || !created {
		t.Fatalf("造反证模型行: created=%v err=%v", created, err)
	}
	if err := st.SetDefaultModelID(ctx, "mdl_nokey"); err != nil {
		t.Fatal(err)
	}
	if err := svc.CredentialProblem(ctx); err == nil {
		t.Fatal("库里没有那把密钥时必须报 credential_missing（引用本身不携带值）")
	}
}

// publisherRegistry 一个最小注册表：**带凭据交付面声明**（v12 起"要不要注入 env"由各底座
// 自己的声明决定，没有声明的 runtime 平台一律不注入——所以凡是要验注入的夹具都得声明它）。
func publisherRegistry() *runtime.Registry { return v12ProviderRegistry() }

// TestProviderWriteBackDisabledInSQLiteMode B4 / A5：sqlite 模式下写面**不再回写**
// `providers.json`——内容与 mtime 都不变（v20 起密钥也不再以任何形态进这个文件）。
//
// 这是"事实源换载体"最容易被忽略的一条：只要有一条路径还在写文件，"文件与 DB 谁是权威"
// 这个问题就会在用户把文件改回去之后重新出现。
func TestProviderWriteBackDisabledInSQLiteMode(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	writeLegacyProviders(t, dir, `{"existing":{"id":"existing","type":"deepseek","key_ref":"providers/existing"}}`)
	before := readProvidersFile(t, dir)
	beforeStat, err := os.Stat(filepath.Join(dir, provider.ProvidersFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	// 先走一次导入，让"文件里的 existing"成为 DB 行的起点（真实升级后的形态）。
	if _, err := provider.ImportProviderFile(ctx, st, dir, definition.DefinitionImportOptions{Auto: true}, nil); err != nil {
		t.Fatalf("provider.ImportProviderFile: %v", err)
	}
	svc := NewService(publisherRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.planes.Credentials.SetLayered(newTestCredentials(t, home))
	svc.planes.Providers.LoadProviderDefinitionsFromStore(ctx, dir)

	// 写面：新建一条（走 store）+ 覆盖一条（走 store）。
	if _, err := svc.planes.Providers.CreateProvider(ctx, providerReq("brand-new", ptr("sk-new-secret"))); err != nil {
		t.Fatalf("CreateProvider(新建): %v", err)
	}
	if _, err := svc.planes.Providers.UpdateProvider(ctx, "existing", dtoprovider.ProviderPatchReq{Note: ptr("改了备注")}); err != nil {
		t.Fatalf("UpdateProvider(覆盖): %v", err)
	}
	// 行确实写进了 DB。
	row, found, err := st.GetProviderDefinition(ctx, "brand-new")
	if err != nil || !found {
		t.Fatalf("新档案应落 DB 行: found=%v err=%v", found, err)
	}
	// 版本号在 v20 里至少是 2：建行一次 + 写密钥一次（两笔都进历史，所以 revision 才可解释）。
	if row.Protocol != "openai" || row.BaseURL == "" || row.Revision < 2 {
		t.Fatalf("新行的列值不符（revision 应 ≥2：建行 + 写密钥各 bump 一次）: %+v", row)
	}
	if v, found, kErr := st.ProviderKey(ctx, "brand-new"); kErr != nil || !found || v != "sk-new-secret" {
		t.Fatalf("新建供应商的密钥应落 key 列（引用由 id 派生，不再是行上的 key_ref）: v=%q found=%v err=%v", v, found, kErr)
	}

	// 文件：内容与 mtime 都没变。
	after := readProvidersFile(t, dir)
	afterStat, _ := os.Stat(filepath.Join(dir, provider.ProvidersFileName))
	if after != before {
		t.Fatalf("sqlite 模式下不应回写 providers.json:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if !afterStat.ModTime().Equal(beforeStat.ModTime()) {
		t.Fatalf("providers.json 的 mtime 被改动了: %s → %s", beforeStat.ModTime(), afterStat.ModTime())
	}
}

// TestProviderMemoryCarrierStillUsesFile A6：memory 模式**逐字保持今天的行为**——文件仍是
// 事实源，写面落文件，重启后仍在。
func TestProviderMemoryCarrierStillUsesFile(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "providers")
	svc := providerTestSvc(t, dir) // memory 模式（无持久层）
	if _, err := svc.planes.Providers.CreateProvider(ctx, providerReq("p1", ptr("sk-memory-secret"))); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	raw := readProvidersFile(t, dir)
	if strings.Contains(raw, "sk-memory-secret") {
		t.Fatalf("密钥不应以明文落文件: %s", raw)
	}
	if !strings.Contains(raw, "key_ref") || !strings.Contains(raw, "providers/p1") {
		t.Fatalf("文件应只留 key_ref: %s", raw)
	}
	// 删除必须**落盘**：文件载体是"先摘内存 → 再序列化"，顺序反了会把刚删的条目原样写回
	// （界面上没了、文件里还在，重启后它又回来了——完全静默）。
	if del, err := svc.planes.Providers.DeleteProvider(ctx, "p1"); err != nil || !del.Deleted {
		t.Fatalf("删除应成功: %+v err=%v", del, err)
	}
	if raw := readProvidersFile(t, dir); strings.Contains(raw, `"p1"`) {
		t.Fatalf("删除后文件里仍留着该条目（先落盘后摘内存的顺序反了）: %s", raw)
	}
	if _, err := svc.planes.Providers.CreateProvider(ctx, providerReq("p1", ptr("sk-memory-secret"))); err != nil {
		t.Fatalf("重建用于重启断言: %v", err)
	}

	// 重启（新 Service，同一目录）→ 档案与注入都还在。
	svc2 := providerTestSvc(t, dir)
	items, _ := svc2.Planes().Providers.ListProviders(context.Background())
	if len(items) != 1 || items[0].ID != "p1" || !items[0].HasKey {
		t.Fatalf("memory 模式重启后档案丢失: %+v", items)
	}
	if got := svc2.Planes().Providers.ProviderEnvFor(runtimetest.ID, "p1"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_P1_") || !strings.HasSuffix(got[0], "_API_KEY=sk-memory-secret") {
		t.Fatalf("memory 模式重启后注入不符: %v", got)
	}
}

// TestProviderExportRoundTrip B6：`mate provider export` 是回退窗口的唯一回灌路径，
// 因此它必须**确定化**（导出两次逐字节相同），且导出物只含**派生引用**、绝不含明文密钥。
//
// v20 的一条如实代价：导出物单独搬到另一台机器上**不够**（密钥不在文件里，也不在任何可搬运的
// 密文里），新机器上要重新填一次密钥。本用例断言的是"同一台机器上回灌即用"——那份值来自
// 本机的密钥面（`credentials.json`），不是来自这份文件。
func TestProviderExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc := NewService(publisherRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.planes.Credentials.SetLayered(newTestCredentials(t, home))
	svc.planes.Providers.LoadProviderDefinitionsFromStore(ctx, dir)
	for _, id := range []string{"p-b", "p-a"} {
		if _, err := svc.planes.Providers.CreateProvider(ctx, providerReq(id, ptr("sk-"+id))); err != nil {
			t.Fatalf("CreateProvider(%s): %v", id, err)
		}
	}
	// DryRun 不写盘。
	dry := filepath.Join(home, "dry")
	stats, err := provider.ExportProviderFile(ctx, st, dry, true, nil)
	if err != nil || stats.Written != 2 {
		t.Fatalf("dry-run 导出计数不符: %+v err=%v", stats, err)
	}
	if _, err := os.Stat(filepath.Join(dry, provider.ProvidersFileName)); !os.IsNotExist(err) {
		t.Fatal("--dry-run 不应写盘")
	}
	// 真导出两次：逐字节相同（确定性）。
	if _, err := provider.ExportProviderFile(ctx, st, dir, false, nil); err != nil {
		t.Fatalf("导出: %v", err)
	}
	first := readProvidersFile(t, dir)
	time.Sleep(10 * time.Millisecond)
	if _, err := provider.ExportProviderFile(ctx, st, dir, false, nil); err != nil {
		t.Fatalf("二次导出: %v", err)
	}
	if second := readProvidersFile(t, dir); second != first {
		t.Fatalf("导出不确定（两次不同）:\n--- 1 ---\n%s\n--- 2 ---\n%s", first, second)
	}
	var file map[string]map[string]any
	if err := json.Unmarshal([]byte(first), &file); err != nil {
		t.Fatalf("导出物不是合法 JSON: %v", err)
	}
	if len(file) != 2 || file["p-a"]["protocol"] != "openai" || file["p-a"]["key_ref"] != credential.ProviderRef("p-a") {
		t.Fatalf("导出物内容不符: %+v", file)
	}
	if strings.Contains(first, "sk-p-a") {
		t.Fatal("导出物不得含明文密钥")
	}
	// 回灌：同一台机器上（文件 + 本机密钥面）再装一次即用——与 A4 的"逐字段等价"反向闭合。
	svcMemory := providerTestSvc(t, dir)
	items, _ := svcMemory.Planes().Providers.ListProviders(context.Background())
	if len(items) != 2 {
		t.Fatalf("回灌后档案数不符: %+v", items)
	}
	if got := svcMemory.Planes().Providers.ProviderEnvFor(runtimetest.ID, "p-a"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_P_A_") || !strings.HasSuffix(got[0], "_API_KEY=sk-p-a") {
		t.Fatalf("回灌后注入不符: %v", got)
	}
}
