package manager

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtoautomation"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/automation"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/golib/gerror"
)

// ——— Automation 定义注册表（M4-3）———
//
// 事实源与能力定义同一条纪律：**文件是事实源**（`<AutomationDir>/<id>.json`），面板只是编辑器。
// 落盘完全复用 `capability_write.go` 的 C9 五步（读原文件 → 只改 patch 键 → 未知键保留 →
// 原子替换 → mtime 乐观锁），所以这里不再写第二份写盘实现。
//
// 定义文件里同时住着两样东西：**用户写的**（schedule / target）与**平台写回的运行态**
// （last_fired_at / last_status / state / consecutive_failures）。刻意同文件：运维打开一个
// 文件就能看到"上次什么时候跑的、跑成没成"；拆成两份必然出现"删了定义忘了状态"。
// 写回走的是同一套 patch 纪律，所以用户手写的注释性字段不会被平台抹掉。
//
// 内存里另有两样**不落盘**的调度态：`nextDue`（下次到期，改 schedule 后立刻重算）与
// `startup`（启动时该干什么，ADR-9 的落点）。它们由时钟决定、且随时可重算，落盘只会
// 制造第二个事实源。

// automationRec 一条 automation 的注册表记录。
type automationRec struct {
	def automation.Automation
	// nextDue 下次到期时刻（零值 = 没有下一次：已跑完的 once / 已归档 / 装载失败）。
	nextDue time.Time
	// startup 启动时的动作（wait | recover | expire | inactive）——由调度器的启动 pass 消费。
	startup automation.StartupAction
	// loadErr 装载失败原因（**留在注册表里**而不是静默跳过：用户写了文件却在面板上看不见，
	// 且不知道为什么，是最难排查的一类投诉）。
	loadErr string
}

// automationDirFor 取自动化定义目录；未配置 → 显式报错（不静默"内存生效、盘上没留痕"）。
func (s *Service) automationDirFor(what string) (string, error) {
	s.mu.Lock()
	dir := s.automationDir
	s.mu.Unlock()
	if dir == "" {
		return "", acl.NewError(acl.ClassRequest, "automation_dir_unset",
			fmt.Sprintf("未配置自动化目录（FORGE_AUTOMATIONS），%s只能内存生效、重启即失；设好目录后重试", what), nil)
	}
	return dir, nil
}

// LoadAutomationDefinitions 装载 `<dir>/*.json` 并登记目录（app 启动调用）。
//
// 坏定义**留在注册表里带 `loadErr`**（而不是像能力装载那样 WARN 后跳过）：automation 是
// 用户逐条手写的少量定义，写错一条却"列表里没有、调度也不跑、日志里一行 WARN"很难排查。
// 返回成功装载的条数（供启动日志）。
func (s *Service) LoadAutomationDefinitions(dir string) int {
	s.mu.Lock()
	s.automationDir = dir
	s.mu.Unlock()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("automation 目录读取失败", "dir", dir, "err", err)
		}
		return 0
	}
	now := time.Now()
	loaded := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		path := filepath.Join(dir, e.Name())
		rec := loadAutomationFile(path, id, now)
		if rec.loadErr == "" {
			loaded++
		} else {
			slog.Warn("automation 定义装载失败（已留在列表里带 error，便于面板显示）", "id", id, "err", rec.loadErr)
		}
		s.mu.Lock()
		if s.automations == nil {
			s.automations = map[string]*automationRec{}
		}
		s.automations[id] = rec
		s.mu.Unlock()
	}
	return loaded
}

// loadAutomationFile 读一个定义文件并算出调度态。**不改文件**（装载期的副作用会让每次重启
// 都改一遍用户文件；"once 过期置 inactive"那类改写由调度器的启动 pass 显式做，M4-4）。
func loadAutomationFile(path, id string, now time.Time) *automationRec {
	// 先把**文件名**记进 id：装载失败时列表仍然要指出"是哪个文件坏了"（否则一条 error
	// 挂在空 id 上，等于没说）。
	rec := &automationRec{def: automation.Automation{ID: id}}
	raw, err := os.ReadFile(path)
	if err != nil {
		rec.loadErr = fmt.Sprintf("读取失败：%v", err)
		return rec
	}
	var def automation.Automation
	if err := json.Unmarshal(raw, &def); err != nil {
		rec.loadErr = fmt.Sprintf("不是合法 JSON：%v", err)
		return rec
	}
	if def.ID == "" {
		def.ID = id
	}
	if def.ID != id {
		rec.loadErr = fmt.Sprintf("文件里的 id %q 与文件名 %q 不一致", def.ID, id)
		return rec
	}
	def.Schedule.Normalize()
	if err := def.Validate(); err != nil {
		rec.loadErr = errText(err)
		return rec
	}
	rec.def = def
	rec.def.State = automation.NormalizeState(def.State)
	rec.applySchedule(now)
	return rec
}

