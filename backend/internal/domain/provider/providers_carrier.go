package provider

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/morehao/ark-mate/internal/platform/credential"
	defn "github.com/morehao/ark-mate/internal/platform/definition"
	provrow "github.com/morehao/ark-mate/internal/domain/provider/row"

	"github.com/morehao/ark-mate/internal/platform/logging"
	"github.com/morehao/golib/glog"
)

// ——— provider 档案读写面的**载体**（v11；v13 起 sqlite 侧是资源表） ———
//
// 分派判据是 `pl.st != nil`：
//
//	memory 模式：<ProviderDir>/providers.json（typed marshal + 同目录 tmp + rename，逐字节同今天）
//	sqlite 模式：providers 表（事务 + 行 revision CAS + 同事务一条审计）
//
// 两条路对外承诺相同：未知档案 → `provider_not_found`；并发改同一条 → `definition_changed`(40901)；
// **失败即无副作用**（内存与载体不会各改一半）。
//
// v13 把 sqlite 那侧从「定义载体」（body 唯一权威、未知键原样保留）改成「资源表」（平台字段成列）：
// 于是"未知键保留"这件事整体消失——键集合是封闭的，"写了、存了、什么都不发生"的字段
// 结构上进不来（设计 P7）。代价是不再有兼容层：列化后旧代码读不到 body，新代码写不出 body，
// 所以本批必须同批合并（设计「既有行为变化」B2）。
//
// 与文件载体的第三处差别是**方向性的**：sqlite 模式下 `providers.json` 降级为导入/导出载体，
// 写面**一个字节都不回写它**（B4）——"文件与 DB 谁是权威"这个问题因此在这条路上不存在。

// ProviderLocator 档案的定位串（人类可读）。
func (pl *Plane) ProviderLocator(id string) string {
	if pl.st != nil {
		return "db:provider/" + id
	}
	dir := pl.ProviderDirSnapshot()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ProvidersFileName)
}

// ——— provider 面的装载入口（v19：agent 面删除后，这里是定义装载的公开面） ———
//
// 为什么仍然保留"目录"这个入参：memory 模式下 `providers.json` 就在它下面，sqlite 模式下它
// 是导入/导出载体（`mate provider import|export`）与 doctor 的读取对象。**v20 起它不再是任何
// 密钥的锚点**：托管文档锚在 `MATE_HOME`（`<MATE_HOME>/credentials.json`），密钥本体在库里。

// ProviderDirSnapshot provider 定义目录的快照（空 = 未启用 definitions 目录）。
func (pl *Plane) ProviderDirSnapshot() string {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.providerDir
}

// SetProviderDir 登记 provider 定义目录（幂等；空串 = 不落盘，仅内存）。
//
// 它同时是 `providers.json` 的落点与 `ProviderLocator` 的取值来源，所以装配期**先行**登记一次
// （顺序敏感，2026-10-11 修）：装配期的 runtime 同步（`EnableStore` → `SyncRuntime` →
// `providerForSync`）发生在定义装载**之前**，那一刻若 locator 还是空的，报错文案就会指向一个
// 不存在的路径。
//
// **v20 起这条顺序不再是"密钥能不能读到"的前提**：密钥面在库（`providers.key`）与
// `<MATE_HOME>/credentials.json`，两者都不依赖这个目录；sqlite 模式下 `providerForSync`
// 的回落路径直接从行上的密钥列取值，所以"锚点还不知道"这一类错误结论结构上消失了
// （v13–v19 的旧机制句柄缓存曾把一句错结论钉死到整个进程，见 `docs/decisions.md` 决策 v20）。
func (pl *Plane) SetProviderDir(dir string) {
	pl.mu.Lock()
	pl.providerDir = dir
	pl.mu.Unlock()
}

// LoadProviderDefinitions 按**文件载体**装载 provider 档案（memory 模式；返回装载条数）。
//
// sqlite 模式走 `LoadProviderDefinitionsFromStore`（另一条路的差别只有"从哪读"）。
func (pl *Plane) LoadProviderDefinitions(dir string) int {
	pl.SetProviderDir(dir)
	if dir == "" {
		return 0
	}
	pl.loadProviders(context.Background(), dir)
	pl.mu.Lock()
	n := len(pl.providers)
	pl.mu.Unlock()
	if n > 0 {
		glog.Infow(context.Background(), "providers: 档案已加载", "count", n, "dir", dir)
	}
	return n
}

// LoadProviderDefinitionsFromStore 从持久层装载 provider 档案（sqlite 模式）。
func (pl *Plane) LoadProviderDefinitionsFromStore(ctx context.Context, dir string) int {
	return pl.loadProviderDefinitionsFromStore(ctx, dir, glog.InfoLevel)
}

// ReloadProviderDefinitionsFromStore 重新装载 provider 档案（**网关每次取数前**调用）。
//
// 与 `LoadProviderDefinitionsFromStore` 是同一份实现，只有汇总行的日志档位不同（理由同
// `ReloadCapabilityDefinitions`：网关是个长命进程，每次 tools/list/call 都会重装）。
func (pl *Plane) ReloadProviderDefinitionsFromStore(ctx context.Context, dir string) int {
	return pl.loadProviderDefinitionsFromStore(ctx, dir, glog.DebugLevel)
}

