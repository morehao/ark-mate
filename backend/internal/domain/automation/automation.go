package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/automation"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/gerror"
	"github.com/morehao/golib/glog"
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

// Rec 一条 automation 的注册表记录。
type Rec struct {
	// Def 定义本体。
	Def automation.Automation
	// NextDue 下次到期时刻（零值 = 没有下一次：已跑完的 once / 已归档 / 装载失败）。
	NextDue time.Time
	// Startup 启动时的动作（wait | recover | expire | inactive）——由调度器的启动 pass 消费。
	Startup automation.StartupAction
	// LoadErr 装载失败原因（**留在注册表里**而不是静默跳过：用户写了文件却在面板上看不见，
	// 且不知道为什么，是最难排查的一类投诉）。
	LoadErr string
}

// automationDirFor 取自动化定义目录；未配置 → 显式报错（不静默"内存生效、盘上没留痕"）。
func (pl *Plane) automationDirFor(what string) (string, error) {
	pl.mu.Lock()
	dir := pl.dir
	pl.mu.Unlock()
	if dir == "" {
		return "", acl.NewError(acl.ClassRequest, "automation_dir_unset",
			fmt.Sprintf("未配置自动化目录（MATE_AUTOMATIONS），%s只能内存生效、重启即失；设好目录后重试", what), nil)
	}
	return dir, nil
}

// LoadAutomationDefinitions 装载 `<dir>/*.json` 并登记目录（app 启动调用）。
//
// 坏定义**留在注册表里带 `loadErr`**（而不是像能力装载那样 WARN 后跳过）：automation 是
// 用户逐条手写的少量定义，写错一条却"列表里没有、调度也不跑、日志里一行 WARN"很难排查。
// 返回成功装载的条数（供启动日志）。
func (pl *Plane) LoadAutomationDefinitions(dir string) int {
	pl.mu.Lock()
	pl.dir = dir
	pl.mu.Unlock()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			glog.Warnw(context.Background(), "automation 目录读取失败", "dir", dir, "err", err)
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
		if rec.LoadErr == "" {
			loaded++
		} else {
			glog.Warnw(context.Background(), "automation 定义装载失败（已留在列表里带 error，便于面板显示）", "id", id, "err", rec.LoadErr)
		}
		pl.mu.Lock()
		if pl.recs == nil {
			pl.recs = map[string]*Rec{}
		}
		pl.recs[id] = rec
		pl.mu.Unlock()
	}
	return loaded
}

// loadAutomationFile 读一个定义文件并算出调度态。**不改文件**（装载期的副作用会让每次重启
// 都改一遍用户文件；"once 过期置 inactive"那类改写由调度器的启动 pass 显式做，M4-4）。
func loadAutomationFile(path, id string, now time.Time) *Rec {
	// 先把**文件名**记进 id：装载失败时列表仍然要指出"是哪个文件坏了"（否则一条 error
	// 挂在空 id 上，等于没说）——`buildAutomationRec` 承接这条约定。
	raw, err := os.ReadFile(path)
	if err != nil {
		return &Rec{Def: automation.Automation{ID: id}, LoadErr: fmt.Sprintf("读取失败：%v", err)}
	}
	var def automation.Automation
	if err := json.Unmarshal(raw, &def); err != nil {
		return &Rec{Def: automation.Automation{ID: id}, LoadErr: fmt.Sprintf("不是合法 JSON：%v", err)}
	}
	return buildAutomationRec(def, id, now)
}