// applySchedule 重算 nextDue 与启动动作（装载、改定义、改 schedule 后都要走这里）。
//
// 用 `Schedule.Startup` 而不是直接 `NextAfter`：只有它能区分"未到期待跑"与"停机期间错过"，
// 并且把 `once` 的两条终局（过期未跑 / 已跑完）分开——这正是 ADR-9 的落点。
func (r *automationRec) applySchedule(now time.Time) {
	if r.loadErr != "" {
		return
	}
	action, occ, err := r.def.Schedule.Startup(r.def.LastFiredAt, now)
	if err != nil {
		r.loadErr = errText(err)
		return
	}
	r.startup = action
	r.nextDue = occ.At
}

// patchID 从 patch 的 `id` 值取字符串：写面（原文 JSON 对象）给的是 json.RawMessage，
// 测试与调度器给的是 string。两种都认，且类型不符要能判出来。
func patchID(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.RawMessage:
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return "", false
		}
		return s, true
	default:
		return "", false
	}
}

// errText 把任意错误压成一句人话（装载告警、触发失败原因都要能直接显示在面板上）。
//
// 三层来源都要照顾：acl 带码错误（给 Details，那才是人话）、经 `code.FromError` 归一过的
// gerror（给 Msg），其余给 Error()。
func errText(err error) string {
	if err == nil {
		return ""
	}
	var ae *acl.Error
	if asACLError(err, &ae) {
		if ae.Details != "" {
			return ae.Details
		}
		return ae.Code
	}
	if gerr, ok := code.FromError(err).(gerror.Error); ok && gerr.Msg != "" {
		return gerr.Msg
	}
	return err.Error()
}

// notFoundAutomation / invalidAutomation / automationDisabled 运行面（手动触发）的三类拒绝。
func notFoundAutomation(id string) error {
	return acl.NewError(acl.ClassRequest, "automation_not_found",
		fmt.Sprintf("未知 automation %q（列表刷新一下）", id), nil)
}

// invalidAutomation 定义本身不可执行（装载期就发现了）→ 报出装载期记下的原因，而不是"未知错误"。
func invalidAutomation(rec automationRec) error {
	return acl.NewError(acl.ClassRequest, "definition_invalid",
		fmt.Sprintf("automation %s 的定义不可执行：%s", rec.def.ID, rec.loadErr), nil)
}

// automationDisabled 用户把它关了（enabled=false）→ 手动触发也拒绝：
// 手动跑一次会绕过用户自己设的开关，而"关了还跑"是最难解释的一种行为。
func automationDisabled(id string) error {
	return acl.NewError(acl.ClassRequest, "automation_disabled",
		fmt.Sprintf("automation %s 已停用（enabled=false）：先启用再手动触发", id), nil)
}

