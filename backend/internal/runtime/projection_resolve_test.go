package runtime

import (
	"path/filepath"
	"strings"
	"testing"
)

// fakeEnv 构造一个确定性的占位符环境（只认显式给出的键，其余一律「未设置」）。
// 用假环境而不是 os.Setenv：默认根的展开结果不能随跑测试的机器变化。
func fakeEnv(kv map[string]string) ProjectionRootEnv {
	return func(name string) (string, bool) {
		v, ok := kv[name]
		return v, ok
	}
}

var testEnv = fakeEnv(map[string]string{
	"HOME":             "/home/u",
	"XDG_CONFIG_HOME":  "/home/u/.config",
	"PATH":             "/usr/bin",
	"WEIRD_VAR_UNSET2": "",
})

func skillSpec(id string, p Projection) Spec {
	return Spec{ID: id, Name: id, Projections: []Projection{p}}
}

func baseSkillProjection() Projection {
	return Projection{
		Kind:   ProjectionSkill,
		Scope:  ScopeGlobal,
		Root:   "${HOME}/.agents/skills",
		Layout: LayoutIDToken + "/SKILL.md",
		Format: "SKILL.md+frontmatter",
	}
}

// TestResolveProjectionChain 表驱动覆盖「3 条链 × 4 类非法输入」。
func TestResolveProjectionChain(t *testing.T) {
	cases := []struct {
		name      string
		spec      Spec
		overrides map[string]string
		wantOK    bool
		wantRoot  string
		wantErr   string // 子串；空 = 期望无错
	}{
		{
			name:     "链2 声明根：展开 ${HOME}",
			spec:     skillSpec("rt-alt", baseSkillProjection()),
			wantOK:   true,
			wantRoot: "/home/u/.agents/skills",
		},
		{
			name:     "链2 声明根：展开 ${XDG_CONFIG_HOME}",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "${XDG_CONFIG_HOME}/rt-alt/skills", Layout: LayoutIDToken + "/SKILL.md", Format: "SKILL.md"}),
			wantOK:   true,
			wantRoot: "/home/u/.config/rt-alt/skills",
		},
		{
			name:      "链1 覆盖优先于声明",
			spec:      skillSpec("rt-alt", baseSkillProjection()),
			overrides: map[string]string{"rt-alt:skill": "/deploy/skills"},
			wantOK:    true,
			wantRoot:  "/deploy/skills",
		},
		{
			name:      "链1 覆盖只作用于自己的键",
			spec:      skillSpec("rt-alt", baseSkillProjection()),
			overrides: map[string]string{"dsh:skill": "/deploy/skills"},
			wantOK:    true,
			wantRoot:  "/home/u/.agents/skills",
		},
		{
			name:      "链1 覆盖成空 → 报错（不静默关闭）",
			spec:      skillSpec("rt-alt", baseSkillProjection()),
			overrides: map[string]string{"rt-alt:skill": "  "},
			wantErr:   "值为空",
		},
		{
			name:    "链3 未声明该 kind → 关闭（远程端点即此）",
			spec:    Spec{ID: "remote-1", Projections: nil},
			wantOK:  false,
			wantErr: "",
		},
		{
			name:      "链3 只有覆盖、没有声明 → 仍关闭（不知道 Layout/Format）",
			spec:      Spec{ID: "remote-1", Projections: nil},
			overrides: map[string]string{"remote-1:skill": "/deploy/skills"},
			wantOK:    false,
			wantErr:   "",
		},
		{
			name:    "链3 声明的 Root 为空 → 关闭（声明式不投影）",
			spec:    skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "", Layout: LayoutIDToken + "/SKILL.md", Format: "SKILL.md"}),
			wantOK:  false,
			wantErr: "",
		},
		{
			name:     "非法：占位符变量未设置",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "${NOPE}/skills", Layout: LayoutIDToken + "/SKILL.md", Format: "SKILL.md"}),
			wantErr:  "环境变量 NOPE 未设置",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：${HOME} 存在但为空也算未设置",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "${WEIRD_VAR_UNSET2}/skills", Layout: LayoutIDToken + "/SKILL.md", Format: "SKILL.md"}),
			wantErr:  "未设置",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：占位符未闭合",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "${HOME/skills", Layout: LayoutIDToken + "/SKILL.md", Format: "SKILL.md"}),
			wantErr:  "未闭合",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：展开后是相对路径",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "relative/skills", Layout: LayoutIDToken + "/SKILL.md", Format: "SKILL.md"}),
			wantErr:  "绝对路径",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：Layout 是绝对路径",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "/r", Layout: "/abs/SKILL.md", Format: "SKILL.md"}),
			wantErr:  "落点",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：Layout 含 ..",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "/r", Layout: "../<id>/SKILL.md", Format: "SKILL.md"}),
			wantErr:  "落点",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：Layout 缺 <id>",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "/r", Layout: "SKILL.md", Format: "SKILL.md"}),
			wantErr:  "<id>",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：Format 为空",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Root: "/r", Layout: LayoutToken("SKILL.md"), Format: ""}),
			wantErr:  "format",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "非法：Scope 取值不在枚举内",
			spec:     skillSpec("rt-alt", Projection{Kind: ProjectionSkill, Scope: "globalx", Root: "/r", Layout: LayoutToken("SKILL.md"), Format: "SKILL.md"}),
			wantErr:  "scope",
			wantOK:   false,
			wantRoot: "",
		},
		{
			name:     "kind 不适用：声明了 skill、问 command",
			spec:     skillSpec("rt-alt", baseSkillProjection()),
			wantOK:   false,
			wantErr:  "",
			wantRoot: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind := ProjectionSkill
			if strings.Contains(c.name, "问 command") {
				kind = ProjectionCommand
			}
			p, ok, err := Resolve(c.spec, kind, c.overrides, testEnv)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("期望报错（含 %q），got nil（投影 %+v ok=%v）", c.wantErr, p, ok)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("错误文案应含 %q，got %v", c.wantErr, err)
				}
			} else if err != nil {
				t.Fatalf("不应报错：%v", err)
			}
			if ok != c.wantOK {
				t.Fatalf("ok 应为 %v，got %v（投影 %+v）", c.wantOK, ok, p)
			}
			if c.wantRoot != "" && p.Root != c.wantRoot {
				t.Fatalf("root 应为 %q，got %q", c.wantRoot, p.Root)
			}
			// 命中时 Scope 必须已被补全（空 scope 不得流出）。
			if ok && p.Scope == "" {
				t.Fatalf("命中投影的 Scope 不应为空")
			}
		})
	}
}

