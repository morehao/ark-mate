// Package resource 定义**应用层资源**的声明值对象与「声明 → 交付 → 回带结论」端口。
//
// 它是 leaf 包（不 import 应用内核的任何包，也不 import `internal/runtime`）：底座 adapter 要实现
// 这里的 `Syncer`，而 `internal/runtime` 已经 import 各 adapter 实现——接口定义在那里会形成
// 父子 import 环。仓库已有同形先例：`core/object/objdelivery` 同时被 DTO、用例层与守卫消费。
//
// 本包只表达**意图与结论**，不表达任何底座的配置格式：
//
//	Provider / Model / Set —— 平台拥有的、与底座无关的事实（写入方是 manager 的写面）
//	Syncer                 —— adapter 实现的交付端口（唯一接触面）
//	Delivery               —— 平台交给 adapter 的「改启动面」窄口（进程是平台拉起的）
//	Outcome                —— adapter 回带的事实（逐条，不给合并结论）
//
// 判据（archtest 守）：adapter（`internal/infra/<adapter>`）不得 import 应用内核
// （`layers_v32_test.go` 的 `v32AdapterDeny` + `knowledge_boundary_test.go` 的
// `TestAdapterHasNoStoreLiterals`）；`internal/**` 不得出现底座配置格式字面量
// （`TestInternalHasNoRuntimeConfigLiterals`）。两条合起来就是「知识归属」的机器判据。
package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Protocol 供应商的上游协议。本期只有一种取值。
//
// 枚举而不是自由串（ADR-3）：v11 的 `type` 是自由串、唯一的消费者是环境变量名推导函数，
// 于是「写错一个词」既不报错也不生效。取值集刻意**逐项扩张**——每加一个都要逐底座实测
// wire 协议映射才准进枚举（未实测不声明）。
type Protocol string

// ProtocolOpenAI 本期唯一取值：一个 OpenAI 兼容端点（`GET <base_url>/models` + Bearer）。
const ProtocolOpenAI Protocol = "openai"

// ValidProtocols 合法协议集合（写面与迁移共用）。
var ValidProtocols = map[Protocol]bool{ProtocolOpenAI: true}

// 输入模态取值（两态；ADR-12）。它是**平台拥有的封闭字段集**，所以枚举在端口层，
// 由写面校验、由 adapter 生成底座声明。
const (
	// ModalityText 纯文本（**默认值**：未知/未设一律按它，平台不猜）。
	ModalityText = "text"
	// ModalityImage 图片输入（附带在文本之上：`["text","image"]`）。
	ModalityImage = "image"
)

// ValidModalities 合法模态集合（写面校验用：非法取值报 40501 并列出合法取值）。
var ValidModalities = map[string]bool{ModalityText: true, ModalityImage: true}

// NormalizeModalities 把用户给的模态列表归一成**规范序**（**恒含 `text`**、按 `text,image` 排列、去重）。
//
// 两条不变量：① 两态取值只有 `text` 与 `text,image`（ADR-12），所以"只勾 image"也是
// `["text","image"]`——图片输入是在文本之上追加的能力；② 规范序的理由与端口其它值对象一致：
// 下游按"同输入同字节"生成配置（逐字节 golden 依赖它），用户勾选的顺序不该改变产物。
func NormalizeModalities(in []string) []string {
	hasImage := false
	for _, v := range in {
		switch strings.TrimSpace(v) {
		case ModalityText:
		case ModalityImage:
			hasImage = true
		}
	}
	out := make([]string, 0, 2)
	out = append(out, ModalityText)
	if hasImage {
		out = append(out, ModalityImage)
	}
	return out
}

// ModalitiesFromColumn 把落库形态（逗号分隔）折成数组。
//
// 空列（不该出现，除非手写行）按默认 `["text"]`：读面永远不返回"不知道"，
// 因为默认 `text` 是**明确值**（ADR-12）。
func ModalitiesFromColumn(v string) []string {
	parts := strings.Split(v, ",")
	return NormalizeModalities(parts)
}