// asACLError 是 errors.As 的小包装（避免本文件为了一个断言再引 errors 的命名冲突）。
func asACLError(err error, dst **acl.Error) bool {
	for err != nil {
		if ae, ok := err.(*acl.Error); ok {
			*dst = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// writeAutomation 写面的**唯一入口**（新建与覆盖写都走这里；端口形态见 automation_rest.go）。
//
// `create=true`：id 已存在 → 40901 `automation_exists`，**绝不覆盖**。
// `patch` 是"这次要改的键"（覆盖写只改出现过的键 —— C9 第 2 步）；落盘前先按**合并结果**校验，
// 校验不过 → **零字节落盘**（与能力写面同一条纪律）。
func (s *Service) writeAutomation(id string, patch map[string]any, create bool) (dtoautomation.AutomationResp, error) {
	dir, err := s.automationDirFor("automation 的增删改")
	if err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	path, err := definitionPath(dir, "automation", id)
	if err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	_, statErr := os.Stat(path)
	exists := statErr == nil
	switch {
	case create && exists:
		return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassLimit, "automation_exists",
			fmt.Sprintf("automation %s 已存在（换一个 id，或用 PUT 覆盖它）", id), nil)
	case !create && !exists:
		return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassRequest, "automation_not_found",
			fmt.Sprintf("automation %s 不存在（列表刷新一下；平台不会\"顺便新建\"）", id), nil)
	}
	// 平台独占键（认领游标/运行史）不得由用户写面写入：`last_fired_at` 就是"这次触发过没有"
	// 的判据，能写它就能让同一次触发重放（重复跑有副作用的 prompt），或把它永久卡住不跑。
	for k := range patch {
		if automation.IsReservedKey(k) {
			return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassRequest, "invalid_param",
				fmt.Sprintf("键 %q 由平台写回（运行态），不可通过接口写入", k), nil)
		}
	}
	// 定义文件里 `id` 是路径的事实：body 与路径不一致 → 参数错（而不是悄悄按路径改掉 body）。
	// 写面给进来的 id 是 `json.RawMessage`（原文对象），测试与调度器给的是 string，两种都要认；
	// 类型不符要报错，而不是当成"没传 id"（那会让 body 里的脏 id 悄悄落盘）。
	if v, ok := patch["id"]; ok && v != nil {
		s, okStr := patchID(v)
		if !okStr {
			return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassRequest, "invalid_param",
				`字段 "id" 须为字符串`, nil)
		}
		if s != "" && s != id {
			return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassRequest, "invalid_param",
				fmt.Sprintf("路径 id %q 与 body id %q 不一致", id, s), nil)
		}
	}
	patch["id"] = id
	// 合并后的**确切字节**先解成强类型再校验：PUT 的语义是"这几个键置成这些值"，
	// 所以必须校验**合并结果**（而不是只校验 patch 里的键）。
	draft, err := openDefinition(dir, "automation", id, patch)
	if err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	merged, err := draft.Merged()
	if err != nil {
		return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("序列化 automation %s 失败：%v", id, err), err)
	}
	var def automation.Automation
	if err := json.Unmarshal(merged, &def); err != nil {
		return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassRequest, "definition_invalid",
			fmt.Sprintf("automation %s 合并后不是合法定义：%v", id, err), err)
	}
	def.Schedule.Normalize()
	if err := def.Validate(); err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	// 规范化结果要**落进文件**：否则"文件里写的"与"调度实际用的"是两个值，
	// 而 weekly 的 days_of_week 的去重升序正是靠这一步才看得见（C9 只改必要的键：
	// 这里只重写 schedule.days_of_week 一个键，schedule 下用户自加的别的键原样保留）。
	if def.Schedule.Kind == automation.KindWeekly {
		var asMap map[string]any
		if err := json.Unmarshal(merged, &asMap); err == nil {
			if sch, ok := asMap["schedule"].(map[string]any); ok {
				days := make([]any, 0, len(def.Schedule.DaysOfWeek))
				for _, d := range def.Schedule.DaysOfWeek {
					days = append(days, d)
				}
				sch["days_of_week"] = days
				if err := draft.df.Set("schedule", sch); err != nil {
					return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassService, "definition_write_failed",
						fmt.Sprintf("写入 automation %s 的规范化 days_of_week 失败：%v", id, err), err)
				}
			}
		}
	}
	if err := saveDefinition(draft); err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	def.State = automation.NormalizeState(def.State)
	rec := &automationRec{def: def}
	rec.applySchedule(time.Now())
	s.mu.Lock()
	if s.automations == nil {
		s.automations = map[string]*automationRec{}
	}
	s.automations[id] = rec
	s.mu.Unlock()
	return rec.view(), nil
}

// DeleteAutomationByID 删定义文件 + 摘内存条目；幂等（本来就不存在 → deleted:false）。
func (s *Service) DeleteAutomationByID(id string) (bool, error) {
	dir, err := s.automationDirFor("automation 的删除")
	if err != nil {
		return false, err
	}
	removed, err := removeDefinitionPermanently(dir, "automation", id)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	delete(s.automations, id)
	s.mu.Unlock()
	return removed, nil
}

