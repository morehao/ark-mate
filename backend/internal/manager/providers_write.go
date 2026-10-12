package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objdelivery"
	"github.com/morehao/ark-mate/pkg/resource"
)

// ——— 供应商的**写面**（v12 形状，v13 列化） ———
//
// 写面顺序是固定的，且不是实现细节而是**语义**：
//
//	① 校验（id 词法、协议、端点）
//	② vault 写入/删除（密文）
//	③ 载体提交（sqlite = 行 CAS + 审计；memory = 原子替换文件）
//	④ 更新内存
//
// 为什么 ③ 在 ② 之后：vault（文件 + AES-GCM）与 SQLite 无法同事务，所以只能选一个"谁先谁后"，
// 并在 ③ 失败时**补偿** ②。
//
// ④ 在最后是 ③ 的自然推论：**在载体提交成功之前，内存里一个字段都不动**。
//
// v13 的两条**结构收紧**（ADR-8）：
//   - vault 不可用时**拒绝**存密钥（`vault_unavailable` + 可执行出路）。理由：这条路径上用户的
//     目标是"把密钥存下来"，而 v13 的两张表**没有能放明文的列**——假装存了才是欺骗。
//   - 密钥的"有没有"不再由行上的字段表达（没有 `key_ref` 列）：`has_key` 一律看内存里
//     是否解出了明文，引用由 `providerRef(id)` 派生。