// ModalitiesToColumn 把数组折成落库形态（逗号分隔；`text` | `text,image`）。
func ModalitiesToColumn(in []string) string {
	return strings.Join(NormalizeModalities(in), ",")
}

// Provider 应用层的供应商声明：**一个 OpenAI 兼容端点 + 一把密钥**。
//
// `APIKey` 是**明文，只在内存里存在**（v20 起库里的 `providers.key` 列是权威，
// `<MATE_HOME>/credentials.json` 只是它的 0600 可重建镜像）。2026-10-10 拍板改口径：
// 明文**进 adapter**——因为「写进底座的凭据文档」这件事只有 adapter 知道怎么写（dsh 的
// `refs:` 分节、0600 权限、版本字段）。adapter 对它的去向受 `Syncer` 契约⑤约束：
// 只许落到底座私有凭据文档或子进程 env 两处之一，绝不进日志/错误文案/事件/返回值。
type Provider struct {
	ID       string
	Name     string
	Protocol Protocol
	BaseURL  string
	APIKey   string
	// Ref 该供应商的**凭据引用名**（编排层按 `CredentialSpec.RefPattern` 派生，逐供应商唯一）。
	// adapter 写 `refs:` 条目 / `apiKeyEnv` / `env: []` 时都用它——不再由各底座自己拼。
	//
	// 逐供应商唯一的理由是修 v11 的真实缺陷：`envKeyOf(type)` 在逐 route 的循环里求值，
	// 而 `providers` 表对 `type` 无唯一约束 → 两个同 type 的供应商共用一把密钥 → 至少一条
	// route 用错密钥发请求（ADR-16）。
	Ref string
}

// Model 应用层的模型声明。**没有 runtime 字段**：底座维度由同步层投影。
type Model struct {
	// ID 平台模型 id（清单行主键；`Set.DefaultModel` 用的就是它）。
	ID string
	// ProviderID 挂靠的供应商 id。
	ProviderID string
	// Model 发给上游的模型名（**不是**平台 id）；线上键名就是 `model`（v24 从 `model_id` 改名）。
	Model string
	// Name 展示名（空 = 回落 Model）。
	Name string
	// InputModalities 该模型能接受的输入模态（**总是带值**，默认 `["text"]`）。
	//
	// 它是**意图载荷**：adapter 据此生成底座声明里的 `models[].input`（dsh），而不是平台去猜
	// 底座目录里那条模型本来支持什么（ADR-12）。取值两态：`text` | `text,image`。
	//
	// 为什么"总是带值"：`text` 是用户的**明确选择**而不是"没设"，所以 adapter 必须显式写它
	// ——代价是覆盖底座 catalog（底座目录里本来能收图的模型会变成纯文本，除非用户勾选图片）。
	// 这条代价是拍板结果、不是 bug：界面必须让用户能看见并一键改。
	InputModalities []string
	// MaxInputTokens / MaxOutputTokens **平台声明的容量**（v25；nil = 未声明）。
	//
	// 为什么是**指针**而不是 int64：nil（未声明）与一个数（声明过）必须可分——adapter 只在非 nil 时
	// 写底座对应字段，nil 就什么都不写（让底座自己的兜底生效，并在诊断里如实说"这不是平台声明"）。
	// 0 不是合法容量（底座 schema 是 `min(1)`），写面已把它折成 nil。
	//
	// 字段名用平台的词（输入/输出上限），**不抄底座**的 `contextWindow`/`maxTokens`：那是底座词表，
	// 映射只住在 adapter 里（A9 的精神）。
	MaxInputTokens  *int64
	MaxOutputTokens *int64
}

