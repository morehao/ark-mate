// Package runtimetest 提供一个**完全合成**的 runtime adapter（id = `rtx`），
// 它是「adapter 层还在」这句话的**可执行证据**。
//
// # 它证明什么
//
// 产品的验收口径是：**接一个新底座 = 新建一个目录 + 写一个 `xxxSpec()` + 挂两个端口实现，
// 不需要动框架/内核的任何文件**。这个口径如果只用"我们保留了目录结构"来支撑，是**口号**——
// 没有任何东西会因为它已经失效而变红（框架里悄悄长回一个 `switch runtimeID` 照样编译、照样绿）。
//
// 所以这里放一个**框架完全不认识**的底座：它不在 `AllBuiltinSpecs()` 里、不在任何隔离表的
// Go 侧声明里、不在前端启动器的表里，内核与 `pkg/runtime` 都从未听说过 `rtx`。
// 只要 `internal/manager` 的 `TestSyntheticAdapterIsFirstClass` 还能带着它跑完整条链路
// （隔离投影 → 拉起子进程 → 原生权限注入 → 清单端口被调用），
// 「adapter 层能承载一个没被硬编码过的底座」这件事就是**被机器执行过**的，而不是被声明的。
//
// # 这也是下一个底座的骨架
//
// 要接一个真底座，把本文件里的五样东西各抄一份到 `pkg/runtime/<新底座>/`：
//
//	Spec（`xxxSpec()` 的等价物，落进 pkg/runtime/adapters.go）—— 纯声明：隔离变量、
//	    投影、清单落点、凭据面、MCP 传输、能力基线；
//	NativePermission（可执行端口，见 pkg/runtime/native_permission.go）；
//	Syncer（可执行端口，见 pkg/resource 的契约五条）；
//	ModelValue（可执行端口，见 pkg/runtime/model_value.go）；
//	以及"把哪些 id 登记进注册表"那一段（`registerBuiltinPorts` 加一个 case）。
//
// **"要抄几样"就是这套 adapter 层的全部接口面**：每多一张注册表，接一个底座的成本就多一样。
// 所以加表之前先问"这一维真的不能进 `Spec` 吗"（`Spec` 侧只需纯数据，那是最便宜的形态）。
//
// 刻意**不**引入任何具体的 adapter 子包：夹具一旦依赖 `dsh`，它就变成了"第二个 dsh"，
// 也就重新失去了"框架没见过它"这个唯一的证明力。这条由 `archtest` 的
// `TestInternalDoesNotImportRuntimeAdapters` 自动钉住（本包在 `internal/**` 里）。
package runtimetest

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/platform/resource"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ID 合成底座的注册表键。
//
// 刻意选一个**任何真实底座都不会用**的名字：判据要能区分"内核按 id 特判了 dsh"与
// "内核真的走了端口"，所以它必须不可能撞上内置清单。
const ID = "rtx"

// 合成底座自己的私有词表。三个键都**不能**与 dsh / opencode 的重名——
// 重名会让"内核没按名字特判"这条判据失去区分度。
const (
	// PermissionEnvKey 原生权限变量名。
	PermissionEnvKey = "RTX_PERMISSION_MODE"
	// InlineConfigKey 内联配置变量名（清单通道）。
	InlineConfigKey = "RTX_CONFIG_CONTENT"
	// HomeKey 该底座的 home 变量名（隔离面）。
	HomeKey = "RTX_HOME"
	// CacheKey 一个**只由核心隔离**的通用面（`LauncherFallback` 为 false），
	// 用来证明三份隔离表的一致性判据认得"核心专供"这一档。
	CacheKey = "RTX_PLUGIN_CACHE"
)

// PermissionModeFull 合成底座自己的"完全放开"取值。
//
// 它**故意**不等于 dsh 的 `danger-full-access`：内核把一个 mode 原样转发过来，翻译成底座词表
// 是 adapter 的事——本条判据会拿这个非 dsh 取值做断言。
const PermissionModeFull = "rtx-unrestricted"

