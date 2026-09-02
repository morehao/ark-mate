package handler

// mockService 是 Service 的占位实现，便于后端在 internal/manager 实现前独立起服冒烟。
// manager 落地后以实际实现替换。
type mockService struct{}

// NewMockService 返回一个返回固定骨架数据的占位 Service。
func NewMockService() Service { return &mockService{} }

func (mockService) ListAgents() ([]AgentResp, error) {
	return []AgentResp{
		{ID: "a1", Name: "opencode-assistant", Runtime: "opencode", State: "running"},
		{ID: "a2", Name: "dsh-agent", Runtime: "deepseekharness", State: "stopped"},
	}, nil
}

func (mockService) ListRuntimes() ([]RuntimeResp, error) {
	return []RuntimeResp{
		{Name: "opencode", Version: "opencode acp"},
		{Name: "deepseekharness", Version: "dsh-acp-demo"},
	}, nil
}

func (mockService) ListSessions() ([]SessionResp, error) {
	return []SessionResp{
		{ID: "s1", AgentID: "a1", Runtime: "opencode", Status: "in_progress"},
	}, nil
}
