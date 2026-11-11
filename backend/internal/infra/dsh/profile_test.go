package dsh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestProvisionProfileWritesFiles 幂等落盘四份 profile 文件。
func TestProvisionProfileWritesFiles(t *testing.T) {
	home := t.TempDir()
	n, err := ProvisionProfile(home)
	if err != nil {
		t.Fatalf("ProvisionProfile: %v", err)
	}
	if n != 4 {
		t.Fatalf("首次应写 4 个文件，实际 %d", n)
	}
	dir := ProfileDir(home)
	for _, f := range []string{"package.json", "cordis.yml", "cordis.patch.yml", "pnpm-workspace.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("缺文件 %s: %v", f, err)
		}
	}
	// 覆写层必须含 office 三件套 + 官方 disabled 门控原文。
	patch, _ := os.ReadFile(filepath.Join(dir, "cordis.patch.yml"))
	s := string(patch)
	for _, want := range []string{
		"id: workspace-dependencies",
		"id: skill-office",
		"id: office-to-pdf",
		"@deepseek-ai/dsh-tool-workspace-dependencies",
		"@deepseek-ai/dsh-skill-office",
		"@deepseek-ai/dsh-office-to-pdf",
		"DSH_PRIMARY_RUNTIME",
		// present / deliverable-file-references：照 sdk-app 原样补进 office block（一直启用）。
		"id: present",
		"@deepseek-ai/dsh-tool-present",
		"id: deliverable-file-references",
		"@deepseek-ai/dsh-deliverable-file-references",
		// time-context / tool-ask-user：standard preset 独有、acp 基座无（WP-A 补入，无 disabled 门控）。
		"id: time-context",
		"@deepseek-ai/dsh-time-context",
		"id: tool-ask-user",
		"@deepseek-ai/dsh-tool-ask-user",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("cordis.patch.yml 缺 %q", want)
		}
	}
	// 幂等：再跑一次不写任何文件。
	n2, err := ProvisionProfile(home)
	if err != nil || n2 != 0 {
		t.Fatalf("幂等重放应写 0 个，得到 %d / err=%v", n2, err)
	}
}

// TestProvisionProfileEmptyHome 空 homeDir 报错。
func TestProvisionProfileEmptyHome(t *testing.T) {
	if _, err := ProvisionProfile("  "); err == nil {
		t.Fatal("空 homeDir 应报错")
	}
}

// TestProvisionedProfileHasOfficeInDsh 端到端：provision 出来的 mate-acp 交给真实 dsh --dump-config，
// 断言 office 三件套进组合且 enabled、且 ACP 传输仍在。dsh 不在 PATH 时跳过。
func TestProvisionedProfileHasOfficeInDsh(t *testing.T) {
	bin, err := exec.LookPath("dsh")
	if err != nil {
		t.Skip("PATH 中没有 dsh，跳过端到端校验")
	}
	home := t.TempDir()
	if _, err := ProvisionProfile(home); err != nil {
		t.Fatalf("provision: %v", err)
	}
	// DSH_HOME 与 profile 落点一致：<home>/runtime/dsh。
	dshHome := filepath.Join(home, "runtime", "dsh")
	cmd := exec.Command(bin, "--profile", ProfileName, "--dump-config")
	cmd.Env = append(os.Environ(), "DSH_HOME="+dshHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dsh --dump-config 失败（profile 可能让底座拒启）：%v\n%s", err, out)
	}
	s := string(out)
	// ACP 传输要在。
	if !strings.Contains(s, "id: acp-app-startup") || !strings.Contains(s, "id: acp\n") {
		t.Errorf("ACP 传输未进组合（被换了？）")
	}
	// office 三件套 + time-context/tool-ask-user 要在且 enabled（条目往下 220 字符内不应有 disabled: true）。
	for _, id := range []string{
		"id: workspace-dependencies", "id: skill-office", "id: office-to-pdf",
		"id: time-context", "id: tool-ask-user",
	} {
		idx := strings.Index(s, id)
		if idx < 0 {
			t.Fatalf("dump 里没有 %q（office 未进组合）", id)
		}
		tail := s[idx:min(idx+220, len(s))]
		if strings.Contains(tail, "disabled: true") {
			t.Errorf("%q 被 disabled 关掉了：\n%s", id, tail)
		}
	}
}

