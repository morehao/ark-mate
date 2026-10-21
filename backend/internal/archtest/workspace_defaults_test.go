package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **工作空间默认值** 的两条守卫（ADR-7 / v27 / P1-f）。
//
// 被钉的两种腐化都不会让任何既有测试变红：
//
//	① **合并链被写第二遍**：谁在 `manager` 里顺手 `if explicit == "" { explicit = ws.Default }`，
//	   两条合并链从此各自演化。表现不是报错——是"某个入口不吃工作空间默认值"，而那看起来像
//	   一个全新的 bug，不像一处漏改；
//	② **改默认值顺手改了会话**：既有会话的能力集与权限模式是**快照**。连带改写它们，表现是
//	   「我调了个默认值，昨天那个会话的行为变了」——静默、且不可逆（历史里的行为被改写）。

// TestWorkspaceDefaultsAreResolvedOnce 合并链单点：`ResolveSessionDefaults` 一处声明。
func TestWorkspaceDefaultsAreResolvedOnce(t *testing.T) {
	root := backendRoot(t)

	// ① 唯一声明在 `domain/workspace`（工作空间默认值这件事的家）。
	declared := 0
	var at string
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		if strings.HasSuffix(p, "_test.go") || strings.Contains(filepath.ToSlash(p), "/archtest/") {
			continue
		}
		if strings.Contains(stripLineComments(readFileOrFatal(t, p)), "func ResolveSessionDefaults(") {
			declared++
			at = p
		}
	}
	if declared != 1 {
		t.Fatalf("`func ResolveSessionDefaults(` 在 internal/ 里声明了 %d 处（want 1），最后一处 %s：\n"+
			"显式 > 工作空间默认 > 平台缺省 这条链必须只有一份实现——两处各写一遍的表现是"+
			"「某个入口不吃工作空间默认值」，而那看起来像一个全新的 bug", declared, at)
	}
	if !strings.Contains(filepath.ToSlash(at), "internal/domain/workspace/") {
		t.Fatalf("合并链声明在 %s，不在 `internal/domain/workspace/`：它属于工作空间这个域", at)
	}

	// ② 链的语义必须逐条在（顺序错了不会编译失败，只会静默地把显式值让给默认值）。
	src := stripLineComments(readFileOrFatal(t, at))
	for _, anchor := range []string{
		"out.CapabilityJSON = ws.CapabilityJSON",
		"out.PermissionMode = ws.PermissionMode",
		"if out.CapabilityJSON == \"\" {",
		"if out.PermissionMode == \"\" {",
	} {
		if !strings.Contains(src, anchor) {
			t.Fatalf("%s 里找不到 %q：合并链必须是「显式优先、空才回落」——"+
				"顺序反了不会编译失败，只会让显式值被默认值盖掉", filepath.Base(at), anchor)
		}
	}

	// ③ 缺省的**第三档**不许在本包落成具体值：本包只回答"有没有工作空间默认值"，
	//    空串原样往上传（能力选定的缺省是能力面的事、权限模式的缺省是权限面的事）。
	if strings.Contains(src, "PermissionModeDefault") || strings.Contains(src, "DefaultPermissionMode ==") {
		t.Fatalf("%s 里出现了平台缺省的具体值：本包把缺省落死就成了**第三份**缺省，"+
			"而三份缺省迟早不一致（同一个空请求从两个入口建出来的会话不一样）", filepath.Base(at))
	}
}

// TestWorkspaceDefaultsDoNotRewriteSessions 改默认值**不许**连带改会话快照。
func TestWorkspaceDefaultsDoNotRewriteSessions(t *testing.T) {
	root := backendRoot(t)

	// ① 写面（store）的语句集合里不得出现 `sessions`。
	//
	// v3.3 起 `SetDefaults` 的写面在 store（DAO 只剩列词汇与泛型方法）——判据必须跟着实现走。
	// 找不到函数即失败：写面再搬一次时，`continue` 会让这条守卫对着一个空文件静默空转。
	{
		rel := filepath.Join("internal", "persistence", "store", "workspace.go")
		src := stripLineComments(readFileOrFatal(t, filepath.Join(root, rel)))
		const fn = "func (s *Store) SetWorkspaceDefaults("
		body, ok := goFuncBody(src, fn)
		if !ok {
			t.Fatalf("%s 里找不到 `%s`：写面搬走后这条守卫会静默空转", rel, fn)
		}
		// 只扫**写语句**：`sessions` 出现在注释里（解释为什么不动它）是正常的。
		for _, bad := range []string{"sessions\"", "TableNameSession", "UPDATE sessions"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s 的 `%s` 里出现了 %q：改工作空间默认值**不许**连带改会话——\n"+
					"既有会话的能力集与权限模式是**快照**，改写它们等于把历史里的行为改掉，"+
					"而用户看到的是「我调了个默认值，昨天那个会话的行为变了」", rel, fn, bad)
			}
		}
	}

	// ② 迁移**也不许**碰 `sessions`（v27 只加两列）。
	mig := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "persistence", "store", "migrate_v27.go")))
	if strings.Contains(mig, "sessions") {
		t.Fatalf("`migrate_v27.go` 里出现了 `sessions`：v27 只给 `workspaces` 加两列——" +
			"一次全表写就多一次中断面，而「把存量会话的默认值也刷一遍」正是本批刻意不做的事")
	}

	// ③ 建会话那条解析链必须真的取**工作空间默认值**（否则上面两条都只是在保护一个没人读的列）。
	// 只数**调用点**：定义那一行本身也含这个名字（`func (pl *Plane) WorkspaceDefaultsOf(`），
	// 按子串数会被它自己喂饱——那是本守卫第一版的样子，等于什么都没查。
	reader := 0
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		relSlash := filepath.ToSlash(rel(root, p))
		if strings.HasSuffix(p, "_test.go") || strings.HasPrefix(relSlash, "internal/archtest/") ||
			relSlash == "internal/domain/workspace/registry.go" {
			continue
		}
		if strings.Contains(stripLineComments(readFileOrFatal(t, p)), "WorkspaceDefaultsOf(") {
			reader++
		}
	}
	if reader == 0 {
		t.Fatalf("`WorkspaceDefaultsOf(` 没有任何生产调用点：两列默认值没有任何读者——" +
			"那样的迁移只是让 schema 看起来支持了一件产品上不存在的能力")
	}
}
