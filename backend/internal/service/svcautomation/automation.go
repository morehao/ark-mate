// Package svcautomation 定义 automation（定时任务）面的服务端口。
//
// 实现方是本包的 `Service`（控制器在构造函数里用 `New(plane)` 造）；`manager` 只负责把
// 域平面接上，不再实现这个端口。
package svcautomation

import (
	"context"

	"github.com/morehao/ark-mate/internal/contract/dtoautomation"
)

// AutomationSvc automation 数据面的服务端口。
//
// 三条纪律对全部写面方法一致（与能力写面同源）：先按**合并后**的定义过闸门 → 通过才落盘 →
// 落盘失败绝不留下"内存里有、盘上没有"的幻影。
type AutomationSvc interface {
	// ListAutomations 列出全部 automation（含装载失败的那些，带 `error` 字段），按 id 排序。
	ListAutomations() ([]dtoautomation.AutomationResp, error)

	// GetAutomation 取一条（不存在 → `automation_not_found` 40401）。
	GetAutomation(id string) (*dtoautomation.AutomationResp, error)

	// CreateAutomation 新建（id 已存在 → `automation_exists` 40901，绝不覆盖）。
	CreateAutomation(ctx context.Context, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error)

	// UpdateAutomation 覆盖写：只改请求体里出现的键（未出现的保留原值），id 不存在 → 40401。
	UpdateAutomation(ctx context.Context, id string, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error)

	// DeleteAutomation 删除定义（幂等：不存在 → deleted=false 且无错）。
	DeleteAutomation(ctx context.Context, id string) (*dtoautomation.AutomationDeleteResp, error)

	// PreviewAutomation 只算未来若干次到期时刻：**不落盘、不建会话**（把"什么时候跑"变成可见事实）。
	PreviewAutomation(req dtoautomation.AutomationPreviewReq) (*dtoautomation.AutomationPreviewResp, error)

	// ListAutomationRuns 运行史（最新在前；含没跑成的那些，skipped/overlap 必须可见）。
	ListAutomationRuns(id string) (*dtoautomation.AutomationRunsResp, error)

	// RunAutomationNow 手动触发一次（走同一认领判据；失败既报错又留痕）。
	RunAutomationNow(ctx context.Context, id string) (*dtoautomation.AutomationRunResp, error)
}