// loadProviderDefinitionsFromStore 装载内核（公开入口与网关共用；level 只作用于汇总行）。
func (pl *Plane) loadProviderDefinitionsFromStore(ctx context.Context, dir string, level glog.Level) int {
	pl.SetProviderDir(dir)
	if pl.st == nil {
		return pl.LoadProviderDefinitions(dir)
	}
	pl.loadProviders(ctx, dir)
	pl.mu.Lock()
	n := len(pl.providers)
	pl.mu.Unlock()
	if n > 0 {
		logging.At(ctx, level, "providers: 档案已从持久层装载", "count", n)
	}
	return n
}

// loadProviders 按持久层模式装载 provider 档案。
//
// 顺序要求：它必须发生在**任何读 provider 的装配步骤之前**（runtime 配置同步、模型清单
// 装载、诊断面），否则那些步骤会按"本机没有 provider"得出错结论并把它写进 meta。
func (pl *Plane) loadProviders(ctx context.Context, dir string) {
	if pl.st != nil {
		pl.loadProvidersFromStore(ctx, dir)
		return
	}
	pl.loadProvidersFile(dir)
}

// loadProvidersFromStore 从 `providers` 表装载（sqlite 模式）。
//
// v20 起"有没有密钥"的权威是**行上的 `key` 列**（非空即有），但读出来的生效值仍走
// `KeyFor`（四层回落）——于是"进程环境里有同名变量"这类事实在列表上就看得见
// （`key_source`），而不是等到某次请求 401 才被发现。
//
// 装载期不再有"跳过坏行"的分支：行里没有 JSON 可比坏（设计 P2 的收尾）。列值异常
// （protocol 不在 CHECK 白名单之类）在库级约束上就已经不可能落库。
func (pl *Plane) loadProvidersFromStore(ctx context.Context, dir string) {
	rows, err := pl.st.ListProviderDefinitions(ctx)
	if err != nil {
		glog.Warnw(ctx, "providers: 从持久层装载失败（档案面为空）", "err", err)
		return
	}
	loaded := make(map[string]*Def, len(rows))
	for i := range rows {
		row := rows[i]
		p := &Def{
			ID: row.ID, Name: row.Name, Protocol: row.Protocol, BaseURL: row.BaseURL, Note: row.Note,
			// KeyRef 是**派生量**（不再是载体字段）：装载时算一次，供托管文档读写与诊断文案复用。
			KeyRef:    credential.ProviderRef(row.ID),
			Revision:  row.Revision,
			UpdatedAt: row.UpdatedAt,
		}
		p.applyResolution(pl.KeyFor(ctx, p))
		loaded[row.ID] = p
	}
	pl.mu.Lock()
	pl.providers = loaded
	pl.mu.Unlock()
	_ = dir // 目录只在文件载体里参与拼路径；sqlite 模式下它仍被 locator 与导入/导出面用
}

// providerColumnsOf 把内存档案投影成 store 的列载荷（sqlite 载体的提交物）。
func providerColumnsOf(d *Def) provrow.Columns {
	return provrow.Columns{
		ID: d.ID, Name: d.Name, Protocol: d.Protocol, BaseURL: d.BaseURL, Note: d.Note,
	}
}

// persistProviderDefinition 提交一条档案（新建或覆盖）。
//
// sqlite 模式走行 CAS：`Revision == 0` 是新建，否则仅当行仍等于该版本。
// 返回 `definition_changed`(40901) 而不是静默覆盖——与其余四个定义面逐字同形。
func (pl *Plane) persistProviderDefinition(ctx context.Context, d *Def) error {
	if pl.st == nil {
		// 文件载体：写"内存现状 + 这一条"（提交时它还没进内存，见 persistProvidersFileWith 的注释）。
		return pl.persistProvidersFileWith(d)
	}
	expected := d.Revision
	ok, err := pl.st.UpsertProviderDefinition(ctx, providerColumnsOf(d), expected, defn.DefinitionWriteActor)
	if err != nil {
		return err
	}
	if !ok {
		return defn.DefinitionChangedErr(pl.ProviderLocator(d.ID))
	}
	d.Revision = expected + 1
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// removeProviderDefinition 删除载体里的一条档案。
//
// 返回 `(deleted, refModelIDs, err)`：
//   - sqlite：**引用检查与删行在同一事务**（`DeleteProviderDefinitionIfUnreferenced`）——
//     v12 是"先查引用再删行"的两次独立调用，中间有窗口会让并发「删供应商」+「给它加模型」
//     留下孤儿行（设计 P3）。所以删除判据不再由 manager 做。
//   - memory：文件里没有"引用"可查（模型清单只在 sqlite 有），调用方已先摘内存，
//     所以这里只把结果落盘一次，不再回内存里判"有没有"——那样会因为已经摘掉而误报 false。
func (pl *Plane) removeProviderDefinition(ctx context.Context, id string, expectedRev int64) (bool, []string, error) {
	if pl.st == nil {
		return true, nil, pl.persistProvidersFile()
	}
	return pl.st.DeleteProviderDefinitionIfUnreferenced(ctx, id, expectedRev, defn.DefinitionWriteActor)
}

// providerFileExists `<ProviderDir>/providers.json` 是否存在（doctor 与迁移提示用）。
func providerFileExists(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ProvidersFileName))
	return err == nil
}
