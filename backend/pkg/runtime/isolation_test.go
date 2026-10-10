package runtime

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/runtime/dsh"
	"github.com/morehao/ark-mate/pkg/runtime/opencode"
)

// TestIsolationEnvBuiltinRuntimes 逐变量钉住两个内置 runtime 的隔离注入表（设计 §6.6.1）。
//
// 每条都写死而不是遍历返回值做宽松断言：变量名与落点是**实测契约**，底座换名（升级后重新核对）
// 必须让这条测试红，而不是被"至少有个 DSH_HOME 就行"放过——注错的变量不会报错，只会静默失效。
func TestIsolationEnvBuiltinRuntimes(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		runtimeID string
		want      []string
	}{
		{
			runtimeID: dsh.ID,
			want: []string{
				"DSH_HOME=" + filepath.Join(home, "runtime", "dsh"),
				"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
				"npm_config_prefix=" + filepath.Join(home, "npm", "global"),
			},
		},
		{
			runtimeID: opencode.ID,
			want: []string{
				// F3：配置层③⑥与"关项目级发现"三个必堵键 + 两个可复现键。
				// 空值 = "等价于没设"（JS 侧 falsy），`true` 是开关键——它们**不是路径**，
				// 所以下面 underHome 的断言对它们单独放行（见该循环的例外表）。
				"OPENCODE_CONFIG=",
				"OPENCODE_CONFIG_CONTENT=",
				"OPENCODE_CONFIG_DIR=" + filepath.Join(home, "runtime", "opencode", "config", "opencode"),
				"OPENCODE_CONFIG_PROJECT_DISABLE=true",
				"OPENCODE_DB=" + filepath.Join(home, "runtime", "opencode", "data", "opencode.db"),
				"OPENCODE_MODELS_PATH=",
				"OPENCODE_MODELS_URL=",
				"XDG_CACHE_HOME=" + filepath.Join(home, "runtime", "opencode", "cache"),
				"XDG_CONFIG_HOME=" + filepath.Join(home, "runtime", "opencode", "config"),
				"XDG_DATA_HOME=" + filepath.Join(home, "runtime", "opencode", "data"),
				// v2 的服务登记面（`<home>/opencode/service.json`）：不隔离就会读/替换用户自己的服务。
				"XDG_STATE_HOME=" + filepath.Join(home, "runtime", "opencode", "state"),
				"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
				"npm_config_prefix=" + filepath.Join(home, "npm", "global"),
			},
		},
	}
	for _, tc := range cases {
		got := IsolationEnv(tc.runtimeID, home)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s 的隔离环境不符:\n got %v\nwant %v", tc.runtimeID, got, tc.want)
		}
		for _, kv := range got {
			key, val, _ := strings.Cut(kv, "=")
			// 例外表：这三个键的**正确取值不是路径**——空值表示"清掉用户的同名变量"（等价于没设），
			// `true` 是开关键。它们不落任何目录，所以不适用"值必须在数据目录之下"这条。
			switch key {
			case "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_MODELS_PATH", "OPENCODE_MODELS_URL":
				if val != "" {
					t.Fatalf("%s 的 %s 应被清空（用户设了它会顶掉平台那份配置），实际 %q", tc.runtimeID, key, val)
				}
				continue
			case "OPENCODE_CONFIG_PROJECT_DISABLE":
				if val != "true" {
					t.Fatalf("%s 的 %s 应为 true，实际 %q", tc.runtimeID, key, val)
				}
				continue
			}
			if val == "" {
				t.Fatalf("%s 的 %s 值不应为空（空值等于没隔离）", tc.runtimeID, key)
			}
			if !underHome(home, val) {
				t.Fatalf("%s 的值 %q 不在数据目录 %q 之下", tc.runtimeID, val, home)
			}
		}
		// 返回顺序必须确定：同参数两次调用逐字相等，且已排序（环境数组会拼进启动路径/可能进日志）。
		if !sort.StringsAreSorted(got) {
			t.Fatalf("%s 的返回未排序: %v", tc.runtimeID, got)
		}
		if again := IsolationEnv(tc.runtimeID, home); !reflect.DeepEqual(again, got) {
			t.Fatalf("%s 两次调用结果不同: %v vs %v", tc.runtimeID, got, again)
		}
	}
}

// TestIsolationEnvUnknownRuntimeOnlyNpm 其它 id（远程端点、用户自定义 runtime）只给 npm 面：
// 不臆造 runtime 专属变量。多注一个不存在的变量不会报错，只会静默失效，还会让"隔离已生效"变成假象。
func TestIsolationEnvUnknownRuntimeOnlyNpm(t *testing.T) {
	home := t.TempDir()
	want := []string{
		"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
		"npm_config_prefix=" + filepath.Join(home, "npm", "global"),
	}
	for _, id := range []string{"remote-echo", "自定义", "OpenCode", "DSH"} {
		got := IsolationEnv(id, home)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q 应只给 npm 面: got %v want %v", id, got, want)
		}
		for _, kv := range got {
			name, _, _ := strings.Cut(kv, "=")
			if name != "npm_config_cache" && name != "npm_config_prefix" {
				t.Fatalf("%q 混入了 runtime 专属变量: %q", id, kv)
			}
		}
	}
}

