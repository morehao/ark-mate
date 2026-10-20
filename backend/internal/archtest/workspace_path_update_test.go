package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **改登记路径** 的守卫（W4 / P1-g）。
//
// 改登记路径是"改身份的一半"，两种腐化都不会让任何既有测试变红：
//
//	① **顺手把会话也搬了**：`UPDATE sessions SET workspace_id/cwd`。表现不是报错——是
//	   「我改了个工作空间的目录，昨天那些会话的历史指到别处了」。`cwd` 是**执行事实**
//	   （ACP cwd + 沙箱边界），会话的 transcript 就落在那个目录里；
//	② **冲突被吞成 get-or-create**：目标目录已被另一条登记占着时，静默返回那条既有行。
//	   两个入口给出同一个"成功"，而用户改的那个工作空间还在老地方。
//
// 判据三段，都 fail-closed。

// TestWorkspacePathUpdateKeepsIdentity 见文件头。
func TestWorkspacePathUpdateKeepsIdentity(t *testing.T) {
	root := backendRoot(t)

	// ① 写面**只**改 path/updated_at，且函数体里零 `sessions` 写。
	storeSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "persistence", "store", "workspace.go")))
	relocate, ok := goFuncBody(storeSrc, "func (s *Store) RelocateWorkspace(")
	if !ok {
		t.Fatalf("`store/workspace.go` 里找不到 `RelocateWorkspace`（改名了？本守卫 fail-closed）")
	}
	for _, bad := range []string{"SessionEntity", "sessions\"", "UPDATE sessions"} {
		if strings.Contains(relocate, bad) {
			t.Errorf("`RelocateWorkspace` 里出现了 %q：改登记路径**不许**连带改会话——\n"+
				"既有会话的 `cwd` 是**执行事实**（ACP cwd + 沙箱边界），transcript 也落在那个目录里；"+
				"跟着搬等于把历史指到别处", bad)
		}
	}
	// 冲突必须**显式**报（不吞成"返回既有行"）。
	if !strings.Contains(relocate, "ErrWorkspacePathTaken") {
		t.Errorf("`RelocateWorkspace` 没有把唯一索引冲突翻成 `ErrWorkspacePathTaken`：" +
			"吞掉它 = get-or-create，而「改路径成功」与「那个目录已经是别人的」是两件必须能分辨的事")
	}

	// 写面那一句（v3.3 起在 store 里：DAO 只剩列词汇，`RelocateWorkspace` 用 UpdateMap 写两列）。
	// 只写 `path` 与 `updated_at`：id 是身份，改它等于让该工作空间下的会话归属悬空。
	if !strings.Contains(relocate, `"path":`) || !strings.Contains(relocate, "canonicalPath") {
		t.Fatalf("`RelocateWorkspace` 没有写 `path`: 它到底改了什么？")
	}
	for _, bad := range []string{`"id":`, `"is_default":`, `"default_capability_json":`} {
		if strings.Contains(relocate, bad) {
			t.Errorf("`RelocateWorkspace` 的写面出现了 %s：改路径只该动 `path` 与 `updated_at`——\n"+
				"顺带改 id 会让该工作空间下的会话归属悬空（界面上表现为「会话跑到别的分组去了」），"+
				"改 is_default 会破坏「有且只有一个默认」的不变量", bad)
		}
	}

	// ② 域层的 Patch 必须把冲突**翻成显式错误**，而不是当作成功。
	reg := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "workspace", "registry.go")))
	patch, ok := goFuncBody(reg, "func (pl *Plane) PatchWorkspace(")
	if !ok {
		t.Fatalf("`domain/workspace/registry.go` 里找不到 `PatchWorkspace`")
	}
	if !strings.Contains(patch, "RelocateWorkspace(") {
		t.Fatalf("`PatchWorkspace` 没有走 `RelocateWorkspace(`：改路径的语义只能有一处实现")
	}
	// v3.2 刀 6 起错误分类走端口（`IsPathTaken`），领域层不再认识 `store.Err*`：
	// 判据钉"冲突被显式分类"，而不是钉某个具体拼写。
	if !strings.Contains(patch, "IsPathTaken(") && !strings.Contains(patch, "store.ErrWorkspacePathTaken") {
		t.Fatalf("`PatchWorkspace` 没有处理路径占用冲突（`IsPathTaken(`）：冲突会被当成成功")
	}
	if strings.Contains(patch, "workspace_path_immutable") {
		t.Fatalf("`PatchWorkspace` 仍然无条件拒绝改路径（`workspace_path_immutable`）：" +
			"W4 已落地，那条拒绝只该在默认工作空间那一条路上出现（用 `workspace_default_immutable`）")
	}

	// ③ 前端要有**行内补救**入口（Q10 选①）：目录不见了的时候，用户得能就地指一个新目录。
	feRoot := filepath.Join(root, "..", "frontend")
	hit := false
	for _, p := range uiSrcRoots(t) {
		for _, f := range listFrontendSources(t, p) {
			if strings.Contains(stripTSComments(readFileOrFatal(t, f)), "选择新目录") {
				hit = true
			}
		}
	}
	if !hit {
		t.Fatalf("界面里找不到「选择新目录」这一句（Q10 选①的行内补救）：%s\n"+
			"目录被删/搬走之后，如果没有就地改登记路径的入口，用户唯一的出路是删掉这个工作空间"+
			"再重新建一个——而删登记会把该工作空间下的会话释放回默认工作空间", feRoot)
	}
}