// LoadAutomationDefinitionsFromStore 从持久层装载定时任务（sqlite 模式；M6-7）。
//
// 与文件装载的差别只有三处，都是"载体换了"的必然结果：
//
//  1. **整体替换式**：先把内存清空再逐行铺——库里删掉的行不能以幽灵任务留在内存里
//     （与能力/agent 面同源）。这也是"定义与运行态分列"之后唯一安全的装载方式；
//  2. 一行 = 定义正文（含 `state`）+ 运行态列：由 `automationDefFromRow` 合成完整定义，
//     再走**同一套** `buildAutomationRec`（校验、规范化、nextDue 计算全在那一处）；
//  3. `dir` 仍然记下（`MATE_AUTOMATIONS` 是导入/导出目录，且 memory 模式的写面仍需要它）。
//
// 容错逐字保留：坏定义**留在列表里带 `loadErr`**（用户写了文件却在面板上看不见、还不知道
// 为什么，是最难排查的一类投诉）。
func (pl *Plane) LoadAutomationDefinitionsFromStore(ctx context.Context, dir string) int {
	pl.mu.Lock()
	pl.dir = dir
	pl.recs = map[string]*Rec{}
	pl.mu.Unlock()
	if pl.st == nil {
		return 0
	}
	rows, err := pl.st.ListAutomationDefinitions(ctx)
	if err != nil {
		glog.Warnw(ctx, "automation: 从持久层读定义失败", "err", err)
		return 0
	}
	now := time.Now()
	loaded := 0
	for i := range rows {
		rec := loadAutomationRow(&rows[i], now)
		if rec.LoadErr == "" {
			loaded++
		} else {
			glog.Warnw(ctx, "automation 定义装载失败（已留在列表里带 error，便于面板显示）",
				"id", rows[i].ID, "err", rec.LoadErr)
		}
		pl.mu.Lock()
		if pl.recs == nil {
			pl.recs = map[string]*Rec{}
		}
		pl.recs[rows[i].ID] = rec
		pl.mu.Unlock()
	}
	return loaded
}

// applySchedule 重算 nextDue 与启动动作（装载、改定义、改 schedule 后都要走这里）。
//
// 用 `Schedule.Startup` 而不是直接 `NextAfter`：只有它能区分"未到期待跑"与"停机期间错过"，
// 并且把 `once` 的两条终局（过期未跑 / 已跑完）分开——这正是 ADR-9 的落点。
func (r *Rec) ApplySchedule(now time.Time) {
	if r.LoadErr != "" {
		return
	}
	action, occ, err := r.Def.Schedule.Startup(r.Def.LastFiredAt, now)
	if err != nil {
		r.LoadErr = errText(err)
		return
	}
	r.Startup = action
	r.NextDue = occ.At
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
func invalidAutomation(rec Rec) error {
	return acl.NewError(acl.ClassRequest, "definition_invalid",
		fmt.Sprintf("automation %s 的定义不可执行：%s", rec.Def.ID, rec.LoadErr), nil)
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
func (pl *Plane) WriteAutomation(ctx context.Context, id string, patch map[string]any, create bool) (dtoautomation.AutomationResp, error) {
	// 打开草稿（**这一步就把载体的存在性与当前版本握在手里**：文件 = mtime，DB = 行 revision），
	// 后面的"合并 → 校验 → 规范化 → 提交"对两种载体逐字相同。
	draft, err := pl.openAutomationDraft(ctx, id)
	if err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	exists := draft.Existed
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
	// `target.runtime_hint` / `target.agent_id` 是**已下线的选择面**（底座构建期固定；
	// v19 起 agent 面整体撤销）。两条路分开处理，
	// 因为正确出路不同：
	//   ① 本次请求**带了这个键**（有人在试着设它）→ 40001 + 指路。静默忽略会让它变成
	//      "写了、存了、什么都不发生"的骗人字段（同 dtoagent 的 default_model / runtimes）；
	//   ② 键只是**老定义里的残留**（本次没带）→ 落盘前删掉（自愈）+ WARN。这一条不能也拒绝：
	//      否则任何带该键的存量 automation 会**永远存不回去**，而界面已经没有这个字段可让
	//      用户删——那等于把一个历史遗留键变成资源锁死。
	if err := rejectRuntimeHintInPatch(id, patch); err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	if err := rejectAgentIDInPatch(id, patch); err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	patch["id"] = id
	// 应用 patch（值 nil = 删键；顺序稳定）。文件载体与 DB 载体共用这一步，所以"只改出现过的键"
	// 这条 C9 语义不因换载体而变。
	for _, k := range definition.SortedPatchKeys(patch) {
		if patch[k] == nil {
			draft.DF.Delete(k)
			continue
		}
		if err := draft.DF.Set(k, patch[k]); err != nil {
			return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassService, "definition_write_failed",
				fmt.Sprintf("automation 定义 %s 的键 %q 无法序列化：%v", draft.Locator, k, err), err)
		}
	}
	// 合并后的**确切字节**先解成强类型再校验：PUT 的语义是"这几个键置成这些值"，
	// 所以必须校验**合并结果**（而不是只校验 patch 里的键）。
	merged, err := draft.DF.Marshal()
	if err != nil {
		return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("序列化 automation %s 失败：%v", id, err), err)
	}
	merged, droppedLegacy, err := dropLegacyRuntimeHint(draft.DF, merged, id)
	if err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	if droppedLegacy {
		glog.Warnw(ctx, "automation 定义里已下线的 target.runtime_hint / target.agent_id 已清除",
			"automation", id, "runtime", runtime.SelectedRuntime())
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
				if err := draft.DF.Set("schedule", sch); err != nil {
					return dtoautomation.AutomationResp{}, acl.NewError(acl.ClassService, "definition_write_failed",
						fmt.Sprintf("写入 automation %s 的规范化 days_of_week 失败：%v", id, err), err)
				}
			}
		}
	}
	if err := pl.commitAutomationDraft(ctx, draft, create); err != nil {
		return dtoautomation.AutomationResp{}, err
	}
	def.State = automation.NormalizeState(def.State)
	rec := &Rec{Def: def}
	rec.ApplySchedule(time.Now())
	pl.mu.Lock()
	if pl.recs == nil {
		pl.recs = map[string]*Rec{}
	}
	pl.recs[id] = rec
	pl.mu.Unlock()
	return rec.View(), nil
}