// Spec 等价于框架里各 adapter 的 `xxxSpec()`：这个底座在注册表里的**全部声明**（纯数据）。
//
// launch 由调用方给（真底座的 Command/Args 是它自己的知识；合成底座的"可执行文件"则是
// 调用方的桩命令），其余全部由本夹具声明——这正是"新底座要写的东西"的形状。
func Spec(launch runtime.LaunchSpec) runtime.Spec {
	return runtime.Spec{
		ID:      ID,
		Name:    "RTX 合成底座",
		Version: "rtx synthetic acp",
		Launch:  launch,
		Caps: runtime.Capabilities{
			SessionsPersistent:     true,
			MultiSessionConcurrent: true,
		},
		// 隔离声明：一条底座专属（要启动器兜底）+ 一条核心专供。
		//
		// 后者（CacheKey）刻意**不**让桌面启动器兜底：它是通用面，壳里改它会连累用户其它工具。
		// 一个只有"要兜底"这一档的夹具测不出这一档的存在。
		Isolation: []runtime.IsolationVar{
			{Key: HomeKey, Kind: runtime.IsolationPath, Path: []string{"runtime", "rtx"}, LauncherFallback: true},
			{Key: CacheKey, Kind: runtime.IsolationPath, Path: []string{"cache", "rtx-plugins"}},
		},
		Description:       "合成底座（只存在于测试夹具里；用来证明 adapter 层不依赖任何具体底座）",
		SetupSteps:        []string{"本底座是测试夹具：没有安装步骤"},
		Notes:             "由 internal/testutil/runtimetest 声明；刻意不出现在 AllBuiltinSpecs() 里",
		Projections: []runtime.Projection{{
			Kind:   runtime.ProjectionSkill,
			Root:   "${HOME}/.agents/skills",
			Layout: "<id>/SKILL.md",
			Format: capability.FormatSkillMDFrontmatter,
			Tested: true,
			Note:   "合成底座：投影形状照 dsh 的扁平模板（夹具证明的是机制，不是某个底座的实测结论）",
		}},
		// 清单落点：根取**本底座自己的** home 变量（证明占位符解析走的是该 runtime 的隔离 env，
		// 而不是内核里写死的某个名字）。文件名与凭据文档名都刻意避开内置底座的名字。
		ModelConfig: runtime.ModelConfigLocation{
			Root:          "${" + HomeKey + "}",
			Filename:      "mate-rtx-models.json",
			CredentialDoc: ".rtx-credentials.json",
		},
		// 凭据交付面：合成底座走 **env 通道**（`FallbackEnv: true` + 空的 `NativeDoc`）。
		//
		// 这一维不是可选的装饰：内核的 `providerEnvFor` 按**这一份声明**决定"注入 env 还是写
		// 原生文档"，零值 = 该底座不注入任何密钥。随 opencode 下线，仓库里已没有第二个走 env
		// 通道的真实 adapter，于是"env 通道"这条链路今天**只由本夹具覆盖**——它一旦缺了这一项，
		// 相关用例会从"验 env 通道"静默退化成"验空操作"。
		Credential: resource.CredentialSpec{
			RefPattern:  "MATE_%s_API_KEY",
			FallbackEnv: true,
			Tested:      false,
		},
		ModelConfigWriter: runtime.StaticModelConfig{DeclaresInput: true},
		// 能力面（C3 4.7 策略接口）：只认 stdio、上下文自管、绑定粒度保守 process（未实测）。
		Capability: runtime.StaticCapability{
			Transports: []string{"stdio"},
			OwnContext: true,
			Binding:    runtime.CapabilityBinding{Scope: runtime.CapabilityScopeProcess},
		},
	}
}

// ModelValue 合成底座的「模型值投影」实现（见 `pkg/runtime/model_value.go` 的端口契约）。
//
// 形态刻意与 dsh 不同（`<provider>/<model>` vs JSON 数组）：判据要能区分"内核按 id 挑了一个
// 形态"与"内核把活儿交给了 adapter"——两边形态一样的话，内核里那段 `switch runtimeID` 塌成
// 一条直路也不会红。两处用同一个分隔符只是巧合，不是契约。
type ModelValue struct{}

// Value 把平台清单里的一条编成 `<provider>/<model>`（本合成底座自己的词表）。
//
// 返回错误的那个分支在这里是**可达的**（分隔符出现在 id 里就编不出来）——它不是装饰：端口契约
// 要求"编不出来"必须如实交出去，而不是静默拼出一个含歧义的串。
func (ModelValue) Value(providerID, model string) (string, error) {
	if strings.ContainsAny(providerID+model, "/") {
		return "", fmt.Errorf("合成底座的值形态用 %q 作分隔符，而 provider=%q model=%q 里就有它："+
			"这一条无法无歧义地表示", "/", providerID, model)
	}
	return providerID + "/" + model, nil
}

