package doctor

import (
	"context"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/contract/dtomodel"
	goruntime "github.com/morehao/ark-mate/internal/runtime"
)

// ——— 逐 runtime 的模型清单事实（体检的"清单同步"那一段） ———
//
// 分工（与读模型那一段、以及 §3.1.33 的端口同一手法）：
//
//	**判断在机制层**：物化状态怎么读、差集怎么算、"无法校验"与"一条都没生效"怎么区分、
//	  哪些形态算高危——这些是判据，与谁提供数据无关。
//	**数据在会话/装配侧**：同步状态、平台声明清单、运行时广告目录、不透明值派生规则，
//	  四件都由调用方经 `ModelFactsSource` 交下来。
//
// 于是本包**不再 `NewService`**："造一个只读 Service 去读数"这件事留在拥有实现的那一层
// （`internal/manager`），机制层只认端口。

// ProviderRuntimeFacts 某 runtime 的清单同步事实（状态 + 对账）。
//
// v12 起"能不能写"这件事不再由平台门控（没有 `ConfigProjection.Tested`/`Writable` 了）：
// 交付结论由 adapter 回带，平台如实转述。所以这一段的字段就是**端口结论的直投影**。
type ProviderRuntimeFacts struct {
	Runtime string `json:"runtime"`
	// Status applied | unchanged | unsupported | untested | failed。
	Status string `json:"status"`
	// Where 人话落点（"同步到哪了"）。
	Where string `json:"where,omitempty"`
	// Effect 清单通道的生效时刻（immediate | next_run | next_session | next_process）。
	Effect string `json:"effect,omitempty"`
	// CredentialChannel native_doc | env | none（凭据通道与清单分开报，ADR-14）。
	CredentialChannel string `json:"credentialChannel,omitempty"`
	// CredentialTested 该凭据通道是否已实测（false → 界面上只能说「待实测」）。
	CredentialTested bool `json:"credentialTested"`
	// Pending true = 平台已交付但底座还没读到（**不许说成"已生效"**）。
	Pending bool `json:"pending"`
	// ModelCount 平台声明的清单条数。
	ModelCount int `json:"modelCount"`
	// DeclaresModelInput 该底座的清单交付面是否声明模型的输入模态（ADR-12）。
	//
	// 它由各 adapter 自己声明（例如 dsh=true：平台总是写 `models[].input`；有的底座 false：
	// 平台不写、底座自己知道）。这一段存在的理由就是**如实区分两侧**：同一份平台清单在不同
	// 底座上的实际行为不同，用户不该以为自己勾的能力一定生效（在 false 的那一侧它只是平台记录）。
	DeclaresModelInput bool `json:"declaresModelInput"`
	// Note 人话说明（未同步过、未声明交付面、逐条 problem…）。
	Note string `json:"note,omitempty"`

	// AdvertisedKnown 本次是否真的取到了运行时广告的目录（离线命令 = false）。
	AdvertisedKnown bool `json:"advertisedKnown"`
	// AdvertisedCount 运行时广告的模型候选数（AdvertisedKnown=false 时无意义）。
	AdvertisedCount int `json:"advertisedCount"`
	// Missing 平台声明了、底座没广告（"写了没生效"）的条目。
	Missing []string `json:"missing,omitempty"`
	// DiffNote 差集不可得的原因（AdvertisedKnown=false 时必填）。
	DiffNote string `json:"diffNote,omitempty"`
	// HighRisk **写了完全没生效**的形态：declared 非空而 advertised 一条都没有。
	HighRisk bool `json:"highRisk"`
}

