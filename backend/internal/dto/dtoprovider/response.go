package dtoprovider

import "github.com/morehao/ark-mate/pkg/object/objdelivery"

// ProviderResp 一条供应商的对外表示（密钥只以 `has_key` 呈现，**绝不读回**）。
//
// 它承担四件事：身份与端点（id/name/protocol/base_url）、密钥状态（has_key）、
// **派生只读面**（model_count / problems / applicability / refs）、以及载体版本
// （revision/updated_at，写面的乐观锁依据）。
type ProviderResp struct {
	ID string `json:"id"`
	// Name 展示名（空 = 前端回落显示 id）。v11 没有这个字段——那时列表显示的就是 id。
	Name string `json:"name,omitempty"`
	// Protocol 上游协议（本期恒为 `openai`）。
	Protocol string `json:"protocol"`
	// BaseURL OpenAI 兼容端点（可空 = **缺端点**：B1 的存量供应商可能推不出来，
	// 这时界面与 doctor 要显式标出来，且它不进任何 Set）。
	BaseURL string `json:"base_url"`
	// HasKey 是否有可用密钥（明文绝不读回；分层读不到时这里也是 false，理由在 problems 里）。
	HasKey bool `json:"has_key"`
	// KeySource 这把密钥**从哪一层来的**（v20 四层回落：env | managed | dotenv_cwd | dotenv_home；
	// 空 = 没有密钥）。
	//
	// 它必须读出来：层序是"继承环境 > 托管"，所以生效值来自 `env` 时，面板里保存的那一条被
	// 遮住了——用户轮换密钥却毫无变化，且没有任何报错（2026-10-11 的实测缺陷）。
	KeySource string `json:"key_source,omitempty"`
	// KeyRef 这条密钥在托管文档里的键（派生量 `providers/<id>`；**不是**密钥本身）。
	//
	// 它是"密钥落在哪"的线索（`<MATE_HOME>/credentials.json` 的哪一条），所以读出来有用；
	// 而引用本身反推不出"有没有值"，算不上泄漏面。
	KeyRef string `json:"key_ref,omitempty"`
	// Note 备注（不驱动行为；写面可改，生效回执会如实回 effect=none）。
	Note string `json:"note,omitempty"`
	// Revision 写面乐观锁计数（sqlite = 行 revision，CAS 判据；memory = 0）。
	//
	// **不是内容版本**：同一内容写两次它照样前进，而"那一版到底是什么"的内容指纹在审计
	// digest 里（ADR-13）。它今天零消费者，留着是给将来真要做 `If-Match` 的集成方一个现成判据。
	Revision int64 `json:"revision"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano；memory 模式为空）。
	UpdatedAt string `json:"updated_at,omitempty"`
	// ModelCount 挂靠在本供应商下的模型条数（读取时现算的派生值——存一份必然漂移）。
	ModelCount int `json:"model_count"`
	// Problems 可执行的问题清单（缺端点 / 密钥缺失 / 凭据分层读不了…）。空数组 = 本地段全部通过。
	Problems []string `json:"problems"`
	// Applicability 逐**已注册** runtime 的可达性（只读派生）。
	//
	// 它刻意不给"合并结论"：同一份凭据在不同底座上的可达性本来就是逐条的。v12 里它回答的是
	// "这条端点与密钥能不能被该底座拿到"（凭据通道 + 清单通道各是什么），不再是 v11 的
	// "type 推不推得出 env 名"。
	Applicability []objdelivery.Applicability `json:"applicability"`
	// Refs 引用面：哪些模型条目挂在它下面、它们可能落在哪些 runtime（只读派生）。
	Refs ProviderRefs `json:"refs"`
	// Effect 写面回执（**只在写面响应里出现**，GET 列表里恒为空）。
	Effect *ProviderEffect `json:"effect,omitempty"`
}

// ProviderRefs 供应商的引用面（逐处可跳转）。
//
// v19 起引用者只有**模型条目**（agent 面撤销）：供应商不再是"某个 agent 的凭据选择"，
// 而是"一组模型条目的端点与密钥"。
type ProviderRefs struct {
	// Models 挂靠在该供应商下的模型定义 id（升序）。
	Models []string `json:"models"`
	// Runtimes 可能消费它的 runtime（升序、去重）：有模型挂靠才谈得上"哪个底座拿得到"。
	Runtimes []string `json:"runtimes"`
}

// ProviderEffect 写面的生效面回执（每一条写面都如实回带"生效到哪、什么时候"）。
//
// 判据与代际机制**同源**（`planGenerationSwitch` 的同一组输入）：`affected_sessions` 是
// 引用该供应商的会话数，`deferred` 是其中"本轮不能就地换代际"的那部分（有在途轮次 /
// 有未应答的权限或提问）。两处各算一遍迟早漂移，所以这里直接调那个只读裁决函数。
type ProviderEffect struct {
	// Channel 交付通道（`native_doc` | `env`，取自本供应商实际走的凭据面）。
	Channel string `json:"channel"`
	// Effect 生效时刻：`next_run`（下一次轮次边界，键变了就地换代际）| `none`（只改了不驱动
	// 行为的字段，如 name/note——这时不许说"将在下一轮生效"）。
	Effect string `json:"effect"`
	// AffectedSessions 引用该供应商的会话数。
	AffectedSessions int `json:"affected_sessions"`
	// Deferred 其中本轮会顺延的会话数（有在途轮次 / 有未应答权限或提问）。
	Deferred int `json:"deferred"`
	// Note 一句话人话说明（含顺延的理由）。
	Note string `json:"note,omitempty"`
}

// ProviderDeleteResp 删除供应商的结果。
//
// 幂等：不存在 → `deleted:false` 且 code=0（与 skill/mcp 删面同形）。
// 被模型条目引用 → `provider_in_use`(40901) 且回带引用名单（**不做级联清空**：
// 删掉挂靠的模型等于静默删用户的数据）。
type ProviderDeleteResp struct {
	Deleted bool `json:"deleted"`
	// ReferencedByModels 拒绝删除时给出的模型引用名单（成功删除时为空）。
	ReferencedByModels []string `json:"referenced_by_models,omitempty"`
	// CredentialsLeft 行已删但托管文档里那一条还在（镜像删除失败）——**如实回带**，
	// 并在 `mate doctor` 的镜像对账段列出。反过来（先删镜像再删行）会让 has_key 撒谎。
	CredentialsLeft string `json:"credentials_left,omitempty"`
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
	// Name 检查项：exists / protocol / base_url / secret / refs / delivery / upstream。
	Name string `json:"name"`
	// Status pass | fail | warn | skip | unknown。
	Status string `json:"status"`
	// Detail 事实（含数字与变量名，人话）。
	Detail string `json:"detail"`
	// Remedy 可执行的下一步（空 = 这一条不需要动作，如纯提示的 delivery）。
	Remedy string `json:"remedy,omitempty"`
}
