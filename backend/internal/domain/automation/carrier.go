package automation

import (
	"context"
	"encoding/json"
	"fmt"
	defn "github.com/morehao/ark-mate/internal/definition"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"path/filepath"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/automation"
	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/glog"
)

// ——— automation 定义的载体（M6-7） ———
//
// 与能力/agent 面同一个判据（`pl.st != nil`），但这里的载体分工更细，因为 automation 的载体里
// 住着**两种东西**：
//
//	memory 模式：`<AutomationDir>/<id>.json` —— 定义与运行态同文件（C9 纪律 + mtime 乐观锁）；
//	sqlite 模式：`automations` 表 —— 定义（body + revision）与运行态（last_*/recent_runs）**分列**。
//
// 分列带来两处**有意**的行为差异，都写在这里而不是留在代码里让人猜：
//
//  1. 运行态写回**不抬高定义 revision**（文件载体里它会抬高 mtime）→ 用户在调度器跑过一轮之后
//     再保存定义，不会被 `definition_changed` 无故挡下。唯一例外是 `state`：它**是定义键**
//     （刻意不在 `ReservedKeys` 里，用户要能把 needs_attention 改回 active），所以它走正文
//     patch、会前进一版——但那只在值真的变了时才发生（首次推进补一个 state，之后逐字节稳定）；
//  2. DB 载体在**建会话之前先认领**（`last_fired_at` 的 CAS，见 `claimAutomationOccurrence`）：
//     同一 occurrence 至多触发一次（at-most-once），而文件载体保持"跑完再写回"的历史语义
//     （崩溃窗口内可能重复跑一次）。这正是 Plan §7 P-12 明说要改的那条失败语义。

// Locator automation 定义在 DB 载体里的定位串（与能力面的 `db:skill/<id>` 同形）。
func Locator(id string) string { return "db:automation/" + id }

// Draft 一份未落盘的定义草稿（两种载体共用一个流程）。
type Draft struct {
	// ID 定义 id（= 文件名 / 行键）——由 openAutomationDraft 填，避免从定位串反推。
	ID string
	DF *capability.DefinitionFile
	// Locator 定位串（DB = `db:automation/<id>`，文件 = 定义文件路径）——错误文案与响应里都用它。
	Locator string
	// Existed 打开时载体里是否已有这一条（`automation_exists` / `automation_not_found` 的判据）。
	Existed bool
	// File 非 nil = 文件载体（落盘走它的 mtime 乐观锁）；nil = DB 载体（走行 revision CAS）。
	File *definition.DefinitionDraft
	// Rev DB 载体的行 revision（CAS 期望值）。
	Rev int64
}

// openAutomationDraft 打开定义草稿（不存在 → 新建草稿；**不落盘**）。
func (pl *Plane) openAutomationDraft(ctx context.Context, id string) (*Draft, error) {
	if pl.st == nil {
		dir, err := pl.automationDirFor("automation 的增删改")
		if err != nil {
			return nil, err
		}
		d, err := definition.OpenDefinition(dir, "automation", id, nil)
		if err != nil {
			return nil, err
		}
		return &Draft{ID: id, DF: d.Doc(), Locator: d.Path(), Existed: d.Existed(), File: d}, nil
	}
	locator := Locator(id)
	row, found, err := pl.st.GetAutomationDefinition(ctx, id)
	if err != nil {
		return nil, acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("读取 automation 定义 %s 失败：%v", locator, err), err)
	}
	if !found {
		// DB 行的新建路径用**内存文档**（`NewDefinitionDoc`）：行没有文件路径，
		// 文件那三条纪律（staging/原子 rename/权限沿用）由行 revision 的事务承担。
		return &Draft{ID: id, DF: capability.NewDefinitionDoc(locator), Locator: locator}, nil
	}
	df, err := capability.DecodeDefinitionDoc(locator, []byte(row.Body))
	if err != nil {
		// 库里的正文不是 JSON 对象 → 与文件载体同解：**先修它**（40001，可操作），而不是
		// "写入失败"（用户会去重试一个永远不会成功的东西）。
		return nil, acl.NewError(acl.ClassRequest, "definition_invalid",
			fmt.Sprintf("automation 定义 %s 不是合法 JSON 对象，先修好或删掉它再写：%v", locator, err), err)
	}
	return &Draft{ID: id, DF: df, Locator: locator, Existed: true, Rev: row.Revision}, nil
}

