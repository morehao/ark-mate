package sessionruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	defn "github.com/morehao/ark-mate/internal/definition"
	"github.com/morehao/ark-mate/internal/mechanism/definition"

	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/code"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/gerror"
)

// 本文件是**文件载体**（memory 模式 / 导入导出共用）那一层的单测：`definition.WriteDefinition` /
// `definition.OpenDefinition` / `definition.RemoveDefinitionPermanently` 与 C9 的落盘纪律（未知键保留、原子替换、
// 权限沿用、mtime 乐观锁）。DB 载体的同一批对外语义在 `capability_writeface_test.go`
// （同一套断言跑两种载体）与 `capability_write_store_test.go`（事务 + revision CAS）。
//
// readDef 读定义文件为 map（断言用；空文件/不存在 → 空 map）。
func readDef(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("解析 %s: %v（原文 %s）", path, err, b)
	}
	return m
}

// numericCode 取错误归一后的**数值**业务码（`code.FromError` 的出口码）。
func numericCode(err error) int {
	gerr, ok := code.FromError(err).(gerror.Error)
	if !ok {
		return 0
	}
	return gerr.Code
}

// errCode 取 acl 错误的稳定 code（非 acl 错误 → 空串）。
func errCode(err error) string {
	if ae, ok := acl.As(err); ok {
		return ae.Code
	}
	return ""
}

