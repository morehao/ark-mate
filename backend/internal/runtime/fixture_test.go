package runtime

import (
	"context"

	"github.com/morehao/ark-mate/internal/platform/resource"
)

// ——— 测试夹具：与内置 adapter 同形的声明 ———
//
// v3.1 §3.2.1 规则 5：`internal/runtime` **不得 import 任何 adapter**（那条边是
// `TestRuntimeDoesNotImportRuntimeAdaptersV31` 钉住的），而本包的内部测试（`package runtime`）
// 又要验证"注册表 / 隔离 / 落点 / 端口表"这些机制。两者相加只剩一条路：**在测试里合成一份
// 与内置 adapter 同形的声明**。
//
// 真实 adapter 的形状由 `internal/infra/dsh` 自己的测试钉住（`TestSpecShape` 等）；
// 本夹具只保证"字段齐备、形态合法"，让机制判据有对象可测。
func fixtureSpec() Spec {
	return Spec{
		ID:      "dsh",
		Name:    "dsh",
		Version: "dsh --profile acp",
		Launch:  LaunchSpec{Command: "dsh", Args: []string{"--profile", "acp"}},
		Caps: Capabilities{
			SessionsPersistent:     true,
			MultiSessionConcurrent: true,
		},
		Isolation: []IsolationVar{
			{Key: "DSH_HOME", Kind: IsolationPath, Path: []string{"runtime", "dsh"}, LauncherFallback: true},
		},
		Description:                 "dsh（deepseek-harness）ACP 执行后端",
		DeclaresModelInput:          true,
		ModelCatalogNeedsCredential: true,
		McpTransports:               []string{"stdio", "streamable_http"},
		ManagesOwnContext:           true,
		ModelConfig: ModelConfigLocation{
			Root:          "${DSH_HOME}",
			Filename:      "mate-models.patch.yml",
			CredentialDoc: ".credentials.yaml",
		},
		Credential: resource.CredentialSpec{
			NativeDoc:   "${DSH_HOME}/.credentials.yaml",
			RefPattern:  "MATE_%s_API_KEY",
			FallbackEnv: false,
			Tested:      true,
		},
		Projections: []Projection{{
			Kind:   ProjectionSkill,
			Scope:  ScopeGlobal,
			Root:   "${HOME}/.agents/skills",
			Layout: LayoutIDToken + "/SKILL.md",
			Format: "SKILL.md+frontmatter",
			Tested: true,
		}},
		CapabilityScope: CapabilityScopeProcess,
		Notes:           "dsh --profile acp：启动形态说明（夹具）",
	}
}

// fixtureSpecs 全部内置 adapter 的声明（夹具版：当前只有一个）。
func fixtureSpecs() []Spec { return []Spec{fixtureSpec()} }

// stubAdapter 一个只有声明、没有执行半场的 adapter（注册表机制测试用）。
type stubAdapter struct{ spec Spec }

func (a stubAdapter) Spec() Spec { return a.spec }

func (a stubAdapter) RegisterPorts(r *Registry) {
	r.RegisterNativePermission(a.spec.ID, stubNativePermission{})
	// 同步实现按落点解析登记（与真实 adapter 同形）：隔离 env 未应用时解析失败 → 不登记。
	// 落点必须取**注册表里那份**（`ApplyIsolationEnv` 会就地更新它），否则重登记永远解析不出来。
	spec, ok := r.Get(a.spec.ID)
	if !ok {
		return
	}
	if loc, declared, err := ResolveConfigLocation(spec); err == nil && declared {
		r.RegisterSyncer(a.spec.ID, stubSyncer{PatchPath: loc.Path})
	}
}

// stubSyncer 只实现"可执行半场已登记"这一事实（同步行为归 adapter 自己的测试）。
type stubSyncer struct{ PatchPath string }

func (s stubSyncer) SyncModels(context.Context, resource.Set) (resource.Outcome, error) {
	return resource.Outcome{}, nil
}

type stubNativePermission struct{}

func (stubNativePermission) Env(string, string, map[string]string) ([]string, string) {
	return nil, ""
}

// fixtureRegistry 用夹具 adapter 构造的注册表（= 旧 `fixtureRegistry()` 的测试替身）。
func fixtureRegistry() *Registry { return NewAdapterRegistry(stubAdapter{spec: fixtureSpec()}) }