// TestBundledEnvFromOwnDirs 由 ark-mate 自有目录派生 4 env；任一为空返回 nil（不借用外部）。
//
// DSH_BUNDLED_OFFICE_CLI 不再是 skills 目录本身：它由 officeSkills 同级的 libreoffice-kit/lib/cli.js
// 推导，且必须是**文件**（dsh 的 skill-office 用 statSync(cli).isFile() 校验）。这里造一个真实的
// cli.js 文件来断言完整路径；文件不在场时应显式置空串（见 TestBundledEnvOfficeCLIIsFile）。
func TestBundledEnvFromOwnDirs(t *testing.T) {
	if got := BundledEnv("", "/x/office"); got != nil {
		t.Fatalf("缺 primary-runtime 应返回 nil，得到 %v", got)
	}
	if got := BundledEnv("/x/pr", ""); got != nil {
		t.Fatalf("缺 office skills 应返回 nil，得到 %v", got)
	}
	// 造 <载荷>/node_modules/@deepseek-ai/dsh-skill-office/assets + 同级 libreoffice-kit/lib/cli.js。
	root := t.TempDir()
	officeSkills := filepath.Join(root, "node_modules", "@deepseek-ai", "dsh-skill-office", "assets")
	if err := os.MkdirAll(officeSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	cliDir := filepath.Join(root, "node_modules", "@deepseek-ai", "libreoffice-kit", "lib")
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(cliDir, "cli.js")
	if err := os.WriteFile(cliPath, []byte("// cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := BundledEnv("/opt/pr", officeSkills)
	want := map[string]string{
		"DSH_PRIMARY_RUNTIME":       "/opt/pr",
		"DSH_BUNDLED_OFFICE_SKILLS": officeSkills,
		"DSH_BUNDLED_OFFICE_CLI":    cliPath,
		"DSH_BUNDLED_OFFICE_NODE":   "/opt/pr/dependencies/node/bin/node",
	}
	if len(got) != len(want) {
		t.Fatalf("应 4 个 env，得到 %v", got)
	}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if want[k] != v {
			t.Errorf("%s=%q 期望 %q", k, v, want[k])
		}
	}
}

// TestBundledEnvOfficeCLIIsFile 断言 DSH_BUNDLED_OFFICE_CLI 的语义正确：
//   - 有真实 cli.js 时，是**以 cli.js 结尾的文件路径**（非目录）——dsh 的 isFile() 校验才能过；
//   - cli.js 不在场时，显式置空串（显式关闭），而不是退回 skills 目录（目录会误抛错）。
func TestBundledEnvOfficeCLIIsFile(t *testing.T) {
	root := t.TempDir()
	officeSkills := filepath.Join(root, "node_modules", "@deepseek-ai", "dsh-skill-office", "assets")
	if err := os.MkdirAll(officeSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	// 文件不在场：CLI 置空串（显式关闭），不退回目录。
	got := BundledEnv("/opt/pr", officeSkills)
	var cliVal string
	for _, kv := range got {
		if k, v, _ := strings.Cut(kv, "="); k == "DSH_BUNDLED_OFFICE_CLI" {
			cliVal = v
		}
	}
	if cliVal != "" {
		t.Fatalf("cli.js 不在场时 DSH_BUNDLED_OFFICE_CLI 应为空串（显式关闭），得到 %q", cliVal)
	}
	// 造真实 cli.js：应解析为以 cli.js 结尾的文件（不是目录）。
	cliDir := filepath.Join(root, "node_modules", "@deepseek-ai", "libreoffice-kit", "lib")
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(cliDir, "cli.js")
	if err := os.WriteFile(cliPath, []byte("// cli"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = BundledEnv("/opt/pr", officeSkills)
	for _, kv := range got {
		if k, v, _ := strings.Cut(kv, "="); k == "DSH_BUNDLED_OFFICE_CLI" {
			cliVal = v
		}
	}
	if !strings.HasSuffix(cliVal, "cli.js") {
		t.Fatalf("DSH_BUNDLED_OFFICE_CLI 应以 cli.js 结尾，得到 %q", cliVal)
	}
	if fi, err := os.Stat(cliVal); err != nil || fi.IsDir() {
		t.Fatalf("DSH_BUNDLED_OFFICE_CLI 应是存在的文件（非目录），得到 %q / err=%v", cliVal, err)
	}
}

// TestResolveBundledDirsFromEntry 由 MATE_DSH_ENTRY 推导 ark-mate 自有资产目录（不碰桌面端）。
func TestResolveBundledDirsFromEntry(t *testing.T) {
	root := t.TempDir()
	// 造 <payloads>/dsh/v1/node_modules/@deepseek-ai/dsh/lib/bin.js
	entry := filepath.Join(root, "dsh", "v1", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	// office assets: 同级 @deepseek-ai/dsh-skill-office/assets/office-docx/SKILL.md
	osi := filepath.Join(root, "dsh", "v1", "node_modules", "@deepseek-ai", "dsh-skill-office", "assets", "office-docx")
	if err := os.MkdirAll(osi, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(osi, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// primary-runtime: <payloads>/primary-runtime/runtime.json
	prDir := filepath.Join(root, "primary-runtime")
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prDir, "runtime.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MATE_DSH_ENTRY", entry)
	t.Setenv("MATE_DSH_BUNDLED_RUNTIME", "")
	t.Setenv("MATE_DSH_BUNDLED_OFFICE_SKILLS", "")
	pr, os2 := ResolveBundledDirs()
	if pr != prDir {
		t.Errorf("primary-runtime 应得 %q，得到 %q", prDir, pr)
	}
	wantOS := filepath.Join(root, "dsh", "v1", "node_modules", "@deepseek-ai", "dsh-skill-office", "assets")
	if os2 != wantOS {
		t.Errorf("office skills 应得 %q，得到 %q", wantOS, os2)
	}
}

// TestResolveBundledDirsNoEntry 无 MATE_DSH_ENTRY 且无显式 env → 两目录皆空（不借用桌面端）。
func TestResolveBundledDirsNoEntry(t *testing.T) {
	t.Setenv("MATE_DSH_ENTRY", "")
	t.Setenv("MATE_DSH_BUNDLED_RUNTIME", "")
	t.Setenv("MATE_DSH_BUNDLED_OFFICE_SKILLS", "")
	pr, os2 := ResolveBundledDirs()
	if pr != "" || os2 != "" {
		t.Fatalf("无 entry 无 env 应两空（绝不借桌面端），得到 pr=%q os=%q", pr, os2)
	}
}

// TestBuildProfilePatchYMLAnswerer answerer 条目：有路径则追加且 YAML 可解析、含绝对路径与门控；
// 无路径则不写条目（如实关闭，不误开）。
func TestBuildProfilePatchYMLAnswerer(t *testing.T) {
	// 有路径：追加条目，name 是绝对路径，带 disabled 门控。
	plugin := "/opt/ark-mate/ark-mate-answerer/lib/index.js"
	withEntry := buildProfilePatchYML(plugin)
	for _, want := range []string{
		"id: ark-mate-answerer",
		"name: '" + plugin + "'",
		"MATE_ANSWERER_ENDPOINT",
	} {
		if !strings.Contains(withEntry, want) {
			t.Errorf("含 answerer 路径的 patch 缺 %q", want)
		}
	}
	var doc any
	if err := yaml.Unmarshal([]byte(withEntry), &doc); err != nil {
		t.Fatalf("含 answerer 条目的 patch 不是合法 YAML: %v", err)
	}
	// 无路径：不追加条目。
	without := buildProfilePatchYML("")
	if strings.Contains(without, "ark-mate-answerer") {
		t.Errorf("无路径时不应写 answerer 条目")
	}
	if err := yaml.Unmarshal([]byte(without), &doc); err != nil {
		t.Fatalf("无 answerer 条目的 patch 不是合法 YAML: %v", err)
	}
}

// TestAnswererEnv 端点或插件缺失 → nil（如实关闭）；两者都在 → 3 个 KEY=VALUE，令牌非空。
func TestAnswererEnv(t *testing.T) {
	// 显式给一个存在的插件文件。
	dir := t.TempDir()
	plugin := filepath.Join(dir, "index.js")
	if err := os.WriteFile(plugin, []byte("export function apply(){}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MATE_ANSWERER_PLUGIN", plugin)

	if env := AnswererEnv(""); len(env) != 0 {
		t.Fatalf("端点为空应返回 nil，得到 %v", env)
	}
	env := AnswererEnv("http://127.0.0.1:10001")
	if len(env) != 3 {
		t.Fatalf("应返回 3 个 env，得到 %v", env)
	}
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if m["MATE_ANSWERER_ENDPOINT"] != "http://127.0.0.1:10001" || m["MATE_ANSWERER_PLUGIN"] != plugin {
		t.Fatalf("env 值不符: %+v", m)
	}
	if m["MATE_ANSWERER_TOKEN"] == "" {
		t.Fatalf("令牌不应为空")
	}
}
