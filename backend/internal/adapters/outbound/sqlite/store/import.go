package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"gorm.io/gorm"
)

// ImportItem 是一次会话导入的输入（transcript 文件内容）：会话元数据 + 事件原语。
type ImportItem struct {
	Session sessionrow.Item
	// Events PayloadJSON 已序列化；**ID 必须带**（v17：transcript v2 的事件行身份，原样落库），
	// `Seq` 仍然忽略——位次由 DB 这个权威按**本会话**重新分配（v21 起是会话内位次 0 起、
	// 按本切片的行序推进；transcript 里那个 seq 是写入时那一刻的位次，跨版本可能是全局号，
	// 拿它当权威会把旧口径的洞带进新库）。
	Events []event.Item
}

// ImportBatch 在一个事务内导入一批会话（transcript → DB，P1 迁移关键路径）：
// 逐会话按行序分配**会话内位次** → 插 events（**按 id 判重**）→ upsert sessions（水位=本会话末条 seq）→
// 校验 → 设导入标记 → 提交。
// 任一步失败整体回滚（staging 语义）；marker 与库内 imported_transcripts 一致时幂等返回 ErrAlreadyImported。
// expectedFiles/expectedEvents > 0 时做导入前后数量校验（防止漏行/错位）。
//
// 事件级判重（v17，D2）：同 id 且内容相同 → 跳过（不写第二份）；同 id 但内容不同 → **报错**
// （同一身份的两个不同内容意味着这份 transcript 与库里的历史分叉了，静默写入只会让"哪一份是
// 真的"永远说不清）。计数口径是"导入后**在场**的事件数"（新插 + 已存在且相同），所以
// expectedEvents 仍然对得上。
//
// 位次口径（v21）：每条新事件经 `appendEventTx` 从 `event_sequence(sessionID)` 取号，
// 所以**新会话**的位次就是本切片的下标（0、1、2…），与 transcript 行序一致；重复导入时
// 已存在的事件不取号（判重早退），新事件接在既有水位之后——不会出现"跳号"或"改写既有位次"。
func (s *Store) ImportBatch(ctx context.Context, items []ImportItem, marker string, expectedFiles, expectedEvents int) (int64, int, error) {
	var (
		totalEvents  int64
		sessionsDone int
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if marker != "" {
			cur, ok, err := s.meta.WithTx(tx).Get(ctx, model.MetaKeyImportMarker)
			if err != nil {
				return err
			}
			if ok && cur == marker {
				return ErrAlreadyImported
			}
		}

		for _, item := range items {
			if item.Session.ID == "" {
				return fmt.Errorf("store: import item missing session id")
			}
			// 先建会话（events 表 FK 引用 sessions），再插事件，最后抬会话水位。
			// 用窄 upsert：不覆盖 provider_id/model_id/runtime_id/runtime_session_id（见 dao 注释）。
			conv := sessionEntityOf(item.Session)
			conv.CreatedAt = nonZeroTime(conv.CreatedAt)
			conv.UpdatedAt = nonZeroTime(conv.UpdatedAt)
			if err := s.upsertSessionForImportTx(ctx, tx, &conv); err != nil {
				return err
			}
			// `lastSeq` 初值 -1 = "这一批一条都没分配/命中"（v21 起位次从 0 起，用 0 当
			// "还没有"会把"该会话的第一条事件"误判成没有事件、从而不推进水位）。
			lastSeq := int64(-1)
			for _, ev := range item.Events {
				seq, err := s.importEventTx(ctx, tx, conv.ID, ev)
				if err != nil {
					return err
				}
				if seq > lastSeq {
					lastSeq = seq
				}
				totalEvents++
			}
			if lastSeq >= 0 {
				// 只升不降（与 live 路径同一条语义）：跳过重复事件时最后一条在场事件的 seq
				// 可能比既有水位小，`SetLastSeq` 会把水位拉回去。
				if err := s.touchLastSeqTx(ctx, tx, conv.ID, lastSeq); err != nil {
					return err
				}
			}
			sessionsDone++
		}

		if expectedFiles > 0 && sessionsDone != expectedFiles {
			return fmt.Errorf("store: import validation failed: files %d != expected %d", sessionsDone, expectedFiles)
		}
		if expectedEvents > 0 && int(totalEvents) != expectedEvents {
			return fmt.Errorf("store: import validation failed: events %d != expected %d", totalEvents, expectedEvents)
		}

		if marker != "" {
			if err := s.meta.WithTx(tx).Set(ctx, model.MetaKeyImportMarker, marker); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return totalEvents, sessionsDone, nil
}

// importEventTx 导入一条事件：**先按身份判重，再取号落库**。
//
// 判重只按 `id`（身份），不按内容哈希：内容相同 + 同 id = 同一件事（跳过）；同 id 不同内容 =
// 分叉（报错）。两条都是**显式**的——把分叉静默写进去，等于让同一支 id 在库里有两份语义。
func (s *Store) importEventTx(ctx context.Context, tx *gorm.DB, sessionID string, ev event.Item) (int64, error) {
	if err := validateImportEventID(ev.ID); err != nil {
		return 0, fmt.Errorf("store: import session %s type %s: %w", sessionID, ev.Type, err)
	}
	prev, ok, err := s.FindEventByIDTx(ctx, tx, ev.ID)
	if err != nil {
		return 0, err
	}
	if ok {
		if prev.SessionID != sessionID || prev.Type != ev.Type || prev.PayloadJSON != ev.PayloadJSON {
			return 0, fmt.Errorf(
				"store: import 分叉：事件 %s 已存在且内容不同（库内 session=%s type=%s，transcript session=%s type=%s）——"+
					"同一支 id 的两份不同内容无法调和；请确认这份 transcript 与库里的历史是否同源",
				ev.ID, prev.SessionID, prev.Type, sessionID, ev.Type)
		}
		return prev.Seq, nil
	}
	return s.appendEventTx(ctx, tx, ev.ID, sessionID, ev.Type, ev.PayloadJSON)
}

// validateImportEventID 校验导入事件的 id 形状（`evt_<26 位 Crockford>`）。
//
// 与 transcript 装载面同一条判据（都拒绝旧形状），但这里的后果不同：装载是尽力而为（跳过该
// 文件 + WARN），导入是**数据面写入**，缺 id 或 id 非法必须让整批回滚（不猜、不代铸——
// 代铸就等于导入面自己发明身份，与 transcript 里那一行对不上）。
func validateImportEventID(id string) error {
	if id == "" {
		return fmt.Errorf("事件缺 id（v17 起 transcript 事件行必须带 `id`；v1 格式请用 `mate transcripts prune` 处置）")
	}
	if !strings.HasPrefix(id, objid.Event) || !objid.IsPlatformID(id) {
		return fmt.Errorf("事件 id 形状非法 %q（期望 %s<26 位 ULID>）", id, objid.Event)
	}
	return nil
}
