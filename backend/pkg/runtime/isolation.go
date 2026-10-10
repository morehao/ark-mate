package runtime

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/pkg/runtime/dsh"
	"github.com/morehao/ark-mate/pkg/runtime/opencode"
)

// ——— runtime 子进程的配置隔离（M3-3；设计 §6.6 / §6.6.1）———
//
// 为什么需要这一层：三端（CLI / 浏览器 / 桌面）共用同一份数据目录，但各底座默认把 home / config
// 放在**用户自己装的那份**的位置（~/.dsh、~/.config/opencode、~/.npm……）。不重定向就会串味：
// 平台拉起的底座读到用户自己的配置（行为不可复现、用户改了这边就变），用户手装的那份也可能被平台
// 改写。隔离因此落在**配置面**而不是环境面（设计 §6.4）：只重定向这些 home / cache / prefix 变量，
// 不重写 HOME / PATH，用户工具链（git / uv / 代理 / SSH_AUTH_SOCK）照旧可见。
//
// 变量名是**载荷实测值**，不是猜的（设计 §6.6.1 逐条记录了出处：dsh 读 JS 源码，opencode 对二进制
// 做 `strings`）。也不"多注几个以防万一"：多注一个不存在的变量不会报错，只会静默失效，还会让
// "已经隔离了"变成假象。底座换名时的正确做法是重新核对并更新这张表。

