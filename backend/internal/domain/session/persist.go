package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/object/objid"
	"github.com/morehao/ark-mate/internal/object/objusage"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// persistSession 把会话记录同步写库（best-effort：失败告警不阻断会话流程）。
func (pl *Plane) persistSession(rec *SessionRec) {
	if pl.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := pl.st.UpsertSession(ctx, model.SessionEntity{
		ID:                 rec.ID,
		Cwd:                rec.Cwd,
		WorkspaceID:        rec.WorkspaceID,
		WorkspaceTemporary: rec.WorkspaceTemporary,
		ModelID:            rec.ModelID,
		RuntimeID:          rec.RuntimeID,
		RuntimeSessionID:   rec.RuntimeSessionID,
		Title:              rec.Title,
		Pinned:             rec.Pinned,
		Archived:           rec.Archived,
		Status:             rec.Status,
		Origin:             rec.Origin,
		CapabilityJSON:     rec.CapabilityJSON,
		// **钉住的指纹**（代际）：普通持久化写的就是记录里的那个值（会话一旦建立就不再重新解析），
		// 所以这里不会"每次落库都把指纹刷成当前值"；只有 M3-6 的轮次边界迁移会显式改写它。
		CapabilityFingerprint: rec.Fingerprint,
		// **钉住的原生权限模式**（v29）：与指纹同一条纪律——写的是记录里的那个值，不重算。
		NativeMode: rec.NativeMode,
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
	})
	if err != nil {
		glog.Warnw(context.Background(), "store: upsert session failed", "session", rec.ID, "err", err)
	}
}

// PersistMemoryAdd 写一条权限记忆（permission_memories 表；同键覆盖，since 为授予时刻）。
func (pl *Plane) PersistMemoryAdd(sessionID, title, kind, decision, scope, grantInputDigest string, since time.Time) {
	if pl.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pl.st.AddMemory(ctx, model.PermissionMemoryEntity{
		ID:        objid.New(objid.PermissionMemory),
		SessionID: sessionID, Title: title, Kind: kind, Decision: decision,
		Scope: scope, GrantInputDigest: grantInputDigest, CreatedAt: since,
	}); err != nil {
		glog.Warnw(context.Background(), "store: add permission memory failed", "session", sessionID, "title", title, "err", err)
	}
}

// PersistMemoryDel 撤销一条权限记忆。
func (pl *Plane) PersistMemoryDel(sessionID, title, kind string) {
	if pl.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := pl.st.DeleteMemory(ctx, sessionID, title, kind); err != nil {
		glog.Warnw(context.Background(), "store: delete permission memory failed", "session", sessionID, "title", title, "err", err)
	}
}

// PersistMemoryDelAll 一键撤销整会话权限记忆。
func (pl *Plane) PersistMemoryDelAll(sessionID string) {
	if pl.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pl.st.DeleteSessionMemories(ctx, sessionID); err != nil {
		glog.Warnw(context.Background(), "store: delete session memories failed", "session", sessionID, "err", err)
	}
}

// MemoryRows 读全库权限记忆（供 sqlite 启动恢复装载；无 store 时返回空）。
func (pl *Plane) MemoryRows(ctx context.Context) ([]model.PermissionMemoryEntity, error) {
	if pl.st == nil {
		return nil, nil
	}
	return pl.st.ListAllMemories(ctx)
}

