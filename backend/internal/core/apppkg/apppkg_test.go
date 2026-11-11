package apppkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validManifestJSON 返回一份合法清单的 JSON（entrypoint 哈希由调用方替换/由测试目录实际内容决定）。
func validManifestJSON(t *testing.T, entrypoint, sha string) []byte {
	t.Helper()
	m := map[string]any{
		"id":          "my-app",
		"version":     "1.0.0",
		"api_version": "^1",
		"provides": map[string]any{
			"impls": []map[string]any{{"impl_id": "my.sync", "kind": "slot"}},
			"tools": []map[string]any{{"domain": "myai", "tools": []string{"echo"}}},
		},
		"permissions": []string{"read:usage"},
		"entrypoint":  entrypoint,
		"integrity":   map[string]any{"sha256": sha},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("构造清单 JSON 失败: %v", err)
	}
	return b
}

// ——— 解析 + 校验正/负例 ———

func TestParseManifestAccepts(t *testing.T) {
	// 用 testdata 里真实的最小样例包（哈希与 entrypoint.sh 实际内容一致）。
	raw, err := os.ReadFile(filepath.Join("testdata", "minimal-app", ManifestFileName))
	if err != nil {
		t.Fatalf("读 testdata 清单失败: %v", err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("合法清单不应被拒: %v", err)
	}
	if m.ID != "minimal-app" {
		t.Errorf("id 解析错: %q", m.ID)
	}
	if len(m.Provides.Impls) != 1 || m.Provides.Impls[0].ImplID != "minimal.sync" {
		t.Errorf("impls 解析错: %+v", m.Provides.Impls)
	}
	if len(m.Provides.Tools) != 1 || m.Provides.Tools[0].Domain != "minimal" {
		t.Errorf("tools 解析错: %+v", m.Provides.Tools)
	}
	if m.Provides.Page == nil || m.Provides.Page.Route != "/apps/minimal-app" {
		t.Errorf("page 解析错: %+v", m.Provides.Page)
	}
	if len(m.Permissions) != 1 || m.Permissions[0] != PermReadUsage {
		t.Errorf("permissions 解析错: %+v", m.Permissions)
	}
}

func TestParseManifestDefaultsAPIVersion(t *testing.T) {
	sha := strings.Repeat("a", 64)
	raw := validManifestJSON(t, "run.sh", sha)
	// 删掉 api_version，应被补成 ^1。
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	delete(obj, "api_version")
	raw, _ = json.Marshal(obj)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("缺省 api_version 应通过: %v", err)
	}
	if m.APIVersion != "^1" {
		t.Errorf("api_version 应补为 ^1，得到 %q", m.APIVersion)
	}
}

