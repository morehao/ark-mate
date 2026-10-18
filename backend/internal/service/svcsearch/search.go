package svcsearch

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosearch"
)

// SearchSvc 跨会话消息检索用例端口。
//
// v3.1 §3.2.1：控制器不得 import `internal/domain`。检索是纯读模型（搜索实现与域的锁、
// transcript/events 事实源绑死），所以这一层只复述那一个方法——不加校验、不二次投影。
type SearchSvc interface {
	Messages(ctx context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error)
}

type searchSvc struct{ plane *session.Plane }

// New 构造消息检索用例。
func New(plane *session.Plane) SearchSvc { return &searchSvc{plane: plane} }

func (s *searchSvc) Messages(ctx context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error) {
	return s.plane.SearchMessages(ctx, q, limit)
}