// TestIsolationEnvEmptyHome home 为空（含仅空白）→ nil：不注入半截路径。
//
// 半截路径（`/runtime/dsh`）会落到进程 CWD 下，把底座状态散到不可预测的地方——宁可不注入。
// 前后空白不算"有值"：trim 后判空，免得拼出带空格的落点。
func TestIsolationEnvEmptyHome(t *testing.T) {
	for _, home := range []string{"", "   ", "\t", "\n  "} {
		if got := IsolationEnv(opencode.ID, home); got != nil {
			t.Fatalf("home=%q 应返回 nil，got %v", home, got)
		}
		if got := IsolationEnv(dsh.ID, home); got != nil {
			t.Fatalf("home=%q 应返回 nil，got %v", home, got)
		}
	}
	if got, want := IsolationEnv(opencode.ID, "  /tmp/mate-home  "), IsolationEnv(opencode.ID, "/tmp/mate-home"); !reflect.DeepEqual(got, want) {
		t.Fatalf("前后空白应被去掉: %v vs %v", got, want)
	}
}

// TestApplyIsolationEnv 装机期的叠加：本地子进程 runtime 拿到隔离环境，远程端点不注入，
// 已有键不被覆盖，且重复调用幂等。
func TestApplyIsolationEnv(t *testing.T) {
	home := t.TempDir()
	reg := NewRegistry(
		Spec{ID: opencode.ID, Name: "opencode", Launch: LaunchSpec{Command: "opencode", Args: []string{"acp"}}},
		Spec{ID: dsh.ID, Name: "dsh", Launch: LaunchSpec{Command: "dsh"}},
		Spec{ID: "custom-acp", Name: "custom", Launch: LaunchSpec{Command: "custom-acp"}},
		// 远程端点：transport 非 stdio → 不拉子进程，Launch.Env 不会被读，故整份不注入。
		Spec{ID: "remote-ws", Launch: LaunchSpec{Transport: "ws", Endpoint: "ws://127.0.0.1:1/acp"}},
	)
	// 未装配前内置 spec 本身不带隔离（注入是装机期动作，不是 adapter 声明的一部分）。
	if spec, _ := reg.Get(opencode.ID); len(spec.Launch.Env) != 0 {
		t.Fatalf("装配前不应带隔离 env: %v", spec.Launch.Env)
	}

	applied := reg.ApplyIsolationEnv(home)
	if want := []string{"custom-acp", dsh.ID, opencode.ID}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("实际注入的 runtime 不符: got %v want %v", applied, want)
	}
	for _, id := range []string{opencode.ID, dsh.ID, "custom-acp"} {
		spec, _ := reg.Get(id)
		if want := IsolationEnv(id, home); !reflect.DeepEqual(spec.Launch.Env, want) {
			t.Fatalf("%s 的 Launch.Env 不符:\n got %v\nwant %v", id, spec.Launch.Env, want)
		}
	}
	if spec, _ := reg.Get("remote-ws"); len(spec.Launch.Env) != 0 {
		t.Fatalf("远程端点不应注入任何 env: %v", spec.Launch.Env)
	}
	// 幂等：再装配一次不该翻倍（全部键已存在 → 没有 runtime 被算作"注入过"）。
	if again := reg.ApplyIsolationEnv(home); len(again) != 0 {
		t.Fatalf("重复装配不应再注入: %v", again)
	}
}

