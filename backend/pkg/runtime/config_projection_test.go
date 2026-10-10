package runtime

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveConfigProjectionUsesIsolationEnv 落点解析必须**先看该 runtime 的隔离 env**。
//
// 这条是隔离纪律在 T5 上的落点：`OPENCODE_CONFIG_DIR` / `DSH_HOME` 是平台注入**子进程**的私有值，
// 进程环境里通常没有它们。只看进程环境会解析失败——更糟的是会命中用户在 shell 里设的同名变量，
// 于是平台去写**用户真实目录**（这正是 AGENTS.md 的硬约束禁止的事）。
func TestResolveConfigProjectionUsesIsolationEnv(t *testing.T) {
	home := t.TempDir()
	// 诱饵：进程环境里放一个同名变量，指向"用户自己的目录"。
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(home, "user-owned-opencode"))
	t.Setenv("DSH_HOME", filepath.Join(home, "user-owned-dsh"))

	reg := NewRegistry(opencodeSpec(), deepseekharnessSpec())
	applied := reg.ApplyIsolationEnv(home)
	if len(applied) != 2 {
		t.Fatalf("两个本地 runtime 都应被注入隔离 env: %v", applied)
	}
	for _, id := range []string{"opencode", "deepseekharness"} {
		spec, ok := reg.Get(id)
		if !ok {
			t.Fatalf("%s 未注册", id)
		}
		p, ok, err := ResolveConfigProjection(spec)
		if err != nil || !ok {
			t.Fatalf("%s 的配置落点应可解析: ok=%v err=%v", id, ok, err)
		}
		if !filepath.IsAbs(p.Path) {
			t.Fatalf("%s 的落点应为绝对路径: %q", id, p.Path)
		}
		if strings.Contains(p.Path, "user-owned") {
			t.Fatalf("%s 的落点命中了进程环境里的同名变量（隔离被绕过）: %q", id, p.Path)
		}
		if want := filepath.Join(home, "runtime"); !strings.HasPrefix(p.Path, want) {
			t.Fatalf("%s 的落点应落在 <MATE_HOME>/runtime 下: %q", id, p.Path)
		}
	}

	// opencode：文件 + 内联双通道；dsh：平台自有 overlay + 启动参数。
	oc, _ := reg.Get("opencode")
	ocp, _, _ := ResolveConfigProjection(oc)
	if ocp.Channel != ConfigChannelEnvContent || ocp.FileChannel != ConfigChannelDirRead ||
		ocp.Filename != "opencode.json" || ocp.Format != ConfigFormatOpencodeJSON {
		t.Fatalf("opencode 的声明不符: %+v", ocp.ConfigProjection)
	}
	dsh, _ := reg.Get("deepseekharness")
	dshp, _, _ := ResolveConfigProjection(dsh)
	if dshp.Channel != ConfigChannelCLIPatchArg || dshp.FileChannel != "" ||
		dshp.Filename != "mate-models.patch.yml" || dshp.Format != ConfigFormatDshPatchYAML {
		t.Fatalf("dsh 的声明不符: %+v", dshp.ConfigProjection)
	}
	// 写入权是**用实测结论换的**：两个底座都已在随包载荷上实测通过（见各自 adapter 的 Note 与
	// TestedOnVersion）→ 都可写。这条判据存在的意义是"没实测就必须只读"，所以两个都断言
	// **实测版本号非空**——把 Tested 置 true 却不记版本号，等于把结论与它成立的那一版解绑。
	if !ocp.Writable() || !dshp.Writable() {
		t.Fatalf("两个底座都已实测通过，应可写：opencode=%v dsh=%v", ocp.Writable(), dshp.Writable())
	}
	if ocp.TestedOnVersion == "" || dshp.TestedOnVersion == "" {
		t.Fatalf("可写就必须记实测版本号（结论与版本绑定）：opencode=%q dsh=%q",
			ocp.TestedOnVersion, dshp.TestedOnVersion)
	}
	if ocp.EffectiveEffect() != ConfigEffectNextProcess || dshp.EffectiveEffect() != ConfigEffectNextProcess {
		t.Fatalf("未声明 effect 时应取最保守的 next_process")
	}
	// 诊断面不能出现空原因。
	if ocp.UnverifiedReason() == "" || dshp.UnverifiedReason() == "" {
		t.Fatal("未实测时 UnverifiedReason 不得为空")
	}
}

