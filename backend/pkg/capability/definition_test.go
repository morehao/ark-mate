package capability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefinitionFilePreservesUnknownKeys C9 的核心：未知键（含嵌套结构与不常见取值）必须原样留下。
//
// 断言是**语义**级而不是逐字节级：平台会把文件重新序列化（键排序 + 2 空格缩进），所以排版会
// 规范化，但值与结构一个都不许丢、不许变。用户手写的自定义字段就在这一类里。
func TestDefinitionFilePreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	// 故意写得很乱：未排序的键、单行数组、嵌套对象、null、数字、布尔、unicode。
	original := `{"id":"demo","x-user-note":{"keep":["me",1,true,null],"deep":{"a":{"b":[]}}},"body":"# 正文\n含 emoji 🎯","enabled":true}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := ReadDefinitionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Has("x-user-note") || f.Has("nope") {
		t.Fatalf("Has 判定不符")
	}
	if err := f.Set("enabled", false); err != nil {
		t.Fatal(err)
	}
	if !f.Dirty() {
		t.Fatal("改过键应脏")
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	// 逐键语义比对（未知键一个都不能少）。
	var before, after map[string]any
	if err := json.Unmarshal([]byte(original), &before); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("键数变了（丢键或加键）：before=%v after=%v", keysOf(before), keysOf(after))
	}
	for k, v := range before {
		if k == "enabled" {
			if after[k] != false {
				t.Fatalf("enabled 应被改成 false，got %v", after[k])
			}
			continue
		}
		bj, _ := json.Marshal(v)
		aj, _ := json.Marshal(after[k])
		if string(bj) != string(aj) {
			t.Fatalf("未知键 %q 的值被改动了：before=%s after=%s", k, bj, aj)
		}
	}
	// 权限位保留（0600 不能被悄悄放宽成 0644）。
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限位应保留 0600，got %v", fi.Mode().Perm())
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDefinitionFileStableAndIdempotent 第二次写回逐字节稳定（幂等的落点），且未改动时不触碰文件。
func TestDefinitionFileStableAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	if err := os.WriteFile(path, []byte(`{"b":2,"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := ReadDefinitionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Dirty() {
		t.Fatal("刚读出来不该是脏的")
	}
	// 未改动 → Save 什么都不做（mtime 都不动）。
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("未改动时不该触碰文件")
	}
	// 设成同样的值 → 不算改动。
	if err := f.Set("a", 1); err != nil {
		t.Fatal(err)
	}
	if f.Dirty() {
		t.Fatal("把值设成原值不该算改动")
	}
	// 真正改一次：写出的内容键序稳定、末尾有换行。
	if err := f.Set("c", []string{"x", "y"}); err != nil {
		t.Fatal(err)
	}
	want, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("落盘内容应与 Marshal 一致:\n%q\nwant\n%q", got, want)
	}
	if !strings.HasSuffix(string(got), "\n") {
		t.Fatal("应有一个末尾换行")
	}
	// 再读再写（不设已存在的键）→ 逐字节相同。
	f2, err := ReadDefinitionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f2.Set("c", []string{"x", "y"}); err != nil {
		t.Fatal(err)
	}
	if f2.Dirty() {
		t.Fatal("同值重复设置不该脏")
	}
	if err := f2.Save(); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(path)
	if string(got2) != string(got) {
		t.Fatalf("幂等重写应逐字节一致:\n%q\n%q", got, got2)
	}
}

// TestDefinitionFileOptimisticLock 读取后被外部改动 → 拒绝覆盖（用户的手改不能被砸掉）。
func TestDefinitionFileOptimisticLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	if err := os.WriteFile(path, []byte(`{"id":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := ReadDefinitionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Set("enabled", false); err != nil {
		t.Fatal(err)
	}
	// 外部改动（模拟用户在面板操作期间手改了文件）。mtime 精度在某些文件系统上很粗，
	// 显式把 mtime 推后并改长度，保证复核必然发现。
	if err := os.WriteFile(path, []byte(`{"id":"demo","user":"手改"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	err = f.Save()
	if err == nil || !strings.Contains(err.Error(), "被外部改动") {
		t.Fatalf("外部改动应拒写: %v", err)
	}
	// 目标文件必须**一个字节都没变**（还是用户手改的那份）。
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "手改") {
		t.Fatalf("拒绝写入时不该改目标文件: %s", got)
	}
	// 目录里不该留下 staging 残骸。
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("拒绝写入后不该留下临时文件: %s", e.Name())
		}
	}
}

// TestDefinitionFileDeleteAndDecode 摘除键 / 读未知键（迁移与写面都靠这两个口）。
func TestDefinitionFileDeleteAndDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	if err := os.WriteFile(path, []byte(`{"id":"demo","sources":[{"runtime":"b"},{"runtime":"a"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := ReadDefinitionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var srcs []struct {
		Runtime string `json:"runtime"`
	}
	if ok, err := f.Decode("sources", &srcs); err != nil || !ok || len(srcs) != 2 || srcs[0].Runtime != "b" {
		t.Fatalf("Decode 不符: ok=%v err=%v srcs=%+v", ok, err, srcs)
	}
	if ok, _ := f.Decode("nope", &srcs); ok {
		t.Fatal("不存在的键应 ok=false")
	}
	if !f.Delete("sources") {
		t.Fatal("Delete 应报 true")
	}
	if f.Delete("sources") {
		t.Fatal("重复 Delete 应报 false")
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "sources") {
		t.Fatalf("sources 应已摘除: %s", raw)
	}
}

// TestDefinitionFileRejectsNonObject 顶层不是 JSON 对象 → 明确报错（不是"当作空文档继续写"）。
func TestDefinitionFileRejectsNonObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	for _, bad := range []string{`[1,2]`, `not-json{`} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDefinitionFile(path); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
	// 不存在 → os.IsNotExist 可判定（调用方据此给"无处可写"的出口）。
	if _, err := ReadDefinitionFile(filepath.Join(dir, "missing.json")); !os.IsNotExist(err) {
		t.Fatalf("不存在应返回 IsNotExist 错误: %v", err)
	}
}