// openRun 开一轮：把 run 行以 **running** 写进库（v22 起一轮在**开始**时就有行）。
//
// 与 `materializeRun` 的分工：本函数写"这一轮开始了"（first_seq = `run.started` 的位次、
// last_seq = -1），终态那次只 UPDATE。best-effort：失败仅告警——读模型缺一行不该阻断会话，
// 而 events 里那条 `run.started` 还在（`reconcileRunRows` 会把它补成行，J7）。
//
// `started_at` 取 **`run.started` 事件的落库时刻**（回读那一行），不是 `time.Now()`：本轮起点
// 的唯一事实源是那条事件，各写各的会让 `runs.started_at` 与事件时间分叉，而 `first_token_ms`
// 正是相对它算的。
//
// **回读到那条事件才开轮**（位次与 type 都对得上）：开轮行是"查询 `run.started` 得到的物化行"，
// 事件不在（append 失败退化、或位次来自总线而非库）时插一行等于给这一轮编一个不存在的起点——
// 终态收口会把**别的事件**当成它的窗口。宁可没有行（那条 WARN 就是信号）。
func (pl *Plane) OpenRun(sessionID, runID, text string, firstSeq int64) {
	if pl.st == nil || runID == "" || firstSeq < 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	evs, _, err := pl.st.EventsAfter(ctx, sessionID, firstSeq-1, 1)
	if err != nil || len(evs) != 1 || evs[0].Seq != firstSeq || evs[0].Type != BusRunStarted {
		glog.Warnw(context.Background(), "store: 开轮前回读 run.started 失败，跳过开轮行",
			"session", sessionID, "run", runID, "first_seq", firstSeq, "err", err)
		return
	}
	started := evs[0].CreatedAt
	r := model.RunEntity{
		ID: runID, SessionID: sessionID, UserText: text,
		Status: SessionStatusInProgress,
		// first_seq 是本轮 `run.started` 的位次（窗口起点从此由行给出，不再由"上一轮的末尾"推导）。
		FirstSeq: firstSeq,
		// last_seq = -1 = 还没收口：**0 是合法位次**（v21），拿它当"未知"会让进行中的轮看起来
		// 已经覆盖了会话第 0 条事件。
		LastSeq:   -1,
		StartedAt: started,
	}
	if err := pl.st.CreateRun(ctx, r); err != nil {
		glog.Warnw(context.Background(), "store: open run failed", "session", sessionID, "run", runID, "err", err)
	}
}

// ListRuns 列出某会话的轮次明细（runs 表，started_at 升序；失败轮重试/统计用）。
// 内存（无 store）模式下返回空列表（runs 是 sqlite 读模型）。
func (pl *Plane) ListRuns(ctx context.Context, id string) ([]dtosession.RunResp, error) {
	if pl.st == nil {
		return []dtosession.RunResp{}, nil
	}
	rows, err := pl.st.ListRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]dtosession.RunResp, 0, len(rows))
	for _, r := range rows {
		resp := dtosession.RunResp{
			ID: r.ID, UserText: r.UserText, Status: r.Status,
			ErrorCode: r.ErrorCode, StopReason: r.StopReason,
			FirstSeq: r.FirstSeq, LastSeq: r.LastSeq, FirstTokenMS: r.FirstTokenMS,
			StartedAt: r.StartedAt.UTC().Format(time.RFC3339Nano),
		}
		if !r.EndedAt.IsZero() {
			resp.EndedAt = r.EndedAt.UTC().Format(time.RFC3339Nano)
		}
		if r.UsageJSON != "" {
			var u objusage.UsageInfo
			if err := json.Unmarshal([]byte(r.UsageJSON), &u); err == nil && u.Reported() {
				resp.Usage = &dtosession.RunUsageResp{
					Used: u.Used, Size: u.Size, CostAmount: u.CostAmount, CostCurrency: u.CostCurrency,
				}
			}
		}
		out = append(out, resp)
	}
	return out, nil
}

