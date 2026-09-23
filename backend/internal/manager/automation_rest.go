package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtoautomation"
	"github.com/morehao/ark-forge/internal/service/svcautomation"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/automation"
	"github.com/morehao/ark-forge/pkg/code"
)

// ——— M4-5：automation 的端口实现（写面适配 + 预览 + 运行史 + 手动触发）———
//
// 写面适配的职责只有两件：把原文 JSON 对象转成落盘层要的 patch（`rawPatch`）与把领域视图
// 转成 DTO。校验/落盘/并发那三件事全在 M4-3/M4-4 的实现里，这里不重复也不绕过。

var _ svcautomation.AutomationSvc = (*Service)(nil)

// GetAutomation 取一条（不存在 → `automation_not_found` 40401）。
func (s *Service) GetAutomation(id string) (*dtoautomation.AutomationResp, error) {
	v, ok := s.AutomationByID(id)
	if !ok {
		return nil, notFoundAutomation(id)
	}
	return &v, nil
}

// CreateAutomation 新建定义。`id` 必填（写面 DTO 的原文对象里取，非法类型 → 40000）。
func (s *Service) CreateAutomation(_ context.Context, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	raw, ok := req["id"]
	if !ok {
		return nil, capWriteErrParam("缺少 id（automation 的 id 是文件名，必填且不可改）")
	}
	id, okStr := patchID(raw)
	if !okStr || id == "" {
		return nil, capWriteErrParam(`字段 "id" 须为非空字符串`)
	}
	v, err := s.writeAutomation(id, rawPatch(req), true)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// UpdateAutomation 覆盖写：只改请求体里出现的键（未出现的保留原值；`null` 删键）。
func (s *Service) UpdateAutomation(_ context.Context, id string, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	v, err := s.writeAutomation(id, rawPatch(req), false)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// DeleteAutomation 删除定义（幂等）。
func (s *Service) DeleteAutomation(_ context.Context, id string) (*dtoautomation.AutomationDeleteResp, error) {
	removed, err := s.DeleteAutomationByID(id)
	if err != nil {
		return nil, err
	}
	return &dtoautomation.AutomationDeleteResp{ID: id, Deleted: removed}, nil
}

// PreviewAutomation 只算未来若干次到期时刻：**不落盘、不建会话**（也不要求定义已存在——
// 面板里正在编辑、还没保存的 schedule 同样能预览，这正是这个端点的用处）。
func (s *Service) PreviewAutomation(req dtoautomation.AutomationPreviewReq) (*dtoautomation.AutomationPreviewResp, error) {
	if len(req.Schedule) == 0 {
		return nil, code.Error(code.InvalidParam, "缺少 schedule（预览要算的就是它）")
	}
	var sched automation.Schedule
	if err := json.Unmarshal(req.Schedule, &sched); err != nil {
		return nil, code.Error(code.InvalidParam, "schedule 不是合法对象: "+err.Error())
	}
	sched.Normalize()
	from := time.Time{}
	if req.From != "" {
		t, err := time.Parse(time.RFC3339, req.From)
		if err != nil {
			return nil, code.Error(code.InvalidParam, "from 须为 RFC3339 时间: "+err.Error())
		}
		from = t
	}
	occs, err := automation.Preview(sched, from, req.Count)
	if err != nil {
		return nil, code.FromError(err)
	}
	resp := &dtoautomation.AutomationPreviewResp{Occurrences: make([]dtoautomation.AutomationPreviewItem, 0, len(occs))}
	for _, o := range occs {
		resp.Occurrences = append(resp.Occurrences, dtoautomation.AutomationPreviewItem{
			At: o.At.UTC().Format(time.RFC3339), Wall: o.Wall, Timezone: o.Timezone,
			Warnings: o.Warnings,
		})
		resp.Warnings = append(resp.Warnings, o.Warnings...)
	}
	return resp, nil
}

// ListAutomationRuns 运行史（最新在前）。定义装载失败时**不假装没有运行史**：
// 如实报 `definition_invalid`，因为"文件坏了"正是用户此刻要解决的问题。
func (s *Service) ListAutomationRuns(id string) (*dtoautomation.AutomationRunsResp, error) {
	v, ok := s.AutomationByID(id)
	if !ok {
		return nil, notFoundAutomation(id)
	}
	if v.Error != "" {
		return nil, invalidAutomationByID(id, v.Error)
	}
	// 空史也要是 `[]` 而不是 `null`：前端类型写的是数组，`null` 会让"没触发过"与
	// "字段缺失"在客户端无从区分（这条是被 browser-free 冒烟脚本抓出来的真实漂移）。
	runs := make([]automation.RunRecord, 0, len(v.RecentRuns))
	runs = append(runs, v.RecentRuns...)
	return &dtoautomation.AutomationRunsResp{
		ID: id, Runs: runs, Limit: automation.RecentRunsMax,
	}, nil
}

// RunAutomationNow 手动触发一次（端口形态）。
func (s *Service) RunAutomationNow(ctx context.Context, id string) (*dtoautomation.AutomationRunResp, error) {
	out, err := s.runAutomationNow(ctx, id)
	resp := &dtoautomation.AutomationRunResp{
		ID: id, Status: out.status, Trigger: out.trigger,
		ConversationID: out.conversationID, Reason: out.reason, State: out.state,
	}
	if err != nil {
		// 失败也要把结果回带（面板要显示"我点过、没跑成、为什么"）：用 detail 承载
		// 而不是丢掉——HTTP 层是错误信封，命中的前端可以直接用 resp 渲染原因。
		return resp, err
	}
	return resp, nil
}

// invalidAutomationByID 定义装载失败 → 40001（与 invalidAutomation 同一语义，只要有 id 与原因）。
func invalidAutomationByID(id, reason string) error {
	return acl.NewError(acl.ClassRequest, "definition_invalid",
		fmt.Sprintf("automation %s 的定义不可执行：%s", id, reason), nil)
}