// commitAutomationDraft 提交草稿（DB：行 CAS + 审计；文件：mtime 乐观锁）。
func (pl *Plane) commitAutomationDraft(ctx context.Context, d *Draft, create bool) error {
	if d.File != nil {
		return definition.SaveDefinition(d.File)
	}
	body, err := d.DF.Marshal()
	if err != nil {
		return acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("序列化 automation 定义 %s 失败：%v", d.Locator, err), err)
	}
	expected := d.Rev
	if create {
		// 新建走"仅当不存在"的 CAS：并发新建时后到的那次返回 `automation_exists`（与文件载体
		// 的 O_EXCL 语义同解），而不是覆盖别人刚建的定义。
		expected = 0
	}
	ok, err := pl.st.UpsertAutomationDefinition(ctx, d.ID, string(body), expected, definition.DefinitionWriteActor)
	if err != nil {
		return acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("写入 automation 定义 %s 失败：%v", d.Locator, err), err)
	}
	if !ok {
		if create {
			return acl.NewError(acl.ClassLimit, "automation_exists",
				fmt.Sprintf("automation %s 已存在（换一个 id，或用 PUT 覆盖它）", d.ID), nil)
		}
		return defn.DefinitionChangedErr(d.Locator)
	}
	return nil
}

// deleteAutomationDefinition 删掉载体里的定义；返回是否真的删掉了（幂等删除）。
func (pl *Plane) deleteAutomationDefinition(ctx context.Context, id string) (bool, error) {
	if pl.st == nil {
		dir, err := pl.automationDirFor("automation 的删除")
		if err != nil {
			return false, err
		}
		return definition.RemoveDefinitionPermanently(dir, "automation", id)
	}
	row, found, err := pl.st.GetAutomationDefinition(ctx, id)
	if err != nil {
		return false, acl.NewError(acl.ClassService, "definition_delete_failed",
			fmt.Sprintf("读取 automation 定义 %s 失败：%v", Locator(id), err), err)
	}
	if !found {
		return false, nil
	}
	ok, err := pl.st.DeleteAutomationDefinition(ctx, id, row.Revision, definition.DefinitionWriteActor)
	if err != nil {
		return false, acl.NewError(acl.ClassService, "definition_delete_failed",
			fmt.Sprintf("删除 automation 定义 %s 失败：%v", Locator(id), err), err)
	}
	if !ok {
		// 删掉别人刚改过的那一版比覆盖更不可逆 → 一律报冲突让用户刷新重试。
		return false, defn.DefinitionChangedErr(Locator(id))
	}
	return true, nil
}

// ——— 定义 ⇄ 行/文件的形态转换 ———

// automationRuntimeOf 把强类型定义的运行态抽成列结构（**不含** `state`：它属于定义，见 P-10）。
func automationRuntimeOf(def automation.Automation) model.AutomationRuntimeState {
	return model.AutomationRuntimeState{
		LastFiredAt:         LastFiredText(def.LastFiredAt),
		LastStatus:          def.LastStatus,
		LastReason:          def.LastReason,
		LastSessionID:       def.LastSessionID,
		ConsecutiveFailures: int64(def.ConsecutiveFailures),
	}
}