// TestApplyIsolationEnvKeepsExplicitKeys 已有键不被覆盖：适配器/provider 档案显式给的取值优先，
// 其余隔离键照常补上（"没有才追加"）。
func TestApplyIsolationEnvKeepsExplicitKeys(t *testing.T) {
	home := t.TempDir()
	reg := NewRegistry(Spec{
		ID: opencode.ID,
		Launch: LaunchSpec{
			Command: "opencode",
			Env: []string{
				"XDG_CONFIG_HOME=/user/pinned/opencode", // 与隔离表同键 → 保留显式值
				"DEEPSEEK_API_KEY=sk-x",                 // 与隔离无关的键 → 原样保留
			},
		},
	})
	if applied := reg.ApplyIsolationEnv(home); !reflect.DeepEqual(applied, []string{opencode.ID}) {
		t.Fatalf("应注入 opencode: %v", applied)
	}
	spec, _ := reg.Get(opencode.ID)
	env := spec.Launch.Env
	if countKey(env, "XDG_CONFIG_HOME") != 1 {
		t.Fatalf("显式键应保留且不重复: %v", env)
	}
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "XDG_CONFIG_HOME=") && kv != "XDG_CONFIG_HOME=/user/pinned/opencode":
			t.Fatalf("显式键被覆盖: %q", kv)
		case kv == "DEEPSEEK_API_KEY=sk-x":
			// 保留即可
		}
	}
	if !contains(env, "DEEPSEEK_API_KEY=sk-x") {
		t.Fatalf("无关的显式键被丢掉: %v", env)
	}
	// 被显式覆盖之外的隔离键仍要补上（否则"有一个键存在"会让整份隔离静默失效）。
	for _, want := range []string{
		"OPENCODE_DB=" + filepath.Join(home, "runtime", "opencode", "data", "opencode.db"),
		"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
	} {
		if !contains(env, want) {
			t.Fatalf("缺失隔离键 %q: %v", want, env)
		}
	}
}

// TestApplyIsolationEnvEmptyHome home 为空时整份不注入（装机期没有数据目录 → 不给半截路径）。
func TestApplyIsolationEnvEmptyHome(t *testing.T) {
	reg := NewRegistry(Spec{ID: opencode.ID, Launch: LaunchSpec{Command: "opencode"}})
	if applied := reg.ApplyIsolationEnv(""); len(applied) != 0 {
		t.Fatalf("空 home 不应注入: %v", applied)
	}
	if spec, _ := reg.Get(opencode.ID); len(spec.Launch.Env) != 0 {
		t.Fatalf("空 home 不应改动 Launch.Env: %v", spec.Launch.Env)
	}
}

// underHome 判断 p 是否落在 home 之下：用 filepath.Rel（而非字符串前缀）——
// 前缀判断会把 "/tmp/home-evil" 当成 "/tmp/home" 的子路径，那正是"隔离看起来生效、其实写到别处"的形态。
func underHome(home, p string) bool {
	rel, err := filepath.Rel(home, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// countKey 统计环境数组里某键出现次数（同键出现两次 = 子进程实际取哪个不可预期）。
func countKey(env []string, key string) int {
	n := 0
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name == key {
			n++
		}
	}
	return n
}

// contains 环境数组是否含某条完整条目。
func contains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// TestIsolationKeepsSkillRootAlignedWithProjection 隔离环境**不得**把底座的 skill 读根
// 从平台声明的投影根上撬走。
//
// 这不是风格问题而是功能问题：平台把 skill 写到 `${HOME}/.agents/skills`（两个 adapter 的 skill
// 投影根），而 dsh 的 agents 根按 `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents` 解析——
// 只要隔离里注入 `DSH_AGENTS_HOME`，**写根与读根就分叉**，skill 静默不生效（平台照旧的
// `in_sync`/`Tested: true` 还会说一切正常）。
//
// 2026-10-09 E5 实测（把 provider 指向本地假 LLM，抓 dsh 真实请求体）：注入时 dsh 会话的
// `<available_skills>` 里**没有**投影根下那条 skill、只有注入根下的；去掉注入后投影根那条回来了。
// 历史上本表注入过它（"agents 面也要隔离"），所以这里把它钉成不变量——同目录的
// TestIsolationEnvBuiltinRuntimes 只管"注了什么"，这条管"注了会不会砸到投放面"。
func TestIsolationKeepsSkillRootAlignedWithProjection(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		runtimeID string
		proj      Projection
		diverting []string // 会把该 runtime 的 skill 读根撬到他处的环境变量前缀
	}{
		{dsh.ID, dshSkillProjection(), []string{"DSH_AGENTS_HOME="}},
		{opencode.ID, opencodeSkillProjection(), nil},
	}
	for _, c := range cases {
		// 前提：投影根仍是"用户全局 agents 面"。哪天它挪到平台自己的目录，撬根的变量就
		// 未必是缺陷了——那时这条断言会红，提醒连带复核隔离表。
		if !strings.Contains(c.proj.Root, "/.agents/skills") {
			t.Fatalf("%s 的 skill 投影根不再是用户全局 agents 面（%q）：请连同隔离表与 E5 结论一起复核",
				c.runtimeID, c.proj.Root)
		}
		for _, kv := range IsolationEnv(c.runtimeID, home) {
			for _, bad := range c.diverting {
				if strings.HasPrefix(kv, bad) {
					t.Fatalf("隔离环境注入了 %s——它会让 %s 的 skill 读根与投影根 %s 分叉"+
						"（2026-10-09 E5 实测：dsh 会话的 <available_skills> 里看不到投影根下的 skill）",
						kv, c.runtimeID, c.proj.Root)
				}
			}
		}
	}
}
