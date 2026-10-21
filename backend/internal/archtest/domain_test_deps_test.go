package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ftsSQLiteTest 领域层里**唯一允许**直连 SQLite 的测试文件（DoD3 收窄口径的例外）。
//
// 它验的是 FTS5 语义（分词、rank、snippet）与 sqlite 读模型——那是 SQLite 特有的实现，
// 没有内存替身；见 `docs/agents/guards.md` 与计划 §6 DoD 的 2026-10-21 注记。
const ftsSQLiteTest = "internal/domain/session/messages_search_test.go"

// TestDomainTestsStayOffSQLite DoD3 的**收窄口径**判据（2026-10-21 用户裁决）：
// 「聚合读写测试不依赖 SQLite；FTS 语义测试允许直连 SQLite」。
//
// 为什么要这条判据：收窄之后"允许直连 SQLite"只对 FTS 成立——但没有任何东西拦住下一条领域
// 测试图省事 import 一个 `sqlite/store`/`sqlite/model`（构造测试数据最快的方式就是拿真表）。
// 那样领域层对 SQLite 的依赖静默回来，而测试全绿（它们本来就跑在 SQLite 上）。这条守卫把
// "例外只有一条"变成机器判据。
//
// 双向（fail-closed）：
//   - 例外之外的任何领域测试 import `sqlite/*` → 红；
//   - 例外文件**不再** import `sqlite/*`（FTS 那条被删/改成内存实现）→ 红，提示删掉豁免。
func TestDomainTestsStayOffSQLite(t *testing.T) {
	root := backendRoot(t)
	seenExempt := false
	for _, dir := range goPackagesUnder(t, filepath.Join(root, "internal", "domain")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读目录 %s 失败: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			self := rel(root, path)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("解析 %s 失败: %v", self, err)
			}
			for _, imp := range file.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if !strings.Contains(p, "sqlite") {
					continue
				}
				if self == ftsSQLiteTest {
					seenExempt = true
					continue
				}
				t.Errorf("%s import 了 %s：领域测试直连 SQLite 只允许 FTS 语义测试（%s）；"+
					"聚合读写测试请用内存 store（DoD3 收窄口径）", self, p, ftsSQLiteTest)
			}
		}
	}
	if !seenExempt {
		t.Errorf("%s 不再 import `sqlite/*`：FTS 语义测试若已改成内存实现，"+
			"请把这条豁免从守卫里删掉（fail-closed：留着就是「边界已收口，却仍被当例外」）", ftsSQLiteTest)
	}
}
