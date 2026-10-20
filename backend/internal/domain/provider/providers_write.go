package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/credential"

	"github.com/morehao/ark-mate/internal/core/object/objdelivery"
	"github.com/morehao/ark-mate/internal/core/resource"
	provrow "github.com/morehao/ark-mate/internal/domain/provider/row"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/golib/glog"
)

// ——— 供应商的**写面**（v12 形状，v13 列化，v20 换密钥面） ———
//
// 写面顺序是固定的，且不是实现细节而是**语义**：
//
//	① 校验（id 词法、协议、端点）
//	② 库（sqlite = 行 CAS + 审计 + `providers.key` 列；memory = 原子替换 providers.json
//	   + 托管文档）
//	③ 更新内存
//	④ 托管镜像双写（**失败只 WARN**，绝不因此回滚一次已提交的库写入）
//
// v20 删掉了整条**两阶段写 + 补偿**：v13–v19 里密钥在盘上（文件 + AES-GCM）、档案在 SQLite，
// 两者无法同事务，于是写密钥要先落密文、载体失败再补偿删掉它——"补偿"本身就是承认这段窗口
// 存在，而它在崩溃/断电时照样留下孤儿密文。v20 把密钥搬进**同一行**（`providers.key`），
// 一笔 UPDATE 就是全部事实；镜像由库重建，所以它不需要参与任何一致性论证。
//
// 三条结构判据：
//   - `has_key` 只由 `providers.key` 是否非空回答（读面则是"内存里有没有生效的明文"，
//     它同样来自那一列或托管文档的解析结果）；
//   - `key_ref` 是**派生量**（托管文档里的键 `providers/<id>`），没有对应列；
//   - 密钥写入缺 store 侧的独立审计出口（`SetProviderKey` 只 bump revision），见汇报。