// DeleteAutomationByID 删载体里的定义 + 摘内存条目；幂等（本来就不存在 → deleted:false）。
func (pl *Plane) DeleteAutomationByID(ctx context.Context, id string) (bool, error) {
	removed, err := pl.deleteAutomationDefinition(ctx, id)
	if err != nil {
		return false, err
	}
	pl.mu.Lock()
	delete(pl.recs, id)
	pl.mu.Unlock()
	return removed, nil
}

// ListAutomations 列出注册表（含装载失败的那些，带 `error` 字段），按 id 排序。
func (pl *Plane) ListAutomations() ([]dtoautomation.AutomationResp, error) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]dtoautomation.AutomationResp, 0, len(pl.recs))
	for _, rec := range pl.recs {
		out = append(out, rec.View())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AutomationByID 取一条（含装载失败的那些）。
func (pl *Plane) AutomationByID(id string) (dtoautomation.AutomationResp, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.recs[id]
	if !ok {
		return dtoautomation.AutomationResp{}, false
	}
	return rec.View(), true
}

// DueSnapshot 取"到期待处理"的快照（调度器每 tick 调用）。
//
// 过滤掉四类：装载失败、`enabled=false`、非 `active` 状态、以及没有下一次到期的（跑完的 once）。
// 排序按 `(next_due, id)`——与 openwork `selectDueAutomations` 同序，让同刻到期的多条
// automation 有稳定顺序（否则测试与运维观察都会飘）。
func (pl *Plane) DueSnapshot(now time.Time) []Rec {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]Rec, 0, len(pl.recs))
	for _, rec := range pl.recs {
		if rec.LoadErr != "" || !rec.Def.Enabled || !automation.Schedulable(rec.Def.State) {
			continue
		}
		if rec.NextDue.IsZero() || rec.NextDue.After(now) {
			continue
		}
		r := *rec
		r.Def.Schedule.Normalize()
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NextDue.Equal(out[j].NextDue) {
			return out[i].NextDue.Before(out[j].NextDue)
		}
		return out[i].Def.ID < out[j].Def.ID
	})
	return out
}

// RecByID 取一份记录副本（调度器执行前用；副本避免持锁跑 agent 会话动作）。
func (pl *Plane) RecByID(id string) (Rec, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.recs[id]
	if !ok {
		return Rec{}, false
	}
	return *rec, true
}

// automationRuntimeState 取一条 automation 当前的**会话是否还在跑**（认领的 overlap 判据）。
//
// 判据用"最近一次触发产生的会话仍在 in_progress"：会话自身状态就是最权威的运行态
// （ADR-4：不建第二套 run 状态机）。
func (pl *Plane) HasActiveRun(rec Rec) bool {
	// 判据只有一条：**会话状态是不是 in_progress**（不另写一份终态清单——两份清单必然漂移，
	// 而漂移的后果是"卡住的会话"被当成已结束，于是同一 occurrence 反复重跑）。
	if pl.hostOf() == nil {
		return false
	}
	return pl.hostOf().SessionRunning(rec.Def.LastSessionID)
}