// CreateProvider 新建一条供应商（POST /providers）。
func (s *Service) CreateProvider(ctx context.Context, req dtoprovider.ProviderCreateReq) (*dtoprovider.ProviderResp, error) {
	if err := validateProviderID(req.ID); err != nil {
		return nil, err
	}
	protocol, err := normalizeProtocol(req.Protocol)
	if err != nil {
		return nil, err
	}
	// ① 已存在？——先看内存（快路径），再让载体自己兜并发（CreateCAS 未命中 → provider_exists）。
	s.mu.Lock()
	_, exists := s.providers[req.ID]
	s.mu.Unlock()
	if exists {
		return nil, providerExistsErr(req.ID)
	}

	d := &providerDef{ID: req.ID, Protocol: protocol}
	if req.Name != nil {
		d.Name = *req.Name
	}
	if req.BaseURL != nil {
		d.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Note != nil {
		d.Note = *req.Note
	}

	// ② vault：新建时"清空密钥"没有意义（还没有密文），只在给了非空 key 时写。
	var st *vault.Store
	if req.APIKey != nil && *req.APIKey != "" {
		var verr error
		st, verr = s.providerVault()
		if st == nil {
			return nil, vaultUnavailableErr(verr)
		}
		if err := st.Put(providerRef(req.ID), vault.KindProviderKey, []byte(*req.APIKey)); err != nil {
			return nil, acl.NewError(acl.ClassRequest, "vault_write_failed",
				"密钥加密写入失败: "+err.Error(), nil)
		}
		d.Key, d.KeyRef = *req.APIKey, providerRef(req.ID)
	}

	// ③ 载体（sqlite = INSERT + 审计；memory = 原子替换文件）。
	if err := s.persistProviderDefinition(ctx, d); err != nil {
		// 补偿 ②：密文刚写进去而载体没提交 → 删掉它，避免留下孤儿密文。
		if d.KeyRef != "" && st != nil {
			if derr := st.Delete(d.KeyRef); derr != nil {
				return nil, acl.NewError(acl.ClassRequest, "vault_delete_failed",
					"供应商未创建（载体提交失败: "+err.Error()+"），且刚写入的密文删除失败（残留于 "+d.KeyRef+"）: "+derr.Error(), nil)
			}
		}
		if isDuplicateDefinition(err) {
			return nil, providerExistsErr(req.ID)
		}
		return nil, err
	}

	// ④ 内存。
	s.mu.Lock()
	s.providers[d.ID] = d
	resp := s.providerRespLocked(ctx, d.ID, d)
	s.mu.Unlock()
	resp.Effect = s.providerEffect(d.ID)
	return &resp, nil
}

// UpdateProvider 覆盖一条已存在的供应商（**只更新，不再 upsert**；不存在 → `provider_not_found`）。
//
// api_key 语义：`nil` = 不动；`""` = 清除（删密文，引用由 id 派生所以没有第二个副本要清）；
// 非空 = 覆盖。
func (s *Service) UpdateProvider(ctx context.Context, id string, patch dtoprovider.ProviderPatchReq) (*dtoprovider.ProviderResp, error) {
	if err := validateProviderID(id); err != nil {
		return nil, err
	}

	// ① 读窗口：取旧值快照（**只读**，不改内存）。
	s.mu.Lock()
	old, exists := s.providers[id]
	var snap providerDef
	if exists {
		snap = *old
	}
	s.mu.Unlock()
	if !exists {
		return nil, acl.NewError(acl.ClassRequest, "provider_not_found",
			fmt.Sprintf("供应商 %q 不存在：PATCH 只更新、不再顺带新建（要新建用 POST /providers）", id), nil)
	}

	// 目标状态：只改 patch 里出现过的键。
	next := snap
	behaviorChanged := false
	if patch.Protocol != nil {
		protocol, err := normalizeProtocol(*patch.Protocol)
		if err != nil {
			return nil, err
		}
		if next.Protocol != protocol {
			behaviorChanged = true
		}
		next.Protocol = protocol
	}
	if patch.BaseURL != nil {
		v := strings.TrimSpace(*patch.BaseURL)
		if next.BaseURL != v {
			behaviorChanged = true
		}
		next.BaseURL = v
	}
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.Note != nil {
		next.Note = *patch.Note
	}

	// ② vault：写入新密文或删除旧密文（失败即中止，载体与内存都没动过）。
	oldKey := next.Key
	var st *vault.Store
	needsVault := patch.APIKey != nil
	if needsVault {
		var verr error
		st, verr = s.providerVault()
		if st == nil {
			// 两种情形都要拒绝：设新密钥没地方放（v13 无明文列）；清密钥要求真的把密文删掉
			// （否则下次装载 `vault.Has` 会把它找回来，"清了"就成了假的）。
			return nil, vaultUnavailableErr(verr)
		}
	}
	secretChanged := false
	switch {
	case patch.APIKey != nil && *patch.APIKey != "":
		if err := st.Put(providerRef(id), vault.KindProviderKey, []byte(*patch.APIKey)); err != nil {
			return nil, acl.NewError(acl.ClassRequest, "vault_write_failed",
				"密钥加密写入失败: "+err.Error(), nil)
		}
		next.Key, next.KeyRef, next.keyErr = *patch.APIKey, providerRef(id), nil
		secretChanged = true
	case patch.APIKey != nil && *patch.APIKey == "":
		// 清除：删密文。删不掉就**不继续**（否则会留下一条"界面说清了、密文还在"的档案，
		// 而下次装载它会被重新解出来）。
		if err := st.Delete(providerRef(id)); err != nil {
			return nil, acl.NewError(acl.ClassRequest, "vault_delete_failed",
				"密钥密文删除失败: "+err.Error(), nil)
		}
		next.Key, next.KeyRef, next.keyErr = "", "", nil
		secretChanged = true
	}

	// ③ 载体提交（行 CAS + 审计）。
	if err := s.persistProviderDefinition(ctx, &next); err != nil {
		// 补偿 ②：把 vault 恢复到刚才那一份，避免"行说改过了、密钥却是新的/没了"。
		if secretChanged && st != nil {
			switch {
			case oldKey != "":
				_ = st.Put(providerRef(id), vault.KindProviderKey, []byte(oldKey))
			case patch.APIKey != nil && *patch.APIKey != "":
				_ = st.Delete(providerRef(id))
			}
		}
		return nil, err
	}

	// ④ 内存。
	s.mu.Lock()
	s.providers[id] = &next
	resp := s.providerRespLocked(ctx, id, &next)
	s.mu.Unlock()

	// 生效面回执：只有驱动行为的字段变了才说"下一轮生效"；只改 name/note 就如实回 none。
	if !secretChanged && !behaviorChanged {
		resp.Effect = &dtoprovider.ProviderEffect{
			Channel: dialEnvChannel(), Effect: "none",
			AppliesToAgents: []string{},
			Note:            "只改了不驱动行为的字段（协议/端点/密钥都未变）：本轮与后续轮次的行为都不变",
		}
		return &resp, nil
	}
	resp.Effect = s.providerEffect(id)
	return &resp, nil
}

// DeleteProvider 删除一条供应商（幂等；被引用 → `provider_in_use`(40901) + 引用名单）。
//
// 顺序刻意与写面**相反**：**先删行 + 审计 → 再摘内存 → 最后删密文**。
// 理由：反过来（先删密文再删行）会留下一个"行在、密钥没了"的档案，那会让 `has_key` 撒谎；
// 而"行已删、密文残留"是一个**如实可报**的状态（响应里说明，doctor 里列进孤儿密文）。
//
// **引用判据在 store 的同一事务里**（v13）：v12 是"manager 先查引用名单、再单独删行"，
// 中间有窗口——并发「删供应商」+「给它加模型」会留下孤儿 model 行（P3）。
// manager 现在只翻错误码（`provider_in_use`），文案与引用名单形状不变。
func (s *Service) DeleteProvider(ctx context.Context, id string) (*dtoprovider.ProviderDeleteResp, error) {
	if err := validateProviderID(id); err != nil {
		return nil, err
	}

	// agent 引用面仍读内存（它不是 DB 引用，且写面已有显式拒绝）。
	s.mu.Lock()
	refAgents := make([]string, 0, 4)
	for agentID, a := range s.agents {
		if a != nil && a.Provider == id {
			refAgents = append(refAgents, agentID)
		}
	}
	sort.Strings(refAgents)
	_, exists := s.providers[id]
	s.mu.Unlock()
	if !exists {
		// 幂等：不存在（或本来就没这一条）→ deleted:false 且 code=0。
		return &dtoprovider.ProviderDeleteResp{Deleted: false}, nil
	}
	if len(refAgents) > 0 {
		// agent 引用已经足以拒绝，**不进入删除事务**；模型名单只为文案完整而读（只读，不参与判据）。
		return nil, acl.NewError(acl.ClassLimit, "provider_in_use",
			providerInUseMsg(id, refAgents, s.providerModelRefsReadOnly(ctx, id)), nil)
	}

	s.mu.Lock()
	d := s.providers[id]
	s.mu.Unlock()

	if s.st == nil {
		// **文件载体**：先摘内存再落盘——`persistProvidersFile` 序列化的是"内存现状"，
		// 顺序反了就会把刚删掉的那条原样写回文件（一个只在 memory 模式出现、且完全静默的失败：
		// 界面上没了、文件里还有，重启后它又回来了）。落盘失败则把内存放回去。
		s.mu.Lock()
		delete(s.providers, id)
		s.mu.Unlock()
		if err := s.persistProvidersFile(); err != nil {
			s.mu.Lock()
			s.providers[id] = d
			s.mu.Unlock()
			return nil, err
		}
	} else {
		// **sqlite 载体**：读行取 revision（CAS 判据）→ 同一事务内查引用 + 删行 + 审计。
		row, found, err := s.stProviderRow(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			// 内存里有、库里没有：**什么都不做**（不摘内存，如实报未删）——避免"界面上一闪而过、
			// 实际库行还在"的不一致被我们主动制造出来。
			return &dtoprovider.ProviderDeleteResp{Deleted: false}, nil
		}
		removed, refModels, err := s.removeProviderDefinition(ctx, id, row.Revision)
		if err != nil {
			return nil, err
		}
		if len(refModels) > 0 {
			return nil, acl.NewError(acl.ClassLimit, "provider_in_use",
				providerInUseMsg(id, refAgents, refModels), nil)
		}
		if !removed {
			return &dtoprovider.ProviderDeleteResp{Deleted: false}, nil
		}
		// 行已删 → 摘内存（列表立刻不再有它）。
		s.mu.Lock()
		delete(s.providers, id)
		s.mu.Unlock()
	}

	// 最后删密文。失败**如实回带**（行已删、密文残留），并交给 doctor 的孤儿密文段。
	resp := &dtoprovider.ProviderDeleteResp{Deleted: true}
	if d != nil && d.KeyRef != "" {
		if st, verr := s.providerVault(); verr == nil && st != nil {
			if derr := st.Delete(d.KeyRef); derr != nil {
				resp.VaultCiphertextLeft = d.KeyRef
				return resp, acl.NewError(acl.ClassRequest, "vault_delete_failed",
					fmt.Sprintf("供应商 %q 已删除，但密钥密文删除失败（残留于 %s）: %v；"+
						"该残留不会造成误用（AAD 绑定引用，且已无行指向它），`mate doctor` 的孤儿密文段会列出它",
						id, d.KeyRef, derr), nil)
			}
		}
	}
	return resp, nil
}

// stProviderRow 读一条 provider 行（memory 载体没有行 → (nil,false,nil) 由调用方处理）。
func (s *Service) stProviderRow(ctx context.Context, id string) (*model.ProviderEntity, bool, error) {
	if s.st == nil {
		// 文件载体：没有"行"可读，revision 恒 0；删除由文件路径完成（见 removeProviderDefinition）。
		return &model.ProviderEntity{ID: id}, true, nil
	}
	return s.st.GetProviderDefinition(ctx, id)
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
		return "", acl.NewError(acl.ClassUnsupported, "protocol_unsupported",
			fmt.Sprintf("不支持的协议 %q：本期只支持 OpenAI 兼容端点（`openai`）。"+
				"原生 anthropic / gemini 协议需要各自的 api 映射与鉴权形态，尚未取证", v), nil)
	}
	return string(p), nil
}