func TestParseManifestRejects(t *testing.T) {
	sha := strings.Repeat("a", 64)
	cases := []struct {
		name    string
		mutate  func(obj map[string]any)
		wantSub string
	}{
		{"id 大写非法", func(o map[string]any) { o["id"] = "MyApp" }, "id"},
		{"id 含下划线非法", func(o map[string]any) { o["id"] = "my_app" }, "id"},
		{"id 空", func(o map[string]any) { o["id"] = "" }, "id"},
		{"version 空", func(o map[string]any) { o["version"] = "" }, "version"},
		{"api_version 非 caret", func(o map[string]any) { o["api_version"] = "1.0" }, "api_version"},
		{"api_version 不兼容 ^2", func(o map[string]any) { o["api_version"] = "^2" }, "不兼容"},
		{"permission 不在白名单", func(o map[string]any) { o["permissions"] = []string{"root:all"} }, "白名单"},
		{"工具域含下划线", func(o map[string]any) {
			o["provides"] = map[string]any{"tools": []map[string]any{{"domain": "bad_dom", "tools": []string{"x"}}}}
		}, "domain"},
		{"impl_id 非法", func(o map[string]any) {
			o["provides"] = map[string]any{"impls": []map[string]any{{"impl_id": "Bad ID", "kind": "slot"}}}
		}, "impl_id"},
		{"impl kind 非法", func(o map[string]any) {
			o["provides"] = map[string]any{"impls": []map[string]any{{"impl_id": "a.b", "kind": "9bad"}}}
		}, "kind"},
		{"entrypoint 绝对路径", func(o map[string]any) { o["entrypoint"] = "/etc/passwd" }, "绝对路径"},
		{"entrypoint 上跳", func(o map[string]any) { o["entrypoint"] = "../evil.sh" }, "上跳"},
		{"entrypoint 空", func(o map[string]any) { o["entrypoint"] = "" }, "entrypoint"},
		{"sha256 缺失", func(o map[string]any) { o["integrity"] = map[string]any{} }, "sha256"},
		{"sha256 非 64 位", func(o map[string]any) { o["integrity"] = map[string]any{"sha256": "abc"} }, "sha256"},
		{"page route 非法", func(o map[string]any) {
			o["provides"] = map[string]any{"page": map[string]any{"nav_label": "X", "route": "no-slash"}}
		}, "route"},
		{"tagline 超长", func(o map[string]any) { o["tagline"] = strings.Repeat("字", MaxManifestTaglineRunes+1) }, "tagline"},
		{"examples 超条数", func(o map[string]any) {
			ex := make([]string, MaxManifestExamples+1)
			for i := range ex {
				ex[i] = "x"
			}
			o["examples"] = ex
		}, "examples"},
		{"usage_guide 超长", func(o map[string]any) { o["usage_guide"] = strings.Repeat("字", MaxManifestUsageRunes+1) }, "usage_guide"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj := map[string]any{}
			_ = json.Unmarshal(validManifestJSON(t, "run.sh", sha), &obj)
			c.mutate(obj)
			raw, _ := json.Marshal(obj)
			_, err := ParseManifest(raw)
			if err == nil {
				t.Fatalf("应被拒但通过了: %s", c.name)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("错误信息应含 %q，实际: %v", c.wantSub, err)
			}
		})
	}
}

func TestParseManifestRejectsUnknownField(t *testing.T) {
	// 严格解码：拼错的字段名应被拒（防静默失效）。
	raw := []byte(`{"id":"x","version":"1","entrypoint":"a.sh","integrity":{"sha256":"` + strings.Repeat("a", 64) + `"},"typo_field":1}`)
	if _, err := ParseManifest(raw); err == nil {
		t.Fatal("未知字段应被拒")
	}
}

// ——— 目录扫描 + 完整性（哈希）校验 ———

// writePkg 在 dir/<id>/ 下写一个包（manifest + entrypoint），返回实际入口内容。
func writePkg(t *testing.T, root, id, entrypoint, content, declaredSHAOverwrite string) {
	t.Helper()
	pkgDir := filepath.Join(root, id)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("建包目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, entrypoint), []byte(content), 0o644); err != nil {
		t.Fatalf("写 entrypoint 失败: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	if declaredSHAOverwrite != "" {
		sha = declaredSHAOverwrite
	}
	m := map[string]any{
		"id": id, "version": "1.0.0", "api_version": "^1",
		"provides":   map[string]any{"impls": []map[string]any{{"impl_id": "x.y", "kind": "slot"}}},
		"entrypoint": entrypoint, "integrity": map[string]any{"sha256": sha},
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(pkgDir, ManifestFileName), b, 0o644); err != nil {
		t.Fatalf("写 manifest 失败: %v", err)
	}
}

func TestScanEmptyDir(t *testing.T) {
	root := t.TempDir()
	res, err := Scan(root)
	if err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}
	if len(res.Loaded) != 0 || len(res.Failed) != 0 {
		t.Fatalf("空目录应空集合，得到 %+v", res)
	}
}

func TestScanNonExistentDir(t *testing.T) {
	res, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("不存在目录不应报错: %v", err)
	}
	if len(res.Loaded) != 0 || len(res.Failed) != 0 {
		t.Fatalf("不存在目录应空集合，得到 %+v", res)
	}
}

func TestScanLoadsValidPackage(t *testing.T) {
	root := t.TempDir()
	writePkg(t, root, "good-app", "run.sh", "#!/bin/sh\necho hi\n", "")
	res, err := Scan(root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("不应有失败包: %+v", res.Failed)
	}
	if len(res.Loaded) != 1 || res.Loaded[0].Manifest.ID != "good-app" {
		t.Fatalf("应装载 good-app，得到 %+v", res.Loaded)
	}
	if res.Loaded[0].EntrypointPath == "" {
		t.Error("EntrypointPath 不应为空")
	}
}

