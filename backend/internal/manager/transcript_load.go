package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// loadTranscripts 在 daemon 启动（SetTranscriptDir 之后）回读 transcript 目录，重建会话列表与
// 元数据（标题/状态/时间），并把历史事件注入 eventbus——从而把「M1 内存态重启即失」放宽为
// 「transcript 落盘 → 重启重放」，让管理面在重启后仍有会话可见、旧会话可经 WS/history 回看。
// 这是轻量的「会话重启存活」，不引入 DB（落库属 M2）。加载为尽力而为：坏文件告警跳过，不阻断启动。
//
// **v17（transcript v2）**：事件行自带 `id`（身份）与 `seq`（位次），装载面**原样恢复两者**
// （`PublishAt`），不再按行号重铸 `seq`。于是"同一个事件在两次启动里位次不同"这件事被消掉，
// 客户端书签里的 `since_seq` 跨重启仍然指向同一条事件。
//
// **v21**：`seq` 是**会话内**位次，所以恢复注入只需要"同一会话内升序"（见下方排序），
// 跨会话的相对顺序不再影响任何号。旧文件里那一列是按旧口径（全局号）写的，装载面照原样恢复
// ——要把它拉直就走 `mate migrate`（导入路径按行序重排位次）。
func (s *Service) loadTranscripts() {
	if s.tx == nil || s.tx.dir == "" {
		return
	}
	files, err := filepath.Glob(filepath.Join(s.tx.dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return
	}
	sort.Strings(files)
	// 先把全部文件读进内存、再按 `(会话, seq)` **分组有序**注入（理由见下）。
	var pending []pendingTranscriptEvent
	loaded := 0
	for _, path := range files {
		rec := loadTranscriptFile(path)
		if rec == nil {
			continue
		}
		s.registerSession(rec.session)
		loaded++
		for _, line := range rec.lines {
			pending = append(pending, pendingTranscriptEvent{sessionID: rec.session.ID, line: line})
		}
	}
	// 按 `(会话 id, seq)` 升序注入（**不是**按文件名、也不是按逐文件顺序）：
	//   · 文件名字典序与会话的事件位次无关。逐文件注入时，**同一个会话**的若干文件之间
	//     可能乱序（同一会话理论上只有一份文件，但外部同步/复制会造出第二份），
	//     乱序送进 `PublishAt` 就会撞"取号回落"——位次被**改写**，于是"恢复出来的 seq"
	//     与"transcript 里写的 seq"又不一致了；
	//   · v21 起位次只在会话内可比，所以需要的是"**组内**升序"——按 `(会话, seq)` 排
	//     同时给了分组与组内有序，而且比 v21 之前的"全局归并"更弱、更好满足。
	// 同一个 `(会话, seq)` 只可能来自两份互相冲突的文件，用 sort.SliceStable + 文件名序
	// 定型，保证可复现。
	if s.bus != nil {
		sort.SliceStable(pending, func(i, j int) bool {
			if pending[i].sessionID != pending[j].sessionID {
				return pending[i].sessionID < pending[j].sessionID
			}
			return pending[i].line.seq < pending[j].line.seq
		})
		for _, p := range pending {
			s.bus.PublishAt(p.sessionID, p.line.seq, p.line.id, p.line.typ, p.line.version, p.line.payload)
		}
	}
	if n := legacyTranscriptSkips.Load(); n > 0 {
		// 汇总一条：逐文件的 WARN 会被日志轮转冲掉，而"这次启动被丢掉了几个会话"是
		// 排障时要看的那一眼（处置命令在逐文件 WARN 里）。
		glog.Warnw(context.Background(),
			"transcripts: 有旧形状会话 id 的文件被跳过（平台会话 id 只由 objid 铸：ses_<26 位 ULID>）",
			"skipped", n, "scanned", len(files), "dispose", legacyTranscriptDisposeHint)
	}
	if n := legacySchemaSkips.Load(); n > 0 {
		glog.Warnw(context.Background(),
			"transcripts: 有 v1 格式（事件行没有 id/seq 身份）的文件被跳过——v17 起事件身份随数据落盘，v1 的 seq 只是文件内位次，回读会把它当成会话水位",
			"skipped", n, "scanned", len(files), "dispose", legacyTranscriptDisposeHint)
	}
	glog.Debugw(context.Background(), "transcripts loaded", "loaded", loaded, "scanned", len(files), "events", len(pending))
}

// pendingTranscriptEvent 是"待注入 bus 的一条事件"（会话 id + 文件内读出的原语）。
//
// 存在的理由是把"读文件"与"注入 bus"分成两步：注入必须按**会话分组、组内按 seq 有序**
// （见 loadTranscripts）。
type pendingTranscriptEvent struct {
	sessionID string
	line      transcriptLine
}

// legacyTranscriptDisposeHint 旧 transcript 的**显式**处置命令。
//
// 刻意不做自动删除（ADR-5）：transcript 是 memory 模式的事实源，自动删是不可逆的；而
// 这条提示出现在逐文件的 WARN 里，刚好在用户看到"会话不见了"的那一刻。
const legacyTranscriptDisposeHint = "mate transcripts prune --older-than 1s"

// legacyTranscriptSkips 本次进程启动时因"会话 id 不是平台形状"被跳过的 transcript 文件数。
var legacyTranscriptSkips atomic.Int64

// LegacyTranscriptSkips 返回被跳过的旧形状 transcript 数（诊断与测试用）。
func LegacyTranscriptSkips() int64 { return legacyTranscriptSkips.Load() }

// legacySchemaSkips 本次进程启动时因"v1 文件格式"被跳过的 transcript 文件数（v17 起）。
var legacySchemaSkips atomic.Int64

// LegacySchemaSkips 返回被跳过的 v1 格式 transcript 数（诊断与测试用）。
func LegacySchemaSkips() int64 { return legacySchemaSkips.Load() }

// transcriptLine 是回读时保留的事件原语（**身份 + 位次 + 聚合版本 + 载荷**），用于注入 bus。
//
// `id`/`seq` 都是文件里写着的值，装载面一个都不重铸（v17）：`version` 仍按**该文件内的行序**
// 推导（重启稳定，且它只是聚合版本、不跨会话比较）。
//
// v21 起 `seq` 是**会话内**位次（0 起）。旧文件（v17–v20 写的）里那一列是全局号——装载面
// 仍然原样恢复它（file 是 memory 模式的事实源，改它等于改写用户手里的历史），于是那些会话
// 的位次会**带着旧口径的洞**；新事件接着最大号往后排。要把它拉直就走 `mate migrate` 导进
// SQLite（导入路径按行序重排位次），这与 v15「旧形状 id 不迁移」是同一条取舍。
type transcriptLine struct {
	id      string
	seq     int64
	typ     string
	version int64
	payload map[string]any
}

// transcriptFile 是回读一个 transcript 文件的结果。
type transcriptFile struct {
	session *SessionRec
	lines   []transcriptLine
}

// loadTranscriptFile 解析一个 JSONL transcript 文件（首行 header + 事件行），
// 返回会话记录与注入用事件；坏文件/非本仓库 schema 返回 nil。
func loadTranscriptFile(path string) *transcriptFile {
	f, err := os.Open(path)
	if err != nil {
		glog.Warnw(context.Background(), "transcript: load open failed", "path", path, "err", err)
		return nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20) // 允许大行（工具输出可能很长）

	var hdr *wireTranscriptHeader
	// sessionID 是**已过形状校验**的平台会话 id（v15 起只认 `ses_<26 位 ULID>`）。
	// 单独一个变量是为了让"未经校验就把 header 的 id 当平台 id"在类型上不可能发生：
	// `hdr.SessionID` 只在校验那一处被读。
	var sessionID string
	lines := make([]transcriptLine, 0, 64)
	var lastType string
	var lastTS string
	var firstUserText string
	var titleSet bool
	lineNo := 0

	for sc.Scan() {
		line := sc.Bytes()
		lineNo++
		if len(line) == 0 {
			continue
		}
		if hdr == nil {
			// 首行期望是 header（schema/version 存在）。
			var h wireTranscriptHeader
			if err := json.Unmarshal(line, &h); err == nil && h.Schema == wireTranscriptSchema && h.SessionID != "" {
				// v15（Q10，2026-10-11）：旧数据不要 —— 旧 transcript 的 header 里是裸 uuid，
				// **跳过该文件 + WARN**，既不做确定性派生（`FromLegacy`）也不放行直通。
				//
				// 放行的后果是静默的：数据面会出现 `ses_…` 与裸 uuid 两种会话 id，
				// "平台 id 形状单一"这条不变式在数据上被击穿，而代码侧的守卫看不见数据。
				// 派生的后果是"旧数据其实还在用"，与用户的决定直接矛盾。
				if !objid.IsPlatformID(h.SessionID) {
					legacyTranscriptSkips.Add(1)
					glog.Warnw(context.Background(),
						"transcript: 跳过旧形状会话 id（v15 起平台会话 id 形如 ses_<26 位 ULID>，旧数据不迁移）",
						"path", path, "session_id", h.SessionID, "dispose", legacyTranscriptDisposeHint)
					return nil
				}
				sessionID = h.SessionID
				hdr = &h
				continue
			}
			// v1 格式：schema 名是 v17 之前那一版 → 跳过整份 + WARN（**不能**回读：v1 的 seq
			// 只是文件内位次，而装载面现在会把它当全局水位注入 bus，两套语义混起来就是
			// "位次看起来对、跨重启却对不上"）。
			if err := json.Unmarshal(line, &h); err == nil && h.Schema == legacyWireTranscriptSchemaV1 {
				legacySchemaSkips.Add(1)
				glog.Warnw(context.Background(),
					"transcript: 跳过 v1 格式文件（v17 起事件行带 id/seq 身份；v1 的 seq 只是文件内位次，不是全局水位）",
					"path", path, "dispose", legacyTranscriptDisposeHint)
				return nil
			}
			// 非 header（异常）→ 该文件视为不可回读。
			return nil
		}
		var ev wireTranscriptLine
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
			continue // 跳过脏行（如误写的 header 行）
		}
		// v2 事件行必须带**合法身份 + 合法位次**。缺任何一样都说明这份文件不是 v2 写的
		// （手工改动 / 半截写入），整份跳过而不"补一个"——补出来的身份与位次是装载面自己
		// 发明的，与用户手里那份文件对不上，而这正是 v17 要消灭的东西。
		// v21 起 `seq >= 0` 即合法（0 是会话第一条事件的位次），所以下界是 0 而不是 1。
		if !strings.HasPrefix(ev.ID, objid.Event) || !objid.IsPlatformID(ev.ID) || ev.Seq < 0 {
			legacySchemaSkips.Add(1)
			glog.Warnw(context.Background(),
				"transcript: 事件行缺 v2 身份（id 形如 evt_<26 位 ULID> 且 seq >= 0），整份跳过",
				"path", path, "line", lineNo, "id", ev.ID, "seq", ev.Seq, "dispose", legacyTranscriptDisposeHint)
			return nil
		}
		lastType = ev.Type
		if ev.TS != "" {
			lastTS = ev.TS
		}
		if !titleSet && ev.Type == BusUserMessage {
			if text, _ := ev.Payload["text"].(string); text != "" {
				firstUserText = deriveTitle(text)
				titleSet = true
			}
		}
		lines = append(lines, transcriptLine{
			id: ev.ID, seq: ev.Seq, typ: ev.Type,
			version: int64(len(lines)) + 1, payload: ev.Payload,
		})
	}
	if err := sc.Err(); err != nil {
		glog.Warnw(context.Background(), "transcript: load scan failed", "path", path, "err", err)
	}
	if hdr == nil {
		return nil
	}

	createdAt := parseRFC3339(hdr.CreatedAt)
	updatedAt := parseRFC3339(lastTS)
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}

	return &transcriptFile{
		session: &SessionRec{
			ID: sessionID,
			// memory 模式以 transcript 为事实源：工作空间与其"是否代建"标志都从 header 回灌
			// （旧文件缺 workspace_temporary → false，历史会话本就不是代建的）。
			Cwd:                hdr.Cwd,
			WorkspaceTemporary: hdr.WorkspaceTemporary,
			RuntimeID:          runtime.NormalizeID(hdr.Runtime),
			Status:             statusFromLastEventType(lastType),
			Title:              firstUserText,
			CreatedAt:          createdAt,
			UpdatedAt:          updatedAt,
		},
		lines: lines,
	}
}