// validateProviderID id 词法校验（它会进 vault 文件名 → 必须过引用校验，防路径穿越）。
func validateProviderID(id string) error {
	if id == "" {
		return acl.NewError(acl.ClassRequest, "invalid_provider_id", "provider id 必填", nil)
	}
	if _, err := vault.ValidateRef(providerRef(id)); err != nil {
		return acl.NewError(acl.ClassRequest, "invalid_provider_id",
			fmt.Sprintf("provider id %q 非法（仅 a-zA-Z0-9._-，1-64 字符）: %v", id, err), nil)
	}
	return nil
}

// vaultUnavailableErr 「没有可用的 vault」的统一出路文案（fail-closed 的唯一出口）。
//
// cause 为 nil = agentDir 未配置（没有 mate home，vault 根本无从打开）。
func vaultUnavailableErr(cause error) error {
	msg := "无法加密保存密钥：本机没有可用的 vault。" +
		"v13 起 providers/models 是**没有明文列**的资源表，密钥只以密文落盘（`<mate home>/vault/`），" +
		"所以这里宁可拒绝也不假装存下来了。" +
		"出路：设置 MATE_VAULT_KEY（32 字节 base64 或 64 位 hex），或修好 `<mate home>/vault.key`，或让 agentDir 指向一个真实目录后重试"
	if cause != nil {
		msg += "；原因：" + cause.Error()
	}
	return acl.NewError(acl.ClassRequest, "vault_unavailable", msg, nil)
}

