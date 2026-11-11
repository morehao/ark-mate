package archtest

// 本文件钉住「自动化目标会话巡检」的两个前提（2026-11-10 加）：
//
// 背景：`SweepDeadSessionTargets` 判定「delivery=session 的目标会话是否存在」靠的是
// `sessionPlane.Lookup`（内存会话表）。那张表由 `session.Plane.Restore`（assembly 的
// `startRestore` 步骤）在**启动期**填充——若巡检早于会话恢复跑，表是空的，就会把**所有**
// session 目标误判为「已不存在」，一次性把全部定时任务标成 needs_attention。这不是假想的边界，
// 是两个后台步骤的相对顺序一旦被调整就会真实发生的静默失效。
//
// 因此钉两条（都 fail-closed：扫不到即红，避免"守卫照样绿、只是不再检查"）：
//
//	① 装配顺序：`startRestore` 步骤必须**早于** `startAutomationScheduler` 步骤
//	   （assembly.go 的 Step 声明顺序，即实际启动顺序）；
//	② 巡检接线：`StartAutomationScheduler` 里必须调用 `SweepDeadSessionTargets`
//	   （删了调用 = 巡检悄悄失效，而本文件之外的测试都不会红）。
//
// 反证（实跑）：把 assembly.go 里两个 Step 对调 → ① 红；删掉 sched.go 里的 Sweep 调用 → ② 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutomationSweepAfterSessionRestore ①：startRestore 早于 startAutomationScheduler。
func TestAutomationSweepAfterSessionRestore(t *testing.T) {
	p := filepath.Join(backendRoot(t), "internal", "bootstrap", "assembly", "assembly.go")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 assembly.go 失败: %v", err)
	}
	src := string(raw)
	// 用 Step 的 Start 字段定位（Start: as.startRestore / as.startAutomationScheduler）。
	iRestore := strings.Index(src, "Start: as.startRestore")
	iSched := strings.Index(src, "Start: as.startAutomationScheduler")
	if iRestore < 0 {
		t.Fatalf("assembly.go 里找不到 `Start: as.startRestore`（改名了？守卫需同步）")
	}
	if iSched < 0 {
		t.Fatalf("assembly.go 里找不到 `Start: as.startAutomationScheduler`（改名了？守卫需同步）")
	}
	if iRestore > iSched {
		t.Fatalf("启动顺序错误：会话恢复(startRestore)必须早于自动化调度(startAutomationScheduler)，"+
			"否则巡检会把尚未恢复的会话误判为不存在。restore@%d sched@%d", iRestore, iSched)
	}
}

// TestAutomationSchedulerCallsSweep ②：StartAutomationScheduler 内调用 SweepDeadSessionTargets。
func TestAutomationSchedulerCallsSweep(t *testing.T) {
	p := filepath.Join(backendRoot(t), "internal", "domain", "automation", "sched.go")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 sched.go 失败: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "pl.SweepDeadSessionTargets(ctx)") {
		t.Fatalf("sched.go 里找不到 `pl.SweepDeadSessionTargets(ctx)` 调用：巡检被摘掉了？" +
			"（目标会话失效将不再被提前标出，只在下次触发时失败）")
	}
}