// LastFiredText 认领游标的文本形态（**唯一格式**：UTC + RFC3339Nano）。
//
// 它是 CAS 的比较值，两处写法必须逐字节一致——所以只有这一个函数负责格式化。
func LastFiredText(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// automationRunsJSON 把运行史编成列的文本（空切片 → `[]`，不留 NULL/空串的第二种表示）。
func RunsJSON(runs []automation.RunRecord) string {
	if runs == nil {
		runs = []automation.RunRecord{}
	}
	b, err := json.Marshal(runs)
	if err != nil {
		// RunRecord 全是基本类型，编不出来只可能是内存里的脏值；如实报错而不是写半个 JSON。
		glog.Warnw(context.Background(), "automation: 运行史序列化失败，本列不更新", "err", err)
		return ""
	}
	return string(b)
}

// automationDefFromRow 把一行还原成"完整定义"（body + 运行态列），与文件载体的解析入口同形。
func automationDefFromRow(row *model.AutomationEntity) (automation.Automation, error) {
	var def automation.Automation
	if err := json.Unmarshal([]byte(row.Body), &def); err != nil {
		return def, fmt.Errorf("不是合法 JSON：%v", err)
	}
	// id 的权威是**行键**：正文里的 id 只可能是历史遗留，由 buildAutomationRec 判不一致。
	if def.ID == "" {
		def.ID = row.ID
	}
	if row.LastFiredAt != "" {
		t := model.ParseTime(row.LastFiredAt)
		if t.IsZero() {
			return def, fmt.Errorf("last_fired_at %q 不是 RFC3339 时间", row.LastFiredAt)
		}
		def.LastFiredAt = &t
	}
	def.LastStatus = row.LastStatus
	def.LastReason = row.LastReason
	def.LastSessionID = row.LastSessionID
	def.ConsecutiveFailures = int(row.ConsecutiveFailures)
	if row.RecentRuns != "" && row.RecentRuns != "[]" {
		if err := json.Unmarshal([]byte(row.RecentRuns), &def.RecentRuns); err != nil {
			return def, fmt.Errorf("recent_runs 不是合法 JSON：%v", err)
		}
	}
	return def, nil
}

// buildAutomationRec 从完整定义算出调度态记录（文件装载 / DB 装载 / 运行态写回三处共用）。
//
// 坏定义**留在注册表里带 loadErr**（`keyID` 先记进 id，列表才指得出是哪一条坏了）。
func buildAutomationRec(def automation.Automation, keyID string, now time.Time) *Rec {
	rec := &Rec{Def: automation.Automation{ID: keyID}}
	if def.ID != "" && def.ID != keyID {
		rec.LoadErr = fmt.Sprintf("定义里的 id %q 与载体键 %q 不一致", def.ID, keyID)
		return rec
	}
	def.ID = keyID
	def.Schedule.Normalize()
	if err := def.Validate(); err != nil {
		rec.LoadErr = errText(err)
		return rec
	}
	rec.Def = def
	rec.Def.State = automation.NormalizeState(def.State)
	rec.ApplySchedule(now)
	return rec
}

// loadAutomationRow 从一行算出记录（JSON 坏 → 带 loadErr 的记录）。
func loadAutomationRow(row *model.AutomationEntity, now time.Time) *Rec {
	def, err := automationDefFromRow(row)
	if err != nil {
		return &Rec{Def: automation.Automation{ID: row.ID}, LoadErr: err.Error()}
	}
	return buildAutomationRec(def, row.ID, now)
}

// reloadAutomationFromStore 用库里的那一行刷新内存记录（运行态写回后调用）。
//
// 与文件载体"写回后重新装载这一条"同源：**内存 = 载体**是定义，逐键 merge 迟早漏一个键
// （历史上 `recent_runs` 就这么漏过一次）。重载失败时保留旧内存态并告警——绝不用一条读坏了
// 的记录覆盖调度态。
func (pl *Plane) reloadAutomationFromStore(ctx context.Context, id string) {
	row, found, err := pl.st.GetAutomationDefinition(ctx, id)
	if err != nil || !found {
		glog.Warnw(ctx, "automation 写回后重载失败，保留原内存态", "automation", id, "err", err, "found", found)
		return
	}
	rec := loadAutomationRow(row, time.Now())
	if rec.LoadErr != "" {
		glog.Warnw(ctx, "automation 写回后重载失败，保留原内存态", "automation", id, "err", rec.LoadErr)
		return
	}
	pl.mu.Lock()
	if pl.recs == nil {
		pl.recs = map[string]*Rec{}
	}
	pl.recs[id] = rec
	pl.mu.Unlock()
}

// ——— 运行态写回与认领（P-12 的落点） ———

// saveAutomationOverlay 运行态写回（`last_fired_at` / `last_status` / `state` / …）。
//
// **平台自己调**，不是用户写面。`overlay` 的键按 `automation.IsReservedKey` 分成两半：
// 保留键 → 运行态列；其余（目前只有 `state`）→ 定义正文的 map-patch（未知键与其它用户字段
// 原样保留）。`expectedLastFired` 是认领 CAS 判据（空串 = "从未触发"）。
func (pl *Plane) SaveOverlay(ctx context.Context, id string, overlay map[string]any, expectedLastFired string) error {
	if pl.st == nil {
		dir, err := pl.automationDirFor("automation 运行态写回")
		if err != nil {
			return err
		}
		if err := definition.WriteDefinition(dir, "automation", id, overlay); err != nil {
			return err
		}
		rec := loadAutomationFile(filepath.Join(dir, id+".json"), id, time.Now())
		if rec.LoadErr != "" {
			glog.Warnw(ctx, "automation 写回后重载失败，保留原内存态", "automation", id, "err", rec.LoadErr)
			return nil
		}
		pl.mu.Lock()
		pl.recs[id] = rec
		pl.mu.Unlock()
		return nil
	}

	row, found, err := pl.st.GetAutomationDefinition(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return notFoundAutomation(id)
	}
	rt := model.AutomationRuntimeState{
		LastFiredAt:         stringOf(overlay, "last_fired_at", row.LastFiredAt),
		LastStatus:          stringOf(overlay, "last_status", row.LastStatus),
		LastReason:          stringOf(overlay, "last_reason", row.LastReason),
		LastSessionID:       stringOf(overlay, "last_session_id", row.LastSessionID),
		ConsecutiveFailures: int64Of(overlay, "consecutive_failures", row.ConsecutiveFailures),
	}
	if v, ok := overlay["recent_runs"]; ok {
		rt.RecentRuns = runsJSONOf(v)
	}
	// 非保留键（`state`）走正文 patch；正文没变就不抬高 revision（"什么都没改却换了版本号"
	// 会让用户的保存请求无故撞 40901）。
	body, rev := "", int64(0)
	if keys := nonReservedKeys(overlay); len(keys) > 0 {
		df, err := capability.DecodeDefinitionDoc(Locator(id), []byte(row.Body))
		if err != nil {
			return err
		}
		for _, k := range keys {
			if overlay[k] == nil {
				df.Delete(k)
				continue
			}
			if err := df.Set(k, overlay[k]); err != nil {
				return err
			}
		}
		next, err := df.Marshal()
		if err != nil {
			return err
		}
		if string(next) != row.Body {
			body, rev = string(next), row.Revision
		}
	}
	ok, err := pl.st.SaveAutomationOverlay(ctx, id, model.AutomationOverlay{
		Body: body, ExpectedRev: rev, ExpectedLastFired: expectedLastFired, Runtime: rt,
	})
	if err != nil {
		return err
	}
	if !ok {
		// 两个判据之一没过：认领被别人推进（本 tick 的结论已过期）或用户改了定义。
		return acl.NewError(acl.ClassLimit, "definition_changed",
			fmt.Sprintf("automation %s 在本次运行期间被改动，运行态未写回（下一次触发会带上它）", id), nil)
	}
	pl.reloadAutomationFromStore(ctx, id)
	return nil
}

// claimAutomationOccurrence 认领一个 occurrence（Plan §7 P-12）：把 `last_fired_at` 推到
// `occurrence`，**仅当它仍等于内存里的旧值**。返回 false = 别人先认领了（不重复触发）。
//
// 认领成功后**立刻**把内存游标刷新到库里那一版：否则下一次 tick 还会拿旧游标判定，
// 于是"认领成功但记录失败"会变成重复触发——这正是这条设计要挡住的形态。
func (pl *Plane) claimAutomationOccurrence(ctx context.Context, rec Rec, occurrence time.Time) (bool, error) {
	expected := LastFiredText(rec.Def.LastFiredAt)
	claimedFor := occurrence.UTC().Format(time.RFC3339Nano)
	ok, err := pl.st.ClaimAutomationOccurrence(ctx, rec.Def.ID, expected, claimedFor)
	if err != nil || !ok {
		return ok, err
	}
	pl.reloadAutomationFromStore(ctx, rec.Def.ID)
	return true, nil
}

// ——— overlay map 的小工具（值一律来自平台自己，类型不符只可能是内部 bug） ———

func stringOf(m map[string]any, key, fallback string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return fallback
}

func int64Of(m map[string]any, key string, fallback int64) int64 {
	switch v := m[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return fallback
}

// runsJSONOf 运行史 → 列文本；类型不符（内部 bug）→ 空串 = 不更新本列，而不是写坏它。
func runsJSONOf(v any) string {
	switch t := v.(type) {
	case []automation.RunRecord:
		return RunsJSON(t)
	case string:
		return t
	}
	return ""
}

// nonReservedKeys overlay 里的非保留键（升序：正文 patch 的产物可复现）。
func nonReservedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if !automation.IsReservedKey(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
