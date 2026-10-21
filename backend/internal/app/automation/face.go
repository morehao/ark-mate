package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	pkgauto "github.com/morehao/ark-mate/internal/platform/automation"
	"github.com/morehao/ark-mate/internal/platform/code"
	defn "github.com/morehao/ark-mate/internal/platform/definition"
	domauto "github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/contract/dtoautomation"
)

// ——— automation 的**对外面**（P4a 正文第九刀：从 `domain/automation` 上提到这里）———
//
// `domain/automation/plane.go` 的文件头早就写着本域"**不负责**：对外 DTO 与错误码出口
// （`service/svc*` 与 `dto/*`）"——而 `rest.go` 当时整文件跟着 P2b 搬进了域里，于是这条口径
// 在代码上不成立：域里直接 `dtoautomation.XxxResp` 进、`code.Error` 出。
//
// 这一刀把那一面**上提**：域只留"定义怎么装载/怎么落盘/排期怎么算/一次触发怎么认领写回"
// （`WriteAutomation` / `AutomationByID` / `DeleteAutomationByID` / `Recs` / `RunNow`），
// 对外 DTO 的折叠、错误码出口、运行史的上限与空数组口径都在这一层。
//
// 为什么这一层不是"一行转发"：它做的正是域**不该**做的事——请求形态校验（`id` 必填、
// 类型不符 → 40000）、错误码出口（40401 / 40901 / 40001 的分界）、DTO 折叠（`Rec.View()` →
// `AutomationResp`、`RunOutcome` → `AutomationRunResp`）、以及"空运行史也要是 `[]` 不是 `null`"
// 这类**对外形态**约定。域里那些方法是**领域动作**，不是这些方法的同义改写。

// Service automation 对外面的用例实现。
type Service struct {
	plane *domauto.Plane
}

// New 造用例层（依赖事实：自动化域平面）。
//
// 平面在装配期已经接好宿主端口（`SetHost`）与库句柄（`SetStore`），所以这里**不需要**再传
// 建会话/取消/推事件那些能力——"触发一次会发生什么"是域自己的规则。
func New(plane *domauto.Plane) AutomationSvc { return &Service{plane: plane} }

var _ AutomationSvc = (*Service)(nil)

// ListAutomations 列出全部 automation（含装载失败的那些，带 `error` 字段），按 id 排序。
//
// 排序在这一层：域给的是**注册表快照**（`Recs()`），"按 id 排"是对外读面的口径。
func (s *Service) ListAutomations() ([]dtoautomation.AutomationResp, error) {
	recs := s.plane.Recs()
	views := make([]domauto.AutomationView, 0, len(recs))
	for _, rec := range recs {
		views = append(views, rec.View())
	}
	out := automationResps(views)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetAutomation 取一条（不存在 → `automation_not_found` 40401）。
func (s *Service) GetAutomation(id string) (*dtoautomation.AutomationResp, error) {
	v, ok := s.plane.AutomationByID(id)
	if !ok {
		return nil, notFoundAutomation(id)
	}
	out := automationResp(v)
	return &out, nil
}

// CreateAutomation 新建定义。`id` 必填（写面 DTO 的原文对象里取，非法类型 → 40000）。
func (s *Service) CreateAutomation(ctx context.Context, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	id, ok, err := defn.RawString(req, "id")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, defn.InvalidParamErr("缺少 id（automation 的 id 是文件名，必填且不可改）")
	}
	if id == "" {
		return nil, defn.InvalidParamErr(`字段 "id" 须为非空字符串`)
	}
	v, err := s.plane.WriteAutomation(ctx, id, defn.RawPatch(req), true)
	if err != nil {
		return nil, err
	}
	out := automationResp(v)
	return &out, nil
}

// UpdateAutomation 覆盖写：只改请求体里出现的键（未出现的保留原值；`null` 删键）。
func (s *Service) UpdateAutomation(ctx context.Context, id string, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	v, err := s.plane.WriteAutomation(ctx, id, defn.RawPatch(req), false)
	if err != nil {
		return nil, err
	}
	out := automationResp(v)
	return &out, nil
}

// DeleteAutomation 删除定义（幂等）。
func (s *Service) DeleteAutomation(ctx context.Context, id string) (*dtoautomation.AutomationDeleteResp, error) {
	removed, err := s.plane.DeleteAutomationByID(ctx, id)
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
	var sched pkgauto.Schedule
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
	occs, err := pkgauto.Preview(sched, from, req.Count)
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
	v, ok := s.plane.AutomationByID(id)
	if !ok {
		return nil, notFoundAutomation(id)
	}
	if v.Error != "" {
		return nil, invalidAutomationByID(id, v.Error)
	}
	// 空史也要是 `[]` 而不是 `null`：前端类型写的是数组，`null` 会让"没触发过"与
	// "字段缺失"在客户端无从区分（这条是被 browser-free 冒烟脚本抓出来的真实漂移）。
	runs := make([]pkgauto.RunRecord, 0, len(v.RecentRuns))
	runs = append(runs, v.RecentRuns...)
	return &dtoautomation.AutomationRunsResp{
		ID: id, Runs: runs, Limit: pkgauto.RecentRunsMax,
	}, nil
}

// RunAutomationNow 手动触发一次（端口形态）。
func (s *Service) RunAutomationNow(ctx context.Context, id string) (*dtoautomation.AutomationRunResp, error) {
	out, err := s.plane.RunNow(ctx, id)
	resp := &dtoautomation.AutomationRunResp{
		ID: id, Status: out.Status, Trigger: out.Trigger,
		SessionID: out.SessionID, Reason: out.Reason, State: out.State,
	}
	if err != nil {
		// 失败也要把结果回带（面板要显示"我点过、没跑成、为什么"）：用 detail 承载
		// 而不是丢掉——HTTP 层是错误信封，命中的前端可以直接用 resp 渲染原因。
		return resp, err
	}
	return resp, nil
}

// notFoundAutomation 未知 id（40401）。
//
// 域里那份（`automation.notFoundAutomation`）是运行面（调度/手动触发）用的**同码同文案**；
// 两份必然并存——域不能 import 用例层，而错误码出口归用例层（本文件的头注）。
// 码是同一个 40401，文案也用同一句，避免用户在两处看到不同的说法。
func notFoundAutomation(id string) error {
	return code.RequestError("automation_not_found", fmt.Sprintf("未知 automation %q（列表刷新一下）", id))
}

// invalidAutomationByID 定义装载失败 → 40001（只要有 id 与原因）。
func invalidAutomationByID(id, reason string) error {
	return code.RequestError("definition_invalid", fmt.Sprintf("automation %s 的定义不可执行：%s", id, reason))
}
