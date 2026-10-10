package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/pkg/runtime/dsh"
)

// ——— 构建期固定底座（产品约束的唯一落点） ———
//
// 产品不再让用户在 agent 定义 / 自动化 / REST 里选择底座：**跑在哪个 runtime 上是二进制的属性**。
// 三条纪律：
//
//  1. 它是**编译期**的（`-X` 注入），不是配置、不是环境变量、不是进程参数——进程起来之后改不了；
//  2. 不注入 = 默认 dsh（`go build` / `go test` / 本地 `go run` 与引入本文件之前逐字一致）；
//  3. 注入一个未注册的 id 必须**启动即失败**，不许得到一个"能启动但没底座"的进程
//     （那种形态会把构建期错误伪装成每个会话各自的 `runtime_not_found`）。

// DefaultBuildRuntime 未注入时的默认底座：deepseek-harness（dsh）。
const DefaultBuildRuntime = dsh.ID

// BuildRuntime 构建期注入的底座 id（**不是配置、不是环境变量**）：
//
//	go build -ldflags "-X github.com/morehao/ark-mate/pkg/runtime.BuildRuntime=opencode"
//
// 空 = 用 DefaultBuildRuntime。用户面（REST / 控制台）读不到它——这是"用户不能选择 runtime"
// 那条产品约束的落点。
var BuildRuntime = ""

// SelectedRuntime 归一后的构建期底座 id（永不为空）。
func SelectedRuntime() string {
	if id := strings.TrimSpace(BuildRuntime); id != "" {
		return id
	}
	return DefaultBuildRuntime
}

// AllBuiltinSpecs 全部内置 runtime 的静态描述（不注册、不含 syncer）。
//
// 它服务三件事：① 测试夹具；② 诊断与**能力矩阵守卫**——那些守卫必须看到全部适配面，
// 否则"默认 dsh"的构建会让 opencode 的守卫静默放过（那正是本次改动最容易造成的假绿）；
// ③ ProductRegistry 的内置候选集。
func AllBuiltinSpecs() []Spec {
	return []Spec{opencodeSpec(), dshSpec()}
}

// AllBuiltinRegistry 全部内置 runtime 的注册表（本文件引入之前 `DefaultRegistry` 的语义）。
//
// **它不是产品装配入口**：产品只装配构建期选中的那一个，见 ProductRegistry。保留它是为了
// 测试、诊断与守卫能一次看到全部适配面。
func AllBuiltinRegistry() *Registry {
	r := NewRegistry(AllBuiltinSpecs()...)
	registerBuiltinSyncers(r)
	return r
}

// ProductRegistry 产品装配：**只注册构建期选中的那一个**底座。
//
// 选中 id 的解析顺序：内置 spec → remotes 里同 id 的远程端点 → 都没有则 error。
// 于是"选中的是哪个 id"与"这个 id 的实现从哪来"是两件事：既可以是内置 adapter，也可以是
// `.ark-mate/config/runtimes.json` 里同 id 的远程 ACP 端点（A5 因此不会变成静默死码）。
//
// 为什么 fail-closed（而不是返回一个空注册表）：空注册表下进程照样启动、REST 照样回答，
// 只是每个会话各自报"runtime 未注册"——构建期的一个错字会伪装成运行期的一堆故障。
//
// remotes 由调用方（app 装配层）用 RemoteSpec 构造：本包不认识 config，只认识 Spec。
func ProductRegistry(remotes []Spec) (*Registry, error) {
	builtins := AllBuiltinSpecs()
	builtinIDs := make(map[string]bool, len(builtins))
	for _, s := range builtins {
		builtinIDs[s.ID] = true
	}
	// 远程声明与内置 id 冲突 → 启动即失败（不静默覆盖内置底座）。这条与"是否被选中"无关：
	// 它挡的是"配置文件里写了一个永远不该存在的键"。
	for _, s := range remotes {
		if builtinIDs[s.ID] {
			return nil, fmt.Errorf("registry: 远程 runtime %q 与内置 runtime 冲突（不静默覆盖内置底座）", s.ID)
		}
	}
	id := SelectedRuntime()
	r := NewRegistry()
	for _, s := range builtins {
		if s.ID == id {
			r.Register(s)
			// 注册 syncer 的时机与 AllBuiltinRegistry 一致：此刻隔离 env 还没注入，
			// ResolveConfigLocation 大概率解析不了 → 不登记；随后 ApplyIsolationEnv 会再调一次。
			registerBuiltinSyncers(r)
			return r, nil
		}
	}
	for _, s := range remotes {
		if s.ID == id {
			r.Register(s)
			return r, nil
		}
	}
	return nil, fmt.Errorf(
		"registry: 构建期指定的 runtime %q 未注册（内置 %v；远程声明 %v）：检查 -ldflags 注入的 "+
			"pkg/runtime.BuildRuntime 与 .ark-mate/config/runtimes.json",
		id, sortedKeys(builtinIDs), remoteIDs(remotes))
}

// remoteIDs 远程声明的 id 列表（升序，稳定输出）。
func remoteIDs(remotes []Spec) []string {
	out := make([]string, 0, len(remotes))
	for _, s := range remotes {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

// sortedKeys map 的键升序（稳定输出）。
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