// Set 某个 runtime 该拿到的**一整份**清单（整份替换语义，不是增量）。
//
// 顺序由编排层固定（Providers 按 ID、Models 按 ProviderID+Model），保证同输入同字节
// ——逐字节 golden 等价性（A4/A5）依赖这条。
type Set struct {
	Runtime   string
	Providers []Provider
	Models    []Model
	// DefaultModel 平台模型 id（= `Model.ID`，不是发给上游的那个 `Model`）；空 = 不下发默认模型。
	// 由 adapter 自行在 Models 里解析出上游模型名——端口两边各用各的词汇，不做隐式转换。
	DefaultModel string
	// CredentialDocPath 出参：**已解析的绝对路径**（空 = 本 runtime 不走原生凭据文档）。
	//
	// 为什么由编排层解析：占位符（`${DSH_HOME}`）只能靠注册表里的 `Launch.Env` 解出来，
	// 而 adapter 是纯数据子包、拿不到注册表（约束⑦）。adapter 因此只收**结果**，不收占位符。
	CredentialDocPath string
}

// 一次同步的结论取值域。
const (
	// StatusApplied 已交付（文件已写 / 注入已设 / 参数已装）。
	StatusApplied = "applied"
	// StatusUnchanged 与上一份逐字节相同（幂等重放，没有重写任何字节、没有换代际）。
	StatusUnchanged = "unchanged"
	// StatusUnsupported 该 runtime 没有这条交付面（未注册 / 未声明 syncer）。
	StatusUnsupported = "unsupported"
	// StatusUntested 该落点未实测：**不许冒充 applied**（写入权是用实测换的）。
	StatusUntested = "untested"
	// StatusFailed 交付失败（不覆盖上一份成功的状态）。
	StatusFailed = "failed"
)

// 生效时刻取值域（用户能理解的时间点）。
const (
	EffectImmediate   = "immediate"
	EffectNextRun     = "next_run"
	EffectNextSession = "next_session"
	EffectNextProcess = "next_process"
)

// 凭据交付通道取值域。
const (
	// ChannelNativeDoc 该底座的原生凭据文档（dsh 的 `.credentials.yaml`，可热生效）。
	ChannelNativeDoc = "native_doc"
	// ChannelEnv 平台经子进程 env 注入（平台 env；某些底座还有自己那份官方 env 面）。
	ChannelEnv = "env"
	// ChannelNone 两条都不满足（无密钥 / 未声明）。
	ChannelNone = "none"
)

// EffectiveEffect 把生效时刻折算成「保守值」排序（越大越晚）。
//
// 用户面的 `effective_at` 只能取所有 runtime 里**最晚**的那个（ADR-22：页面不得出现底座名，
// 所以不能逐底座说各自的时机）。代价已拍板接受：改密钥在 dsh 上一轮就生效，页面会说
// 「下次启动应用生效」——保守而非欺骗（实际行为只会比承诺更快）。
func EffectiveEffect(a, b string) string {
	if EffectRank(b) > EffectRank(a) {
		return b
	}
	return a
}

// EffectRank 生效时刻的保守序（未知取值按最晚处理——宁可多说一句"要等"，不可少说）。
func EffectRank(e string) int {
	switch e {
	case EffectImmediate:
		return 0
	case EffectNextRun:
		return 1
	case EffectNextSession:
		return 2
	case EffectNextProcess:
		return 3
	default:
		return 3
	}
}

// CredentialOutcome 一次凭据交付的结论。与清单交付**分开报**（ADR-14）——同一底座上两者的
// 生效时机本就不同（dsh：凭据可热生效、清单要新进程），合并成一个 effect 必然说谎。
type CredentialOutcome struct {
	// Channel native_doc | env | none。
	Channel string
	// Status 取值域同 Outcome.Status。
	Status string
	// Where 人话落点（**不含**路径里的用户名以外的信息；绝不回显密钥）。
	Where string
	// Effect 同 Outcome.Effect 的取值域。
	Effect string
	// Tested 该通道是否已实测；false 时界面必须显示「待实测」而不是「已生效」。
	Tested bool
}

// Outcome 一次同步的结论：逐条事实，不给合并结论。
type Outcome struct {
	// Status applied | unchanged | unsupported | untested | failed。
	Status string
	// Where 人话落点（给用户看的「同步到哪了」）。
	Where string
	// Effect immediate | next_run | next_session | next_process。
	Effect string
	// Credential 凭据交付面的结论，与清单**分开报**（ADR-14）。
	Credential CredentialOutcome
	// Problems 可执行的一句话清单（每条都要能指导下一步动作）。
	Problems []string
}