// LayoutToken 便利函数：拼出含 <id> 的落点（避免测试里到处字符串拼接）。
func LayoutToken(name string) string { return LayoutIDToken + "/" + name }

// TestResolveRealAdapters M1-2 验收：内置 adapter 与自造 adapter 各 1 条 skill 投影，Format 非空；
// 远程端点 not_applicable（空 Projections）。
func TestResolveRealAdapters(t *testing.T) {
	for _, spec := range []Spec{fixtureSpec(), altSpec()} {
		if n := len(spec.Projections); n != 1 {
			t.Fatalf("%s 应恰好声明 1 条投影，got %d（%+v）", spec.ID, n, spec.Projections)
		}
		decl, ok := spec.ProjectionFor(ProjectionSkill)
		if !ok {
			t.Fatalf("%s 应声明 skill 投影", spec.ID)
		}
		if decl.Format == "" {
			t.Fatalf("%s 的 skill 投影 Format 不得为空", spec.ID)
		}
		p, ok, err := Resolve(spec, ProjectionSkill, nil, testEnv)
		if err != nil || !ok {
			t.Fatalf("%s 的 skill 投影应可解析：ok=%v err=%v", spec.ID, ok, err)
		}
		if !filepath.IsAbs(p.Root) {
			t.Fatalf("%s 解析后的 root 应是绝对路径，got %q", spec.ID, p.Root)
		}
		// dsh 只扫根下一层：落点必须恰好是 <id>/<file>（两段）。
		if n := len(strings.Split(strings.Trim(p.Layout, "/"), "/")); n != 2 {
			t.Fatalf("%s 的落点必须是根下两层（dsh 不递归），got %q", spec.ID, p.Layout)
		}
		// 两个 adapter 的默认根当前是同一个跨工具约定目录；记录事实，不当作约束。
		if p.Root != "/home/u/.agents/skills" {
			t.Fatalf("%s 默认根与预期不符：%q", spec.ID, p.Root)
		}
	}
	// 远程端点：无声明 → not_applicable。
	remote := RemoteSpec("r1", "R1", "ws", "ws://127.0.0.1:1/acp", nil)
	if _, ok, err := Resolve(remote, ProjectionSkill, nil, testEnv); ok || err != nil {
		t.Fatalf("远程端点不应有 skill 投影（ok=%v err=%v）", ok, err)
	}
}

