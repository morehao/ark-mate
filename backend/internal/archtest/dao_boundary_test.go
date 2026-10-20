package archtest

// 本文件是 **DAO 不得承载业务读面 / 不得手工拼标识符** 的守卫（v3.3，2026-10-19）。
//
// v3.3 把持久层拆成两半：`internal/persistence/dao` 只留**列词汇**（`XxxCond`，条件用 `clause` 结构化表达）、
// gormdao 的泛型方法与机械原语（KV / 取号 / CAS / 只追加插入）；**排序、筛选组合、分页、聚合**
// 这些读面口径一律写在 `internal/persistence/store`（它用 `GetListByCond`/`CountByCond`/`UpdateMap` 拼）。
//
// 为什么需要机器判据：把 `ListByOrder`/`RunningRuns` 这类方法加回 DAO **不会让任何测试变红**
// ——store 里通常还留着一条同样能跑的调用路径，两层各有一套顺序时，"用户看到的顺序"取决于
// 调用方恰好走了哪条；而单表测试只验证"返回了行"，永远看不出两套口径已经分叉。
//
// 判据（fail-closed：`internal/persistence/dao` 扫不到 Go 文件即失败）：剥离注释后，DAO 源码不得出现
// 下列任一项——读面组合/排序/分页/聚合的写法，以及 `tableName+` 的标识符拼接（条件必须交给
// `clause.Column` 由方言引用，手工拼 `<table>.col = ?` 在保留字/带 schema 的表名上会静默出错）。
// 唯一例外是 `meta.go` 的 `GetByCond(`：`meta` 是键值表，"按 key 取一行"就是它的原语，
// 没有排序与业务筛选。
//
// 反证（实跑 3 条，均红）：把 `workspaceListOrder` 的 `OrderField` 搬回 `dao/workspace.go`；
// 把 `RunningRuns` 的 `.Order(`/`.Limit(` 搬回 `dao/run.go`；把 `MetaCond` 的 `clause.Eq`
// 改回 `tableName+".key = ?"`。

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDaoKeepsNoBusinessReadFace 见文件头。
func TestDaoKeepsNoBusinessReadFace(t *testing.T) {
	root := backendRoot(t)
	daoDir := filepath.Join(root, "internal", "persistence", "dao")
	if !hasGoFiles(daoDir) {
		t.Fatalf("%s 下没有 Go 文件：这条守卫失去检查对象（改名/搬迁了？）", daoDir)
	}

	// 每条规则 = 一处「不属于 DAO」的写法 + 它为什么必须留在 store。
	rules := []struct {
		pattern string
		reason  string
	}{
		{"OrderField:", "排序口径（读面）必须写在 store"},
		{".Order(", "排序口径（读面）必须写在 store"},
		{".Limit(", "有界窗口/翻页必须写在 store"},
		{".Offset(", "有界窗口/翻页必须写在 store"},
		{".Join(", "联表读取必须写在 store"},
		{".Group(", "聚合必须写在 store"},
		{".Select(", "列子集读取必须写在 store"},
		{"GetListByCond(", "列表查询组合必须写在 store"},
		{"GetPageListByCond(", "分页查询组合必须写在 store"},
		{"CountByCond(", "计数口径必须写在 store"},
		{"tableName+", "条件里的标识符必须交给 clause.Column（方言引用），不得手工拼接"},
		{"tableName +", "条件里的标识符必须交给 clause.Column（方言引用），不得手工拼接"},
	}

	// 按键取一行是 meta 的 KV 原语；其余位置出现它同样说明查询组合又长回了 DAO。
	condAllowed := map[string]bool{"meta.go": true}

	for _, path := range goFilesUnder(t, daoDir) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(daoDir, path)
		if err != nil {
			t.Fatalf("算相对路径: %v", err)
		}
		src := stripGoComments(readFileOrFatal(t, path))
		for _, rule := range rules {
			if strings.Contains(src, rule.pattern) {
				t.Errorf("`internal/persistence/dao/%s` 里出现了 %q：%s——DAO 只留列词汇与机械原语，"+
					"两层各有一套口径时，结果取决于调用方走了哪条路，而单表测试永远看不出来",
					rel, rule.pattern, rule.reason)
			}
		}
		if strings.Contains(src, "GetByCond(") && !condAllowed[rel] {
			t.Errorf("`internal/persistence/dao/%s` 里出现了 `GetByCond(`：条件查询组合归 store"+
				"（唯一例外是 meta 的按键取一行）", rel)
		}
	}
}
