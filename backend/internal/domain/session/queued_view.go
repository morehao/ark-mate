package session

// ——— 待投递输入队列的对外形状（v3.2 刀 9）———
//
// 只有字段、没有 json tag：对外形态（`dtosession.QueuedInputResp`）由用例层 `svcsession` 投影。
type QueuedInputItem struct {
	ID          string
	Text        string
	Delivery    string
	TargetRunID string
	Generation  int64
	CreatedAt   int64
}
