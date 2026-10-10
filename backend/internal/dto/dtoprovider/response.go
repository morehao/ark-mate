package dtoprovider

// ProviderResp provider 档案对外表示（密钥只以 has_key 呈现，绝不读回）。
//
// 从 v11 起它承担三件事：身份与密钥状态（id/type/has_key）、**引用面**（refs：哪些 agent 在用它、
// 它们可能落到哪些 runtime）、以及**载体版本**（revision/updated_at，写面的乐观锁依据）。
// `delivery`/`applicability` 在 M6（交付矩阵）落地时补上——它们是逐 runtime 的可达性表示，
// 与 skill/MCP 面共用同一套值对象。
type ProviderResp struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// Model 历史字段（P3 的静默陷阱）：今天不驱动任何行为，**M5 删除**。
	// 在此之前仍透出，是为了不改动既有前端调用点；迁移时它的值原样留在 body 里。
	Model  string `json:"model,omitempty"`
	HasKey bool   `json:"has_key"`
	// Revision 载体版本（sqlite = 行 revision，写面 CAS 判据；memory = 0，文件载体没有版本号）。
	Revision int64 `json:"revision"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano；memory 模式为空 = 由文件 mtime 承载）。
	UpdatedAt string `json:"updated_at,omitempty"`
	// Note 备注（不驱动行为；写面可改，生效回执会如实回 effect=none）。
	Note string `json:"note,omitempty"`
	// Refs 引用面：哪些 agent 用它，以及这些 agent 可能落在哪些 runtime。
	//
	// 它是**只读派生**（每次读取时按 agent 注册表现算），没有写入方——存一份就必然漂移。
	Refs ProviderRefs `json:"refs"`
	// Problems 可执行的问题清单（密钥缺失/密文解不开/type 推不出注入变量名…）。
	// 它是"开会话之前就能看见"的那一面（G3）：空数组 = 本地段全部通过。
	Problems []string `json:"problems"`
	// Effect 写面回执（**只在写面响应里出现**，GET 列表里恒为空）。
	Effect *ProviderEffect `json:"effect,omitempty"`
}

// ProviderRefs provider 档案的引用面（逐处可跳转）。
type ProviderRefs struct {
	// Agents 引用该档案的 agent id（升序）。
	Agents []string `json:"agents"`
	// Runtimes 上述 agent 声明的 runtime 并集（升序、去重）。
	//
	// 它回答的是"这条凭据会被注入到哪些底座的子进程里"，不是"它在哪些 runtime 上被声明过"
	// （后者是 providers.body.projections 的事，M5d 落地）。
	Runtimes []string `json:"runtimes"`
}

// ProviderEffect 写面的生效面回执（T4 的落地形态：每一条写面都如实回带"生效到哪、什么时候"）。
//
// 判据与代际机制**同源**（`planGenerationSwitch` 的同一组输入）：`affected_conversations` 是
// 引用该档案的会话数，`deferred` 是其中"本轮不能就地换代际"的那部分（有在途轮次 / 有未应答的
// 权限或提问）。两处各算一遍迟早漂移，所以这里直接调那个只读裁决函数。
type ProviderEffect struct {
	// Channel 交付通道。provider 密钥恒为 `dial_env`（<TYPE>_API_KEY 注入子进程 env）。
	Channel string `json:"channel"`
	// Effect 生效时刻：`next_turn`（下一次轮次边界，键变了就地换代际）/ `none`（只改了不驱动
	// 行为的字段，如 note——这时不许说"将在下一轮生效"）。
	Effect string `json:"effect"`
	// AppliesToAgents 受影响 agent 的 id（升序）。
	AppliesToAgents []string `json:"applies_to_agents"`
	// AffectedConversations 引用该档案的会话数。
	AffectedConversations int `json:"affected_conversations"`
	// Deferred 其中本轮会顺延的会话数（有在途轮次 / 有未应答权限或提问）。
	Deferred int `json:"deferred"`
	// Note 一句话人话说明（含顺延的理由）。
	Note string `json:"note,omitempty"`
}

// ProviderDeleteResp 删除 provider 档案的结果。
//
// 幂等：不存在 → `deleted:false` 且 code=0（与 skill/mcp 删面同形）。
// 被 agent 引用 → `provider_in_use`(40901) 且回带 `referenced_by_agents[]`（**不做级联清空**：
// 清空 agent 的 provider 等于静默换掉它的凭据来源）。
type ProviderDeleteResp struct {
	Deleted bool `json:"deleted"`
	// ReferencedByAgents 拒绝删除时给出的引用名单（成功删除时为空）。
	ReferencedByAgents []string `json:"referenced_by_agents,omitempty"`
	// VaultCiphertextLeft 行已删但密文残留（vault 删除失败）——**如实回带**，
	// 并在 `mate doctor` 的孤儿密文段列出。反过来（先删密文再删行）会让 has_key 撒谎。
	VaultCiphertextLeft string `json:"vault_ciphertext_left,omitempty"`
}

// ProviderProbeResp `POST /providers/:id/probe` 的响应：一次自检的逐条结论。
//
// `OK` 是"**没有 fail**"（warn/skip/unknown 都不算失败）——与 `mate doctor` 的口径一致：
// **无法校验记警告，事实与声明不符才失败**。所以前端不能用 `OK == false` 之外的方式推导
// "有没有问题"：真正的处置在每一条的 `status` 与 `remedy` 里。
type ProviderProbeResp struct {
	ID string `json:"id"`
	// Upstream 本次是否发起了上游请求（用户显式选择的事实，回执里如实重复一次）。
	Upstream bool `json:"upstream"`
	OK       bool `json:"ok"`
	// Checks 逐条结论（本地段在前，上游段在后）。
	Checks []ProviderCheckResp `json:"checks"`
}

// ProviderCheckResp 一条自检结论。
type ProviderCheckResp struct {
	// Name 检查项：exists / type / secret / refs / injection / catalog / upstream。
	Name string `json:"name"`
	// Status pass | fail | warn | skip | unknown。
	Status string `json:"status"`
	// Detail 事实（含数字与变量名，人话）。
	Detail string `json:"detail"`
	// Remedy 可执行的下一步（空 = 这一条不需要动作，如纯提示的 injection）。
	Remedy string `json:"remedy,omitempty"`
}