// TestWriteDefinitionCreatesAndPreserves 新建 → 只写 patch 里的键；更新 → 未知键与未提及的键原样保留。
func TestWriteDefinitionCreatesAndPreserves(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")

	// 新建：文件里只有 patch 的键。
	if err := definition.WriteDefinition(dir, "skill", "demo", map[string]any{"id": "demo", "body": "# demo"}); err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	path := filepath.Join(dir, "demo.json")
	got := readDef(t, path)
	if got["id"] != "demo" || got["body"] != "# demo" || len(got) != 2 {
		t.Fatalf("新建内容不符: %+v", got)
	}

	// 手工加上一个平台不认识的键（模拟用户自己的字段/别的工具写的字段），并把权限收紧到 0600。
	// 注意 os.WriteFile 的权限参数**只在创建时生效**，改已存在文件的权限要显式 Chmod。
	raw := []byte(`{"id":"demo","body":"# demo","my_note":"留着","future_key":{"a":[1,2]}}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	// 更新一个键 → 未知键与未提及的键必须原样在。
	if err := definition.WriteDefinition(dir, "skill", "demo", map[string]any{"enabled": false}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	got = readDef(t, path)
	if got["enabled"] != false {
		t.Fatalf("目标键没写进去: %+v", got)
	}
	if got["my_note"] != "留着" {
		t.Fatalf("未知键丢失: %+v", got)
	}
	if fk, ok := got["future_key"].(map[string]any); !ok || len(fk["a"].([]any)) != 2 {
		t.Fatalf("未知嵌套键丢失或形态变了: %+v", got["future_key"])
	}
	if got["id"] != "demo" || got["body"] != "# demo" {
		t.Fatalf("未提及的键被改写: %+v", got)
	}
	// 权限沿用（0600 不该被重置成 0644：定义文件里可能有内部 URL）。
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限应沿用 0600，got %v", fi.Mode().Perm())
	}
}

// TestWriteDefinitionNilDeletesKey patch 值为 nil = 删键（PUT 里"要清空请显式传 null"的落点）。
func TestWriteDefinitionNilDeletesKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	path := filepath.Join(dir, "demo.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"demo","name":"Demo","enabled":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := definition.WriteDefinition(dir, "skill", "demo", map[string]any{"name": nil, "enabled": false}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	got := readDef(t, path)
	if _, still := got["name"]; still {
		t.Fatalf("nil 应删键: %+v", got)
	}
	if got["enabled"] != false || got["id"] != "demo" {
		t.Fatalf("其余键不符: %+v", got)
	}
}

// TestWriteDefinitionIdempotent 空 patch 与同值 patch 都**不碰文件**（mtime 不变）：
// 幂等不是"结果一样"，而是"没有多余的写"——每次无谓重写都会让下游（migrate/编辑器）看到假变更。
func TestWriteDefinitionIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if err := definition.WriteDefinition(dir, "skill", "demo", map[string]any{"id": "demo", "body": "# demo"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "demo.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 空 patch。
	if err := definition.WriteDefinition(dir, "skill", "demo", nil); err != nil {
		t.Fatal(err)
	}
	// 同值 patch（注意：值相同但排版不同——文件里是 2 空格缩进，patch 是紧凑 JSON）。
	if err := definition.WriteDefinition(dir, "skill", "demo", map[string]any{"body": "# demo"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("无变更的写入不该动文件：before=%v/%d after=%v/%d",
			before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
}

// TestWriteDefinitionConflictMapsToDefinitionChanged 读→写窗口内被外部改动 → `definition_changed`
// （40901），**绝不 last-write-wins**（风险表第 3 行）。
//
// 检测本身在 `pkg/capability`（`TestDefinitionFileOptimisticLock` 用真实文件改动验证）；这里验的是
// **语义映射**：冲突必须落成"请重试"的 40901，而不是含混的 50000——用户的动作完全不同
// （刷新重试 vs 报障）。
func TestWriteDefinitionConflictMapsToDefinitionChanged(t *testing.T) {
	err := definition.DefinitionSaveErr("/tmp/x/demo.json", &capability.ConflictError{Msg: "被外部改动"})
	if got := errCode(err); got != "definition_changed" {
		t.Fatalf("冲突应映射成 definition_changed，got %q（%v）", got, err)
	}
	// 数值码也要对：40901 而不是 40001/50000（前端按码分派"重试/报障"两条路）。
	if n := numericCode(err); n != code.Conflict {
		t.Fatalf("冲突应是 40901，got %d", n)
	}
	// IO 失败仍是 50000（不是"请重试"，而是"服务端出错了"）。
	ioErr := definition.DefinitionSaveErr("/tmp/x/demo.json", errors.New("disk on fire"))
	if got := errCode(ioErr); got != "definition_write_failed" {
		t.Fatalf("IO 失败应是 definition_write_failed，got %q", got)
	}
}

// TestWriteDefinitionRejectsBadID id 会被拼进路径，**这是一条安全边界**：目录穿越必须在
// 拼路径之前就被拒（上游 Register* 的 nameRE 也拦，但删除/迁移路径也会走到这里）。
func TestWriteDefinitionRejectsBadID(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "skills")
	for _, bad := range []string{"", ".", "..", "../evil", "a/b", `a\b`, "a/../b", strings.Repeat("x", 65)} {
		err := definition.WriteDefinition(dir, "skill", bad, map[string]any{"id": bad})
		if err == nil {
			t.Fatalf("id %q 应被拒", bad)
		}
		if got := errCode(err); got != "invalid_param" {
			t.Fatalf("id %q 的错误码应是 invalid_param，got %q", bad, got)
		}
	}
	// 目录外没有落任何东西（连目录都不该被建出来）。
	if entries, err := os.ReadDir(base); err != nil {
		t.Fatal(err)
	} else {
		for _, e := range entries {
			if e.Name() != "skills" {
				t.Fatalf("不该在能力目录外产生文件: %s", e.Name())
			}
		}
	}
	if _, err := os.Stat(filepath.Join(base, "evil.json")); !os.IsNotExist(err) {
		t.Fatal("目录穿越不应落文件")
	}
}

// TestWriteDefinitionBrokenFileIsUserError 盘上文件不是 JSON 对象 → 40001 `definition_invalid`
// （**出路是去修那个文件**），而不是"写入失败 50000"。
func TestWriteDefinitionBrokenFileIsUserError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "demo.json")
	if err := os.WriteFile(path, []byte(`["not","an","object"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := definition.WriteDefinition(dir, "skill", "demo", map[string]any{"enabled": false})
	if err == nil {
		t.Fatal("坏文件应报错而不是覆盖它")
	}
	if got := errCode(err); got != "definition_invalid" {
		t.Fatalf("应报 definition_invalid，got %q（%v）", got, err)
	}
	// 原文件一个字节都不变。
	b, _ := os.ReadFile(path)
	if string(b) != `["not","an","object"]` {
		t.Fatalf("坏文件不该被改动: %s", b)
	}
}

// TestRemoveDefinitionIdempotent 删除幂等：存在 → true，不存在 → false 且不报错
// （DELETE 的语义是"让它不在"，不是"证明它曾在"）。
func TestRemoveDefinitionIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mcp")
	if err := definition.WriteDefinition(dir, "mcp", "fs", map[string]any{"id": "fs"}); err != nil {
		t.Fatal(err)
	}
	ok, err := definition.RemoveDefinitionPermanently(dir, "mcp", "fs")
	if err != nil || !ok {
		t.Fatalf("首次删除应 true: ok=%v err=%v", ok, err)
	}
	ok, err = definition.RemoveDefinitionPermanently(dir, "mcp", "fs")
	if err != nil || ok {
		t.Fatalf("再次删除应 false 且无错: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fs.json")); !os.IsNotExist(err) {
		t.Fatal("文件应已删除")
	}
}

// TestCapabilityDirUnsetIsActionable 未配置能力目录 → `capability_dir_unset`（40001）且文案给出路。
func TestCapabilityDirUnsetIsActionable(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), nil) // capabilityDir 为空
	if _, err := svc.Planes().Definitions.CapabilityDirFor(defn.CapSubSkills); err == nil {
		t.Fatal("未配置目录应报错")
	} else if got := errCode(err); got != "capability_dir_unset" {
		t.Fatalf("错误码应为 capability_dir_unset，got %q", got)
	} else if !strings.Contains(err.Error(), "MATE_CAPABILITIES") {
		t.Fatalf("文案应给出路（该设哪个键），got %v", err)
	}
}
