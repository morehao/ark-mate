package sessionruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/mechanism/definition"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— v13 的三条判据（设计 A2/A3/A4）在 v20 之下的形态 ———
//
// 它们必须**真连库**跑（`PRAGMA table_info` 动态取列、真实写面与导入路径），所以留在包内
// 而不是 archtest：archtest 的静态那条问的是"DDL 长什么样"，这里问的是"实际写下去的是什么"。
// v20 把 A4 那一条反转了（明文现在**有**落点：`providers.key`），所以它换的是判据而不是退休。

// TestProviderKeyHasExactlyOneColumnInStore v20 的明文落点纪律：**只有 `providers.key` 一列**
// （外加可重建的托管文档）。别的表、别的列都不许出现。
//
// 为什么这条仍然要留着（v13 的 A4 换了判据而不是退休）：v20 把密钥搬进库，等于把"库必须保持
// 可自由复制"这条裁决**反转**了（设计 §2 的代价），于是更需要一条机器判据钉住"搬进来的只有
// 那一列"——否则某天有人顺手把 key 抄进 `body`（或塞进 meta、审计行、事件载荷），外观上
// 什么都看不出来，而库的备份里会多出一份没人预期的明文。
//
// 判据是把**全库所有表的所有列值**扫一遍（列由 `PRAGMA table_info` 动态取），命中位置必须
// 恰好是 `providers.key` 一处；另外两条出口（写面回执、读面列表）也不许回显明文。
func TestProviderKeyHasExactlyOneColumnInStore(t *testing.T) {
	const secret = "sk-plaintext-probe-ONLY-KEY-COLUMN"
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	svc := NewService(v12ProviderRegistry(), nil)
	svc.EnableStore(st)
	svc.planes.Credentials.SetLayered(newTestCredentials(t, home))
	svc.planes.Providers.LoadProviderDefinitionsFromStore(ctx, dir)

	// ① 写面：带密钥新建 → 落进那一列，回执不回显明文。
	key := secret
	resp, err := svc.planes.Providers.CreateProvider(ctx, provider.CreateRequest{
		ID: "write-face", Protocol: "openai", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("带密钥新建应成功（v20 起密钥有落点了）: %v", err)
	}
	if !resp.HasKey || resp.KeyRef != "providers/write-face" {
		t.Fatalf("回执应是 has_key=true + 派生引用：%+v", resp)
	}
	if strings.Contains(fmt.Sprintf("%+v", resp), secret) {
		t.Fatal("写面回执不该回显明文")
	}
	if v, found, _ := st.ProviderKey(ctx, "write-face"); !found || v != secret {
		t.Fatalf("密钥应落在 providers.key 那一列: found=%v len=%d", found, len(v))
	}

	// ② 覆盖写：同一列被换成新值，旧值不留在任何地方。
	key2 := "sk-rotated-value"
	if _, err := svc.planes.Providers.UpdateProvider(ctx, "write-face", provider.PatchRequest{APIKey: &key2}); err != nil {
		t.Fatal(err)
	}
	if hits := secretHitsAnywhere(t, st, secret); len(hits) != 0 {
		t.Fatalf("覆盖写之后旧值不该留在任何列里: %v", hits)
	}

	// ③ 导入：档案里的历史明文进**同一列**（明文不再"跳过"，因为它有落点了）。
	legacy := `{"legacy":{"id":"legacy","type":"openai","key":"` + secret + `"}}`
	if err := os.WriteFile(filepath.Join(dir, provider.ProvidersFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ImportProviderFile(ctx, st, dir, definition.DefinitionImportOptions{Auto: true}, nil); err != nil {
		t.Fatalf("provider.ImportProviderFile: %v", err)
	}
	row, found, _ := st.GetProviderDefinition(ctx, "legacy")
	if !found {
		t.Fatal("档案的其余列应照常导入")
	}
	if row.Protocol != "openai" || row.BaseURL == "" {
		t.Fatalf("档案列值应照常导入: %+v", row)
	}
	if v, _, _ := st.ProviderKey(ctx, "legacy"); v != secret {
		t.Fatalf("导入的历史明文应进 key 列: %q", v)
	}
	// 装载面如实报"有密钥"。
	svc.planes.Providers.DeleteForTest("legacy")
	svc.planes.Providers.LoadProviderDefinitionsFromStore(ctx, dir)
	items, _ := svc.planes.Providers.ListProviders(ctx)
	for _, it := range items {
		if it.ID == "legacy" && !it.HasKey {
			t.Fatalf("库里那一列有值 → has_key 应为 true: %+v", it)
		}
	}
	// 读面列表也不得回显明文。
	if raw := fmt.Sprintf("%+v", items); strings.Contains(raw, secret) {
		t.Fatal("读面列表不该回显明文")
	}

	// ④ 判据：命中的位置**只能是**那一列（几条命中都可以，但一条都不许落在别处）。
	hits := secretHitsAnywhere(t, st, secret)
	if len(hits) == 0 {
		t.Fatal("库里该有这条明文（判据本身失效：探针没被写进去）")
	}
	for _, h := range hits {
		if h != "providers.key" {
			t.Fatalf("明文只许出现在 providers.key 一列，却出现在 %s: %v", h, hits)
		}
	}
}

// secretHitsAnywhere 扫全库所有表的所有列值，返回出现探针密钥的 `表.列` 位置（升序、可重复）。
//
// 列清单由 `PRAGMA table_info` 动态取（不写死列名）：新增一列不会让这条断言"少扫一列"。
func secretHitsAnywhere(t *testing.T, st *store.Store, secret string) []string {
	t.Helper()
	var tables []string
	if err := st.DB().Raw(`SELECT name FROM sqlite_master WHERE type='table'`).Scan(&tables).Error; err != nil {
		t.Fatalf("读表清单: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("扫不到任何表（守卫会静默变绿）")
	}
	hits := []string{}
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
					hits = append(hits, table+"."+cols[i])
				}
			}
		}
		_ = rows.Close()
	}
	if scanned == 0 {
		t.Fatal("一列都没扫到（守卫会静默变绿）")
	}
	sort.Strings(hits)
	return hits
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
	rows := []model.ModelColumns{
		{ID: "zzz", ProviderID: "p1", Model: "m-a", Name: "A", InputModalities: "text"},
		{ID: "mmm", ProviderID: "p1", Model: "m-b", Name: "B", InputModalities: "text"},
		{ID: "aaa", ProviderID: "p1", Model: "m-c", Name: "C", InputModalities: "text"},
		{ID: "yyy", ProviderID: "p2", Model: "m-0", Name: "0", InputModalities: "text"},
		{ID: "bbb", ProviderID: "p3", Model: "m-z", Name: "Z", InputModalities: "text"},
	}
	for _, row := range rows {
		if ok, err := r.svc.planes.Resources.Store().CreateModelDefinition(ctx, row, "rest"); err != nil || !ok {
			t.Fatalf("插入 %s: ok=%v err=%v", row.ID, ok, err)
		}
	}

	got, err := r.svc.planes.Providers.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"p1/m-a", "p1/m-b", "p1/m-c", "p2/m-0", "p3/m-z"}
	if len(got) != len(want) {
		t.Fatalf("条数不符: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if pair := got[i].ProviderID + "/" + got[i].Model; pair != want[i] {
			t.Fatalf("第 %d 条顺序不符：got %s，want %s（完整：%v）", i, pair, want[i], modelPairs(got))
		}
	}
	// 单调性（不依赖上面那张期望表也能表达同一条语义）。
	for i := 1; i < len(got); i++ {
		prev, cur := got[i-1], got[i]
		if prev.ProviderID > cur.ProviderID ||
			(prev.ProviderID == cur.ProviderID && prev.Model > cur.Model) {
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
		out = append(out, list[i].ProviderID+"/"+list[i].Model)
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