// RestoreFromStore 启动恢复（sqlite 模式）：从 DB 载入会话记录并把全部已落库事件
// 注入 eventbus（seq 原样 PublishAt）——重启后 bus 与 DB 共用**同一套会话内位次**，
// WS/断线续传游标不漂移，旧会话仍可回放。返回恢复的会话数与注入事件数。
//
// 注入顺序（v21）：按 `(session_id, seq)` 复合游标逐页取（`EventsAllBySession`），
// 于是**每个会话各自升序**。v21 之前这里按全局 seq 升序——那个顺序的意义是"保证全局单号序
// 不被 PublishAt 改写"，而 v21 起 `nextSeq` 是每会话一张表，跨会话的相对顺序不再影响任何号；
// 仍要保证的是"组内升序"（复合同序恰好同时给了分组与组内有序）。
func (pl *Plane) Restore(ctx context.Context) (int, int, error) {
	if pl.st == nil {
		return 0, 0, nil
	}
	list, err := pl.st.ListSessions(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("restore: list sessions: %w", err)
	}
	pl.SetRestoring(true)
	// `SetRestoring` **自带本域锁**：外面再包一层 `pl.mu` 就是自死锁（同一 goroutine 二次 Lock）。
	// 这不是假设——本批实现时正是这么写了一次，症状是全量测试挂死在这个 defer 上。
	defer pl.SetRestoring(false)

	// openElicitations 各会话「问了但没答」的挂起提问 id（按事件顺序增删；见下面的注入循环）。
	openElicitations := map[string][]string{}
	sessions, injected := 0, 0
	interrupted := 0
	interruptedIDs := make([]string, 0, 8)
	ver := map[string]int64{}
	for _, sess := range list {
		rec := &SessionRec{
			ID: sess.ID, Cwd: sess.Cwd,
			WorkspaceID:        sess.WorkspaceID,
			WorkspaceTemporary: sess.WorkspaceTemporary,
			ModelID:            sess.ModelID, RuntimeID: runtime.NormalizeID(sess.RuntimeID),
			RuntimeSessionID: sess.RuntimeSessionID,
			Title:            sess.Title, Pinned: sess.Pinned, Archived: sess.Archived,
			Status: sess.Status, Origin: sess.Origin, CapabilityJSON: sess.CapabilityJSON,
			// **恢复会话钉住的指纹**：漏了这一段，重启后 rec.Fingerprint 就是空的 →
			// 代际键退回"未声明"形态，会话悄悄换到默认能力集（正是 M3 要防的漂移）。
			Fingerprint: sess.CapabilityFingerprint,
			// **恢复钉住的权限模式**：漏了这段，重启后模式变空 → 该会话的暖进程键变了一维，
			// 它会**悄悄换到一个"不注入原生权限"的进程上**（沙箱边界跟着放宽/收紧），
			// 而界面上没有任何一行会说这件事变过。
			NativeMode: sess.NativeMode,
			CreatedAt:  sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
		}
		pl.RegisterSession(rec) // restoring=true → 不重复回写 DB
		sessions++
		// interrupted 收敛：残留 in_progress（崩溃/重启中断，无终态）→ 可操作态（列表可停止/重发）。
		// 事件保持原样：续聊时 materializeRun 以「上一 run 之后」窗口重建本轮，不受影响。
		if rec.Status == SessionStatusInProgress {
			pl.MarkSession(rec.ID, SessionStatusInterrupted)
			interrupted++
			interruptedIDs = append(interruptedIDs, rec.ID)
		}
	}

	// 事件按 `(session_id, seq)` 升序注入（会话内升序是唯一有意义的顺序；见函数注释）。
	if pl.bus != nil {
		var (
			afterSession string
			afterSeq     int64 = -1
		)
		for {
			evs, more, err := pl.st.EventsAllBySession(ctx, afterSession, afterSeq, 2000)
			if err != nil {
				glog.Warnw(ctx, "restore: read events failed", "err", err)
				break
			}
			for _, ev := range evs {
				ver[ev.SessionID]++
				// 挂起提问的**未决**账：重启之后它们一条都活不下来（管理面在内存里），
				// 但历史里那条 `elicitation_request` 还在——前端回放就会渲染出一张**永远
				// 回答不了**的卡片。这里记下 id，注入结束后逐条补 `resolved{cancel,restart}`。
				switch ev.Type {
				case BusElicitationRequest:
					if m, ok := DecodePayload(ev.PayloadJSON).(map[string]any); ok {
						if id, _ := m["elicitation_id"].(string); id != "" {
							openElicitations[ev.SessionID] = append(openElicitations[ev.SessionID], id)
						}
					}
				case BusElicitationResolved:
					if m, ok := DecodePayload(ev.PayloadJSON).(map[string]any); ok {
						if id, _ := m["elicitation_id"].(string); id != "" {
							openElicitations[ev.SessionID] = removeString(openElicitations[ev.SessionID], id)
						}
					}
				}
				pl.bus.PublishAt(ev.SessionID, ev.Seq, ev.ID, ev.Type, ver[ev.SessionID], DecodePayload(ev.PayloadJSON))
				// `run.status` 的**投影**在同一次回放里重建（不再为它多扫一遍库）：重启之后
				// `GET /sessions/:id` 的 `run` 段仍然说得出「上一轮停在哪一步」。
				if ev.Type == BusRunStatus {
					if m, ok := DecodePayload(ev.PayloadJSON).(map[string]any); ok {
						pl.recordRunStatus(ev.SessionID, ev.Seq, m)
					}
				}
				injected++
				afterSession, afterSeq = ev.SessionID, ev.Seq
			}
			if !more {
				break
			}
		}
	}
	// 崩溃残轮的**收口**（v27 + S7 / ADR-8 的后半）：把"有 `run.started` 而无终态"的轮次补一条
	// `run.status(interrupted)`——`runs` 行因此不再永久停在 `running`。
	//
	// 必须放在**事件注入之后**：这些收口事件是新铸的（落库拿到最新位次），若先发再注入，bus 的
	// `nextSeq` 已经被它推到前面，随后逐条 `PublishAt` 回放的历史事件会被**逐个顶号**——位次是
	// 续传游标，顶号等于把客户端的 since_seq 全部错开（而表面上一切正常）。
	// 挂起提问的**作废**（ADR-11 / P1-d）：同上，必须排在事件注入之后（理由逐字相同）。
	//
	// **比参照产品弱一档（如实写）**：dsh 的 pending 跨 Host 重启仍可回答（回复作为新一轮进入）；
	// 本仓选择「重启即作废」。理由是 elicitation 的归属（D1）只能**推断**（wire /
	// inferred_inflight / ambiguous），跨重启保留 pending 会把一个推断出来的归属当成权威。
	pl.cancelDanglingElicitations(openElicitations)
	pl.closeInterruptedRuns(ctx, interruptedIDs)

	// 读模型重建（v21）：迁移把 `meta.read_models_rebuild` 置成刚跑过的版本号，表示"位次口径换了、
	// 旧读模型里的窗口列已经不可信"。此处消费它并清标记——**只有这一条路径**（见 model/enum.go）。
	pl.rebuildStaleReadModels(ctx)
	// 会话模型引用回填（v26）：`sessions.model_id` 从"底座的不透明值"改成"平台模型行 id"，
	// 存量只能在应用层换算（迁移里做不了：那只把标记置起来）。同一条恢复路径、同一条纪律。
	pl.BackfillSessionModelRefs(ctx)
	// §6：重启前排队未投递的输入一律作废——它们等的那一轮已随进程消失，自动重放等于替用户
	// 按下发送（且上下文已断）。保留行仅作审计（见 inputqueue.go）。
	pl.staleInputsOnRestore(ctx)
	glog.Infow(ctx, "store restored", "sessions", sessions, "events", injected)
	return sessions, injected, nil
}