// AdvertisedSample 一次运行时目录取样（**事实一侧**），并带上"这份取样到底可不可信"。
//
// 为什么要把来源判定一起交下来：空目录有**两种完全不同的含义**，只看长度分不开——
// 没有来源（离线进程没有编排器 / 还没有活跃会话）→ **无法校验**；真的读到了目录 → 它就是
// "底座此刻广告了什么"。把两者混成一个"空"会在对账上造出两个方向的谎：对 `mate doctor` 是
// **假红**（把"没取到"报成"写了完全没生效"），对 `GET /models` 是**假绿**（把"没取到"报成
// "两侧一致"）。
//
// `Known` 由取样方（会话侧，它认得自己的来源词汇）给出；本包不再持有一份来源枚举。
type AdvertisedSample struct {
	// Runtime 本次取样面对的 runtime（差集比对要用它把平台声明折成不透明值）。
	Runtime string
	// Models 运行时不透明值（category=model 的全量候选，按 value 升序）。
	Models []dtomodel.AdvertisedModel
	// Known 本次取样是否真的读到了运行时目录。
	Known bool
	// Note 取样面给的人话说明（Known=false 时就是"为什么没有"）。
	Note string
}

// ModelFactsSource 采集"逐 runtime 清单事实"所需的**数据面**——四件都由会话/装配侧提供。
//
// 为什么是端口而不是把这些读数搬进来：那四件事各自都要会话侧的状态（同步结果记录、目录缓存、
// 编排器、平台清单的可用性标志，以及不透明值的派生规则）。机制层只该知道"能读到这四样"。
type ModelFactsSource interface {
	// RuntimeStates 各 runtime 的清单同步状态（键 = runtime id）。
	RuntimeStates(ctx context.Context) map[string]dtomodel.RuntimeSyncResp
	// DeclaredModels 平台声明的模型清单（与 `GET /models` 同一条取值面）。
	DeclaredModels(ctx context.Context) ([]dtomodel.ModelResp, error)
	// Advertised 取构建期固定那个底座的运行时目录样本（没有执行面时返回 Known=false 的样本）。
	Advertised(ctx context.Context) (AdvertisedSample, error)
	// ModelValue 平台声明的 (provider, model) → 该 runtime 认得的不透明值。
	// **派生规则只有一份实现**（在会话侧，与 adapter 声明同源）：这里只消费它。
	ModelValue(runtimeID, providerID, model string) (string, error)
}

// CollectRuntimeModelFacts 逐 runtime 采集模型清单事实（只读）。
//
// 体检刻意用**全部在册底座**（装配根交下来的声明）而不是产品装配用的 `ProductRegistry`：
// doctor 回答的是"**这台机器上**有哪些底座的配置物化痕迹 / 敏感文件"，不是"本二进制只认哪一个"。
// 构建期把产品钉在 dsh 上，并不代表磁盘上不再有另一份底座的遗留物——那恰恰是体检该如实报出来的
// 东西（换成 ProductRegistry 会让它们整段消失，表现为"报告一切正常"，而磁盘上的东西一个没少）。
func CollectRuntimeModelFacts(ctx context.Context, src ModelFactsSource, specs []goruntime.Spec) []ProviderRuntimeFacts {
	out := []ProviderRuntimeFacts{}
	if src == nil {
		return out
	}
	states := src.RuntimeStates(ctx)
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, RuntimeModelFacts(ctx, src, specs, id, states[id]))
	}
	return out
}

