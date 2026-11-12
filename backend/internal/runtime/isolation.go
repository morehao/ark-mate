package runtime

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ——— runtime 子进程的配置隔离（M3-3；设计 §6.6 / §6.6.1）———
//
// 为什么需要这一层：三端（CLI / 浏览器 / 桌面）共用同一份数据目录，但各底座默认把 home / config
// 放在**用户自己装的那份**的位置（~/.dsh、~/.config/opencode、~/.npm……）。不重定向就会串味：
// 平台拉起的底座读到用户自己的配置（行为不可复现、用户改了这边就变），用户手装的那份也可能被平台
// 改写。隔离因此落在**配置面**而不是环境面（设计 §6.4）：只重定向这些 home / cache / prefix 变量，
// 不重写 HOME / PATH，用户工具链（git / uv / 代理 / SSH_AUTH_SOCK）照旧可见。
//
// **本文件不认识任何具体底座**（本次收口）：变量名与落点是**载荷实测值**，属于该 adapter 的私有知识，
// 由它在自己的 `Spec` 汇入点（`internal/infra/<id>/adapter.go` 里的 `Spec()`，如 dsh 的 `dsh.Spec()`）以 `Spec.Isolation`
// 声明，父包只做机械投影。新增一个 runtime 因此不需要动本文件。
//
// 也不"多注几个以防万一"：多注一个不存在的变量不会报错，只会静默失效，还会让"已经隔离了"变成假象。
// 底座换名/升级时的正确做法是重新核对并更新**它自己那份声明**。

// commonIsolationVars 所有**本地子进程** runtime 共用的隔离面。
//
// npm 面（npm_config_cache / npm_config_prefix）：MCP server 常经 npx 拉起，不隔离就会写用户自己的
// ~/.npm。它对未知 id（远程端点、用户自定义 runtime）也照给——那正是下面 IsolationEnv 的兜底面。
//
// 两条 `LauncherFallback: false`：壳直接跑随包载荷时不经过 npx，所以私有启动器不必兜底
// （登记在**声明里**而不是 archtest 的豁免表里——见 IsolationVar.LauncherFallback）。
var commonIsolationVars = []IsolationVar{
	{Key: "npm_config_cache", Kind: IsolationPath, Path: []string{"npm", "cache"}},
	{Key: "npm_config_prefix", Kind: IsolationPath, Path: []string{"npm", "global"}},
}

// IsolationEnv 返回 runtimeID 的子进程要叠加的配置隔离环境（KEY=VALUE；语义同 LaunchSpec.Env：
// 叠加在 os.Environ 之上）。**纯函数**：无 IO、无全局状态，取值只看入参——装机期与单测共用同一份定义。
//
// 规则：
//   - 公共 npm 面永远给（`commonIsolationVars`）；
//   - 内置 runtime 再加上**它自己声明的** `Spec.Isolation`（见 adapters.go 的汇入点）；
//   - 其它 id（远程端点、用户自定义 runtime）：只给 npm 面，不臆造 runtime 专属变量。
//
// **按 id 而不是按传入的 Spec 取声明**（刻意的 fail-safe）：隔离的失败半径是"去读写用户真实目录"，
// 所以只要 id 是已知底座就一律隔离，不因为某处**手搓了一个没带声明的 Spec**（测试与诊断里很常见、
// 生产装配路径由 `ProductRegistry` 统一构造）而静默退化。远程端点与内置 id 的冲突已由
// `ProductRegistry` 在装配期拒绝，不存在"外部声明冒用内置 id"这条路。
//
// home trim 后为空 → 返回 nil：宁可不注入，也不要给一堆 "/runtime/dsh" 这样的**半截路径**——
// 那会落到进程 CWD 下，把状态散到不可预测的地方。
//
// 返回**已排序**：环境数组会被拼进启动路径、可能进日志/指纹，同参数必须逐字可复现
// （Go 的 map 迭代顺序随机，那种不确定性在这里是不可接受的）。
func IsolationEnv(spec Spec, home string) []string {
	return IsolationEnvFor(spec, home)
}

// IsolationEnvFor 返回**这一份 Spec** 的子进程要叠加的配置隔离环境。
//
// 与 `IsolationEnv(runtimeID, home)` 只差"声明从哪来"：
//   - 该 Spec **自己声明了** `Isolation` → 以它为准（∪ 公共 npm 面）；
//   - 一条都没声明 → 回落成 `IsolationEnv(spec.ID, home)`（按 id 查内置清单）。
//
// 为什么需要这一档（2026-10-12，合成 adapter 夹具逼出来的）：`ApplyIsolationEnv` 是**遍历注册表**
// 逐个底座注入的，它手上就有那份 Spec——再按 id 回查一次，等于把"注册表里这份 Spec 说了什么"丢掉。
// 于是**任何不在 `AllBuiltinSpecs()` 里的 adapter**（测试夹具，以及下一个底座在进内置清单之前的形态）
// 声明的隔离变量都会被**静默忽略**；而隔离的失败半径恰恰是"去读写用户真实目录"，
// 这是这里最坏的一种失败方式（参见 §6.6 的 2026-10-09 实测）。
//
// 回落那一档刻意保留：`IsolationEnv` 的 fail-safe 语义不变——手搓一个没带声明的
// `Spec{ID:"dsh"}`（测试与诊断里很常见）仍然按**内置 dsh** 隔离，不因为漏了声明就退化成"不隔离"。
// 反过来，**显式声明了**就以这份 Spec 为准：那是调用方明说的事实，不该被回查覆盖
// （合成底座声明的 `RTX_HOME` 与内置 id 无关，回查只会得到 npm 面）。
func IsolationEnvFor(spec Spec, home string) []string {
	vars := append(append([]IsolationVar{}, commonIsolationVars...), spec.Isolation...)
	return projectIsolation(vars, home)
}