// CreateProvider 新建一条供应商（POST /providers）。
func (pl *Plane) CreateProvider(ctx context.Context, req CreateRequest) (*ProviderItem, error) {
	if err := ValidateProviderID(req.ID); err != nil {
		return nil, err
	}
	protocol, err := normalizeProtocol(req.Protocol)
	if err != nil {
		return nil, err
	}
	// ① 已存在？——先看内存（快路径），再让载体自己兜并发（CAS 未命中 → provider_exists）。
	pl.mu.Lock()
	_, exists := pl.providers[req.ID]
	pl.mu.Unlock()
	if exists {
		return nil, providerExistsErr(req.ID)
	}

	d := &Def{ID: req.ID, Protocol: protocol, KeyRef: credential.ProviderRef(req.ID)}
	if req.Name != nil {
		d.Name = *req.Name
	}
	if req.BaseURL != nil {
		d.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Note != nil {
		d.Note = *req.Note
	}

	// ② 载体：新建只 INSERT 行（内容列），密钥随后的 `SetProviderKey` 落在同一行上。
	if err := pl.persistProviderDefinition(ctx, d); err != nil {
		if isDuplicateDefinition(err) {
			return nil, providerExistsErr(req.ID)
		}
		return nil, err
	}

	// ②b 密钥（与行同库同表；失败即整笔失败——没有第二处要清理）。
	if req.APIKey != nil && *req.APIKey != "" {
		if err := pl.writeProviderKey(ctx, d, *req.APIKey); err != nil {
			return nil, err
		}
	}

	// ③ 内存。
	pl.mu.Lock()
	pl.providers[d.ID] = d
	resp := pl.providerRespLocked(ctx, d.ID, d)
	pl.mu.Unlock()
	if pl.effect != nil {
		resp.Effect = pl.effect(d.ID)
	}
	return &resp, nil
}

// UpdateProvider 覆盖一条已存在的供应商（**只更新，不再 upsert**；不存在 → `provider_not_found`）。
//
// api_key 语义：`nil` = 不动；`""` = 清除；非空 = 覆盖：
//
//   - 写：`providers.key` 一列，一笔 UPDATE；写成功即整笔成功（没有第二处副本要同步）。
//   - 清：同一列写空串。**内存与镜像都必须跟着变**——留在托管文档里的那条会在下一次解析时
//     被"库是权威"的判据否掉（见 `KeyFor`），但把它删干净才是收尾。
func (pl *Plane) UpdateProvider(ctx context.Context, id string, patch PatchRequest) (*ProviderItem, error) {
	if err := ValidateProviderID(id); err != nil {
		return nil, err
	}

	// ① 读窗口：取旧值快照（**只读**，不改内存）。
	pl.mu.Lock()
	old, exists := pl.providers[id]
	var snap Def
	if exists {
		snap = *old
	}
	pl.mu.Unlock()
	if !exists {
		return nil, acp.NewError(acp.ClassRequest, "provider_not_found",
			fmt.Sprintf("供应商 %q 不存在：PATCH 只更新、不再顺带新建（要新建用 POST /providers）", id), nil)
	}

	// 目标状态：只改 patch 里出现过的键。
	next := snap
	contentChanged := false
	if patch.Protocol != nil {
		protocol, err := normalizeProtocol(*patch.Protocol)
		if err != nil {
			return nil, err
		}
		if next.Protocol != protocol {
			contentChanged = true
		}
		next.Protocol = protocol
	}
	if patch.BaseURL != nil {
		v := strings.TrimSpace(*patch.BaseURL)
		if next.BaseURL != v {
			contentChanged = true
		}
		next.BaseURL = v
	}
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.Note != nil {
		next.Note = *patch.Note
	}

	// ② 内容列：只有真的改了内容字段才提交（否则会写一条"内容更新"的审计，而内容没变）。
	if contentChanged {
		if err := pl.persistProviderDefinition(ctx, &next); err != nil {
			return nil, err
		}
	}

	// ②b 密钥列：nil = 不动；"" = 清；非空 = 覆盖。
	secretChanged := false
	if patch.APIKey != nil {
		if err := pl.writeProviderKey(ctx, &next, *patch.APIKey); err != nil {
			return nil, err
		}
		secretChanged = true
	}

	// ③ 内存。
	pl.mu.Lock()
	pl.providers[id] = &next
	resp := pl.providerRespLocked(ctx, id, &next)
	pl.mu.Unlock()

	// 生效面回执：只有驱动行为的字段变了才说"下一轮生效"；只改 name/note 就如实回 none。
	if !secretChanged && !contentChanged {
		resp.Effect = &ProviderEffect{
			Channel: dialEnvChannel(), Effect: "none",
			Note: "只改了不驱动行为的字段（协议/端点/密钥都未变）：本轮与后续轮次的行为都不变",
		}
		return &resp, nil
	}
	if pl.effect != nil {
		resp.Effect = pl.effect(id)
	}
	return &resp, nil
}

// writeProviderKey 写/清一条供应商的密钥（**唯一**的密钥写入点，sqlite 与 memory 两条载体都在这里）。
//
// 顺序：库 → 内存 → 镜像。**镜像写失败不回滚库、也不让这笔写失败**（它是可重建副本，
// 为它回滚一次已提交的库写入是本末倒置）；失败进 WARN + 这条回执的 `problems`，
// 并由 `mate doctor` 的镜像对账段列出来。
func (pl *Plane) writeProviderKey(ctx context.Context, d *Def, key string) error {
	key = strings.TrimSpace(key)
	if pl.st == nil {
		// memory 载体：托管文档就是事实源，写不进去就是**真的没写进去**（拒绝，不假装成功）。
		// 文档都没装配（数据目录没解析出来）时必须在这里拒绝：否则这条密钥只活在内存里，
		// 面板回 `has_key=true`、重启后消失——那是本仓最忌讳的一类静默谎报。
		if !pl.hasCredentialDoc() {
			return acp.NewError(acp.ClassRequest, "credentials_write_failed",
				"没有可写的凭据落点（托管文档未装配）：memory 模式下 "+credential.CredentialsFileName+
					"就是密钥的事实源，这里拒绝一次只在内存里成功的写入", nil)
		}
		if key == "" {
			if err := pl.mirrorDelete(d.KeyRef); err != nil {
				return acp.NewError(acp.ClassRequest, "credentials_write_failed",
					"清除密钥失败（托管文档 "+credential.CredentialsFileName+" 写不动）: "+err.Error(), nil)
			}
		} else if err := pl.mirrorSet(d.KeyRef, key); err != nil {
			return acp.NewError(acp.ClassRequest, "credentials_write_failed",
				"保存密钥失败（托管文档 "+credential.CredentialsFileName+" 写不动）: "+err.Error(), nil)
		}
		d.Key, d.KeySource, d.KeyEnvKey, d.KeyProblems = key, "", "", nil
		return nil
	}
	written, err := pl.st.SetProviderKey(ctx, d.ID, key)
	if err != nil {
		return acp.NewError(acp.ClassRequest, "credentials_write_failed",
			"密钥写入失败: "+err.Error(), nil)
	}
	if !written {
		return acp.NewError(acp.ClassRequest, "provider_not_found",
			fmt.Sprintf("供应商 %q 不存在（写密钥时行已不在）：刷新后重试", d.ID), nil)
	}
	// 密钥写入会 bump 行版本（"生效时刻"要可解释）：把最新版本读回来，别让内存里的 revision 落后。
	if row, found, rerr := pl.st.GetProviderDefinition(ctx, d.ID); rerr == nil && found {
		d.Revision, d.UpdatedAt = row.Revision, row.UpdatedAt
	}
	d.Key, d.KeySource, d.KeyEnvKey, d.KeyProblems = key, "", "", nil
	// ④ 镜像双写（失败只 WARN + 进 problems）。
	if key == "" {
		if err := pl.mirrorDelete(d.KeyRef); err != nil {
			d.KeyProblems = []string{"库里的密钥已清除，但托管文档里那一份删不掉：" + err.Error() +
				"；镜像可由库重建，`mate doctor` 会列出这条差异"}
		}
		return nil
	}
	if err := pl.mirrorSet(d.KeyRef, key); err != nil {
		d.KeyProblems = []string{"库里的密钥已写成功，但托管文档没跟上：" + err.Error() +
			"；镜像可由库重建，`mate doctor` 会列出这条差异"}
	}
	return nil
}

// DeleteProvider 删除一条供应商（幂等；被引用 → `provider_in_use`(40901) + 引用名单）。
//
// 顺序刻意与写面**相反**：**先删行 + 审计 → 再摘内存 → 最后删镜像条目**。
// 理由：反过来会留下一个"行在、密钥没了"的档案，那会让 `has_key` 撒谎；而"行已删、镜像条目
// 残留"是一个**如实可报**的状态（响应里说明，doctor 的镜像对账段列出）。
//
// **引用判据在 store 的同一事务里**（v13）：v12 是"manager 先查引用名单、再单独删行"，
// 中间有窗口——并发「删供应商」+「给它加模型」会留下孤儿 model 行（P3）。
// manager 现在只翻错误码（`provider_in_use`），文案与引用名单形状不变。
func (pl *Plane) DeleteProvider(ctx context.Context, id string) (*ProviderDelete, error) {
	if err := ValidateProviderID(id); err != nil {
		return nil, err
	}

	// v19 起引用面只有"模型条目"（agent 面撤销）：判据在 store 的同一事务里（见下），
	// 这里只保留"内存里到底有没有这一条"这个幂等判据。
	pl.mu.Lock()
	_, exists := pl.providers[id]
	pl.mu.Unlock()
	if !exists {
		// 幂等：不存在（或本来就没这一条）→ deleted:false 且 code=0。
		return &ProviderDelete{Deleted: false}, nil
	}
	// 文件载体没有事务，引用判据只能先读一次（sqlite 载体在同一事务里判，见下）。
	// 两种载体的对外承诺相同：被引用 → `provider_in_use` + 名单，绝不级联清空。
	if pl.st == nil {
		if refs := pl.ProviderModelRefsReadOnly(ctx, id); len(refs) > 0 {
			return nil, acp.NewError(acp.ClassLimit, "provider_in_use",
				providerInUseMsg(id, refs), nil)
		}
	}

	pl.mu.Lock()
	d := pl.providers[id]
	pl.mu.Unlock()

	if pl.st == nil {
		// **文件载体**：先摘内存再落盘——`persistProvidersFile` 序列化的是"内存现状"，
		// 顺序反了就会把刚删掉的那条原样写回文件（一个只在 memory 模式出现、且完全静默的失败：
		// 界面上没了、文件里还有，重启后它又回来了）。落盘失败则把内存放回去。
		pl.mu.Lock()
		delete(pl.providers, id)
		pl.mu.Unlock()
		if err := pl.persistProvidersFile(); err != nil {
			pl.mu.Lock()
			pl.providers[id] = d
			pl.mu.Unlock()
			return nil, err
		}
	} else {
		// **sqlite 载体**：读行取 revision（CAS 判据）→ 同一事务内查引用 + 删行 + 审计。
		// 密钥是**同一行上的一列**，所以删行即删密钥——没有"行已删而密钥还在"这回事。
		row, found, err := pl.stProviderRow(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			// 内存里有、库里没有：**什么都不做**（不摘内存，如实报未删）——避免"界面上一闪而过、
			// 实际库行还在"的不一致被我们主动制造出来。
			return &ProviderDelete{Deleted: false}, nil
		}
		removed, refModels, err := pl.removeProviderDefinition(ctx, id, row.Revision)
		if err != nil {
			return nil, err
		}
		if len(refModels) > 0 {
			return nil, acp.NewError(acp.ClassLimit, "provider_in_use",
				providerInUseMsg(id, refModels), nil)
		}
		if !removed {
			return &ProviderDelete{Deleted: false}, nil
		}
		// 行已删 → 摘内存（列表立刻不再有它）。
		pl.mu.Lock()
		delete(pl.providers, id)
		pl.mu.Unlock()
	}

	// 最后删镜像条目。失败**如实回带**（行已删、镜像里还留着），并交给 doctor 的对账段。
	resp := &ProviderDelete{Deleted: true}
	if d != nil && d.KeyRef != "" {
		if derr := pl.mirrorDelete(d.KeyRef); derr != nil {
			resp.CredentialsLeft = d.KeyRef
			glog.Warnw(ctx, "credentials: 供应商已删除，但托管文档里那一份删不掉（镜像可由库重建）",
				"provider", id, "ref", d.KeyRef, "err", derr)
		}
	}
	return resp, nil
}

// stProviderRow 读一条 provider 行（memory 载体没有行 → (nil,false,nil) 由调用方处理）。
func (pl *Plane) stProviderRow(ctx context.Context, id string) (*provrow.Item, bool, error) {
	if pl.st == nil {
		// 文件载体：没有"行"可读，revision 恒 0；删除由文件路径完成（见 removeProviderDefinition）。
		return &provrow.Item{ID: id}, true, nil
	}
	return pl.st.GetProviderDefinition(ctx, id)
}

// normalizeProtocol 校验并归一协议取值（本期只有 `openai`）。
//
// 空 = 默认 `openai`（客户端的常见写法）；给了不认识的取值 → 40501 并列出合法取值
// （枚举型键的既有纪律：不静默回落）。
//
// v13 起库级也有 `CHECK (protocol IN ('openai'))`，但那**不替代**这里：裸 SQL 违例会变成
// 一句 50000 的约束错误，而这里给的是 `protocol_unsupported`(40501) + 可执行文案。
func normalizeProtocol(v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return string(resource.ProtocolOpenAI), nil
	}
	p := resource.Protocol(strings.ToLower(strings.TrimSpace(v)))
	if !resource.ValidProtocols[p] {
		return "", acp.NewError(acp.ClassUnsupported, "protocol_unsupported",
			fmt.Sprintf("不支持的协议 %q：本期只支持 OpenAI 兼容端点（`openai`）。"+
				"原生 anthropic / gemini 协议需要各自的 api 映射与鉴权形态，尚未取证", v), nil)
	}
	return string(p), nil
}

