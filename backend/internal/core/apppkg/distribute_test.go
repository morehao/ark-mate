package apppkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ——— WP-C8：分发 / 版本门槛 / 可复用完整性校验 ———

// ——— 语义化版本比较 ———

func TestParseAndCompareSemanticVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int // a 相对 b
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.2.0", "1.1.9", 1},
		{"v2.0.0", "1.9.9", 1},
		{"1", "1.0.0", 0},          // 缺省段补 0
		{"1.2", "1.2.0", 0},        // 缺省段补 0
		{"1.2.3-rc.1", "1.2.3", 0}, // 后缀排序里忽略
		{"1.2.3+build", "1.2.3", 0},
	}
	for _, c := range cases {
		av, err := parseSemanticVersion(c.a)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", c.a, err)
		}
		bv, err := parseSemanticVersion(c.b)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", c.b, err)
		}
		if got := compareSemanticVersion(av, bv); got != c.want {
			t.Errorf("compare(%q,%q)=%d，want %d", c.a, c.b, got, c.want)
		}
	}

	// 不可解析：空、纯后缀、段非数字、段数超三段。
	for _, bad := range []string{"", "dev", "-rc.1", "1.x.0", "1.2.3.4", "1..2"} {
		if _, err := parseSemanticVersion(bad); err == nil {
			t.Errorf("非语义化版本 %q 应解析失败", bad)
		}
	}
}

// ——— min core version 门槛 ———

func manifestWithMinCore(t *testing.T, minCore string) *Manifest {
	t.Helper()
	m := &Manifest{ID: "gate-app", Version: "1.0.0", Entrypoint: "e.sh",
		Integrity: ManifestIntegrity{SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}}
	if minCore != "" {
		m.Requires = &ManifestRequires{MinCoreVersion: minCore}
	}
	return m
}

func TestCheckMinCoreVersion(t *testing.T) {
	// 不设门槛 → 恒通过（含 coreVersion 说不清也不管）。
	if err := CheckMinCoreVersion(manifestWithMinCore(t, ""), "dev"); err != nil {
		t.Errorf("未声明门槛应通过，got %v", err)
	}

	// 核心够新（>= 要求）→ 通过。
	if err := CheckMinCoreVersion(manifestWithMinCore(t, "1.2.0"), "1.2.0"); err != nil {
		t.Errorf("核心 == 要求应通过，got %v", err)
	}
	if err := CheckMinCoreVersion(manifestWithMinCore(t, "1.2.0"), "1.10.3"); err != nil {
		t.Errorf("核心 > 要求应通过，got %v", err)
	}

	// 核心过旧 → 拒绝（明示「低于最低要求」）。
	err := CheckMinCoreVersion(manifestWithMinCore(t, "2.0.0"), "1.9.9")
	if err == nil {
		t.Fatalf("核心低于要求应被拒")
	}
	if !contains(err.Error(), "低于") {
		t.Errorf("拒绝原因应明示「低于最低版本」，got %v", err)
	}

	// 核心版本说不清（dev）→ fail-closed 拒绝（不当成满足）。
	if err := CheckMinCoreVersion(manifestWithMinCore(t, "1.0.0"), "dev"); err == nil {
		t.Errorf("核心版本 dev 且声明了门槛应 fail-closed 拒绝，got nil")
	}

	// 门槛串写错（非语义化）→ 拒绝（不静默当成通过）。
	if err := CheckMinCoreVersion(manifestWithMinCore(t, "abc"), "9.9.9"); err == nil {
		t.Errorf("门槛串非法应被拒，got nil")
	}
}

func TestCurrentCoreVersionIsVersionVariable(t *testing.T) {
	// 门槛的当前核心版本来源必须是 version.Version（唯一版本源），不是硬编码。
	if got := CurrentCoreVersion(); got == "" {
		t.Fatalf("CurrentCoreVersion 不应为空")
	}
}

// ——— 可复用完整性校验 ———

// writePkgDir 在 dir 下铺一个包目录（manifest + entrypoint），返回包目录与 entrypoint 的 sha256。
func writePkgDir(t *testing.T, dir, id, entrypointContent, minCore string) (pkgDir, sha string) {
	t.Helper()
	pkgDir = filepath.Join(dir, id)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("建包目录失败: %v", err)
	}
	sum := sha256.Sum256([]byte(entrypointContent))
	sha = hex.EncodeToString(sum[:])
	m := map[string]any{
		"id":          id,
		"version":     "1.0.0",
		"api_version": "^1",
		"provides":    map[string]any{},
		"entrypoint":  "e.sh",
		"integrity":   map[string]any{"sha256": sha},
	}
	if minCore != "" {
		m["requires"] = map[string]any{"min_core_version": minCore}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("构造清单失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, ManifestFileName), b, 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "e.sh"), []byte(entrypointContent), 0o755); err != nil {
		t.Fatalf("写入口失败: %v", err)
	}
	return pkgDir, sha
}

