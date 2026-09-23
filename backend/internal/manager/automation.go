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

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/automation"
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

// AutomationView 列的对外视图（DTO 由 ctrautomation 映射；管理器不依赖传输层类型）。
type AutomationView struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Enabled             bool                `json:"enabled"`
	State               string              `json:"state"`
	Schedule            automation.Schedule `json:"schedule"`
	Target              automation.Target   `json:"target"`
	MaxRunDuration      string              `json:"max_run_duration,omitempty"`
	NextDueAt           string              `json:"next_due_at,omitempty"`
	LastFiredAt         string              `json:"last_fired_at,omitempty"`
	LastConversationID  string              `json:"last_conversation_id,omitempty"`
	LastStatus          string              `json:"last_status,omitempty"`
	LastReason          string              `json:"last_reason,omitempty"`
	ConsecutiveFailures int                 `json:"consecutive_failures,omitempty"`
	SourceFile          string              `json:"source_file,omitempty"`
	Error               string              `json:"error,omitempty"`
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
		rec.loadErr = validationMessage(err)
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
		r.loadErr = validationMessage(err)
		return
	}
	r.startup = action
	r.nextDue = occ.At
}

// validationMessage 把带码错误压成一句人话（装载告警要能直接显示在列表里）。
func validationMessage(err error) string {
	var ae *acl.Error
	if ok := asACLError(err, &ae); ok {
		if ae.Details != "" {
			return ae.Details
		}
		return ae.Code
	}
	return err.Error()
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

// CreateAutomation 新建定义（id 已存在 → 40901 automation_exists，**绝不覆盖**）。
//
// `patch` 是**定义键的完整集合**（控制器从 DTO 拼好）。落盘前先按合并结果校验：
// 校验不过 → 零字节落盘（与能力写面同一条纪律："被拒的定义零字节落盘"）。
func (s *Service) CreateAutomation(id string, patch map[string]any) (AutomationView, error) {
	return s.writeAutomation(id, patch, true)
}

// UpdateAutomation 覆盖写（只改 patch 里出现的键；未出现的键保留原值 —— C9 第 2 步）。
func (s *Service) UpdateAutomation(id string, patch map[string]any) (AutomationView, error) {
	return s.writeAutomation(id, patch, false)
}

func (s *Service) writeAutomation(id string, patch map[string]any, create bool) (AutomationView, error) {
	dir, err := s.automationDirFor("automation 的增删改")
	if err != nil {
		return AutomationView{}, err
	}
	path, err := definitionPath(dir, "automation", id)
	if err != nil {
		return AutomationView{}, err
	}
	_, statErr := os.Stat(path)
	exists := statErr == nil
	switch {
	case create && exists:
		return AutomationView{}, acl.NewError(acl.ClassLimit, "automation_exists",
			fmt.Sprintf("automation %s 已存在（换一个 id，或用 PUT 覆盖它）", id), nil)
	case !create && !exists:
		return AutomationView{}, acl.NewError(acl.ClassRequest, "automation_not_found",
			fmt.Sprintf("automation %s 不存在（列表刷新一下；平台不会\"顺便新建\"）", id), nil)
	}
	// 定义文件里 `id` 是路径的事实：body 与路径不一致 → 参数错（而不是悄悄按路径改掉 body）。
	if v, ok := patch["id"]; ok {
		if s, _ := v.(string); s != "" && s != id {
			return AutomationView{}, acl.NewError(acl.ClassRequest, "invalid_param",
				fmt.Sprintf("路径 id %q 与 body id %q 不一致", id, s), nil)
		}
	} else {
		patch["id"] = id
	}
	// 合并后的**确切字节**先解成强类型再校验：PUT 的语义是"这几个键置成这些值"，
	// 所以必须校验**合并结果**（而不是只校验 patch 里的键）。
	draft, err := openDefinition(dir, "automation", id, patch)
	if err != nil {
		return AutomationView{}, err
	}
	merged, err := draft.Merged()
	if err != nil {
		return AutomationView{}, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("序列化 automation %s 失败：%v", id, err), err)
	}
	var def automation.Automation
	if err := json.Unmarshal(merged, &def); err != nil {
		return AutomationView{}, acl.NewError(acl.ClassRequest, "definition_invalid",
			fmt.Sprintf("automation %s 合并后不是合法定义：%v", id, err), err)
	}
	def.Schedule.Normalize()
	if err := def.Validate(); err != nil {
		return AutomationView{}, err
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
					return AutomationView{}, acl.NewError(acl.ClassService, "definition_write_failed",
						fmt.Sprintf("写入 automation %s 的规范化 days_of_week 失败：%v", id, err), err)
				}
			}
		}
	}
	if err := saveDefinition(draft); err != nil {
		return AutomationView{}, err
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

// DeleteAutomation 删定义文件 + 摘内存条目；幂等（本来就不存在 → deleted:false）。
func (s *Service) DeleteAutomation(id string) (bool, error) {
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
func (s *Service) ListAutomations() []AutomationView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AutomationView, 0, len(s.automations))
	for _, rec := range s.automations {
		out = append(out, rec.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AutomationByID 取一条（含装载失败的那些）。
func (s *Service) AutomationByID(id string) (AutomationView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.automations[id]
	if !ok {
		return AutomationView{}, false
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
	s.mu.Lock()
	rec := s.automations[id]
	s.mu.Unlock()
	if rec == nil {
		return nil
	}
	// 内存态就地更新（值来自刚写入的 overlay；未知键本来就不在内存结构里）。
	updated := *rec
	mergeOverlayIntoRec(&updated, overlay)
	updated.applySchedule(time.Now())
	s.mu.Lock()
	s.automations[id] = &updated
	s.mu.Unlock()
	return nil
}

// mergeOverlayIntoRec 把 overlay 应用到内存记录（避免为几个字段再读一次文件）。
// 这里只认平台自己写的键；`state` 走状态机归一（未知状态名按 active，不是报错）。
func mergeOverlayIntoRec(rec *automationRec, overlay map[string]any) {
	for k, v := range overlay {
		switch k {
		case "state":
			if s, ok := v.(string); ok {
				rec.def.State = automation.NormalizeState(s)
			}
		case "last_fired_at":
			switch t := v.(type) {
			case string:
				if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
					rec.def.LastFiredAt = &parsed
				}
			case time.Time:
				rec.def.LastFiredAt = &t
			case nil:
				rec.def.LastFiredAt = nil
			}
		case "last_conversation_id":
			rec.def.LastConversationID, _ = v.(string)
		case "last_status":
			rec.def.LastStatus, _ = v.(string)
		case "last_reason":
			rec.def.LastReason, _ = v.(string)
		case "consecutive_failures":
			if n, ok := v.(int); ok {
				rec.def.ConsecutiveFailures = n
			}
		case "enabled":
			if b, ok := v.(bool); ok {
				rec.def.Enabled = b
			}
		}
	}
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
	id := rec.def.LastConversationID
	if id == "" {
		return false
	}
	s.mu.Lock()
	conv, ok := s.conversations[id]
	s.mu.Unlock()
	if !ok {
		return false
	}
	switch conv.Status {
	case ConversationStatusCompleted, ConversationStatusFailed,
		ConversationStatusCancelled, ConversationStatusInterrupted:
		return false
	}
	return true
}

// view 把记录投影成对外视图（时间一律 RFC3339 UTC 文本，与其余接口同格式）。
func (r *automationRec) view() AutomationView {
	v := AutomationView{
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
	}
	if !r.nextDue.IsZero() {
		v.NextDueAt = r.nextDue.UTC().Format(time.RFC3339)
	}
	if r.def.LastFiredAt != nil {
		v.LastFiredAt = r.def.LastFiredAt.UTC().Format(time.RFC3339)
	}
	return v
}
