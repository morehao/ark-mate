package store

import (
	"context"
	"fmt"

	"github.com/morehao/ark-forge/internal/model"
	"gorm.io/gorm"
)

// ImportItem 是一次会话导入的输入（transcript 文件内容）：会话元数据 + 事件原语。
type ImportItem struct {
	Conversation Conversation
	Events       []Event // PayloadJSON 已序列化；Seq 忽略（事务内重分配全局 seq）
}

// ImportBatch 在一个事务内导入一批会话（transcript → DB，P1 迁移关键路径）：
// 分配全局 seq → 插 events → upsert conversations（水位=本会话末条 seq）→ 校验 → 设导入标记 → 提交。
// 任一步失败整体回滚（staging 语义）；marker 与库内 imported_transcripts 一致时幂等返回 ErrAlreadyImported。
// expectedFiles/expectedEvents > 0 时做导入前后数量校验（防止漏行/错位）。
func (s *Store) ImportBatch(ctx context.Context, items []ImportItem, marker string, expectedFiles, expectedEvents int) (int64, int, error) {
	var (
		totalEvents       int64
		conversationsDone int
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
			if item.Conversation.ID == "" {
				return fmt.Errorf("store: import item missing conversation id")
			}
			// 先建会话（events 表 FK 引用 conversations），再插事件，最后抬会话水位。
			// 用窄 upsert：不覆盖 provider/model/runtime_hint/provider_session_id（见 dao 注释）。
			conv := item.Conversation
			conv.CreatedAt = nonZeroTime(conv.CreatedAt)
			conv.UpdatedAt = nonZeroTime(conv.UpdatedAt)
			if err := s.conversations.WithTx(tx).UpsertForImport(ctx, &conv); err != nil {
				return err
			}
			var lastSeq int64
			for _, ev := range item.Events {
				seq, err := s.appendEventTx(ctx, tx, conv.ID, ev.Type, ev.PayloadJSON)
				if err != nil {
					return err
				}
				lastSeq = seq
				totalEvents++
			}
			if lastSeq > 0 {
				if err := s.conversations.WithTx(tx).SetLastSeq(ctx, conv.ID, lastSeq); err != nil {
					return err
				}
			}
			conversationsDone++
		}

		if expectedFiles > 0 && conversationsDone != expectedFiles {
			return fmt.Errorf("store: import validation failed: files %d != expected %d", conversationsDone, expectedFiles)
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
	return totalEvents, conversationsDone, nil
}