// dialEnvChannel 凭据的交付通道（env 注入那一档；取自静态交付表，避免"一处声明"再多一个漂移点）。
func dialEnvChannel() string {
	d, _ := objdelivery.For(objdelivery.KindProvider)
	return d.Channel
}

// providerExistsErr 新建撞上已存在的供应商（与 skill_exists/mcp_exists 同形）。
func providerExistsErr(id string) error {
	return acp.NewError(acp.ClassLimit, "provider_exists",
		fmt.Sprintf("供应商 %q 已存在（要改它用 PATCH /providers/%s）", id, id), nil)
}

// providerInUseMsg 删除被拒时的文案：把两类引用都摆出来，并给出可执行的下一步。
func providerInUseMsg(id string, models []string) string {
	msg := fmt.Sprintf("供应商 %q 正在被引用，删除会静默换掉这些模型条目的凭据来源：", id)
	if len(models) > 0 {
		msg += fmt.Sprintf(" 模型条目 %v 挂靠在它下面", models)
	}
	return msg + "。先解开这些引用（在 设置 · 模型 里删掉/改挂那些模型条目），再删除。"
}

// ProviderModelRefsReadOnly 挂靠在该 provider 下的模型条目 id（**只读**）。
//
// 判据口径：文件载体用它当**唯一**引用判据（没有事务可借）；sqlite 载体的判据仍在 store 的
// 同一事务里（`DeleteProviderDefinitionIfUnreferenced`），这里只在需要给文案列名单时读它。
func (pl *Plane) ProviderModelRefsReadOnly(ctx context.Context, providerID string) []string {
	if pl.st == nil {
		return nil
	}
	rows, err := pl.st.ListModelDefinitionsByProvider(ctx, providerID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].ID)
	}
	sort.Strings(out)
	return out
}

// isDuplicateDefinition 判定载体提交失败是否源于"主键/唯一性冲突"。
//
// memory 与 sqlite 两条路在这里的表现不同：sqlite 侧 CAS 未命中会返回 `definition_changed`
// （它同时表达"并发新建抢先"与"有人改过"两件事），而新建路径上后者不可能发生——所以
// **新建场景下的 CAS 未命中就是 provider_exists**。文件载体没有 CAS，冲突表现为"文件里已经有"。
func isDuplicateDefinition(err error) bool {
	ae, ok := acp.As(err)
	return ok && ae.Code == "definition_changed"
}

// cloneAnyMap 浅拷贝一个 map[string]any（值都是 JSON 标量/数组/对象，写面不会原地修改它们）。
//
// 保留它：**文件载体**与导入路径仍在处理自由 JSON（那是 v11 的落盘形状，v13 不动它）。
func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	return out
}