func TestScanRejectsTamperedPackage(t *testing.T) {
	root := t.TempDir()
	// 声明一个与真实内容不符的哈希 → 完整性校验失败，包进 Failed，不装载。
	writePkg(t, root, "tampered-app", "run.sh", "#!/bin/sh\necho real\n", strings.Repeat("0", 64))
	res, err := Scan(root)
	if err != nil {
		t.Fatalf("扫描不应整体报错: %v", err)
	}
	if len(res.Loaded) != 0 {
		t.Fatalf("被篡改的包不应装载，得到 %+v", res.Loaded)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("应有一个失败包，得到 %+v", res.Failed)
	}
	if !strings.Contains(res.Failed[0].Reason, "完整性校验失败") {
		t.Fatalf("失败原因应明示完整性校验失败，实际: %s", res.Failed[0].Reason)
	}
}

func TestScanIsolatesFailures(t *testing.T) {
	root := t.TempDir()
	writePkg(t, root, "ok-app", "run.sh", "#!/bin/sh\necho ok\n", "")
	writePkg(t, root, "bad-app", "run.sh", "#!/bin/sh\necho tampered\n", strings.Repeat("f", 64))
	res, err := Scan(root)
	if err != nil {
		t.Fatalf("扫描不应整体报错: %v", err)
	}
	// 一个成功、一个失败，互不影响（失败隔离）。
	if len(res.Loaded) != 1 || res.Loaded[0].Manifest.ID != "ok-app" {
		t.Fatalf("应装载 ok-app，得到 %+v", res.Loaded)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("应有一个失败包，得到 %+v", res.Failed)
	}
}

func TestScanMissingEntrypoint(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "no-entry")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"id": "no-entry", "version": "1", "entrypoint": "run.sh",
		"integrity": map[string]any{"sha256": strings.Repeat("a", 64)},
	}
	b, _ := json.Marshal(m)
	_ = os.WriteFile(filepath.Join(pkgDir, ManifestFileName), b, 0o644)
	res, err := Scan(root)
	if err != nil {
		t.Fatalf("扫描不应整体报错: %v", err)
	}
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Reason, "不可读") {
		t.Fatalf("缺 entrypoint 应进 Failed 并明示，得到 %+v", res.Failed)
	}
}

func TestWithinDir(t *testing.T) {
	dir := "/a/b/app"
	if !withinDir(dir, "/a/b/app/run.sh") {
		t.Error("包内文件应在 dir 内")
	}
	if withinDir(dir, "/a/b/app/../evil.sh") {
		t.Error("上跳应判越界")
	}
	if withinDir(dir, "/a/b/other/run.sh") {
		t.Error("兄弟目录不应在 dir 内")
	}
}

// TestWithinDirExported 断言导出封装 WithinDir 与包内 withinDir 语义一致（供 appmarket 复用）。
func TestWithinDirExported(t *testing.T) {
	dir := "/source/root"
	if !WithinDir(dir, "/source/root/apps/weather-query") {
		t.Error("来源根内子目录应在 dir 内")
	}
	if WithinDir(dir, "/etc/passwd") {
		t.Error("系统路径 /etc 不应被判在来源根内（防越界）")
	}
	if WithinDir(dir, "/source/root/../etc") {
		t.Error("上跳到兄弟目录应判越界")
	}
}

// TestParseManifestUsageFields 覆盖 v42 使用说明三字段的解析与规范化。
func TestParseManifestUsageFields(t *testing.T) {
	sha := strings.Repeat("a", 64)
	obj := map[string]any{}
	_ = json.Unmarshal(validManifestJSON(t, "run.sh", sha), &obj)
	obj["tagline"] = "  一句话能力句  "
	obj["examples"] = []string{"  示例一  ", "", "示例二"}
	obj["usage_guide"] = "  适用场景：xx  "
	raw, _ := json.Marshal(obj)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("合法使用说明不应被拒：%v", err)
	}
	if m.Tagline != "一句话能力句" {
		t.Fatalf("tagline 应去空白，得到 %q", m.Tagline)
	}
	if len(m.Examples) != 2 || m.Examples[0] != "示例一" {
		t.Fatalf("examples 应去空白并剔除空条，得到 %#v", m.Examples)
	}
	if m.UsageGuide != "适用场景：xx" {
		t.Fatalf("usage_guide 应去空白，得到 %q", m.UsageGuide)
	}
}