// dialEnvChannel 凭据的交付通道（env 注入那一档；取自静态交付表，避免"一处声明"再多一个漂移点）。
func dialEnvChannel() string {
	d, _ := objdelivery.For(model.DefinitionKindProvider)
	return d.Channel
}

// providerExistsErr 新建撞上已存在的供应商（与 skill_exists/mcp_exists 同形）。
func providerExistsErr(id string) error {
	return acl.NewError(acl.ClassLimit, "provider_exists",
		fmt.Sprintf("供应商 %q 已存在（要改它用 PATCH /providers/%s）", id, id), nil)
}

// providerInUseMsg 删除被拒时的文案：把两类引用都摆出来，并给出可执行的下一步。
func providerInUseMsg(id string, agents, models []string) string {
	msg := fmt.Sprintf("供应商 %q 正在被引用，删除会静默换掉这些引用的凭据来源：", id)
	if len(agents) > 0 {
		msg += fmt.Sprintf(" agent %v", agents)
	}
	if len(models) > 0 {
		msg += fmt.Sprintf("；模型条目 %v 挂靠在它下面", models)
	}
	return msg + "。先解开这些引用（在 设置 · Agent 管理 里换掉 agent 的凭据选择，或在 设置 · 模型 里删掉/改挂那些模型条目），再删除。"
}

// providerModelRefsReadOnly 挂靠在该 provider 下的模型条目 id（**只读**）。
//
// 删除判据**不在**这里：v13 起「检查引用 + 删行」在 store 的同一事务里
// （`DeleteProviderDefinitionIfUnreferenced`）。这个函数只服务一种情形——agent 引用已经
// 足以拒绝删除时，错误文案里仍要如实列出模型引用（"引用名单不变"是对外承诺）。
func (s *Service) providerModelRefsReadOnly(ctx context.Context, providerID string) []string {
	if s.st == nil {
		return nil
	}
	rows, err := s.st.ListModelDefinitionsByProvider(ctx, providerID)
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
// memory 与 sqlite 两条路在这里的表现不同：sqlite 侧 `CreateCAS` 未命中会返回 `definition_changed`
// （它同时表达"并发新建抢先"与"有人改过"两件事），而新建路径上后者不可能发生——所以
// **新建场景下的 CAS 未命中就是 provider_exists**。文件载体没有 CAS，冲突表现为"文件里已经有"。
func isDuplicateDefinition(err error) bool {
	ae, ok := acl.As(err)
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
