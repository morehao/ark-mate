package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/resource"
)

// TestResolveConfigLocationUsesIsolationEnv 落点解析必须**先看该 runtime 的隔离 env**。
//
// 这条是隔离纪律在模型清单落点上的落点：`RT_ALT_HOME` / `DSH_HOME` 是平台注入**子进程**的
// 私有值，进程环境里通常没有它们。只看进程环境会解析失败——更糟的是会命中用户在 shell 里设的同名
// 变量，于是平台去写**用户真实目录**（这正是 `docs/agents/isolation.md` 的硬约束禁止的事）。
func TestResolveConfigLocationUsesIsolationEnv(t *testing.T) {
	home := t.TempDir()
	// 诱饵：进程环境里放一个同名变量，指向"用户自己的目录"。
	t.Setenv("RT_ALT_HOME", filepath.Join(home, "user-owned-rt-alt"))
	t.Setenv("DSH_HOME", filepath.Join(home, "user-owned-dsh"))

	reg := NewRegistry(altSpec(), fixtureSpec())
	applied := reg.ApplyIsolationEnv(home)
	if len(applied) != 2 {
		t.Fatalf("两个本地 runtime 都应被注入隔离 env: %v", applied)
	}
	for _, id := range []string{altID, "dsh"} {
		spec, ok := reg.Get(id)
		if !ok {
			t.Fatalf("%s 未注册", id)
		}
		p, ok, err := ResolveConfigLocation(spec)
		if err != nil || !ok {
			t.Fatalf("%s 的清单配置落点应可解析: ok=%v err=%v", id, ok, err)
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

	// 逐 runtime 的落点形态（文件名与凭据文档面是平台内核对 adapter 的唯一约定）。
	alt, _ := reg.Get(altID)
	altp, _, _ := ResolveConfigLocation(alt)
	if filepath.Base(altp.Path) != "rt-alt-models.json" ||
		filepath.Base(altp.CredentialDocPath) != ".rt-alt-credentials.json" {
		t.Fatalf("%s 的声明不符: %+v", altID, altp)
	}
	dsh, _ := reg.Get("dsh")
	dshp, _, _ := ResolveConfigLocation(dsh)
	if filepath.Base(dshp.Path) != "mate-models.patch.yml" || dshp.CredentialDocPath == "" ||
		filepath.Base(dshp.CredentialDocPath) != ".credentials.yaml" {
		t.Fatalf("dsh 的声明不符: %+v", dshp)
	}
	// 两个落点都在**同一个平台独占根**里：这是"绝不写用户目录"这条纪律的静态判据。
	if filepath.Dir(dshp.CredentialDocPath) != dshp.RootAbs {
		t.Fatalf("凭据文档应与清单配置同住 Root：root=%q doc=%q", dshp.RootAbs, dshp.CredentialDocPath)
	}
}

// TestResolveConfigLocationRejectsBadDeclarations 声明非法即报错（不静默回落"看起来能用"的值）。
func TestResolveConfigLocationRejectsBadDeclarations(t *testing.T) {
	base := Spec{ID: "x", Launch: LaunchSpec{Env: []string{"BASE=/tmp/base"}}}

	cases := []struct {
		name string
		loc  ModelConfigLocation
		want string
	}{
		{"filename_with_dir", ModelConfigLocation{Root: "${BASE}", Filename: "sub/a.json"}, "文件名"},
		{"empty_filename", ModelConfigLocation{Root: "${BASE}", Filename: " "}, "文件名"},
		{"dot_filename", ModelConfigLocation{Root: "${BASE}", Filename: "."}, "文件名"},
		{"unset_var", ModelConfigLocation{Root: "${NOPE_NOT_SET}", Filename: "a.json"}, "未设置"},
		{"relative_root", ModelConfigLocation{Root: "relative/dir", Filename: "a.json"}, "绝对路径"},
		{"absolute_cred_doc", ModelConfigLocation{Root: "${BASE}", Filename: "a.json", CredentialDoc: "/etc/creds.yaml"}, "相对路径"},
		{"escaping_cred_doc", ModelConfigLocation{Root: "${BASE}", Filename: "a.json", CredentialDoc: "../creds.yaml"}, "逃出"},
		{"dot_cred_doc", ModelConfigLocation{Root: "${BASE}", Filename: "a.json", CredentialDoc: "."}, "逃出"},
	}
	for _, tc := range cases {
		spec := base
		spec.ModelConfig = tc.loc
		_, _, err := ResolveConfigLocation(spec)
		if err == nil {
			t.Fatalf("%s：非法声明应报错", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s：错误文案应提到 %q，got %v", tc.name, tc.want, err)
		}
	}

	// 未声明 = ok=false 且**不是错误**（该 runtime 不承载平台模型清单）。
	spec := base
	_, ok, err := ResolveConfigLocation(spec)
	if ok || err != nil {
		t.Fatalf("未声明应为 (false, nil)，got ok=%v err=%v", ok, err)
	}

	// 凭据文档可以带一层子目录（相对 Root 的合法形态）。
	spec = base
	spec.ModelConfig = ModelConfigLocation{Root: "${BASE}", Filename: "a.json", CredentialDoc: "nested/creds.yaml"}
	p, ok, err := ResolveConfigLocation(spec)
	if err != nil || !ok {
		t.Fatalf("带子目录的凭据文档应可解析: ok=%v err=%v", ok, err)
	}
	if p.CredentialDocPath != filepath.Join("/tmp/base", "nested", "creds.yaml") {
		t.Fatalf("凭据文档落点不符: %q", p.CredentialDocPath)
	}
}

// TestSpecModelConfigIsSeparateFromProjections 清单落点与 skill 投影是两种声明，不能互相顶替。
func TestSpecModelConfigIsSeparateFromProjections(t *testing.T) {
	spec := altSpec()
	if _, ok := spec.ProjectionFor(ProjectionSkill); !ok {
		t.Fatal("skill 投影应仍在 Projections 里")
	}
	if !spec.ModelConfig.Declared() {
		t.Fatal("模型清单落点应在 ModelConfig 里")
	}
	// skill 投影的落点模板要求含 `<id>`；清单配置是单例文件，把它的形状喂给投影校验必须被拒——
	// 这正是"两者不能混用"的机器判据。
	if err := ValidateProjectionLayout(spec.ModelConfig.Filename); err == nil {
		t.Fatal("清单配置的单例文件名不该通过 skill 投影的落点校验（两者形态不同，混用会让判据变浑）")
	}
}

// TestBuiltinSyncersRegisteredAfterIsolation 同步实现的登记**必须发生在隔离 env 应用之后**。
//
// 时序事实：`Spec.ModelConfig.Root` 里的 `${DSH_HOME}` / `${RT_ALT_HOME}` 正是
// `ApplyIsolationEnv` 注入的；而构造注册表跑在装配最前面。所以构造时那次登记**必然解析失败**
// （解析刻意不回落进程环境——回落到用户自己的 home 是隔离纪律的红线）。
// 少了"隔离后再登记一次"，`sync[]` 会永远报 `unsupported`：界面说保存成功、底座永远拿不到清单。
//
// 夹具用 `t.TempDir()` 当 home：真正的落点全部落在临时目录里，**绝不碰真 `~/.dsh`**。
//
// 遍历的是 `fixtureSpecs()` 而不是写死的 id 清单：新增一个内置 adapter 时自动被覆盖，
// 而"清单里恰好有一个底座"这件事不该让判据退化成对某个名字的断言。
func TestBuiltinSyncersRegisteredAfterIsolation(t *testing.T) {
	reg := fixtureRegistry()
	specs := fixtureSpecs()
	if len(specs) == 0 {
		t.Fatal("内置 adapter 一个都没扫到：本判据已失效（fail-closed）")
	}
	for _, spec := range specs {
		id := spec.ID
		if _, ok := reg.Syncer(id); ok {
			t.Fatalf("%s：隔离 env 未应用时落点解析不出来，不该有 syncer（拿到就说明解析回落了进程环境）", id)
		}
	}
	home := t.TempDir()
	if ids := reg.ApplyIsolationEnv(home); len(ids) != len(specs) {
		t.Fatalf("全部内置本地 runtime 都应被注入隔离 env（期望 %d 个）: %v", len(specs), ids)
	}
	for _, spec := range specs {
		id := spec.ID
		s, ok := reg.Syncer(id)
		if !ok || s == nil {
			t.Fatalf("%s：隔离 env 应用后应登记同步实现", id)
		}
		// 空集是安全的探针：它会走"撤回"路径（本来就没交付过 → unchanged），不写任何字节。
		out, err := s.SyncModels(context.Background(), resource.Set{Runtime: id})
		if err != nil {
			t.Fatalf("%s：空集撤回不应报错：%v", id, err)
		}
		if out.Status == resource.StatusFailed {
			t.Fatalf("%s：空集撤回不该是 failed：%+v", id, out)
		}
		spec, _ := reg.Get(id)
		p, ok, err := ResolveConfigLocation(spec)
		if err != nil || !ok {
			t.Fatalf("%s：落点应可解析：%v", id, err)
		}
		if !strings.HasPrefix(p.Path, home) {
			t.Fatalf("%s：落点必须落在隔离 home 下（%s），实际 %s", id, home, p.Path)
		}
	}
}
