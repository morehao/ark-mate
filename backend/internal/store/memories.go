package store

import "context"

// ——— permission_memories 表（会话级权限记忆） ———

// AddMemory 写入一条会话级权限记忆（同 conversation+title+kind 已存在时覆盖 decision；
// 同侧重复授权不刷新授予时间——与 PermissionManager.Remember 语义对齐）。
func (s *Store) AddMemory(ctx context.Context, m Memory) error {
	m.CreatedAt = nonZeroTime(m.CreatedAt)
	return s.memories.Upsert(ctx, &m)
}

// ListMemories 列出某会话全部权限记忆（授予时间升序，decision 区分 allow/reject）。
func (s *Store) ListMemories(ctx context.Context, conversationID string) ([]Memory, error) {
	return s.memories.List(ctx, conversationID)
}

// DeleteMemory 撤销一条记忆（conversation+title+kind 精确匹配，不区分 decision）；返回是否删除。
func (s *Store) DeleteMemory(ctx context.Context, conversationID, title, kind string) (bool, error) {
	return s.memories.Delete(ctx, conversationID, title, kind)
}

// DeleteConversationMemories 一键撤销整会话全部权限记忆。
func (s *Store) DeleteConversationMemories(ctx context.Context, conversationID string) error {
	return s.memories.DeleteByConversation(ctx, conversationID)
}

// ListAllMemories 列出全库权限记忆（sqlite 启动恢复：一次装载所有会话的 allow/reject 记忆）。
func (s *Store) ListAllMemories(ctx context.Context) ([]Memory, error) {
	return s.memories.ListAll(ctx)
}