// projectIsolation 把一组声明投影成 KEY=VALUE（已排序）。
//
// 同键**先出现者胜**：公共 npm 面在前，于是 adapter 若重复声明了 npm 面，以公共那份为准
// （内置 adapter 的重复声明已由 `TestIsolationDeclarationsAreWellFormed` 在编译期挡掉；
// 这一层是给不走那条判据的 Spec——测试夹具——兜底的，重复键会让子进程实际取哪个不可预期）。
func projectIsolation(vars []IsolationVar, home string) []string {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	env := make([]string, 0, len(vars))
	seen := make(map[string]bool, len(vars))
	for _, v := range vars {
		key := strings.TrimSpace(v.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		env = append(env, key+"="+v.value(home))
	}
	sort.Strings(env)
	return env
}

// value 该声明的取值（home 已 trim 且非空）。
func (v IsolationVar) value(home string) string {
	switch v.Kind {
	case IsolationLiteral:
		return v.Value
	case IsolationEmpty:
		return ""
	default: // IsolationPath（含零值 Kind：声明必须显式写 Kind，由 TestIsolationDeclarationsAreWellFormed 钉住）
		return filepath.Join(append([]string{home}, v.Path...)...)
	}
}

// IsolationLauncherFallbackKeys 该 runtime 的隔离变量里**桌面私有启动器也必须兜底**的键（升序）。
//
// 它把"哪些变量归启动器兜底"从 archtest 的手抄豁免表变成**声明的投影**：
// `TestDesktopLauncherIsolationMatchesCore` 因此可以判"启动器解析出的键集合 == 这份集合"（双向），
// 而不是"子集 + 一张可能漏登记的豁免表"。核心新增一条隔离变量时，两侧必须一起决定它归哪一边。
//
// 未注册的 id 返回 nil（公共 npm 面两条都是核心专供）。
func IsolationLauncherFallbackKeys(spec Spec) []string {
	out := make([]string, 0, len(spec.Isolation))
	for _, v := range spec.Isolation {
		if v.LauncherFallback {
			out = append(out, strings.TrimSpace(v.Key))
		}
	}
	sort.Strings(out)
	return out
}

// ValidateIsolation 校验一份隔离声明是否良构（装机期 fail-closed；由 TestIsolationDeclarationsAreWellFormed 对
// 全部内置 adapter 跑一遍）。
//
// 为什么要有它：三字段形态下"Kind 忘了写"或"Path 与 Value 同时给了"编译期无感，运行期的表现是
// **静默注错值**——`IsolationPath` 的零值会让一条 literal 声明被拼成 `<home>/true` 这样的怪路径，
// 而底座拿到它只会安静地写到别处。宁可当场报错。
func ValidateIsolation(runtimeID string, vars []IsolationVar) error {
	seen := map[string]bool{}
	for _, v := range vars {
		key := strings.TrimSpace(v.Key)
		if key == "" {
			return fmt.Errorf("runtime %q: 隔离声明缺 Key", runtimeID)
		}
		if seen[key] {
			return fmt.Errorf("runtime %q: 隔离变量 %s 重复声明（子进程实际取哪个不可预期）", runtimeID, key)
		}
		seen[key] = true
		switch v.Kind {
		case IsolationPath:
			if len(v.Path) == 0 {
				return fmt.Errorf("runtime %q: %s 的 Kind=path 但 Path 为空", runtimeID, key)
			}
			if strings.TrimSpace(v.Value) != "" {
				return fmt.Errorf("runtime %q: %s 的 Kind=path 不该同时给 Value", runtimeID, key)
			}
			for _, seg := range v.Path {
				if strings.TrimSpace(seg) == "" || filepath.IsAbs(seg) || strings.ContainsAny(seg, `/\`) {
					return fmt.Errorf("runtime %q: %s 的 Path 段 %q 非法（须是非空相对段，不允许分隔符）", runtimeID, key, seg)
				}
			}
		case IsolationLiteral:
			if strings.TrimSpace(v.Value) == "" {
				return fmt.Errorf("runtime %q: %s 的 Kind=literal 但 Value 为空（空值请用 Kind=empty）", runtimeID, key)
			}
			if len(v.Path) != 0 {
				return fmt.Errorf("runtime %q: %s 的 Kind=literal 不该同时给 Path", runtimeID, key)
			}
		case IsolationEmpty:
			if len(v.Path) != 0 || strings.TrimSpace(v.Value) != "" {
				return fmt.Errorf("runtime %q: %s 的 Kind=empty 不该同时给 Path/Value", runtimeID, key)
			}
		default:
			return fmt.Errorf("runtime %q: %s 的 Kind 取值非法（%q；须显式写 path|literal|empty）", runtimeID, key, v.Kind)
		}
	}
	return nil
}

// ApplyIsolationEnv 把隔离声明投影后叠加到注册表内各 runtime 的 Launch.Env，返回**实际注入**的
// runtime id（升序，供装机期日志说明隔离落在哪几个底座上）。
//
// 三个刻意的选择：
//  1. 远程端点（Transport 非空且不是 stdio）**整份跳过**：它不拉子进程，Launch.Env 在 Connect 的
//     远程分支里根本不会被读——往里塞值只会让 spec 看起来配了却没生效。远程端点因此连 npm 面都不注入
//     （IsolationEnv 对未知 id 本来就只给 npm 面，但没有子进程时那同样是死配置）。
//  2. 已有键**不覆盖**（"没有才追加"）：适配器声明或 provider 档案/用户显式给的取值优先。隔离是
//     "没配才兜底"的默认值，不是强制改写——踩掉显式配置等于"我明明设了却按你的来"。
//  3. 幂等：第二次调用不会再注入任何键（全部已存在 → 返回空切片），重复装配不会让环境数组翻倍。
//
// 必须在**装机期**（注册表装配时、任何拨号之前）调用：注册表按指针共享给 manager，运行期改它会让
// "已连上的进程"与"新拨的进程"拿到不同的 spec。
func (r *Registry) ApplyIsolationEnv(home string) []string {
	if r == nil {
		return nil
	}
	var applied []string
	for id, spec := range r.specs {
		if !isLocalLaunch(spec) {
			continue
		}
		// 用**注册表里这份 Spec** 自己的声明（`IsolationEnvFor`），不是按 id 回查：
		// 回查会把"这份 Spec 说了什么"丢掉，于是不在内置清单里的 adapter 声明的隔离变量
		// 被静默忽略——见 IsolationEnvFor 的注释。
		add := IsolationEnvFor(spec, home)
		if len(add) == 0 {
			continue
		}
		merged := mergeEnv(spec.Launch.Env, add)
		if len(merged) == len(spec.Launch.Env) {
			// 所有键都已显式存在：这不算"注入过"，别在日志里谎报。
			continue
		}
		spec.Launch.Env = merged
		r.specs[id] = spec
		applied = append(applied, spec.ID)
	}
	// map 迭代无序 → 返回值排序，日志与测试才有稳定输出。
	sort.Strings(applied)

	// **落点要等隔离 env 应用之后才可解析**，所以同步实现在这里再登记一次。
	//
	// 时序事实（`NewAdapterRegistry` / `ProductRegistry` 都跑在装配最前面）：`Spec.ModelConfig.Root`
	// 里的 `${VAR}` 占位符（如 dsh 的 `${DSH_HOME}`）认的正是本函数注入的那些变量。构造注册表时
	// 那次登记因此**必然解析失败**（进程环境里没有它们，而解析刻意不回落进程环境——回落到用户
	// 自己的 home 是隔离纪律的硬红线）。所以装机期调用本函数之后重跑一次登记：
	// `Adapter.RegisterPorts` 幂等（同 ID 覆盖），解析不了只会 `continue`。
	// 少了这一步，`sync[]` 会永远如实报 `unsupported`——用户看到"保存成功"而底座永远拿不到清单。
	for _, a := range r.adapters {
		a.RegisterPorts(r)
	}
	return applied
}

// isLocalLaunch 该 spec 是否经**本地子进程**启动：空 / "stdio" 是本地，其余（http/ws/…）走远程端点
// （分类口径与 Connect 一致）。远程端点没有子进程，Launch.Env 不会被读。
func isLocalLaunch(spec Spec) bool {
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "", "stdio":
		return true
	default:
		return false
	}
}

// mergeEnv 把 add 追加到 base 之后，**同键不覆盖**（base 里已有的键视为显式配置，优先级更高）。
//
// 比较键统一转大写：Windows 的环境变量名大小写不敏感，`npm_config_cache` 与 `NPM_CONFIG_CACHE`
// 同时出现时子进程实际取哪个是不可预期的——按大写归一后，已有显式键不会被再叠一份。
func mergeEnv(base, add []string) []string {
	if len(add) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base)+len(add))
	for _, kv := range base {
		seen[envKeyName(kv)] = true
	}
	out := append([]string(nil), base...)
	for _, kv := range add {
		name := envKeyName(kv)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, kv)
	}
	return out
}

// envKeyName 取 `KEY=VALUE` 的键名（大写归一，供跨平台比较）。
func envKeyName(kv string) string {
	name, _, _ := strings.Cut(kv, "=")
	return strings.ToUpper(strings.TrimSpace(name))
}
