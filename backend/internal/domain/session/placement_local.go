package session

// LocalPlacement 是 `SessionPlacement` 的**单 worker（本地/单进程）**实现：所有会话都落在本进程，
// cwd 原样透传。它就是本仓现状的语义，只是显式化成一个可替换端口。
//
// 云端多 worker 时换一个「按会话查端点注册表、把 cwd 映射到该 worker 本地卷」的实现，调用方不动。
type LocalPlacement struct{}

// NewLocalPlacement 构造单 worker 落点解析。
func NewLocalPlacement() LocalPlacement { return LocalPlacement{} }

// Place 单 worker：endpointID 为空（= 本进程），cwd 原样透传（本进程本地卷就是它）。
func (LocalPlacement) Place(runtimeID, cwd string) (endpointID string, localCwd string) {
	return "", cwd
}
