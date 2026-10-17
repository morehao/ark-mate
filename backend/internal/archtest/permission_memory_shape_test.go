package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **权限记忆带授权形态** 的守卫（P2-a / v28；`docs/unimplemented.md` O16、路线 L3-2）。
//
// 两次腐化都不会让任何既有测试变红：
//
//	① **授权形态被并进唯一键**：把 `grant_input_digest` 或 `scope` 加进 `UNIQUE(...)`。
//	   那一刻"这类工具始终允许"就退化成"这一个参数组合始终允许"——**便利功能被拆掉**，
//	   而所有记忆相关测试照旧绿（它们只验证"同键覆盖"）；
//	② **写了但不复原**：`Remember` 落库带了形态，而 `RestoreMemoriesFromStore` 不带回来。
//	   重启之后库里有两列、内存里没有——"当初那句话是在哪次调用上说的"只在磁盘上，
//	   读面（`GET /sessions/:id/permissions` 的记忆段）答不出来。
//
// 判据四段，都 fail-closed。

// TestPermissionMemoryCarriesGrantShape 见文件头。
func TestPermissionMemoryCarriesGrantShape(t *testing.T) {
	root := backendRoot(t)

	// ① 迁移只**增列**，且唯一键保持三元组。
	mig := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "store", "migrate_v28.go")))
	if !strings.Contains(mig, "ADD COLUMN scope TEXT NOT NULL DEFAULT 'session'") {
		t.Fatalf("v28 没有加 `scope` 列（或默认值不是 'session'）：授权形态必须有一个**可写**的落点")
	}
	if !strings.Contains(mig, "ADD COLUMN grant_input_digest TEXT NOT NULL DEFAULT ''") {
		t.Fatalf("v28 没有加 `grant_input_digest` 列")
	}
	if strings.Contains(mig, "UNIQUE(") || strings.Contains(mig, "CREATE TABLE") {
		t.Fatalf("v28 重建了表 / 改了唯一键：**唯一键刻意保持三元组**——把入参或形态并进去，" +
			"「这类工具始终允许」就退化成「这一个参数组合始终允许」，那是把一条便利功能拆掉")
	}

	// ② 两列**不进** `Upsert` 的冲突列（冲突列 = 唯一键）。
	dao := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "dao", "memory.go")))
	upsert, ok := goFuncBody(dao, "func (d *PermissionMemoryDao) Upsert(")
	if !ok {
		t.Fatalf("`dao/memory.go` 里找不到 `Upsert(`")
	}
	conflict := upsert
	if i := strings.Index(upsert, "DoUpdates:"); i > 0 {
		conflict = upsert[:i]
	}
	for _, bad := range []string{"grant_input_digest", "scope"} {
		if strings.Contains(conflict, `Name: "`+bad+`"`) {
			t.Fatalf("`Upsert` 的**冲突列**里出现了 %q：那是唯一键——把授权形态并进去会让"+
				"「这类语言工具始终允许」退化成「这一个参数组合始终允许」", bad)
		}
	}
	// 但**必须写**（否则两列永远空）。
	for _, want := range []string{`"scope":`, `"grant_input_digest":`} {
		if !strings.Contains(upsert, want) {
			t.Fatalf("`Upsert` 没有写 %s：加了两列却从不写入，等于 schema 里多两个没人改的默认值", want)
		}
	}

	// ③ 装载路径必须把形态带回来（"写了但不复原"是这一族最典型的静默失效）。
	orch := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "sessionruntime", "orchestrator.go")))
	restore, ok := goFuncBody(orch, "func (o *Orchestrator) RestoreMemoriesFromStore(")
	if !ok {
		t.Fatalf("找不到 `RestoreMemoriesFromStore(`")
	}
	if !strings.Contains(restore, "WithGrantShape(") {
		t.Fatalf("装载路径没有 `WithGrantShape(`：重启之后库里有两列、内存里没有——" +
			"「当初那句话是在哪次调用上说的」只在磁盘上，读面答不出来")
	}

	// ④ 入参**只进摘要，不进库**（工具入参里可能有路径/token/命令里的密钥）。
	mem := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "interaction", "permission_memory.go")))
	digest, ok := goFuncBody(mem, "func grantInputDigest(")
	if !ok {
		t.Fatalf("找不到 `grantInputDigest(`")
	}
	if !strings.Contains(digest, "sha256.Sum256") {
		t.Fatalf("`grantInputDigest` 不是摘要（没有 `sha256.Sum256`）：入参**原文**不进库——" +
			"它可能含路径、token、甚至命令里的密钥；审计要的是「是不是同一次调用」，摘要就能回答")
	}
	if strings.Contains(mig, "grant_input TEXT") {
		t.Fatalf("v28 加了一列**原文**入参：审计只需要摘要，而原文会让库里多一份敏感数据")
	}
}
