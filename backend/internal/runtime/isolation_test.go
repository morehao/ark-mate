package runtime

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// altID / altSpec 测试用的**第二个** adapter（不是内置底座）。
//
// 多 adapter 面的判据（投影、显式键不覆盖、幂等、声明→投影对账）需要第二个对象，而它**不能**是
// 另一个内置底座：内置清单的增删不该让这些与框架有关的判据跟着变。于是这里自造一个——
// 同时它把「**Spec 自己声明**隔离变量」这条契约单独钉住（`IsolationEnvFor`），
// 那是"不在内置清单里的 adapter 也能声明隔离面"的唯一路径（见 isolation.go 的注释）。
const altID = "rt-alt"

func altSpec() Spec {
	return Spec{
		ID:      altID,
		Name:    "RT Alt",
		Version: "rt-alt synthetic acp",
		Launch:  LaunchSpec{Command: "rt-alt", Args: []string{"acp"}},
		Caps: Capabilities{
			SessionsPersistent:     true,
			MultiSessionConcurrent: true,
		},
		Isolation: []IsolationVar{
			{Key: "RT_ALT_HOME", Kind: IsolationPath, Path: []string{"runtime", "rt-alt"}, LauncherFallback: true},
			// 清空类（`IsolationEmpty`）：值必须是**空串**而不是某个兜底路径——
			// 底座会把一个目录当配置文件读（实测 EISDIR，2026-10-10）。
			{Key: "RT_ALT_INLINE", Kind: IsolationEmpty},
		},
		Description: "测试用的第二个 adapter（不参与产品装配）",
		// 传输面刻意与 dsh 不同（dsh 拒 sse/http，支持 streamable_http）：两端都有人代表，
		// 「按声明过滤 MCP 子集」这条判据才不是只在一个方向上成立。
		McpTransports:     []string{"stdio", "sse", "http"},
		ManagesOwnContext: true,
		Projections:       []Projection{altSkillProjection()},
		ModelConfig: ModelConfigLocation{
			Root:          "${" + "RT_ALT_HOME" + "}",
			Filename:      "rt-alt-models.json",
			CredentialDoc: ".rt-alt-credentials.json",
		},
		CapabilityScope: "process",
	}
}

// altSkillProjection 自造 adapter 的 skill 投影：形状照真实 adapter（扁平落点 +
// frontmatter），根取跨工具的 `~/.agents/skills`——投影根是**用户全局共享面**，不是底座私有的。
func altSkillProjection() Projection {
	return Projection{
		Kind:   ProjectionSkill,
		Root:   "${HOME}/.agents/skills",
		Layout: LayoutIDToken + "/SKILL.md",
		Format: "SKILL.md+frontmatter",
		Tested: true,
		Note:   "自造 adapter 的投影声明（测试夹具）：证明投影声明不依赖内置清单",
	}
}

