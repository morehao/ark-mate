package sessionruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/protocol"
)

// fakeConn 是只用于**身份记账**的连接桩（在途归属、断连摘句柄这类判据只需要两把不同的连接）。
//
// 带 id 字段是必需的：零大小类型的多次取址可能落在同一地址，会让"两把不同连接"互相顶替、
// 判据假绿。放在测试文件里而不是生产包：生产代码不该导出测试用的空实现。
type fakeConn struct{ id int }

func fakeConnOf(id int) protocol.Conn { return &fakeConn{id: id} }

func (s *fakeConn) Capabilities() protocol.Capabilities { return protocol.Capabilities{} }
func (s *fakeConn) StderrTail() []string                { return nil }
func (s *fakeConn) NewSession(context.Context, protocol.SessionOptions) (protocol.Session, error) {
	return nil, nil
}
func (s *fakeConn) LoadSession(context.Context, string, protocol.SessionOptions) (protocol.Session, error) {
	return nil, nil
}
func (s *fakeConn) ResumeSession(context.Context, string, protocol.SessionOptions) (protocol.Session, error) {
	return nil, nil
}
func (s *fakeConn) ListSessions(context.Context, string) ([]protocol.SessionInfo, error) {
	return nil, nil
}
func (s *fakeConn) Close() error { return nil }