func TestVerifyEntrypointIntegrity(t *testing.T) {
	root := t.TempDir()
	pkgDir, _ := writePkgDir(t, root, "good-app", "#!/bin/sh\necho hi\n", "")

	// 取回已解析清单。
	raw, err := os.ReadFile(filepath.Join(pkgDir, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}

	// 未篡改 → 通过。
	if err := VerifyEntrypointIntegrity(pkgDir, m); err != nil {
		t.Fatalf("未篡改包应通过完整性校验，got %v", err)
	}

	// 篡改 entrypoint → 失败，且错误是 integrityError（可区分）。
	if err := os.WriteFile(filepath.Join(pkgDir, "e.sh"), []byte("#!/bin/sh\nrm -rf /\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = VerifyEntrypointIntegrity(pkgDir, m)
	if err == nil {
		t.Fatalf("篡改后应完整性校验失败")
	}
	var ie *integrityError
	if !errors.As(err, &ie) {
		t.Errorf("篡改错误应是 *integrityError，got %T: %v", err, err)
	}
}

// ——— Distribute：扫描 + 完整性 + 版本门槛 汇总 ———

func TestDistributeAppliesVersionGateAndIsolatesFailures(t *testing.T) {
	root := t.TempDir()
	// 三个包：
	//   ok-app      版本门槛 1.0.0，核心 1.5.0 → 通过
	//   too-new     版本门槛 9.0.0，核心 1.5.0 → 版本门槛拒（进 Failed）
	//   tampered    扫描阶段完整性不符（改写 entrypoint）→ 进 Failed（扫描侧）
	writePkgDir(t, root, "ok-app", "ok\n", "1.0.0")
	writePkgDir(t, root, "too-new", "new\n", "9.0.0")
	tamperedDir, _ := writePkgDir(t, root, "tampered", "orig\n", "")
	// 篡改 tampered 的 entrypoint，让扫描侧完整性校验失败。
	if err := os.WriteFile(filepath.Join(tamperedDir, "e.sh"), []byte("changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	dist, err := Distribute(root, "1.5.0")
	if err != nil {
		t.Fatalf("Distribute 整体不应报错（单包失败隔离），got %v", err)
	}

	if len(dist.Loaded) != 1 || dist.Loaded[0].Manifest.ID != "ok-app" {
		t.Fatalf("只应装载 ok-app，got %+v", loadedIDs(dist))
	}
	// 两个失败包都在 Failed 里，各带原因。
	if len(dist.Failed) != 2 {
		t.Fatalf("应有 2 个失败包（too-new + tampered），got %d: %+v", len(dist.Failed), dist.Failed)
	}
	reasons := map[string]string{}
	for _, f := range dist.Failed {
		reasons[filepath.Base(f.Dir)] = f.Reason
	}
	if !contains(reasons["too-new"], "低于") {
		t.Errorf("too-new 应因版本门槛被拒（原因含「低于」），got %q", reasons["too-new"])
	}
	if !contains(reasons["tampered"], "完整性") {
		t.Errorf("tampered 应因完整性失败被拒（原因含「完整性」），got %q", reasons["tampered"])
	}
}

func TestDistributeMissingDirIsEmptyNotError(t *testing.T) {
	// 不存在的目录 = 空集合、不报错（对齐「无定义照跑」）。
	dist, err := Distribute(filepath.Join(t.TempDir(), "does-not-exist"), "1.0.0")
	if err != nil {
		t.Fatalf("缺失目录不应报错，got %v", err)
	}
	if len(dist.Loaded) != 0 || len(dist.Failed) != 0 {
		t.Fatalf("缺失目录应产出空 Distribution，got %+v", dist)
	}
}

func TestDistributeCoreVersionUnparseableFailsGatedPackages(t *testing.T) {
	// 核心版本 dev + 包声明门槛 → 该包被拒（fail-closed）；未声明门槛的包不受影响。
	root := t.TempDir()
	writePkgDir(t, root, "gated", "g\n", "1.0.0")
	writePkgDir(t, root, "ungated", "u\n", "")

	dist, err := Distribute(root, "dev")
	if err != nil {
		t.Fatalf("Distribute 不应整体报错，got %v", err)
	}
	if len(dist.Loaded) != 1 || dist.Loaded[0].Manifest.ID != "ungated" {
		t.Fatalf("dev 核心下只应装载未设门槛的 ungated，got %+v", loadedIDs(dist))
	}
}

// ——— 测试辅助 ———

func loadedIDs(d *Distribution) []string {
	out := make([]string, 0, len(d.Loaded))
	for _, p := range d.Loaded {
		out = append(out, p.Manifest.ID)
	}
	return out
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
