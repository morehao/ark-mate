package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/golib/glog"
)

// ——— provider 档案读写面的**载体**（v11；v13 起 sqlite 侧是资源表） ———
//
// 与 agent 面（`agents_carrier.go`）同源的分派判据（`s.st != nil`）：
//
//	memory 模式：<AgentDir>/providers.json（typed marshal + 同目录 tmp + rename，逐字节同今天）
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

// providerLocator 档案的定位串（人类可读；与 agent 面的 `db:agent/<id>` 同一用途）。
func (s *Service) providerLocator(id string) string {
	if s.st != nil {
		return "db:provider/" + id
	}
	dir := s.agentDirSnapshot()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, providersFileName)
}

// loadProviders 按持久层模式装载 provider 档案。
//
// 必须在 agent 装载之前完成（`LoadAgentDefinitions*` 内部先调它）：agent 的 `provider` 引用
// 要能在装载期被解析，否则"启动时先看到一批悬空引用、下一轮才对"。
func (s *Service) loadProviders(ctx context.Context, dir string) {
	if s.st != nil {
		s.loadProvidersFromStore(ctx, dir)
		return
	}
	s.loadProvidersFile(dir)
}

// loadProvidersFromStore 从 `providers` 表装载（sqlite 模式）。
//
// v13 起**密钥的"有没有"完全由 vault 派生**（`vault.Has(providerRef(id))`）：
// 表里没有 `key_ref` 列（它是纯函数副本，ADR-7），所以"这一行有没有密文"这个问题
// 只能问 vault——这也正是 `has_key` 的语义（**内存里解出了明文才算有**；密文解不开时
// 也是 false，原因走 problems）。
//
// 装载期不再有"跳过坏行"的分支：行里没有 JSON 可比坏（设计 P2 的收尾）。列值异常
// （protocol 不在 CHECK 白名单之类）在库级约束上就已经不可能落库。
func (s *Service) loadProvidersFromStore(ctx context.Context, dir string) {
	rows, err := s.st.ListProviderDefinitions(ctx)
	if err != nil {
		glog.Warnw(ctx, "providers: 从持久层装载失败（档案面为空）", "err", err)
		return
	}
	st, verr := s.providerVault()
	loaded := make(map[string]*providerDef, len(rows))
	for i := range rows {
		row := rows[i]
		p := &providerDef{
			ID: row.ID, Name: row.Name, Protocol: row.Protocol, BaseURL: row.BaseURL, Note: row.Note,
			// KeyRef 是**派生量**（不再是载体字段）：装载时算一次，供诊断文案与 vault 读写复用。
			KeyRef:    providerRef(row.ID),
			Revision:  row.Revision,
			UpdatedAt: row.UpdatedAt,
		}
		switch {
		case st != nil:
			if st.Has(p.KeyRef) {
				pt, err := st.Get(p.KeyRef, vault.KindProviderKey)
				if err != nil {
					p.keyErr = err
					glog.Warnw(ctx, "providers: 密钥密文解密失败（该 provider 在修复前不可用）", "provider", row.ID, "err", err)
					break
				}
				p.Key = string(pt)
			}
		case verr != nil:
			// vault 不可达，而 v13 没有 `key_ref` 列可以告诉我们"这一行本来有没有密文"——
			// 取**保守**口径：报"密钥不可用（vault 不可达）"，绝不让 `has_key` 撒谎说没有密钥。
			p.keyErr = fmt.Errorf("vault 不可用，无法读取密钥密文: %v", vaultUnavailable(verr))
			glog.Warnw(ctx, "providers: vault 不可用（该实例的密钥在修复前不可用）", "provider", row.ID, "err", p.keyErr)
		default:
			// 没有 vault 也没有报错（未配置 mate home）：密钥不可能存在，按"尚未设置"报。
		}
		loaded[row.ID] = p
	}
	s.mu.Lock()
	s.providers = loaded
	s.mu.Unlock()
	_ = dir // 目录只在文件载体里参与拼路径；sqlite 模式下它仍被 agent 面用作 vault 锚点
}

// providerColumnsOf 把内存档案投影成 store 的列载荷（sqlite 载体的提交物）。
func providerColumnsOf(d *providerDef) store.ProviderColumns {
	return store.ProviderColumns{
		ID: d.ID, Name: d.Name, Protocol: d.Protocol, BaseURL: d.BaseURL, Note: d.Note,
	}
}

// persistProviderDefinition 提交一条档案（新建或覆盖）。
//
// sqlite 模式走行 CAS：`Revision == 0` 是新建，否则仅当行仍等于该版本。
// 返回 `definition_changed`(40901) 而不是静默覆盖——与其余四个定义面逐字同形。
func (s *Service) persistProviderDefinition(ctx context.Context, d *providerDef) error {
	if s.st == nil {
		// 文件载体：写"内存现状 + 这一条"（提交时它还没进内存，见 persistProvidersFileWith 的注释）。
		return s.persistProvidersFileWith(d)
	}
	expected := d.Revision
	ok, err := s.st.UpsertProviderDefinition(ctx, providerColumnsOf(d), expected, definitionWriteActor)
	if err != nil {
		return err
	}
	if !ok {
		return definitionChangedErr(s.providerLocator(d.ID))
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
func (s *Service) removeProviderDefinition(ctx context.Context, id string, expectedRev int64) (bool, []string, error) {
	if s.st == nil {
		return true, nil, s.persistProvidersFile()
	}
	return s.st.DeleteProviderDefinitionIfUnreferenced(ctx, id, expectedRev, definitionWriteActor)
}

// providerFileExists `<AgentDir>/providers.json` 是否存在（doctor 与迁移提示用）。
func providerFileExists(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, providersFileName))
	return err == nil
}

// bodyString 从已解析的 JSON 对象里取一个字符串键（缺失/类型不符 → 空串）。
//
// v13 之后它**只服务文件载体与导入路径**（`providers.json` 的条目仍是自由 JSON）；
// DB 行是列，不经过这里。
func bodyString(body map[string]any, key string) string {
	v, ok := body[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