// TestResolveConfigProjectionRejectsBadDeclarations 声明非法即报错（不静默回落"看起来能用"的值）。
func TestResolveConfigProjectionRejectsBadDeclarations(t *testing.T) {
	base := Spec{ID: "x", Launch: LaunchSpec{Env: []string{"BASE=/tmp/base"}}}

	cases := []struct {
		name string
		cp   ConfigProjection
		want string
	}{
		{"unknown_format", ConfigProjection{Format: "toml", Root: "${BASE}", Filename: "a.toml", Channel: ConfigChannelDirRead}, "未知格式"},
		{"unknown_channel", ConfigProjection{Format: ConfigFormatOpencodeJSON, Root: "${BASE}", Filename: "a.json", Channel: "telepathy"}, "未知通道"},
		{"unknown_file_channel", ConfigProjection{Format: ConfigFormatOpencodeJSON, Root: "${BASE}", Filename: "a.json", Channel: ConfigChannelDirRead, FileChannel: "smoke"}, "未知取值"},
		{"filename_with_dir", ConfigProjection{Format: ConfigFormatOpencodeJSON, Root: "${BASE}", Filename: "sub/a.json", Channel: ConfigChannelDirRead}, "文件名"},
		{"empty_filename", ConfigProjection{Format: ConfigFormatOpencodeJSON, Root: "${BASE}", Filename: " ", Channel: ConfigChannelDirRead}, "文件名"},
		{"unset_var", ConfigProjection{Format: ConfigFormatOpencodeJSON, Root: "${NOPE_NOT_SET}", Filename: "a.json", Channel: ConfigChannelDirRead}, "未设置"},
		{"relative_root", ConfigProjection{Format: ConfigFormatOpencodeJSON, Root: "relative/dir", Filename: "a.json", Channel: ConfigChannelDirRead}, "绝对路径"},
	}
	for _, tc := range cases {
		spec := base
		spec.ConfigProjection = tc.cp
		_, _, err := ResolveConfigProjection(spec)
		if err == nil {
			t.Fatalf("%s：非法声明应报错", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s：错误文案应提到 %q，got %v", tc.name, tc.want, err)
		}
	}

	// 未声明 = ok=false 且**不是错误**（该 runtime 不承载平台模型清单）。
	spec := base
	_, ok, err := ResolveConfigProjection(spec)
	if ok || err != nil {
		t.Fatalf("未声明应为 (false, nil)，got ok=%v err=%v", ok, err)
	}
}

// TestSpecConfigProjectionIsSeparateFromProjections T5 与 T4 是两种声明，不能互相顶替。
func TestSpecConfigProjectionIsSeparateFromProjections(t *testing.T) {
	spec := opencodeSpec()
	if _, ok := spec.ProjectionFor(ProjectionSkill); !ok {
		t.Fatal("T4 的 skill 投影应仍在 Projections 里")
	}
	if !spec.ConfigProjection.Declared() {
		t.Fatal("T5 的模型配置落点应在 ConfigProjection 里")
	}
	// T4 的落点模板要求含 <id>；T5 的是单例文件，把它的形状喂给 T4 的校验必须被拒——
	// 这正是"两者不能混用"的机器判据。
	if err := ValidateProjectionLayout(spec.ConfigProjection.Filename); err == nil {
		t.Fatal("T5 的单例文件名不该通过 T4 的落点校验（两者形态不同，混用会让判据变浑）")
	}
}
