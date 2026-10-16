package manager

import "github.com/morehao/ark-mate/pkg/acl"

// liveClient 在连接集合里找某 runtime 此刻的活连接（`runtimeconn.LiveClientSource` 的实现体）。
//
// 放在这里而不是 `diagnostics.go`：连接集合归**执行面**（P3a-2 第三刀会随 `connection.go`
// 一起搬进 `internal/runtimeconn`），到那时这个函数整份搬走，而 `diagnostics.go` 那层适配消失。
func (o *Orchestrator) liveClient(runtimeID string) (*acl.Client, int, bool) {
	o.mu.Lock()
	var rc *runtimeConn
	for _, c := range o.conns {
		if runtimeID != "" && c.spec.ID != runtimeID {
			continue
		}
		if c.client != nil {
			rc = c
			break
		}
	}
	o.mu.Unlock()
	if rc == nil {
		return nil, 0, false
	}
	select {
	case <-rc.client.Done():
		return nil, 0, false
	default:
		return rc.client, rc.pid, true
	}
}