// ListAutomations 列出注册表（含装载失败的那些，带 `error` 字段），按 id 排序。
func (s *Service) ListAutomations() ([]dtoautomation.AutomationResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]dtoautomation.AutomationResp, 0, len(s.automations))
	for _, rec := range s.automations {
		out = append(out, rec.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AutomationByID 取一条（含装载失败的那些）。
func (s *Service) AutomationByID(id string) (dtoautomation.AutomationResp, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.automations[id]
	if !ok {
		return dtoautomation.AutomationResp{}, false
	}
	return rec.view(), true
}

// saveAutomationOverlay 运行态写回（`last_fired_at` / `last_status` / `state` / …）。
//
// **平台自己调**，不是用户写面。走同一套 C9 落盘：只改这几个键，用户手写的字段（含我们不认识的）
// 原样保留；写回后重算调度态（`last_fired_at` 变了，`next_due` 必然要跟着变）。
func (s *Service) saveAutomationOverlay(id string, overlay map[string]any) error {
	dir, err := s.automationDirFor("automation 运行态写回")
	if err != nil {
		return err
	}
	if err := writeDefinition(dir, "automation", id, overlay); err != nil {
		return err
	}
	// 写回后**重新装载这一条**，而不是在内存里逐键 merge。
	//
	// 逐键 merge 看着更省一次读盘，实际是个"忘了某个键"的陷阱：`recent_runs` 就那么漏过一次
	// ——overlay 写盘时用内存里的旧环形缓冲覆盖文件，把刚追加的那条认领记录整段抹掉
	// （文件里只剩最新一条）。重新装载让"内存 = 文件"成为定义，未知键/环形缓冲/时间格式
	// 全走同一条解析路径，不可能各写一半。
	rec := loadAutomationFile(filepath.Join(dir, id+".json"), id, time.Now())
	if rec.loadErr != "" {
		// 我们刚写成功却读不回来 → 只能是并发手改（或磁盘异常）：保留旧内存态并告警，
		// 绝不用一条"读坏了的记录"覆盖调度态。
		slog.Warn("automation 写回后重载失败，保留原内存态", "automation", id, "err", rec.loadErr)
		return nil
	}
	s.mu.Lock()
	s.automations[id] = rec
	s.mu.Unlock()
	return nil
}

// automationDueSnapshot 取"到期待处理"的快照（调度器每 tick 调用）。
//
// 过滤掉四类：装载失败、`enabled=false`、非 `active` 状态、以及没有下一次到期的（跑完的 once）。
// 排序按 `(next_due, id)`——与 openwork `selectDueAutomations` 同序，让同刻到期的多条
// automation 有稳定顺序（否则测试与运维观察都会飘）。
func (s *Service) automationDueSnapshot(now time.Time) []automationRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]automationRec, 0, len(s.automations))
	for _, rec := range s.automations {
		if rec.loadErr != "" || !rec.def.Enabled || !automation.Schedulable(rec.def.State) {
			continue
		}
		if rec.nextDue.IsZero() || rec.nextDue.After(now) {
			continue
		}
		r := *rec
		r.def.Schedule.Normalize()
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].nextDue.Equal(out[j].nextDue) {
			return out[i].nextDue.Before(out[j].nextDue)
		}
		return out[i].def.ID < out[j].def.ID
	})
	return out
}

// automationRecByID 取一份记录副本（调度器执行前用；副本避免持锁跑 agent 会话动作）。
func (s *Service) automationRecByID(id string) (automationRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.automations[id]
	if !ok {
		return automationRec{}, false
	}
	return *rec, true
}

// automationRuntimeState 取一条 automation 当前的**会话是否还在跑**（认领的 overlap 判据）。
//
// 判据用"最近一次触发产生的会话仍在 in_progress"：会话自身状态就是最权威的运行态
// （ADR-4：不建第二套 run 状态机）。
func (s *Service) automationHasActiveRun(rec automationRec) bool {
	// 判据只有一条：**会话状态是不是 in_progress**（不另写一份终态清单——两份清单必然漂移，
	// 而漂移的后果是"卡住的会话"被当成已结束，于是同一 occurrence 反复重跑）。
	return s.conversationRunning(rec.def.LastConversationID)
}

// view 把记录投影成对外视图（时间一律 RFC3339 UTC 文本，与其余接口同格式）。
func (r *automationRec) view() dtoautomation.AutomationResp {
	v := dtoautomation.AutomationResp{
		ID:                  r.def.ID,
		Name:                r.def.Name,
		Enabled:             r.def.Enabled,
		State:               automation.NormalizeState(r.def.State),
		Schedule:            r.def.Schedule,
		Target:              r.def.Target,
		MaxRunDuration:      r.def.MaxRunDuration,
		LastConversationID:  r.def.LastConversationID,
		LastStatus:          r.def.LastStatus,
		LastReason:          r.def.LastReason,
		ConsecutiveFailures: r.def.ConsecutiveFailures,
		Error:               r.loadErr,
		RecentRuns:          r.def.RecentRuns,
	}
	if !r.nextDue.IsZero() {
		v.NextDueAt = r.nextDue.UTC().Format(time.RFC3339)
	}
	if r.def.LastFiredAt != nil {
		v.LastFiredAt = r.def.LastFiredAt.UTC().Format(time.RFC3339)
	}
	return v
}
