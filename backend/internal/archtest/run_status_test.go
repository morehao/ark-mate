package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **S1 `run.status`** 的两条守卫（P0-2，ADR-6）。
//
// 为什么这两条需要机器判据：过程状态是"给界面读"的事实，而它的两种腐化都不会让任何既有测试变红：
//
//	① **两份铸造**：谁在 `manager` 里再手拼一份 `map[string]any{"status": …}` 直接 `publish`，
//	   于是"同状态去重""取值校验"这两件事在那一支上悄悄少一半——事件照常推、界面照常显示，
//	   只是某个阶段的帧数变成几十帧（或者某个取值绕过校验流到前端）。
//	② **终态前没有收口**：界面"还在跑吗"的判据就退回成"这段时间没事件"——那正是被取代的
//	   那套倒推（`serverBusy`）的做法；而补一条终态事件照样能让终态用例绿。
//
// 两条都 fail-closed：锚找不到即失败（改名/搬迁 = 判据失锚，不许静默空转）。

// TestRunStatusIsSingleSource 守卫 7：`run.status` 的铸造点唯一在 `internal/domain/session`。
func TestRunStatusIsSingleSource(t *testing.T) {
	root := backendRoot(t)

	// ① 唯一实现的锚必须真的在（改名 = 判据失锚）。
	mintPath := filepath.Join(root, "internal", "domain", "session", "runstatus.go")
	mint := stripLineComments(readFileOrFatal(t, mintPath))
	for _, anchor := range []string{
		"func RunStatusPayload(",
		"func (pl *Plane) PublishRunStatus(",
		"func (pl *Plane) PublishRunInterrupted(",
		"func IsRunStatus(",
		"func RunStatusValues()",
	} {
		if !strings.Contains(mint, anchor) {
			t.Fatalf("%s 里找不到 %q：`run.status` 的铸造/取值集/校验都该在这一处（fail-closed，"+
				"改名或搬迁会让本条守卫静默空转）", mintPath, anchor)
		}
	}
	// 10 个活体取值逐个在（少一个 = 界面少一档旁白，而那不是编译错误）。
	live := []string{
		`RunStatusPreparing = "preparing"`,
		`RunStatusModelRequesting = "model_requesting"`,
		`RunStatusModelStreaming = "model_streaming"`,
		`RunStatusToolExecuting = "tool_executing"`,
		`RunStatusWaitingForPermission = "waiting_for_permission"`,
		`RunStatusWaitingForUser = "waiting_for_user"`,
		`RunStatusRetrying = "retrying"`,
		`RunStatusCompacting = "compacting"`,
		`RunStatusCancelling = "cancelling"`,
		`RunStatusIdle = "idle"`,
	}
	for _, want := range live {
		if !strings.Contains(mint, want) {
			t.Fatalf("%s 里少了取值声明 %s（ADR-7 的 10 态一字不能少：少一档就是界面少一句旁白，"+
				"而它不会让编译失败）", mintPath, want)
		}
	}

	// ② 域之外**零命中**：manager / service / controller / router / dto 一律不许出现这个 type
	//    的法字面量或常量（连调用 `publish(BusRunStatus, …)` 也不许——必须走域里的方法，
	//    否则去重与校验在那一支上缺席）。
	scanned := 0
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		relSlash := filepath.ToSlash(rel(root, p))
		if strings.HasSuffix(p, "_test.go") ||
			strings.HasPrefix(relSlash, "internal/archtest/") ||
			strings.HasPrefix(relSlash, "internal/domain/session/") {
			continue
		}
		scanned++
		src := stripLineComments(readFileOrFatal(t, p))
		for _, bad := range []string{`"run.status"`, "BusRunStatus"} {
			if strings.Contains(src, bad) {
				t.Fatalf("%s 里出现了 %q：`run.status` 的铸造点唯一在 internal/domain/session。"+
					"在别处直接拼载荷/发常量，会让「同状态去重」与「取值校验」在那一支上静静缺席"+
					"（事件照常推、界面照常显示，只是帧数与取值集悄悄失控）", relSlash, bad)
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("`internal/` 的扫描面为空（fail-closed：守卫会静默空转）")
	}
}

// TestRunStatusCoversEveryTerminal 守卫 8：四个**非** intentional 的终态分支，各自前面都落一次收口。
//
// 判据扫的是**调用顺序**（同一分支体内既有 `o.closeRunStatus(`，又有那一支的终态发射），
// 不只是"文件里有 closeRunStatus"——后者在"四个分支只补了一个"时照样绿。
//
// `case intentional:` 不在名单里，且它**必须**没有 closer：ADR-8 的前半（实时路径不发伪终态）
// 与后半（恢复路径补收口）是相反的行为，两处各有判据（前半由
// `TestInterruptedRunsAreClosedOnRestoreButNotLive` 钉）。
func TestRunStatusCoversEveryTerminal(t *testing.T) {
	root := backendRoot(t)
	runPath := filepath.Join(root, "internal", "domain", "session", "runner.go")
	src := stripLineComments(readFileOrFatal(t, runPath))

	// 每个终态分支：分支标签 → 该分支里必须出现的终态发射锚。
	cases := []struct {
		label     string
		terminal  string
		rationale string
	}{
		{"case userCancel:", "BusRunCancelled", "用户显式取消（优先于 interrupted）"},
		{"case cancelled:", "BusRunCancelled", "底座返回取消类结果"},
		{"case err != nil:", "r.PublishError(", "失败"},
		{"\tdefault:", "BusRunEnded", "正常结束"},
	}
	for _, c := range cases {
		block, ok := switchCaseBlock(src, c.label)
		if !ok {
			t.Fatalf("%s 里找不到分支 %q（改名/重构了？本守卫 fail-closed）", runPath, c.label)
		}
		if !strings.Contains(block, c.terminal) {
			t.Fatalf("%s 的分支 %q 里找不到终态发射 %q：判据的锚变了（fail-closed，"+
				"否则本条会在改结构之后静默空转）", runPath, c.label, c.terminal)
		}
		if !strings.Contains(block, "r.CloseRunStatus(") {
			t.Fatalf("%s 的分支 %q（%s）里没有收口 `r.CloseRunStatus(`：\n"+
				"界面「还在跑吗」的判据必须是一个**状态**，不能是「一段时间没事件」——后者正是"+
				"被取代的那套前端倒推。四个终态分支各要一次，只补一个不会让任何既有用例变红",
				runPath, c.label, c.rationale)
		}
	}

	// `case intentional:` 必须**没有** closer（ADR-8 的前半）。
	block, ok := switchCaseBlock(src, "case intentional:")
	if !ok {
		t.Fatalf("%s 里找不到 `case intentional:` 分支（fail-closed）", runPath)
	}
	if strings.Contains(block, "o.closeRunStatus(") {
		t.Fatalf("%s 的 `case intentional:` 分支里有收口：收口状态同样是「这一轮到此为止」的宣告，"+
			"在这一支上它与伪终态同罪。崩溃残轮的收口由**恢复路径**补（ADR-8 的两半）", runPath)
	}
}