// IsolationEnv 返回 runtimeID 的子进程要叠加的配置隔离环境（KEY=VALUE；语义同 LaunchSpec.Env：
// 叠加在 os.Environ 之上）。**纯函数**：无 IO、无全局状态，取值只看入参——装机期与单测共用同一份定义。
//
// 规则：
//   - 所有本地 runtime 都给 npm 面（npm_config_cache / npm_config_prefix）：MCP server 常经 npx 拉起，
//     不隔离就会写用户自己的 ~/.npm；
//   - dsh（dsh）：只给 DSH_HOME。**刻意不给 DSH_AGENTS_HOME**——dsh 的 skill
//     provider 按 `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents` 解析 agents 根，
//     而平台把 skill 投射到 `${HOME}/.agents/skills`（见 dshSkillProjection）。
//     一旦注入 DSH_AGENTS_HOME，**读根就与写根分叉**，skill 静默不生效（2026-10-09 E5 实测：
//     注入后 dsh 会话的 `<available_skills>` 里没有投影根下那条 skill，只有注入根下的）。
//     历史：本表曾按"agents 面也要隔离"注入它，那是把"隔离用户既有配置"错用到了**平台自己的
//     投放面**上——这条根是**用户全局共享**的（opencode 也读它），不是底座私有配置。
//   - opencode：四个 XDG 变量 + OPENCODE_CONFIG_DIR + OPENCODE_DB（它没有 agents 根这类变量，
//     故它与投影根天然一致——dsh 侧修完两侧才对称）；
//   - 其它 id（远程端点、用户自定义 runtime）：只给 npm 面，不臆造 runtime 专属变量。
//
// home trim 后为空 → 返回 nil：宁可不注入，也不要给一堆 "/runtime/dsh" 这样的**半截路径**——
// 那会落到进程 CWD 下，把状态散到不可预测的地方。
//
// 返回**已排序**：环境数组会被拼进启动路径、可能进日志/指纹，同参数必须逐字可复现
// （Go 的 map 迭代顺序随机，那种不确定性在这里是不可接受的）。
func IsolationEnv(runtimeID, home string) []string {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	env := make([]string, 0, 7)
	env = append(env,
		"npm_config_cache="+filepath.Join(home, "npm", "cache"),
		"npm_config_prefix="+filepath.Join(home, "npm", "global"),
	)
	switch runtimeID {
	case dsh.ID:
		env = append(env, "DSH_HOME="+filepath.Join(home, "runtime", "dsh"))
	case opencode.ID:
		env = append(env,
			"XDG_CONFIG_HOME="+filepath.Join(home, "runtime", "opencode", "config"),
			"XDG_DATA_HOME="+filepath.Join(home, "runtime", "opencode", "data"),
			"XDG_CACHE_HOME="+filepath.Join(home, "runtime", "opencode", "cache"),
			// XDG_STATE_HOME 是 opencode v2 新增的**服务发现面**：后台服务的登记文件是
			// `<XDG_STATE_HOME 或 ~/.local/state>/opencode/service.json`（对 2.0.26 的二进制取
			// strings：`process.env.XDG_STATE_HOME ?? join(homedir(),".local","state")`）。
			// 不隔离它，平台拉起的 acp 会**读用户自己的 service.json**，命中同版本同 url 时直接
			// 复用用户的服务，版本不符时更会走"替换"分支（SIGTERM 旧进程 + 删登记文件）——
			// 那是"mate 动了本机"最直接的一种。隔离后平台得到自己的服务登记。
			"XDG_STATE_HOME="+filepath.Join(home, "runtime", "opencode", "state"),
			"OPENCODE_CONFIG_DIR="+filepath.Join(home, "runtime", "opencode", "config", "opencode"),
			"OPENCODE_DB="+filepath.Join(home, "runtime", "opencode", "data", "opencode.db"),

			// ——— F3：模型清单（T5）的隔离前提 ———
			//
			// opencode 的配置优先级有八层，而**平台写的 global 层（②）是文件系统面里最低的一层**，
			// 上面还有四层能被"用户的环境变量"或"用户仓库里的文件"顶掉。三个必堵：
			//
			//	层③ `OPENCODE_CONFIG`（显式文件，高于 global 文件）：用户设了它，平台那份就被顶掉。
			//	层⑥ `OPENCODE_CONFIG_CONTENT`（内联内容，高于平台写的**任何**文件）：平台自己的
			//	     T5 主通道就是它（物化时覆盖这一条），所以这里先**清空**，避免沿用用户 shell 里的值。
			//	层④⑤ 项目级发现会**走到文件系统根**，而平台拉底座的 cwd 是用户自己的仓库——不关掉，
			//	     一个 clone 下来的仓库就能改平台拉起的底座行为（模型 / 权限 / MCP / plugins / skills）。
			//
			// 空值语义：JS 侧 `process.env.X` 为 `''` 是 falsy，等价于"没设"——而"没设"正是我们要的
			// （不覆盖、不回落到用户的值）。`true` 那一条是开关键。
			//
			// 两个可选（让 `advertised` 可复现：对账要求"同一份清单跑两次结果一样"）：
			// 清掉用户对 models.dev 缓存路径与来源 URL 的覆盖，目录来源因此只由隔离后的
			// `XDG_CACHE_HOME` 决定。
			//
			// **刻意不注 `OPENCODE_DISABLE_MODELS_FETCH`**（设计里列为可选）：关掉拉取后，若本地没有
			// 缓存的 models.dev 目录、而平台又还没声明任何模型，opencode 的目录会**空**——而空目录不是
			// "没模型"而是"起不了会话"（`loadCatalog` 重试 5s 后抛 `No models are available`，事实 #47）。
			// 那比"advertised 不可复现"严重得多，所以这条开关留给"平台清单非空"之后再评估。
			"OPENCODE_CONFIG=",
			"OPENCODE_CONFIG_CONTENT=",
			"OPENCODE_CONFIG_PROJECT_DISABLE=true",
			"OPENCODE_MODELS_PATH=",
			"OPENCODE_MODELS_URL=",
		)
	}
	sort.Strings(env)
	return env
}

// ApplyIsolationEnv 把 IsolationEnv 叠加到注册表内各 runtime 的 Launch.Env，返回**实际注入**的
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
		add := IsolationEnv(spec.ID, home)
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
	// 时序事实（`ProductRegistry` / `AllBuiltinRegistry` 都跑在装配最前面）：`Spec.ModelConfig.Root`
	// 用的是 `${OPENCODE_CONFIG_DIR}` / `${DSH_HOME}`，而这两个变量正是本函数注入的。构造注册表时
	// 那次登记因此**必然解析失败**（进程环境里没有它们，而解析刻意不回落进程环境——回落到用户
	// 自己的 home 是隔离纪律的硬红线）。所以装机期调用本函数之后重跑一次登记：
	// `registerBuiltinSyncers` 幂等（`RegisterSyncer` 同 ID 覆盖），解析不了只会 `continue`。
	// 少了这一步，`sync[]` 会永远如实报 `unsupported`——用户看到"保存成功"而底座永远拿不到清单。
	registerBuiltinSyncers(r)
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
