package archtest

// 本文件是 **定义变更审计必须带哈希链** 的守卫（v32 / T4，2026-10-21）。
//
// 为什么需要机器判据：v32 给 `definition_revisions` 加了 `prev_hash` / `hash` 两列，链的完整性
// 靠**唯一一条写入路径**（`store.appendDefinitionRevision`：在同一事务里取链尾、算哈希、插行）。
// 任何人另起一条插入路径——直接调 DAO 的 `Append`、或写一句裸 SQL——都不会让别的测试变红：
// 链上只是多了一条 `hash=''` 的行。而 `hash=''` 在 v32 之前的历史行里**本来就合法**（它们是链的
// 前缀），于是"新插入的行绕过链"与"这条是历史行"在库里长得一模一样，直到有人跑校验才可能发现。
//
// 判据两条（均 fail-closed：扫不到文件即失败，改名/搬迁后守卫不许静默空转）：
//
//	① 生产代码里 `definition_revisions` 的追加只能出现在 `definition_revision_chain.go`；
//	② 生产代码里不得出现对这张表的**裸 INSERT**（迁移步除外：`migrate*.go` 是历史，绝不回改）。
//
// 反证（实跑 2 条，均红）：把 `commitDefinitionChange` 里的 `s.appendDefinitionRevision(...)`
// 改回 `s.definitionRevision.WithTx(tx).Append(...)`；在 `store/capability.go` 里加一句
// `INSERT INTO definition_revisions ...`。

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDefinitionRevisionWritesCarryChain 见文件头。两条判据一起跑：一处绕过链就够红了。
func TestDefinitionRevisionWritesCarryChain(t *testing.T) {
	root := backendRoot(t)
	storeDir := filepath.Join(root, "internal", "adapters", "outbound", "sqlite", "store")

	// ① 追加只能用链助手（`definition_revision_chain.go` 里的那一句是唯一合法调用）。
	const appendCall = "definitionRevision.WithTx(tx).Append("
	const chainFile = "definition_revision_chain.go"
	var appendCallSites []string
	for _, path := range goFilesUnder(t, storeDir) {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "migrate") {
			continue
		}
		code := stripLineComments(readFileOrFatal(t, path))
		if !strings.Contains(code, appendCall) {
			continue
		}
		appendCallSites = append(appendCallSites, base)
	}
	if len(appendCallSites) == 0 {
		t.Fatalf("在 %s 里找不到任何 `%s`：审计写入路径被改名/挪走了？"+
			"（本守卫是 fail-closed：判据找不到对象时必须红，而不是静默通过）", storeDir, appendCall)
	}
	for _, base := range appendCallSites {
		if base != chainFile {
			t.Errorf("%s 直接调用了 `%s`：审计追加必须走 `appendDefinitionRevision`（同事务取链尾 + 算哈希），"+
				"否则链上会多一条 `hash=''` 的行，而它与 v32 之前的历史行在库里长得一模一样",
				base, appendCall)
		}
	}

	// ② 裸 INSERT 零命中（迁移步除外：它们描述的是**当时那一版**的表形态，回改等于改写历史）。
	for _, path := range goFilesUnder(t, filepath.Join(root, "internal")) {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "migrate") {
			continue
		}
		code := stripLineComments(readFileOrFatal(t, path))
		for _, banned := range []string{"INSERT INTO definition_revisions", "INSERT INTO ` + definition_revisions"} {
			if strings.Contains(code, banned) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s 出现裸 INSERT（%q）：定义变更审计只许经 `store.appendDefinitionRevision` 写入"+
					"（裸 SQL 会绕过哈希链）", rel, banned)
			}
		}
	}
}