// Delivery 端口实现方**改启动面**的唯一手段。
//
// 为什么需要它：进程是**平台**拉起的，所以「往子进程注入一份内联配置」「给它加一个启动参数」
// 这两件事只能在平台侧生效；而 adapter 是独立子包（不能 import `internal/runtime`，父子 import 环），
// 拿不到注册表——于是平台把注册表按这个窄接口交给 adapter。
//
// **凭据走这里，但只在「该底座声明了原生文档」时**：密钥由 `Set.Providers[i].APIKey` 交给
// adapter，由它写进底座的私有凭据文档。此接口承载的是**清单通道**（内联配置内容 / 启动参数），
// 与凭据通道分开。
type Delivery interface {
	// SetEnv 同键替换（**不是追加**：平台的 env 合并语义是"先出现者胜"，追加不会生效）；
	// value 为空 = 撤销该项注入。
	SetEnv(key, value string) error
	// SetArg 覆盖某个启动参数的值；value 为空 = 摘掉该参数（例：撤销 `--patch`）。
	SetArg(flag, value string) error
	// SetArgs 覆盖**可重复**启动参数的全部取值（按 values 顺序）；values 为空 = 摘掉该 flag 的所有出现。
	//
	// 为什么必须有它：agent-mate 里有两条**互相独立**的交付面共用同一个 flag——模型清单
	// （`mate-models.patch.yml`）与产品身份（`mate-identity.patch.yml`），都经 `--patch` 递入。
	// 只有 `SetArg` 时两边会互相覆盖：身份后装 → 模型清单被摘掉，或反之；而两处各自都"成功"。
	// 多值形态把"这一个 flag 的完整取值列表"变成一次原子写入，谁也不会静默吃掉谁。
	//
	// **只有声明了多值交付的 adapter 用它**；其余 adapter 继续用 `SetArg`（等价于一条值的它）。
	SetArgs(flag string, values []string) error
}

// Syncer 端口：把应用层声明交付给本底座。
//
// 实现方（`internal/infra/<adapter>`）自己知道写哪个文件、什么格式、怎么校验、怎么撤回。
// 装配形态：`reg.RegisterSyncer("<id>", <id>pkg.NewSyncer(delivery))`——Delivery 从构造函数
// 进来，SyncModels 保持最小签名。
//
// 契约（五条，缺一条实现方即违约）：
//
//	① 输入是**应用层意图**，不是底座配置字节——实现方不得反过来要求调用方提供底座细节；
//	② **幂等**：同一份 Set 重复调用必须收敛（不重写字节、不换代际）；
//	③ **空集必撤回**：`len(Set.Models)==0` 时，必须撤销此前交付的一切（清注入、摘参数、
//	   删或清空文件），而不是「什么都不做」——什么都不做会让上一份配置继续生效（v11 的真实缺陷）。
//	   粒度例外：底座的**共享面**（如 dsh 的凭据文档，它还有 `records:` 与用户自己的 refs）
//	   只撤回「平台自己写过的条目」，绝不整份清空——那会抹掉用户自己的密钥；
//	④ **fail-loud 且不半写**：可能让底座拒启的实现方，必须在落盘前自校验，失败即中止并返回
//	   错误，不允许「写了一半」；未实测的落点必须返回 `StatusUntested`，不得冒充 applied；
//	⑤ **密钥面纪律**：拿到明文密钥的实现方只许把它落到两处之一——底座私有凭据文档
//	   （权限 0600，落盘前校验）或子进程 env；绝不进日志、错误文案、事件、返回值、配置文件
//	   正文，也绝不落进「清单」那一路的字节里（那一路会进 `ps eww` 可读的内联内容）。
//	   凭据交付的结论必须与清单交付**分开报**（见 `Outcome.Credential`）。
type Syncer interface {
	SyncModels(ctx context.Context, set Set) (Outcome, error)
}

