package manager

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/store"
	goruntime "github.com/morehao/ark-mate/pkg/runtime"
)

// 本文件是 `mechanism/doctor` 的**模型事实来源**端口在会话/装配侧的实现（P1a-1b-2）。
//
// 判断在机制层（`doctor.CollectRuntimeModelFacts`：怎么读状态、差集怎么算、"无法校验"与
// "一条都没生效"怎么区分）、数据在这里：那四件事各自要会话侧的状态（同步结果记录、目录缓存、
// 编排器、平台清单的可用性标志，以及不透明值的派生规则）。
//
// 于是"造一个只读 Service 去读数"这段逃生门**留在这一层**（拥有实现的那一层），机制层只认端口
// ——与 §3.1.33 的读模型重建端口同一手法。

// providerModelFacts 把 `*Service` 的读数面收窄成 `doctor.ModelFactsSource`。
//
// 为什么是包装而不是给 `*Service` 加四个同名方法：端口的方法名是**消费方**的词汇
// （`RuntimeStates`/`DeclaredModels`/`Advertised`/`ModelValue`），给 Service 加同名方法会让
// 实现侧凭空多出四个只有体检用的公开方法，而且将来要改端口就得改 Service。包装只在接线处存在。
type providerModelFacts struct{ svc *Service }

func (p providerModelFacts) RuntimeStates(ctx context.Context) map[string]dtomodel.RuntimeSyncResp {
	return p.svc.RuntimeConfigStates(ctx)
}

func (p providerModelFacts) DeclaredModels(ctx context.Context) ([]dtomodel.ModelResp, error) {
	return p.svc.ListModels(ctx)
}

func (p providerModelFacts) Advertised(ctx context.Context) (doctor.AdvertisedSample, error) {
	sample, err := p.svc.advertisedSample(ctx)
	if err != nil {
		return doctor.AdvertisedSample{}, err
	}
	// `Known()` 的来源词汇（session|cache|resume|none）是本侧的；端口只带判定结果过去。
	return doctor.AdvertisedSample{
		Runtime: sample.Runtime, Models: sample.Models,
		Known: sample.Known(), Note: sample.Note,
	}, nil
}

func (p providerModelFacts) ModelValue(runtimeID, providerID, model string) (string, error) {
	return modelValueFor(p.svc.reg, runtimeID, providerID, model)
}

// collectRuntimeModelFacts 逐 runtime 采集模型清单事实（**只读**）。
//
// 这里构造一个只读的 Service（不拨号、不落盘）：物化状态与清单读数必须与 `GET /models` 走同一条
// 取值面——自己再拼一套 SQL 等于把"看到的状态不是真的"引回来。三处只为读而做的事，逐条写下理由：
//
//	`ApplyIsolationEnv(home)`：落点声明里的 `${...}` 占位符只认该 runtime 自己的 Launch.Env
//	  （fail-closed）。不注入的话 `ResolveConfigProjection` 会报错、
//	  于是所有 runtime 都从报告里**静默消失**——那是最坏的假绿形态。
//	`EnableStore(st)`：把 `meta.runtime_config_digest` 读进内存（否则"已物化"会被报成"从未物化"）。
//	  它只碰内存与注册表，不写盘。
//	**不**走 `LoadProviderDefinitionsFromStore`：那条路会经 `loadProviders` 读凭据面与库，
//	  而体检要的是"清单事实"，不是"装载档案"（装载会在缺镜像时按库重建镜像——那是落笔）。
//	  这里只装载每个 runtime 的清单。
//
// 判断本身在机制层（`doctor.CollectRuntimeModelFacts`）——本函数只负责把数据面接上去。
func collectRuntimeModelFacts(ctx context.Context, st *store.Store, home string) []doctor.ProviderRuntimeFacts {
	out := []doctor.ProviderRuntimeFacts{}
	if st == nil {
		return out
	}
	reg := goruntime.AllBuiltinRegistry()
	reg.ApplyIsolationEnv(strings.TrimSpace(home))
	svc := NewService(reg, nil)
	defer svc.coalescer.Close()
	svc.EnableStore(st)

	return doctor.CollectRuntimeModelFacts(ctx, providerModelFacts{svc: svc})
}

// TODO(P4b): 删转发（测试直接依赖 doctor.RuntimeModelFacts + 端口）
//
// runtimeModelFacts 单个 runtime 的清单事实（判断在机制层；这里只为**会话侧的真实夹具**保留
// 入口——那个夹具要真 Service + 真编排器 + 伪造的运行时目录，属于本侧的内部测试）。
func (s *Service) runtimeModelFacts(ctx context.Context, runtimeID string, state dtomodel.RuntimeSyncResp) doctor.ProviderRuntimeFacts {
	return doctor.RuntimeModelFacts(ctx, providerModelFacts{svc: s}, runtimeID, state)
}
