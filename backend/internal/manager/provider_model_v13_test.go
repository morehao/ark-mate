package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/vault"
)

// ——— v13 的三条新判据（设计 A2/A3/A4） ———
//
// 它们必须**真连库**跑（`PRAGMA table_info` 动态取列、真实写面与导入路径），所以留在包内
// 而不是 archtest：archtest 的静态那条问的是"DDL 长什么样"，这里问的是"实际写下去的是什么"。

// TestProviderRowsContainNoPlaintextSecret A4：vault 不可用时，**写面与导入两条路径都不把明文
// 写进任何一列**。
//
// v13 之前的形态是"宁可多留一会明文"：写面在 vault 不可用时把 key 写进 `body`，导入路径同理。
// 那条纪律属于**文件载体**（文件本来就在用户的 home 里），而 DB 被 ADR-15 判为"必须保持可自由
// 复制"，于是同一句话在 DB 上变成了安全债（P5）。v13 的修复是结构性的：
//
//	写面：拒绝写入（`vault_unavailable` + 出路）—— 目标本来就是"把密钥存下来"，而 v13 没有能存它的地方；
//	导入：跳过那条密钥并逐条列入报告 —— 档案其余列照常导入。
//
// 判据不是"实现调了哪个分支"，而是**把全库所有表的所有列值都扫一遍**：探针密钥一次都不许出现。
// 列由 `PRAGMA table_info` 动态取——所以将来加列也不会让这条断言悄悄少扫一列。
func TestProviderRowsContainNoPlaintextSecret(t *testing.T) {
	const secret = "sk-plaintext-probe-DO-NOT-PERSIST"
	// 让 vault.Open 必然失败（`MATE_VAULT_KEY` 非法）：这是"vault 不可用"最可控的造法，
	// 且不需要碰任何真实目录（离线的 `vault.Open("")` 反而会回落到"没有 home"的另一条分支）。
	t.Setenv(vault.EnvKey, "not-a-valid-key")

	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	svc := NewService(v12ProviderRegistry(), nil)
	svc.EnableStore(st)
	svc.LoadAgentDefinitionsFromStore(ctx, dir)

	// ① 写面（新建）：带密钥 → 拒绝，且**一行都不落**。
	key := secret
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
		ID: "write-face", Protocol: "openai", APIKey: &key,
	}); errCode(err) != "vault_unavailable" {
		t.Fatalf("vault 不可用时写面应 vault_unavailable，got %q（%v）", errCode(err), err)
	}
	if _, found, _ := st.GetProviderDefinition(ctx, "write-face"); found {
		t.Fatal("被拒的写入不该落行")
	}

	// ② 写面（覆盖）：先建一条无密钥的，再补密钥 → 同样被拒。
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "patch-face", Protocol: "openai"}); err != nil {
		t.Fatalf("无密钥建档案应成功: %v", err)
	}
	if _, err := svc.UpdateProvider(ctx, "patch-face", dtoprovider.ProviderPatchReq{APIKey: &key}); errCode(err) != "vault_unavailable" {
		t.Fatalf("vault 不可用时补密钥应 vault_unavailable，got %q（%v）", errCode(err), err)
	}

	// ③ 导入：文件里带明文 → 跳过该密钥并**逐条列入报告**（明文不进任何列）。
	legacy := `{"legacy":{"id":"legacy","type":"openai","key":"` + secret + `"}}`
	if err := os.WriteFile(filepath.Join(dir, providersFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	stats, err := ImportProviderFile(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil)
	if err != nil {
		t.Fatalf("ImportProviderFile: %v", err)
	}
	if len(stats.SkippedKeys) != 1 || !strings.Contains(stats.SkippedKeys[0], "legacy") {
		t.Fatalf("被跳过的密钥应逐条列出并点名: %+v", stats.SkippedKeys)
	}
	row, found, _ := st.GetProviderDefinition(ctx, "legacy")
	if !found {
		t.Fatal("档案的其余列照常导入（跳过的是密钥，不是整条档案）")
	}
	if row.Protocol != "openai" || row.BaseURL == "" {
		t.Fatalf("档案列值应照常导入: %+v", row)
	}
	// 装载面也不该"假装有密钥"。
	svc.mu.Lock()
	delete(svc.providers, "legacy") // 重新装载一次，确认 has_key 的判据是 vault 派生
	svc.mu.Unlock()
	svc.LoadAgentDefinitionsFromStore(ctx, dir)
	items, _ := svc.ListProviders(ctx)
	for _, it := range items {
		if it.ID == "legacy" && it.HasKey {
			t.Fatalf("vault 里没有密文时 has_key 必须为 false: %+v", it)
		}
	}

	// ④ 判据：全库（每一张表的每一列）都搜不到明文。
	assertNoSecretAnywhere(t, st, secret)
}

// assertNoSecretAnywhere 扫全库所有表的所有列值，断言探针密钥一次都不出现。
//
// 列清单由 `PRAGMA table_info` 动态取（不写死列名）：新增一列不会让这条断言"少扫一列"。
func assertNoSecretAnywhere(t *testing.T, st *store.Store, secret string) {
	t.Helper()
	var tables []string
	if err := st.DB().Raw(`SELECT name FROM sqlite_master WHERE type='table'`).Scan(&tables).Error; err != nil {
		t.Fatalf("读表清单: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("扫不到任何表（守卫会静默变绿）")
	}
	scanned := 0
	for _, table := range tables {
		rows, err := st.DB().Raw(`SELECT * FROM ` + table).Rows()
		if err != nil {
			t.Fatalf("读 %s: %v", table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatalf("%s 取列: %v", table, err)
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				t.Fatalf("%s 扫行: %v", table, err)
			}
			for i, v := range vals {
				scanned++
				var text string
				switch tv := v.(type) {
				case nil:
					continue
				case []byte:
					text = string(tv)
				case string:
					text = tv
				default:
					text = fmt.Sprintf("%v", tv)
				}
				if strings.Contains(text, secret) {
					_ = rows.Close()
					t.Fatalf("明文密钥出现在 %s.%s 里：%q——v13 起它结构上不该有任何落点（ADR-8）",
						table, cols[i], text)
				}
			}
		}
		_ = rows.Close()
	}
	if scanned == 0 {
		t.Fatal("一列都没扫到（守卫会静默变绿）")
	}
}

// TestModelListOrderIsProviderThenModelID A2：`GET /models` 的顺序是 `(provider_id, model_id)` 升序。
//
// 这条修的是 v12 的用户可见缺陷 P1：列表顺序是**平台 UUID 的字典序**——用户每加一条模型，
// 它出现在列表的任意位置。断言刻意让"平台 id 序"与"应有顺序"**相反**：
// 若有人退回按 `id` 排序（或把 SQL 的 ORDER BY 去掉），这里必红。
func TestModelListOrderIsProviderThenModelID(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)

	// 三个供应商（id 升序 p1 < p2 < p3），每个下面按**平台 id 逆序**插入若干条。
	rows := []store.ModelColumns{
		{ID: "zzz", ProviderID: "p1", ModelID: "m-a", Name: "A", InputModalities: "text"},
		{ID: "mmm", ProviderID: "p1", ModelID: "m-b", Name: "B", InputModalities: "text"},
		{ID: "aaa", ProviderID: "p1", ModelID: "m-c", Name: "C", InputModalities: "text"},
		{ID: "yyy", ProviderID: "p2", ModelID: "m-0", Name: "0", InputModalities: "text"},
		{ID: "bbb", ProviderID: "p3", ModelID: "m-z", Name: "Z", InputModalities: "text"},
	}
	for _, row := range rows {
		if ok, err := r.svc.st.CreateModelDefinition(ctx, row, "rest"); err != nil || !ok {
			t.Fatalf("插入 %s: ok=%v err=%v", row.ID, ok, err)
		}
	}

	got, err := r.svc.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"p1/m-a", "p1/m-b", "p1/m-c", "p2/m-0", "p3/m-z"}
	if len(got) != len(want) {
		t.Fatalf("条数不符: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if pair := got[i].ProviderID + "/" + got[i].ModelID; pair != want[i] {
			t.Fatalf("第 %d 条顺序不符：got %s，want %s（完整：%v）", i, pair, want[i], modelPairs(got))
		}
	}
	// 单调性（不依赖上面那张期望表也能表达同一条语义）。
	for i := 1; i < len(got); i++ {
		prev, cur := got[i-1], got[i]
		if prev.ProviderID > cur.ProviderID ||
			(prev.ProviderID == cur.ProviderID && prev.ModelID > cur.ModelID) {
			t.Fatalf("顺序不是 (provider_id, model_id) 升序：%v", modelPairs(got))
		}
	}
	// 平台 id 序**不是**这个顺序（否则这条用例是空转）。
	if got[0].ID == "aaa" {
		t.Fatalf("实现退回成按平台 id 排序了（%v）", modelPairs(got))
	}
}

