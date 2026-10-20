// Package sessionrow 是会话域的**会话行值类型**（叶子包，理由见 `session/queue` 的包注释）。
//
// 它与 `session.SessionRec` 的分工：`SessionRec` 是**进程内聚合**（带 `Generation` 等仅内存字段），
// `Item` 是一张 `sessions` 行的完整形状（含 `LastSeq` 水位、`CapabilityFingerprint` 列名）。
// 两者由 `persist`/`Restore` 在边界处互换；store 适配器只认 `Item`。
package sessionrow

import "time"

// Item 一条会话（一张 `sessions` 行的完整形状）。
type Item struct {
	ID                    string
	Cwd                   string
	WorkspaceID           string
	WorkspaceTemporary    bool
	ModelID               string
	RuntimeID             string
	RuntimeSessionID      string
	Title                 string
	Pinned                bool
	Archived              bool
	Status                string
	Origin                string
	CapabilityJSON        string
	CapabilityFingerprint string
	NativeMode            string
	LastSeq               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// MetaPatch 会话元数据子集更新（nil 指针 = 不更新；updated_at 恒刷新）。
type MetaPatch struct {
	Title    *string
	Pinned   *bool
	Archived *bool
	Status   *string
	// ModelID 平台模型行 id（nil = 不改）。
	ModelID *string
}

// Empty 判断补丁是否为空（空补丁不产生 SQL）。
func (p MetaPatch) Empty() bool {
	return p.Title == nil && p.Pinned == nil && p.Archived == nil && p.Status == nil && p.ModelID == nil
}
