package runtime

import (
	"math/rand"
	"strings"
	"testing"
)

// fpInput 一套固定的、有代表性的输入（两个 runtime、两条 skill、两个 MCP）。
func fpInput() FingerprintInput {
	return FingerprintInput{
		Skills: []SkillPart{
			{ID: "alpha", BodyDigest: "aaaa"},
			{ID: "beta", BodyDigest: "bbbb"},
		},
		Mcps: []McpPart{
			{
				ID: "fs", Transport: "stdio", Command: "/usr/local/bin/npx",
				Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
				// 故意乱序：这两个切片由调用方从 map 展开，顺序随机。
				EnvKeys:    []string{"TOKEN", "DEBUG", "HOME"},
				SecretRefs: []string{"vault:ns/fs-key", "env:FS_TOKEN"},
			},
			{ID: "web", Transport: "http", URL: "https://example.invalid/mcp"},
		},
		Runtimes: []RuntimePart{{
			ID: "opencode", Version: "opencode acp", Scope: CapabilityScopeProcess,
			Projections: []Projection{
				opencodeSkillProjection(),
				{Kind: ProjectionCommand, Scope: ScopeGlobal, Root: "${HOME}/.config/opencode/command", Layout: "<id>.md", Format: "markdown"},
			},
		}},
	}
}

// TestCapabilityFingerprintStable 连续算 N 次必须逐字相同（风险表第 15 行：不稳就会每次建会话
// 都新建代际、进程数失控）。用固定序列的 rand 打乱每个切片的顺序与 map 展开顺序。
func TestCapabilityFingerprintStable(t *testing.T) {
	base := Fingerprint(fpInput())
	if base == "" {
		t.Fatal("非空输入不该得到空指纹")
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		in := fpInput()
		rng.Shuffle(len(in.Skills), func(a, b int) { in.Skills[a], in.Skills[b] = in.Skills[b], in.Skills[a] })
		rng.Shuffle(len(in.Mcps), func(a, b int) { in.Mcps[a], in.Mcps[b] = in.Mcps[b], in.Mcps[a] })
		rng.Shuffle(len(in.Runtimes), func(a, b int) { in.Runtimes[a], in.Runtimes[b] = in.Runtimes[b], in.Runtimes[a] })
		for j := range in.Mcps {
			rng.Shuffle(len(in.Mcps[j].EnvKeys), func(a, b int) {
				in.Mcps[j].EnvKeys[a], in.Mcps[j].EnvKeys[b] = in.Mcps[j].EnvKeys[b], in.Mcps[j].EnvKeys[a]
			})
			rng.Shuffle(len(in.Mcps[j].SecretRefs), func(a, b int) {
				in.Mcps[j].SecretRefs[a], in.Mcps[j].SecretRefs[b] = in.Mcps[j].SecretRefs[b], in.Mcps[j].SecretRefs[a]
			})
		}
		if got := Fingerprint(in); got != base {
			t.Fatalf("第 %d 次打乱顺序后指纹变了：\n%s\n%s", i, base, got)
		}
	}
}

// TestCapabilityFingerprintEmpty 空输入 → 空指纹（R11 缺省零回归的载体：connKey 见到空指纹
// 必须保持与今天逐字相同的键）。
func TestCapabilityFingerprintEmpty(t *testing.T) {
	if got := Fingerprint(FingerprintInput{}); got != "" {
		t.Fatalf("空输入应得到空指纹，got %q", got)
	}
}

