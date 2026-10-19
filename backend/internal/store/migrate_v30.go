package store

// 本文件是 **v30**（把 `runs.status` 的历史取值 `running` 归一成 `in_progress`）的版本步本体。
//
// 为什么需要它：`runs` 表在 v1 建表时写的是 `status TEXT NOT NULL DEFAULT 'running'`，而 v21 起
// 的**唯一写入方**（`domain/session/persist.go` 的 `OpenRun`）显式写 `SessionStatusInProgress`
// （`in_progress`）。两个值并存过一段时间，后果是**静默的**：
//
//	① v30 之前，`RunningRows`（时在 `dao/run.go`）过滤 `status = 'running'` ⇒ 恒空（写入方不写那个值）；
//	   于是 `runRowFor` 的兜底分支是死代码、`store/usage.go` 的 `Running` 计数恒 0；
//	② 只看库的人会以为"没有进行中的轮"，而实际上每一轮都在 `in_progress`。
//
// 本步只做**数据归一**（无 DDL）：历史行统一到写入方用的那个值，此后 `RunningRuns` 与用量计数
// 都只认一个值。v1 的 `DEFAULT 'running'` 保留不动——新行由写入方显式赋值，默认值不再参与。
//
// 这是**就地改写既有数据**，所以带 `backup: "v30"`（与 v12 同一口径）。
var migrationV30 = migration{
	version: 30,
	name:    "runs.status 归一：历史 running → in_progress",
	backup:  "v30",
	statements: []string{
		`UPDATE runs SET status = 'in_progress' WHERE status = 'running'`,
	},
}
