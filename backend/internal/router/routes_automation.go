package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrautomation"
)

// registerAutomation M4：automation（定时任务）面。
//
// 定义的事实源**按持久层模式分派**（M6-7）：sqlite 模式是 `automations` 表（定义列 + 运行态列），
// memory 模式仍是 `<AutomationDir>/<id>.json`（`FORGE_AUTOMATIONS` 在 sqlite 模式下降级为
// 导入/导出目录）。接口只是它的编辑器与观察窗：
//
//   - `GET /automations` 列表（含**装载失败的**定义，带 `error` —— 写得不对却"列表里没有、
//     调度也不跑"是最难排查的一类投诉）；
//   - `POST /automations/preview` 只算未来若干次到期时刻：**不落盘、不建会话**，
//     因此面板里还没保存的 schedule 也能先看到"到底什么时候跑"（时区 + DST + weekly 叠加后，
//     没人能从 JSON 里读出这件事）；
//   - `POST/PUT/DELETE` 写面：`PUT` 只改 body 里出现过的键，`null` 删键；平台独占的运行态键
//     （`last_fired_at`/`recent_runs`…）不可写 → **40001**（`invalid_param`；这里曾把码写成
//     40000，与实现不符，M6-7 收口时改正）；
//   - `GET /automations/:id/runs` 运行史（最新 20 条，**含 skipped/overlap/duplicate**）；
//   - `POST /automations/:id/run` 手动触发一次：走同一认领判据（上一次还在跑 → overlap，
//     手动不等于可以并发），但**不推进认领游标**（不该静默吃掉当晚的排期）。
//
// `preview` 必须在 `:id` 之前注册，否则会被当成一个 id。
func registerAutomation(v1 *gin.RouterGroup, ports Ports) {
	ctr := ctrautomation.NewAutomationCtr(ports.Automation)
	v1.GET("/automations", ctr.ListAutomations)
	v1.POST("/automations", ctr.CreateAutomation)
	v1.POST("/automations/preview", ctr.PreviewAutomation)
	v1.GET("/automations/:id", ctr.GetAutomation)
	v1.PUT("/automations/:id", ctr.UpdateAutomation)
	v1.DELETE("/automations/:id", ctr.DeleteAutomation)
	v1.GET("/automations/:id/runs", ctr.ListAutomationRuns)
	v1.POST("/automations/:id/run", ctr.RunAutomationNow)
}