// RuntimeModelFacts 单个 runtime 的清单 + 物化状态 + 对账（**只读**）。
//
// 导出是为了让"高危"那条判据能被**真实夹具**（带编排器的 rig + 伪造的运行时目录）端到端跑到
// ——`CollectRuntimeModelFacts` 里的数据面天然没有编排器，永远走 unknown 分支。那个夹具在
// 会话侧（它要真 Service），所以它经这个入口调本函数。
func RuntimeModelFacts(ctx context.Context, src ModelFactsSource, specs []goruntime.Spec, runtimeID string, state dtomodel.RuntimeSyncResp) ProviderRuntimeFacts {
	facts := ProviderRuntimeFacts{
		Runtime: runtimeID,
		// 该底座声明不声明输入模态：取它自己的 Spec 字段（**不是**任何静态能力基线）。
		DeclaresModelInput: DeclaresModelInput(specs, runtimeID),
		Status:             state.Status,
		Where:              state.Where,
		Effect:             state.Effect,
		CredentialChannel:  state.Credential.Channel,
		CredentialTested:   state.Credential.Tested,
		Pending:            state.Pending,
		Note:               strings.Join(state.Problems, "；"),
	}
	if declared, err := src.DeclaredModels(ctx); err != nil {
		facts.DiffNote = "读平台清单失败：" + err.Error()
		return facts
	} else {
		facts.ModelCount = len(declared)
	}

	// 对账只对**构建期固定那个底座**有意义：别的 runtime 没有执行面（读不到广告），如实报
	// "无法校验"，而不是拿选中底座的目录去比对另一张表。
	if runtimeID != goruntime.SelectedRuntime() {
		facts.DiffNote = UnknownModelDiffNote("该 runtime 不是本二进制构建期固定的那个，本进程没有它的执行面")
		return facts
	}
	sample, err := src.Advertised(ctx)
	switch {
	case err != nil:
		facts.DiffNote = "运行时目录读不到（" + err.Error() + "）：**无法校验**差集（既不是空、也不是不符）"
	case !sample.Known:
		// 没有来源（离线命令没有编排器 / 没有会话）：**不许**当成"底座一条都没有"。
		facts.DiffNote = UnknownModelDiffNote(sample.Note)
	default:
		facts.AdvertisedKnown = true
		facts.AdvertisedCount = len(sample.Models)
		// 高危：**写了完全没生效**（声明了 N 条、底座一条都没广告）。
		facts.HighRisk = facts.ModelCount > 0 && facts.AdvertisedCount == 0
		facts.Missing = MissingDeclaredModels(ctx, src, sample)
	}
	return facts
}

// DeclaresModelInput 该 runtime 的 Spec 是否声明"平台总是写模型的输入模态"。
//
// 未注册的 runtime（远程端点没在这个二进制里）→ false：**不许**把"没见过"当成"会写"。
// findSpec 按 id 找声明（找不到 = false）。
func findSpec(specs []goruntime.Spec, id string) (goruntime.Spec, bool) {
	for _, s := range specs {
		if s.ID == id {
			return s, true
		}
	}
	return goruntime.Spec{}, false
}

func DeclaresModelInput(specs []goruntime.Spec, runtimeID string) bool {
	spec, ok := findSpec(specs, runtimeID)
	if !ok {
		return false
	}
	return spec.DeclaresModelInput
}

// MissingDeclaredModels 声明了却没出现在运行时广告里的候选（机器面，人话化）。
//
// 判据是**同一份生成规则**（会话侧的 `ModelValue` 端口）：平台声明 → 该 runtime 认得的不透明值，
// 再与广告集合比对。两处各写一遍迟早分叉。
func MissingDeclaredModels(ctx context.Context, src ModelFactsSource, sample AdvertisedSample) []string {
	declared, err := src.DeclaredModels(ctx)
	if err != nil {
		return nil
	}
	advertised := map[string]bool{}
	for _, m := range sample.Models {
		advertised[m.Value] = true
	}
	var out []string
	for i := range declared {
		v, err := src.ModelValue(sample.Runtime, declared[i].ProviderID, declared[i].Model)
		if err != nil || v == "" {
			continue
		}
		if !advertised[v] {
			out = append(out, declared[i].ProviderID+"/"+declared[i].Model)
		}
	}
	sort.Strings(out)
	return out
}

// UnknownModelDiffNote 差集取不到事实一侧时的说明（**"无法校验"**，不是任何一侧为空）。
//
// 为什么把这句话集中在一处：它是本段最重要的一条纪律的落点——离线命令没有编排器/会话，
// 运行时广告取不到；这时既不能报"两侧一致"（假绿），也不能报"一条都没生效"（假红）。
func UnknownModelDiffNote(reason string) string {
	if strings.TrimSpace(reason) == "" {
		reason = "本次没有取到运行时广告的目录"
	}
	return "**无法校验**：" + reason +
		"（离线体检取不到运行时广告——这既不是「两侧一致」，也不是「一条都没生效」；" +
		"发一条消息或跑 `GET /models?refresh=1` 才有事实一侧）"
}