// closeInterruptedRuns 给恢复出来的崩溃残轮补一条收口事件（ADR-8 的后半：**只在恢复路径**）。
//
// 判据是「这一轮有 `run.started`、行却还在 running」——即日志里没有它的终态。实时路径从不调本
// 函数：那边知道自己为什么停（主动拆除），发伪终态会污染历史、失败轮列表与重试入口
// （`Runner.executeRun` 的 `intentional` 分支保留原注释，那是 ADR-8 的**前半**）。
//
// 收口事实带 `reason:"restart"`：界面对它说的是「上一轮因进程重启中断，可重发」，**不**说
// 「失败」——那是底座的结论，平台只知道自己被杀了。
func (pl *Plane) closeInterruptedRuns(ctx context.Context, sessionIDs []string) {
	if pl.st == nil || len(sessionIDs) == 0 {
		return
	}
	closed := 0
	for _, id := range sessionIDs {
		// 判据用 `last_seq < 0`（= 还没有收口事件）而不是「status 是 running」：
		// 开轮行写的状态值是 `in_progress`（与 `sessions.status` 同一套词汇），而"这一轮收口了吗"
		// 在库里的事实是 `last_seq`——`closeRunsWithTerminal` 用的也是这一支。两处必须同一支判据，
		// 否则恢复收口的选取面与重建的选取面会悄悄分叉（一个认为有残轮、另一个认为没有）。
		rows, err := pl.st.ListRuns(ctx, id)
		if err != nil {
			glog.Warnw(ctx, "restore: 读 run 行失败（该会话不补收口）", "session", id, "err", err)
			continue
		}
		for _, r := range rows {
			if r.LastSeq >= 0 {
				continue
			}
			pl.PublishRunInterrupted(id, r.ID)
			closed++
		}
	}
	if closed > 0 {
		glog.Infow(ctx, "restore: 崩溃残轮已补收口", "runs", closed, "sessions", len(sessionIDs))
	}
}