// TestCapabilityFingerprintSensitive 每个输入维度都必须影响指纹（缺一项 = 两套不同的东西共用
// 一个代际）。逐项改一个字段，断言指纹必变。
func TestCapabilityFingerprintSensitive(t *testing.T) {
	base := Fingerprint(fpInput())
	cases := []struct {
		name string
		mut  func(*FingerprintInput)
	}{
		{"skill 少一条", func(in *FingerprintInput) { in.Skills = in.Skills[:1] }},
		{"skill id 变", func(in *FingerprintInput) { in.Skills[0].ID = "alpha2" }},
		{"skill 正文摘要变", func(in *FingerprintInput) { in.Skills[0].BodyDigest = "cccc" }},
		{"MCP transport 变", func(in *FingerprintInput) { in.Mcps[0].Transport = "sse" }},
		{"MCP command 变", func(in *FingerprintInput) { in.Mcps[0].Command = "/usr/bin/other" }},
		{"MCP args 变", func(in *FingerprintInput) { in.Mcps[0].Args = []string{"-y", "pkg"} }},
		{"MCP args 顺序变", func(in *FingerprintInput) { in.Mcps[0].Args[0], in.Mcps[0].Args[1] = in.Mcps[0].Args[1], in.Mcps[0].Args[0] }},
		{"MCP url 变", func(in *FingerprintInput) { in.Mcps[1].URL = "https://other.invalid/mcp" }},
		{"MCP env 键名变", func(in *FingerprintInput) { in.Mcps[0].EnvKeys = []string{"TOKEN", "DEBUG"} }},
		{"MCP 密钥引用名变", func(in *FingerprintInput) { in.Mcps[0].SecretRefs = []string{"vault:ns/other"} }},
		{"runtime id 变", func(in *FingerprintInput) { in.Runtimes[0].ID = "dsh" }},
		{"runtime 版本变", func(in *FingerprintInput) { in.Runtimes[0].Version = "opencode acp v2" }},
		{"CapabilityScope 变", func(in *FingerprintInput) { in.Runtimes[0].Scope = CapabilityScopeSession }},
		{"投影 root 变", func(in *FingerprintInput) { in.Runtimes[0].Projections[0].Root = "/elsewhere" }},
		{"投影 layout 变", func(in *FingerprintInput) { in.Runtimes[0].Projections[0].Layout = "<id>/OTHER.md" }},
		{"投影 format 变", func(in *FingerprintInput) { in.Runtimes[0].Projections[0].Format = "other" }},
		{"投影 scope 变", func(in *FingerprintInput) { in.Runtimes[0].Projections[0].Scope = ScopeWorkspace }},
		{"投影 kind 变", func(in *FingerprintInput) { in.Runtimes[0].Projections[0].Kind = ProjectionCommand }},
		{"少一个投影", func(in *FingerprintInput) { in.Runtimes[0].Projections = in.Runtimes[0].Projections[:1] }},
	}
	for _, c := range cases {
		in := fpInput()
		c.mut(&in)
		if got := Fingerprint(in); got == base {
			t.Fatalf("%s 后指纹没变（该项没参与指纹 → 两套不同的能力会共用一个代际）", c.name)
		}
	}
}

// TestCapabilityFingerprintProjectionMetadataIgnored `Tested`/`Note` 不参与指纹：
// 它们表达的是"我们对这个形态的把握程度"，不是形态本身。P-2 实测完把 Tested 置 true 不该让
// 所有会话换一次底座进程。
func TestCapabilityFingerprintProjectionMetadataIgnored(t *testing.T) {
	base := Fingerprint(fpInput())
	in := fpInput()
	in.Runtimes[0].Projections[0].Tested = true
	in.Runtimes[0].Projections[0].Note = "P-2 实测过了"
	if got := Fingerprint(in); got != base {
		t.Fatalf("Tested/Note 不该影响指纹:\n%s\n%s", base, got)
	}
}

// TestCapabilityFingerprintFramingIsUnambiguous 长度前缀定界：分隔符出现在值里不得伪造碰撞。
func TestCapabilityFingerprintFramingIsUnambiguous(t *testing.T) {
	a := Fingerprint(FingerprintInput{Skills: []SkillPart{{ID: "a|b", BodyDigest: "c"}}})
	b := Fingerprint(FingerprintInput{Skills: []SkillPart{{ID: "a", BodyDigest: "b|c"}}})
	if a == b {
		t.Fatal("字段拼接必须自定界（否则值里带分隔符就能伪造出同一指纹）")
	}
	// 同 id 重复出现与只出现一次必须是不同的指纹（不去重，见 sortSkills 注释）。
	once := Fingerprint(FingerprintInput{Skills: []SkillPart{{ID: "a", BodyDigest: "c"}}})
	twice := Fingerprint(FingerprintInput{Skills: []SkillPart{{ID: "a", BodyDigest: "c"}, {ID: "a", BodyDigest: "c"}}})
	if once == twice {
		t.Fatal("重复项不该被静默去重（会把「输入里多了一份定义」这个真实差异抹平）")
	}
}

// TestCapabilityFingerprintShape 指纹是 sha256 十六进制小写（可安全进日志/DB 列/事件载荷）。
func TestCapabilityFingerprintShape(t *testing.T) {
	got := Fingerprint(fpInput())
	if len(got) != 64 {
		t.Fatalf("应为 64 位十六进制，got %d 位: %q", len(got), got)
	}
	if got != strings.ToLower(got) {
		t.Fatalf("应为小写: %q", got)
	}
}