// TestIsolationEnvBuiltinRuntimes 逐变量钉住两个内置 runtime 的隔离注入表（设计 §6.6.1）。
//
// 每条都写死而不是遍历返回值做宽松断言：变量名与落点是**实测契约**，底座换名（升级后重新核对）
// 必须让这条测试红，而不是被"至少有个 DSH_HOME 就行"放过——注错的变量不会报错，只会静默失效。
func TestIsolationEnvBuiltinRuntimes(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		runtimeID string
		spec      Spec
		want      []string
	}{
		{
			runtimeID: DefaultRuntimeID,
			// 带声明的 Spec（= 内置 adapter 的形状）。v3.1 起**声明是唯一事实源**：
			// 不带声明的手搓 Spec 只拿公共 npm 面（见 TestIsolationEnvFor... 的 ②）。
			spec: fixtureSpec(),
			want: []string{
				"DSH_HOME=" + filepath.Join(home, "runtime", "dsh"),
				"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
				"npm_config_prefix=" + filepath.Join(home, "npm", "global"),
			},
		},
		{
			runtimeID: altID,
			// 第二个对象**不是内置底座**（见 altSpec）：它证明"声明 → 投影"这条链对
			// 「Spec 自己带声明」的 adapter 同样成立，而不只对内置清单里那几个成立。
			spec: altSpec(),
			want: []string{
				"RT_ALT_HOME=" + filepath.Join(home, "runtime", "rt-alt"),
				// Kind=empty：正确取值就是空串（清掉用户 shell 里的同名变量，等价于没设）。
				"RT_ALT_INLINE=",
				"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
				"npm_config_prefix=" + filepath.Join(home, "npm", "global"),
			},
		},
	}
	for _, tc := range cases {
		got := IsolationEnvFor(tc.spec, home)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s 的隔离环境不符:\n got %v\nwant %v", tc.runtimeID, got, tc.want)
		}
		for _, kv := range got {
			key, val, _ := strings.Cut(kv, "=")
			// Kind=empty 的**正确取值就是空串**（清掉用户 shell 里的同名变量），它不落任何目录，
			// 所以不适用"值必须在数据目录之下"这条。除此之外一律要有非空的、落在数据目录下的值——
			// 空值或无关键的隔离等于没隔离。
			if key == "RT_ALT_INLINE" {
				if val != "" {
					t.Fatalf("%s 的 %s 应被清空（用户设了它会顶掉平台那份配置），实际 %q", tc.runtimeID, key, val)
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
		if again := IsolationEnvFor(tc.spec, home); !reflect.DeepEqual(again, got) {
			t.Fatalf("%s 两次调用结果不同: %v vs %v", tc.runtimeID, got, again)
		}
	}
}

// TestIsolationEnvForPrefersSpecDeclarations `IsolationEnvFor` 的两条语义必须同时成立，
// 它们是一对方向相反的纪律：
//
//	① **显式声明优先**：注册表里那份 Spec 说了什么就按什么隔离。丢了这一档，
//	   凡是不在 `fixtureSpecs()` 里的 adapter（自造夹具、下一个底座在进清单之前的形态）
//	   声明的隔离变量都会被**静默忽略**——而静默忽略的后果是去读写用户真实目录；
//	② **没声明就回落内置**：手搓一个 `Spec{ID:"dsh"}` 而忘了带声明（测试与诊断里很常见）
//	   仍然按内置 dsh 隔离，不因为漏了声明就退化成"只注 npm 面"。
func TestIsolationEnvForPrefersSpecDeclarations(t *testing.T) {
	home := t.TempDir()

	// ① 显式声明优先：id 是 dsh，但这份 Spec 自己声明了别的变量 → 以它为准。
	custom := Spec{ID: DefaultRuntimeID, Launch: LaunchSpec{Command: "dsh"}, Isolation: []IsolationVar{
		{Key: "RT_OVERRIDE_HOME", Kind: IsolationPath, Path: []string{"runtime", "override"}},
	}}
	got := IsolationEnvFor(custom, home)
	if contains(got, "DSH_HOME="+filepath.Join(home, "runtime", "dsh")) {
		t.Fatalf("这份 Spec 自己声明了隔离面，就不该再按内置 dsh 注入: %v", got)
	}
	if !contains(got, "RT_OVERRIDE_HOME="+filepath.Join(home, "runtime", "override")) {
		t.Fatalf("Spec 自己的隔离声明没被采纳（这正是「不在内置清单里的 adapter」会静默失去隔离的那条路）: %v", got)
	}

	// ② 没声明 → 只有公共 npm 面（v3.1：声明是唯一事实源，不再按 id 回查内置清单）。
	bare := Spec{ID: DefaultRuntimeID, Launch: LaunchSpec{Command: "dsh"}}
	wantCommon := []string{
		"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
		"npm_config_prefix=" + filepath.Join(home, "npm", "global"),
	}
	if got := IsolationEnvFor(bare, home); !reflect.DeepEqual(got, wantCommon) {
		t.Fatalf("没带声明的 Spec 只该拿到公共 npm 面: got %v want %v", got, wantCommon)
	}
	// 未知 id 且自带声明 → 自己的声明 + 公共 npm 面（不臆造、也不丢通用面）。
	unknown := altSpec()
	unknown.ID = "rt-unknown"
	gotUnknown := IsolationEnvFor(unknown, home)
	if !contains(gotUnknown, "RT_ALT_HOME="+filepath.Join(home, "runtime", "rt-alt")) ||
		!contains(gotUnknown, "npm_config_cache="+filepath.Join(home, "npm", "cache")) {
		t.Fatalf("未知 id 的声明与公共面应同时生效: %v", gotUnknown)
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
	for _, id := range []string{"remote-echo", "自定义", "RT-ALT", "DSH"} {
		got := IsolationEnv(Spec{ID: id}, home)
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
		if got := IsolationEnvFor(altSpec(), home); got != nil {
			t.Fatalf("home=%q 应返回 nil，got %v", home, got)
		}
		if got := IsolationEnv(Spec{ID: DefaultRuntimeID}, home); got != nil {
			t.Fatalf("home=%q 应返回 nil，got %v", home, got)
		}
	}
	if got, want := IsolationEnvFor(altSpec(), "  /tmp/mate-home  "), IsolationEnvFor(altSpec(), "/tmp/mate-home"); !reflect.DeepEqual(got, want) {
		t.Fatalf("前后空白应被去掉: %v vs %v", got, want)
	}
}

// TestApplyIsolationEnv 装机期的叠加：本地子进程 runtime 拿到隔离环境，远程端点不注入，
// 已有键不被覆盖，且重复调用幂等。
func TestApplyIsolationEnv(t *testing.T) {
	home := t.TempDir()
	reg := NewRegistry(
		// 自造 adapter（Spec 自己带隔离声明）。
		altSpec(),
		// 内置 id 但**手搓的 Spec 没带声明** → 必须回落成"按内置 dsh 隔离"（fail-safe）。
		Spec{ID: DefaultRuntimeID, Name: "dsh", Launch: LaunchSpec{Command: "dsh"}},
		Spec{ID: "custom-acp", Name: "custom", Launch: LaunchSpec{Command: "custom-acp"}},
		// 远程端点：transport 非 stdio → 不拉子进程，Launch.Env 不会被读，故整份不注入。
		Spec{ID: "remote-ws", Launch: LaunchSpec{Transport: "ws", Endpoint: "ws://127.0.0.1:1/acp"}},
	)
	// 未装配前内置 spec 的 `Launch.Env` 是空的：隔离是装机期动作，而 `Spec.Isolation` 只是**声明**
	// （声明 → 投影 → 叠加三段的中间那一步是可测的，见 TestIsolationDeclarationsAreWellFormed）。
	if spec, _ := reg.Get(altID); len(spec.Launch.Env) != 0 {
		t.Fatalf("装配前不应带隔离 env: %v", spec.Launch.Env)
	}

	applied := reg.ApplyIsolationEnv(home)
	if want := []string{"custom-acp", DefaultRuntimeID, altID}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("实际注入的 runtime 不符: got %v want %v", applied, want)
	}
	for _, id := range []string{altID, DefaultRuntimeID, "custom-acp"} {
		spec, _ := reg.Get(id)
		// 对账用 **IsolationEnvFor**（按这份 Spec 自己的声明）：这正是装配期实际走的那条路，
		// 用 IsolationEnv(id) 对账会掩盖"注册表里的 Spec 声明没被采纳"这一类失效。
		if want := IsolationEnvFor(spec, home); !reflect.DeepEqual(spec.Launch.Env, want) {
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
	spec0 := altSpec()
	spec0.Launch.Env = []string{
		"RT_ALT_HOME=/user/pinned/rt-alt", // 与隔离声明同键 → 保留显式值
		"DEEPSEEK_API_KEY=sk-x",           // 与隔离无关的键 → 原样保留
	}
	reg := NewRegistry(spec0)
	if applied := reg.ApplyIsolationEnv(home); !reflect.DeepEqual(applied, []string{altID}) {
		t.Fatalf("应注入 %s: %v", altID, applied)
	}
	spec, _ := reg.Get(altID)
	env := spec.Launch.Env
	if countKey(env, "RT_ALT_HOME") != 1 {
		t.Fatalf("显式键应保留且不重复: %v", env)
	}
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "RT_ALT_HOME=") && kv != "RT_ALT_HOME=/user/pinned/rt-alt":
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
		"RT_ALT_INLINE=",
		"npm_config_cache=" + filepath.Join(home, "npm", "cache"),
	} {
		if !contains(env, want) {
			t.Fatalf("缺失隔离键 %q: %v", want, env)
		}
	}
}

// TestApplyIsolationEnvEmptyHome home 为空时整份不注入（装机期没有数据目录 → 不给半截路径）。
func TestApplyIsolationEnvEmptyHome(t *testing.T) {
	reg := NewRegistry(altSpec())
	if applied := reg.ApplyIsolationEnv(""); len(applied) != 0 {
		t.Fatalf("空 home 不应注入: %v", applied)
	}
	if spec, _ := reg.Get(altID); len(spec.Launch.Env) != 0 {
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

// TestIsolationDeclarationsAreWellFormed 所有内置 adapter 的隔离声明必须良构。
//
// 为什么值得一条独立判据：三字段声明形态（Kind + Path/Value）下，"Kind 忘了写"或"Path 与 Value 同时给"
// 编译期无感，运行期的表现是**静默注错值**——`IsolationPath` 的零值会把一条 literal 声明拼成
// `<home>/true` 这样的怪路径，底座拿到它只会安静地写到别处，而外观上"隔离已生效"。
// 这里对**全部内置 adapter** 跑一遍（含尚未被产品选中的那些）：新增 adapter 时自动覆盖。
func TestIsolationDeclarationsAreWellFormed(t *testing.T) {
	// 内置清单 + 自造 adapter：后者保证"新 adapter 一进来就被这条判据覆盖"这件事不依赖
	// 内置清单里恰好有两个底座。
	specs := append(fixtureSpecs(), altSpec())
	if len(specs) < 2 {
		t.Fatal("扫描面少于 2 个 adapter：本判据已失效（fail-closed）")
	}
	total := 0
	for _, spec := range specs {
		if err := ValidateIsolation(spec.ID, spec.Isolation); err != nil {
			t.Errorf("隔离声明不良构：%v", err)
		}
		total += len(spec.Isolation)
	}
	if total == 0 {
		t.Fatal("全部内置 adapter 的隔离声明加起来是 0 条：要么全都漏声明了，要么判据扫错了地方")
	}
	// 与"实际生效的表"对账：声明里的每个键都必须真的出现在投影结果里（声明→投影这条链不许断在中间）。
	home := t.TempDir()
	for _, spec := range specs {
		got := map[string]bool{}
		for _, kv := range IsolationEnvFor(spec, home) {
			got[strings.SplitN(kv, "=", 2)[0]] = true
		}
		for _, v := range spec.Isolation {
			if !got[v.Key] {
				t.Errorf("%s 声明了隔离变量 %s，但投影结果里没有它（声明与投影已分叉）", spec.ID, v.Key)
			}
		}
	}
}

// TestIsolationLauncherFallbackKeysMatchDeclarations 「启动器兜底集合」必须逐条等于声明里
// `LauncherFallback: true` 的那些键。
//
// 它的消费者是 archtest 的 TestDesktopLauncherIsolationMatchesCore（把 JS 侧那张手抄表与它对比）。
// 这里先钉住 Go 侧自身的自洽：投影函数不得多一个、少一个，也不得漏掉排序。
func TestIsolationLauncherFallbackKeysMatchDeclarations(t *testing.T) {
	seenAny := false
	for _, spec := range fixtureSpecs() {
		var want []string
		for _, v := range spec.Isolation {
			if v.LauncherFallback {
				want = append(want, v.Key)
			}
		}
		sort.Strings(want)
		got := IsolationLauncherFallbackKeys(spec)
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		seenAny = true
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s 的启动器兜底键不符:\n got %v\nwant %v", spec.ID, got, want)
		}
	}
	if !seenAny {
		t.Fatal("没有任何 adapter 声明 LauncherFallback：桌面态的隔离兜底已无对象，判据失效（fail-closed）")
	}
	// 无声明的 Spec（远程端点/自定义 runtime）：公共 npm 面两条都是核心专供 → 空。
	if got := IsolationLauncherFallbackKeys(Spec{}); len(got) != 0 {
		t.Errorf("无声明的 Spec 不该有启动器兜底键: %v", got)
	}
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
		{DefaultRuntimeID, fixtureSpec().Projections[0], []string{"DSH_AGENTS_HOME="}},
		{altID, altSkillProjection(), nil},
	}
	for _, c := range cases {
		// 前提：投影根仍是"用户全局 agents 面"。哪天它挪到平台自己的目录，撬根的变量就
		// 未必是缺陷了——那时这条断言会红，提醒连带复核隔离表。
		if !strings.Contains(c.proj.Root, "/.agents/skills") {
			t.Fatalf("%s 的 skill 投影根不再是用户全局 agents 面（%q）：请连同隔离表与 E5 结论一起复核",
				c.runtimeID, c.proj.Root)
		}
		for _, kv := range IsolationEnv(Spec{ID: c.runtimeID}, home) {
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