// NativePermission 合成底座的原生权限投影（结构化满足 `runtime.NativePermissionAdapter`）。
//
// 语义刻意与 dsh 不同，用来证明内核**没有**预设"原生面只有模式这一维"：
//   - mode 为空/"auto" → 不注入（与 dsh 相同的那半）；
//   - 显式 mode → 注入 PermissionEnvKey，**并且**把逐工具项也投影进内联配置（dsh 不做这件事）。
type NativePermission struct{}

// Env 见 `pkg/runtime/native_permission.go` 的端口契约。
func (NativePermission) Env(mode, def string, tools map[string]string) (env []string, desc string) {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" || m == "auto" {
		return nil, "RTX 的原生面只认显式 mode（auto/" + strings.ToLower(strings.TrimSpace(def)) + " 不注入）"
	}
	keys := make([]string, 0, len(tools))
	for k := range tools {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env = []string{PermissionEnvKey + "=" + m}
	note := PermissionEnvKey + "=" + m
	if len(keys) > 0 {
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+strings.ToLower(strings.TrimSpace(tools[k])))
		}
		env = append(env, InlineConfigKey+"="+strings.Join(parts, ","))
		note += " + " + InlineConfigKey + "（逐工具项 " + strings.Join(keys, ",") + "）"
	}
	return env, note
}

// Syncer 合成底座的清单交付端口（记录调用，便于断言"控制流真的走到了端口"）。
//
// 它自己带一个可观察的副作用（`Calls` / `Last`），因为"端口被调用了"与"端口写对了字节"
// 是两件事，而前者是内核的责任（契约③：空集也必须调用）。
type Syncer struct {
	Calls int
	Last  resource.Set
	// Fail 非空时让端口报错（用来证明失败被如实上报，而不是被吞掉）。
	Fail error
}

// SyncModels 见 `pkg/resource` 的 Syncer 契约。
func (s *Syncer) SyncModels(_ context.Context, set resource.Set) (resource.Outcome, error) {
	s.Calls++
	s.Last = set
	if s.Fail != nil {
		return resource.Outcome{}, s.Fail
	}
	// 空集也是正常路径（契约③）：合成底座"撤回"的形态就是什么都不注入。
	status := resource.StatusApplied
	where := "合成底座内联配置（" + InlineConfigKey + "）"
	if len(set.Models) == 0 && len(set.Providers) == 0 {
		status = resource.StatusUnchanged
		where = "合成底座：空集已撤回"
	}
	return resource.Outcome{Status: status, Where: where, Effect: resource.EffectNextProcess}, nil
}

// Register 把合成底座登记进一个**已存在**的注册表：一份 Spec + 三张执行表各一条。
//
// 为什么需要它（而不是让调用方自己 `reg.Register(Spec(...))`）：那样得到的底座**只有声明、
// 没有执行半场**，表现为"这个底座没有原生权限开关 / 清单交付 unsupported / 模型值形态未声明"
// ——三条都是**静默**的假阴性（`runtime.RegisterBuiltinPorts` 的注释里写着同一件事）。
// 产品装配走 `RegisterBuiltinPorts`，夹具侧的对应物就是本函数：**声明与端口必须同批登记**。
//
// 它**没有**走 `runtime.RegisterBuiltinPorts`——那正是要点：合成底座的端口由它自己登记，
// 框架不需要为它"加一个 case"以外的任何改动。
func Register(reg *runtime.Registry, launch runtime.LaunchSpec) *Syncer {
	syncer := &Syncer{}
	reg.Register(Spec(launch))
	reg.RegisterSyncer(ID, syncer)
	reg.RegisterNativePermission(ID, NativePermission{})
	reg.RegisterModelValue(ID, ModelValue{})
	return syncer
}

// NewRegistry 装配一个**只有这个合成底座**的注册表（`runtime.NewRegistry` + `Register` 的组合）。
func NewRegistry(launch runtime.LaunchSpec) (*runtime.Registry, *Syncer) {
	reg := runtime.NewRegistry()
	syncer := Register(reg, launch)
	return reg, syncer
}

// Describe 给出一行"这个夹具是什么"的说明（诊断/日志用；也方便断言失败时读懂上下文）。
//
// 一并列出**三张执行表各自的本底座入口名**：断言失败时最要紧的一点是"这份声明到底覆盖了哪几条
// 链路"——只列隔离面会让人以为它只证明了隔离。
func Describe() string {
	return fmt.Sprintf("合成底座 %s（隔离 %s/%s，原生权限 %s，清单通道 %s，凭据 env 通道 MATE_%%s_API_KEY，模型值 <provider>/<model>）",
		ID, HomeKey, CacheKey, PermissionEnvKey, InlineConfigKey)
}