// CredentialSpec 该底座的**凭据交付面**（2026-10-10 拍板：原生凭据文档优先，env 兜底）。
//
// 这是 adapter 声明在 `Spec` 上的**纯数据**——注意它与 `Syncer`（可执行对象）刻意分开：
// `Spec` 是声明式半场，会被打印、被 `/meta` 与诊断读出、被测试断言；混入可执行对象会让
// 「描述」与「行为」同源（ADR-2）。
//
// 密钥值由 `Set.Providers[i].APIKey` 进 adapter——因为「按底座的文档格式写进去」只有 adapter
// 知道怎么做。平台不再替它注入 env，除非该底座没声明（或未实测）原生文档。
//
// **二选一语义（已实测，不是理论）**：NativeDoc 与 env 通道不得同时生效。dsh 0.2.1 的优先级是
// 「存储文档 > 继承环境」（0.2.0 是 env 赢）——两条同时启用时总有一条是静默死值：用户轮换了
// 一条、另一条还在，生效的仍是旧值，且没有任何报错。所以编排层按 `Tested` 二选一。
//
// **凭据引用名必须逐供应商**（`RefPattern` 而不是一个常量名）：见 `Provider.Ref` 的注释。
type CredentialSpec struct {
	// NativeDoc 原生凭据文档路径，**占位符形态**（dsh：`${DSH_HOME}/.credentials.yaml`；
	// 空 = 不支持）。占位符由编排层解析成绝对路径后放进 `Set.CredentialDocPath`。
	NativeDoc string
	// RefPattern **逐供应商**的凭据引用名模板，`%s` = 供应商 id 的规范化形式
	// （例：`MATE_%s_API_KEY` → `MATE_MY_GATEWAY_7F3A2B91_API_KEY`）。
	//
	// 为什么不用 `OPENAI_API_KEY` 这类"知名"名：① 逐供应商必须唯一（见上）；
	// ② 实测 env 优先于文档，而知名名很容易出现在用户的 shell 里 → 静默遮蔽。
	RefPattern string
	// FallbackEnv 是否允许把同一名字当 env 通道用（dsh 实测后=false：0.2.1 起它的原生文档
	// 优先于继承环境，同时启用会让 env 变成静默死值。详见该 adapter 的 Credential 声明）。
	FallbackEnv bool
	// Tested 原生通道是否已端到端实测；false → 必须走 env 并如实报 untested。
	Tested bool
	// TestedOnVersion 实测的载荷版本（dsh 侧 = "0.2.1-rc.2"；版本一变必须重测）。
	TestedOnVersion string
	// Note 实测结论与已知边界（写进诊断面，避免下一个人重新踩）。
	Note string
}

// RefSuffixLen 引用名里哈希后缀的长度（截断的 id 摘要）。
const RefSuffixLen = 8

// RefFor 按 `CredentialSpec.RefPattern` 派生某个供应商的凭据引用名。
//
// 规范化两步（ADR-16）：
//
//  1. 把 id 里所有非 `[A-Za-z0-9]` 换成 `_` 并整体大写。供应商 id 的词法已受限
//     （`^[A-Za-z0-9._-]{1,64}$`），但 `.` / `-` / `_` 三种字符都会映到 `_` —— 这个方向**会碰撞**；
//  2. 因此再拼一个**原 id 的摘要前 8 位**（大写十六进制），保证转换是单射。
//
// 结果是「同 id 同引用名、不同 id 不同引用名」——后者正是 A12 的判据（同一引用名命中两条
// route 即失败）。pattern 里没有 `%s` 时原样返回（adapter 自己负责，编排层不猜）。
func RefFor(pattern, providerID string) string {
	if pattern == "" {
		return ""
	}
	if !strings.Contains(pattern, "%s") {
		return pattern
	}
	norm := normalizeRefToken(providerID)
	sum := sha256.Sum256([]byte(providerID))
	suffix := strings.ToUpper(hex.EncodeToString(sum[:]))[:RefSuffixLen]
	return fmt.Sprintf(pattern, norm+"_"+suffix)
}

// normalizeRefToken 把供应商 id 折成引用名里能用的词法（非字母数字 → `_`，其余大写）。
func normalizeRefToken(id string) string {
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
