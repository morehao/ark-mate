package archtest

import (
	"github.com/morehao/ark-mate/internal/adapters/outbound/dsh"
	"testing"

	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 第 10 条守卫：能力绑定粒度不得超出已实测范围 ———
//
// `CapabilityScope` 声明「该 runtime 的能力面（MCP 集、skill 目录扫描）在哪个粒度上被确定」：
// `process`（最粗，默认）/ `directory` / `session`。它决定平台能否让**多个会话共享一个进程**。
//
// 为什么需要静态守卫：把某个 adapter 声明成比 `process` 更细，等于断言"这个底座允许按会话/按目录
// 收窄能力面"。这条断言**没有实测就是错的**——一旦错，平台会把 A 会话的 skill/MCP 投给 B 会话
// （能力串味，且是安全边界问题：MCP 白名单与权限策略都会被绕过）。而它的表现是"大部分时候看不出
// 问题"，只在多会话并发时偶发，最难排查。
//
// 所以：**未 `Tested` 者不得声明比 `process` 更细**。放开的唯一路径是先跑 P-2 探针（E1–E6）拿到
// 结论，再把 `Tested=true` + `TestedOnVersion` 一起写上。这属于 C8 的第 10 条守卫。

// capabilityScopeRank 绑定粒度由粗到细（数值越大越细）。
var capabilityScopeRank = map[string]int{
	"":                               0, // 空 = process（未声明时最保守）
	runtime.CapabilityScopeProcess:   0,
	runtime.CapabilityScopeDirectory: 1,
	runtime.CapabilityScopeSession:   2,
}

// capabilityScopeViolations 返回「未实测却声明了比 process 更细」的违规描述（空 = 全部合规）。
//
// 抽成函数是为了能对**合成**的违规输入自测：守卫只对真实注册表跑一次的话，"它到底有没有牙齿"
// 无法验证——而一条不会红的守卫等于没有守卫。
func capabilityScopeViolations(specs []runtime.Spec) []string {
	var out []string
	for _, s := range specs {
		rank, known := capabilityScopeRank[s.CapabilityScope]
		if !known {
			// 未知取值同样算违规：它不是"保守"，而是"没人知道它什么意思"。
			out = append(out, "runtime "+s.ID+" 声明了未知 CapabilityScope "+s.CapabilityScope)
			continue
		}
		if rank == 0 {
			continue
		}
		if !s.Tested {
			out = append(out, "runtime "+s.ID+" 声明 CapabilityScope="+s.CapabilityScope+
				" 但未实测（Tested=false）：比 process 更细的绑定必须先有 P-2 探针结论")
			continue
		}
		if s.TestedOnVersion == "" {
			// 实测必须绑定版本：底座一升级，绑定范围可能就变了，那时 Tested 自动失效。
			out = append(out, "runtime "+s.ID+" 声明 CapabilityScope="+s.CapabilityScope+
				" 且 Tested=true，但没写 TestedOnVersion：无法在底座升级后判定该结论是否仍成立")
		}
	}
	return out
}

// TestCapabilityScopeDeclaration 真实注册表必须全部合规，且守卫本身有牙齿（合成违规必须被抓到）。
func TestCapabilityScopeDeclaration(t *testing.T) {
	// 1) 真实声明（两个 adapter）。
	if bad := capabilityScopeViolations(dsh.Registry().List()); len(bad) > 0 {
		t.Fatalf("runtime 能力绑定粒度声明违规：\n%s", joinLines(bad))
	}
	// 2) 负例：声明比 process 更细但不实测 → 必须红。
	cases := []struct {
		name string
		spec runtime.Spec
	}{
		{"session 但未实测", runtime.Spec{ID: "x", CapabilityScope: runtime.CapabilityScopeSession}},
		{"directory 但未实测", runtime.Spec{ID: "x", CapabilityScope: runtime.CapabilityScopeDirectory}},
		{"实测了但没写版本", runtime.Spec{ID: "x", CapabilityScope: runtime.CapabilityScopeSession, Tested: true}},
		{"未知取值", runtime.Spec{ID: "x", CapabilityScope: "workspace"}},
	}
	for _, c := range cases {
		if bad := capabilityScopeViolations([]runtime.Spec{c.spec}); len(bad) == 0 {
			t.Fatalf("%s 应被判违规（守卫没牙齿）", c.name)
		}
	}
	// 3) 合规正例：process（显式/缺省）与"实测 + 版本"的更细声明都要放行。
	for _, ok := range [][]runtime.Spec{
		{{ID: "x"}},
		{{ID: "x", CapabilityScope: runtime.CapabilityScopeProcess}},
		{{ID: "x", CapabilityScope: runtime.CapabilityScopeSession, Tested: true, TestedOnVersion: "dsh 1.2.3"}},
	} {
		if bad := capabilityScopeViolations(ok); len(bad) > 0 {
			t.Fatalf("合规声明被误判：%v → %s", ok, joinLines(bad))
		}
	}
}

func joinLines(ss []string) string {
	out := ""
	for _, s := range ss {
		out += "  - " + s + "\n"
	}
	return out
}