// view 把记录投影成对外视图（时间一律 RFC3339 UTC 文本，与其余接口同格式）。
func (r *Rec) View() dtoautomation.AutomationResp {
	v := dtoautomation.AutomationResp{
		ID:                  r.Def.ID,
		Name:                r.Def.Name,
		Enabled:             r.Def.Enabled,
		State:               automation.NormalizeState(r.Def.State),
		Schedule:            r.Def.Schedule,
		Target:              r.Def.Target,
		MaxRunDuration:      r.Def.MaxRunDuration,
		LastSessionID:       r.Def.LastSessionID,
		LastStatus:          r.Def.LastStatus,
		LastReason:          r.Def.LastReason,
		ConsecutiveFailures: r.Def.ConsecutiveFailures,
		Error:               r.LoadErr,
		RecentRuns:          r.Def.RecentRuns,
	}
	if !r.NextDue.IsZero() {
		v.NextDueAt = r.NextDue.UTC().Format(time.RFC3339)
	}
	if r.Def.LastFiredAt != nil {
		v.LastFiredAt = r.Def.LastFiredAt.UTC().Format(time.RFC3339)
	}
	return v
}

// rejectRuntimeHintInPatch 本次请求若显式带了 `target.runtime_hint`（非 null）→ 40001。
//
// 只看**请求体**，不看合并结果：合并结果里可能是老定义的残留，那条走自愈（见 dropLegacyRuntimeHint）。
// 两条路必须分开，否则"存量定义带着历史键"会被锁死成永远存不回去。
func rejectRuntimeHintInPatch(id string, patch map[string]any) error {
	tgt, ok := patch["target"].(map[string]any)
	if !ok {
		return nil
	}
	v, ok := tgt["runtime_hint"]
	if !ok || v == nil {
		return nil
	}
	return acl.NewError(acl.ClassRequest, "runtime_selection_removed",
		fmt.Sprintf("target.runtime_hint 已下线：底座由构建期固定（本机为 %s），不能在 automation 上选择；"+
			"请从请求体里去掉这个键（automation %s）", runtime.SelectedRuntime(), id), nil)
}

// rejectAgentIDInPatch 本次请求若显式带了 `target.agent_id`（非 null）→ 40001。
//
// 与 rejectRuntimeHintInPatch 同形同由（v19 撤销 agent 面）：会话不再有 agent 身份，
// automation 的目标只剩"cwd + 模型 + prompt"。静默忽略会让用户以为"我指定了用哪个 agent"。
func rejectAgentIDInPatch(id string, patch map[string]any) error {
	tgt, ok := patch["target"].(map[string]any)
	if !ok {
		return nil
	}
	v, ok := tgt["agent_id"]
	if !ok || v == nil {
		return nil
	}
	return acl.NewError(acl.ClassRequest, "agent_selection_removed",
		fmt.Sprintf("target.agent_id 已下线：agent 概念已并入 runtime（本机为 %s），会话不再有 agent 身份；"+
			"请从请求体里去掉这个键（automation %s）", runtime.SelectedRuntime(), id), nil)
}

// dropLegacyRuntimeHint 把**合并结果**里残留的 `target.runtime_hint` / `target.agent_id`
// 删掉（自愈），返回新字节与是否真的删了键。
//
// 只删这两个键：`target` 下用户自加的别的键原样保留（C9 的"只改必要的键"在这里同样成立，
// 所以走 map 删除而不是用强类型 `def.Target` 整体覆盖）。
func dropLegacyRuntimeHint(df *capability.DefinitionFile, merged []byte, id string) ([]byte, bool, error) {
	var doc map[string]any
	if err := json.Unmarshal(merged, &doc); err != nil {
		return merged, false, nil // 形状问题由紧随其后的强类型解与 Validate 报出来
	}
	tgt, ok := doc["target"].(map[string]any)
	if !ok {
		return merged, false, nil
	}
	// 两个键都是**已下线的选择面**（底座构建期固定 + agent 面撤销），删法相同故同一处自愈。
	dropped := false
	for _, k := range []string{"runtime_hint", "agent_id"} {
		if _, has := tgt[k]; !has {
			continue
		}
		delete(tgt, k)
		dropped = true
	}
	if !dropped {
		return merged, false, nil
	}
	if err := df.Set("target", tgt); err != nil {
		return merged, false, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("automation %s 的 target 无法序列化：%v", id, err), err)
	}
	next, err := df.Marshal()
	if err != nil {
		return merged, false, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("序列化 automation %s 失败：%v", id, err), err)
	}
	return next, true, nil
}