// statusFromLastEventType 由最后一条事件类型推导会话终态；无终态事件 → in_progress
// （可能是会话进行中或进程被中断，用户可经「停止」自愈，见 serverBusy 逻辑）。
func statusFromLastEventType(ltype string) string {
	switch ltype {
	case BusRunError:
		return SessionStatusFailed
	case BusRunCancelled:
		return SessionStatusCancelled
	case BusRunEnded:
		return SessionStatusCompleted
	default:
		return SessionStatusInProgress
	}
}

// parseRFC3339 宽容解析时间戳（RFC3339 / RFC3339Nano）；失败返回零值。
func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// ——— 导出面（供 `mate migrate` 等外部命令复读 transcript，避免重复解析） ———

// TranscriptEvent 是一条可复读的事件原语（身份 + 位次 + type + payload）。
//
// v17 起带上 `ID`/`Seq`：`mate migrate` 按 `ID` 判重（同 id 同行 → 跳过、不同 → 报错），
// 而 `Seq` 只做诊断输出（落库时**由 DB 重新分配**全局位次——位次的权威是 DB 那一列）。
type TranscriptEvent struct {
	ID      string
	Seq     int64
	Type    string
	Payload map[string]any
}

// TranscriptData 是单个 transcript 文件的复读结果（会话元数据 + 事件原语）。
type TranscriptData struct {
	SessionID string
	RuntimeID string
	Title     string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Lines     []TranscriptEvent
}

// ReadTranscriptFile 复读一个 JSONL transcript 文件（首行 header + 事件行）。
// 坏文件/非本仓库 schema/v1 格式返回 (nil, false)。
func ReadTranscriptFile(path string) (*TranscriptData, bool) {
	rec := loadTranscriptFile(path)
	if rec == nil {
		return nil, false
	}
	data := &TranscriptData{
		SessionID: rec.session.ID,
		RuntimeID: runtime.NormalizeID(rec.session.RuntimeID),
		Title:     rec.session.Title,
		Status:    rec.session.Status,
		CreatedAt: rec.session.CreatedAt,
		UpdatedAt: rec.session.UpdatedAt,
		Lines:     make([]TranscriptEvent, 0, len(rec.lines)),
	}
	for _, l := range rec.lines {
		data.Lines = append(data.Lines, TranscriptEvent{ID: l.id, Seq: l.seq, Type: l.typ, Payload: l.payload})
	}
	return data, true
}