// rebuildStaleReadModels 消费 `meta.read_models_rebuild`：有值 = 上一次迁移改动了事件位次口径，
// 全部会话的读模型（M1：messages；M2 起还有 runs）必须按新位次重建，然后清掉标记。
//
// best-effort：任何一步失败只 WARN（标记留着，下次启动重试——比"半个库重建了一半还清了标记"好）。
//
// 它与 `mate doctor --rebuild-read-models` 共用本平面的 `RebuildAllReadModels`：区别只在**触发条件**
// （这边看标记、那边是人工显式要求），而"重建什么"必须只有一份实现——两份的话，
// "doctor 重建出来的"与"启动重建出来的"就能不一样，而那是最难查的一类不一致（J7 的可重建性
// 要求"同一份 events 得到同一组读模型"，与谁触发无关）。
func (pl *Plane) rebuildStaleReadModels(ctx context.Context) {
	if pl.st == nil {
		return
	}
	ver, ok, err := pl.st.GetMeta(ctx, model.MetaKeyReadModelsRebuild)
	if err != nil {
		glog.Warnw(ctx, "read models: 读重建标记失败", "err", err)
		return
	}
	if !ok || ver == "" {
		return
	}
	// 循环在机制层（`mechanism/doctor`）：它是"遍历全部会话"这件事，与会话域无关；
	// "重建一个会话"也由本平面实现（`RebuildSessionReadModels`）——
	// 这正是"逃生门"消失的那一处：不再需要造一个 Service 来满足接口。
	total, failed, err := pl.RebuildAllReadModels(ctx)
	if err != nil {
		glog.Warnw(ctx, "read models: 列会话失败（重建跳过，标记保留）", "err", err)
		return
	}
	if failed > 0 {
		glog.Warnw(ctx, "read models: 重建未全部成功，标记保留待下次重试", "failed", failed, "total", total)
		return
	}
	if err := pl.st.DeleteMeta(ctx, model.MetaKeyReadModelsRebuild); err != nil {
		glog.Warnw(ctx, "read models: 清重建标记失败", "err", err)
		return
	}
	glog.Infow(ctx, "read models rebuilt", "migration", ver, "sessions", total)
}

// backfillSessionModelRefs 消费 `meta.sessions_model_ref_backfill`（v26 定案「辰」的存量回填）。
//
// 一次性、**幂等**、best-effort：
//
//	① 读标记；无值即返回（只有这一条路径消费它，同 G7 的纪律）；
//	② 对每个 runtime 建"该 runtime 的运行时值 → 平台模型行 id"的表（`modelValueFor` 派生，
//	   多命中按 fail-closed 不收录——与目录注解同一份口径）；
//	③ 存量 `model_id` 是**旧形态的不透明值**：唯一命中才写 `model_id = <行 id>`；
//	   零命中 / 多命中一律**置空串**并按类别计数（日志里给条数与样例）——
//	   空 = "没有模型引用"，与 `sessions.model_id` 的 `NOT NULL DEFAULT ''` 一致；
//	④ **已经是平台 id 的行直接跳过**：这是幂等的关键——否则"上一次跑到一半"的产物会在下一次
//	   被当成"旧形态的不透明值"而清掉（把成功的结果当成失败来处置）；
//	⑤ 全部成功才清标记；任何一步失败只 WARN、**标记留着**（下次启动重试）。
//	   **绝不在"算不出来"时打勾**：算不出来 = 下次还有机会，打勾 = 永久把一次失败固化成事实。
//
// 代价（如实写进升级文档，不是迁移的 bug）：模型**从未被平台声明过**的会话，升级后失去模型选择，
// 下一次发消息落到平台默认（或底座默认）。这是"可表示性收窄"的直接后果——平台只接受自己声明过的
// 模型，回填只是尽力把能对上的救回来。
func (pl *Plane) BackfillSessionModelRefs(ctx context.Context) {
	if pl.st == nil {
		return
	}
	ver, ok, err := pl.st.GetMeta(ctx, model.MetaKeySessionsModelRefBackfill)
	if err != nil {
		glog.Warnw(ctx, "session model refs: 读回填标记失败", "err", err)
		return
	}
	if !ok || ver == "" {
		return
	}
	sessions, err := pl.st.ListSessions(ctx, nil)
	if err != nil {
		glog.Warnw(ctx, "session model refs: 列会话失败（回填跳过，标记保留）", "err", err)
		return
	}

	// 按 runtime 惰性建表：`modelValueFor` 逐 runtime 形态不同，混在一起算会把 A 底座的值
	// 拿去比 B 底座的声明。
	byRuntime := map[string]map[string]string{}
	valueTable := func(runtimeID string) map[string]string {
		if t, done := byRuntime[runtimeID]; done {
			return t
		}
		t := pl.RuntimeValueToModelID(ctx, runtimeID)
		byRuntime[runtimeID] = t
		return t
	}

	migrated, cleared, skipped, failed := 0, 0, 0, 0
	clearedSample := []string{}
	for i := range sessions {
		rec := sessions[i]
		cur := strings.TrimSpace(rec.ModelID)
		if cur == "" {
			continue
		}
		if strings.HasPrefix(cur, objid.Model) && objid.IsPlatformID(cur) {
			skipped++ // 已经是平台 id（上一次跑到一半的产物）：幂等，不动它
			continue
		}
		runtimeID := runtime.NormalizeID(rec.RuntimeID)
		next := valueTable(runtimeID)[cur]
		if next == "" {
			cleared++
			if len(clearedSample) < 5 {
				clearedSample = append(clearedSample, rec.ID)
			}
		} else {
			migrated++
		}
		if err := pl.st.SetSessionModelID(ctx, rec.ID, next); err != nil {
			failed++
			glog.Warnw(ctx, "session model refs: 写回一条失败（标记保留，下次重试）",
				"session", rec.ID, "err", err)
		}
	}
	if failed > 0 {
		glog.Warnw(ctx, "session model refs: 回填未全部成功，标记保留待下次重试",
			"migration", ver, "failed", failed, "total", len(sessions))
		return
	}
	if err := pl.st.DeleteMeta(ctx, model.MetaKeySessionsModelRefBackfill); err != nil {
		glog.Warnw(ctx, "session model refs: 清回填标记失败", "err", err)
		return
	}
	// 清不掉的会话**按类别计数**（"失去了模型选择"这件事必须被看见，不能静默）。
	glog.Infow(ctx, "session model refs backfilled", "migration", ver, "sessions", len(sessions),
		"migrated", migrated, "cleared", cleared, "already_platform_id", skipped,
		"cleared_sample", clearedSample)
}