// TestResolveAdapterOverride 覆盖链在真实 adapter 上的落点：只换根，形态不变。
func TestResolveAdapterOverride(t *testing.T) {
	spec := altSpec()
	p, ok, err := Resolve(spec, ProjectionSkill, map[string]string{altID + ":skill": "/deploy/oc-skills"}, testEnv)
	if err != nil || !ok {
		t.Fatalf("应解析成功：ok=%v err=%v", ok, err)
	}
	if p.Root != "/deploy/oc-skills" {
		t.Fatalf("root 应被覆盖，got %q", p.Root)
	}
	if p.Layout != LayoutIDToken+"/SKILL.md" || p.Format != "SKILL.md+frontmatter" {
		t.Fatalf("覆盖只应换根，形态须保持声明值，got %+v", p)
	}
}

// TestApplyProjectionLayout 落点实例化：正常替换 + id 里的路径分隔符被拒。
func TestApplyProjectionLayout(t *testing.T) {
	got, err := ApplyProjectionLayout("<id>/SKILL.md", "demo")
	if err != nil || got != filepath.FromSlash("demo/SKILL.md") {
		t.Fatalf("替换失败：got %q err=%v", got, err)
	}
	for _, bad := range []string{"a/b", `a\b`, ".", "..", ""} {
		if _, err := ApplyProjectionLayout("<id>/SKILL.md", bad); err == nil {
			t.Fatalf("id %q 应被拒（会把产物写到落点之外）", bad)
		}
	}
	if _, err := ApplyProjectionLayout("SKILL.md", "demo"); err == nil {
		t.Fatal("缺 <id> 的落点应被拒")
	}
}

// TestProjectionPathAndDir 拼绝对路径与所属目录（.mate-managed 标记与 sidecar 落在目录里）。
func TestProjectionPathAndDir(t *testing.T) {
	p := baseSkillProjection()
	p.Root = "/r"
	path, err := ProjectionPath(p, "demo")
	if err != nil {
		t.Fatalf("ProjectionPath: %v", err)
	}
	if path != filepath.FromSlash("/r/demo/SKILL.md") {
		t.Fatalf("路径不符：%q", path)
	}
	dir, err := ProjectionDir(p, "demo")
	if err != nil {
		t.Fatalf("ProjectionDir: %v", err)
	}
	if dir != filepath.FromSlash("/r/demo") {
		t.Fatalf("目录不符：%q", dir)
	}
}

// TestCapabilityScopeDefaults 纪律①③：未实测 / 非法值一律回落 process。
func TestCapabilityScopeDefaults(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"零值", Spec{}, CapabilityScopeProcess},
		{"声明 session 但未实测", bindCapability(CapabilityScopeSession, false), CapabilityScopeProcess},
		{"声明 session 且已实测", bindCapability(CapabilityScopeSession, true), CapabilityScopeSession},
		{"声明 directory 且已实测", bindCapability(CapabilityScopeDirectory, true), CapabilityScopeDirectory},
		{"非法值即使 Tested 也回落", bindCapability("workspace", true), CapabilityScopeProcess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.spec.EffectiveCapabilityScope(); got != c.want {
				t.Fatalf("应为 %q，got %q", c.want, got)
			}
		})
	}
	// 内置与自造 adapter、以及远程端点当前都必须落在最保守的一档（P-2 无结论）。
	for _, spec := range []Spec{fixtureSpec(), altSpec(), RemoteSpec("r1", "R1", "ws", "ws://x/acp", nil)} {
		if got := spec.EffectiveCapabilityScope(); got != CapabilityScopeProcess {
			t.Fatalf("%s 未实测应回落 process，got %q", spec.ID, got)
		}
	}
}

// bindCapability 造一份"只声明绑定粒度"的 spec（C3 4.7：粒度是策略接口，不再是裸字段）。
func bindCapability(scope string, tested bool) Spec {
	return Spec{Capability: StaticCapability{Binding: CapabilityBinding{Scope: scope, Tested: tested}}}
}
