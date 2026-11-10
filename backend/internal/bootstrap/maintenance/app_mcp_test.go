package maintenance

import (
	"context"
	"testing"
)

// TestEmptyAggregatorHandshakeable 回归：无包/包起不来时 `mate mcp app` 必须能构造**空工具面**并正常握手，
// 而不是直接退出。
//
// 真起 serve + 真模型验证暴露的 bug：emptyAggregator 曾用占位域名 "_empty"，而 platformmcp.New 拒绝含
// 下划线的域名（域名即工具名第一段）→ New 返回 error → emptyAggregator 返回 nil → serveAppMcp 打
// "无法构造工具面" 直接 return（不打握手）→ dsh 建会话报 "mcp-client(mate-app-runtime): initial
// connection or tool synchronization failed"。修好占位域名后本测试守护它。
func TestEmptyAggregatorHandshakeable(t *testing.T) {
	agg := emptyAggregator()
	if agg == nil {
		t.Fatal("emptyAggregator 必须返回非 nil（否则桥无法空握手，会拖垮整条 app 会话创建）")
	}
	h := agg.Handler()
	if h == nil {
		t.Fatal("emptyAggregator 的 handler 不能为 nil")
	}
	tools, err := h.ListTools(context.Background())
	if err != nil {
		t.Fatalf("空工具面 ListTools 不应报错：%v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("空工具面不应含任何工具，got %d", len(tools))
	}
}