// runtimeValueToModelID 建"该 runtime 的运行时值 → 平台模型行 id"表（多命中按 fail-closed 不收录）。
//
// 与 `modelIDByValue`（目录注解）同口径：**派生值对派生值**，平台不解析底座词表。两处都必须
// fail-closed——"两条平台行派生出同一个值"时随便挑一条就是静默选错模型。
func (pl *Plane) RuntimeValueToModelID(ctx context.Context, runtimeID string) map[string]string {
	rows, err := pl.st.ListModelDefinitions(ctx)
	if err != nil {
		glog.Warnw(ctx, "session model refs: 读平台清单失败（该底座本次全部按零命中处置）",
			"runtime", runtimeID, "err", err)
		return nil
	}
	seen := make(map[string]string, len(rows))
	dup := map[string]bool{}
	for i := range rows {
		v, verr := pl.modelValueFor(runtimeID, rows[i].ProviderID, rows[i].Model)
		if verr != nil || v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			dup[v] = true
			continue
		}
		seen[v] = rows[i].ID
	}
	for v := range dup {
		delete(seen, v)
	}
	return seen
}

// cancelDanglingElicitations 重启恢复后把「问了但没答」的挂起提问逐条作废（`reason:restart`）。
//
// 为什么必须有这一条：挂起提问只活在进程内存里，重启后**一条都不在了**——但历史里那条
// `elicitation_request` 还在。前端回放会据此渲染一张卡片，而那张卡片背后已经没有任何挂起项：
// 用户填完点提交，服务端只能回「不存在或已过期」。**看起来能点，点下去才发现不行**——这正是
// 本仓最忌的那类静默。
//
// 不发事件的另一半后果更隐蔽：卡片一直挂着，用户会以为"它还在等我"。
func (pl *Plane) cancelDanglingElicitations(open map[string][]string) {
	if pl.bus == nil {
		return
	}
	for sessionID, ids := range open {
		for _, id := range ids {
			pl.Publish(BusElicitationResolved, sessionID, map[string]any{
				"session_id":     sessionID,
				"elicitation_id": id,
				"action":         "cancel",
				"reason":         ElicitationReasonRestart,
			})
		}
	}
}

// removeString 从切片里删掉一个值（只删第一处；不存在即原样返回）。
func removeString(list []string, v string) []string {
	for i, it := range list {
		if it == v {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}