// modelPairs 把清单折成 `provider/model` 串（断言失败时的可读输出）。
func modelPairs(list []dtomodel.ModelResp) []string {
	out := make([]string, 0, len(list))
	for i := range list {
		out = append(out, list[i].ProviderID+"/"+list[i].ModelID)
	}
	return out
}

// TestMigrationNotesV13Reader v13 迁移笔记的读面（键名、结构、缺失时 (nil,false,nil)）。
//
// 与 v12 的笔记**分键**是刻意的：v13 覆盖 `migration_notes` 会把 v12 那些尚未处置的事实
// 静默抹掉（`MetaKeyMigrationNotesV13` 的注释）。
func TestMigrationNotesV13Reader(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))

	// 空值（清键）→ (nil,false,nil)：读面不把"空串"当成一份笔记。
	if err := st.SetMeta(ctx, model.MetaKeyMigrationNotesV13, ""); err != nil {
		t.Fatal(err)
	}
	if notes, ok, err := st.MigrationNotesV13(ctx); err != nil || ok || notes != nil {
		t.Fatalf("空值时应 (nil,false,nil): notes=%+v ok=%v err=%v", notes, ok, err)
	}
	// **分键**：先往 v12 的键里写一份哨兵，再写 v13 的笔记——两者互不影响。
	if err := st.SetMeta(ctx, model.MetaKeyMigrationNotes, `{"backup_path":"/tmp/v12-sentinel"}`); err != nil {
		t.Fatal(err)
	}
	raw := `{"backup_path":"/tmp/mate.db.bak-v13-x","legacy_plaintext_keys":["p1"],` +
		`"modality_defaulted":["p1/m1"],"orphan_models":["m9（gone/m9）"]}`
	if err := st.SetMeta(ctx, model.MetaKeyMigrationNotesV13, raw); err != nil {
		t.Fatal(err)
	}
	notes, ok, err := st.MigrationNotesV13(ctx)
	if err != nil || !ok {
		t.Fatalf("读 v13 笔记: ok=%v err=%v", ok, err)
	}
	if notes.BackupPath != "/tmp/mate.db.bak-v13-x" || len(notes.LegacyPlaintextKeys) != 1 ||
		notes.LegacyPlaintextKeys[0] != "p1" || len(notes.ModalityDefaulted) != 1 ||
		len(notes.OrphanModels) != 1 {
		t.Fatalf("笔记内容失真: %+v", notes)
	}
	if v12, ok, _ := st.MigrationNotesV12(ctx); !ok || v12 == nil || v12.BackupPath != "/tmp/v12-sentinel" {
		t.Fatalf("v13 笔记不该动 v12 的键（分键的理由就是这条）: %+v", v12)
	}
}
