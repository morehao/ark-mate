package router_test

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
)

// ——— 已下线键的传输面（v19「显式拒绝面」） ———
//
// 为什么单列一个文件：下面这些用例**必须写出历史键的名字**才谈得上判据（"带了它会被拒绝"、
// "带了几层都没被半路丢掉"），而 `rest_test.go` 是路由契约的主用例集——让主用例集保持
// 零命中（`TestNoAgentVocabulary`），把"指出历史"的用例收口在这一个文件里，两件事互不牵制。
//
// 纪律与 `internal/manager/reject_removed_keys.go` 同源：JSON / 查询串绑定默认**丢弃未知键**，
// 所以"把字段从 DTO 里删掉"不等于"用户带了这个键会被拦下"——传输层必须**原样送到领域层**，
// 拒绝与否由领域层那唯一一处判据决定（控制器既不自作聪明地读取，也不静默丢弃）。

// TestCreateSessionRemovedAgentKeyReachesService 会话写面：已下线的 agent 选择键必须原样透传。
func TestCreateSessionRemovedAgentKeyReachesService(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", `{"agent_id":"agent-rtx","prompt":"hi"}`)
	if env.Code != 0 {
		t.Fatalf("传输层不该自己拒绝（判据在 manager）: %+v", env)
	}
	if !svc.createCalled || string(svc.createReq.AgentID) != `"agent-rtx"` || svc.createReq.Prompt != "hi" {
		t.Fatalf("已下线的键应原样透传给 service: %+v", svc.createReq)
	}
}

// TestUsageSummaryRemovedAgentKeyReachesService 用量读面：查询串里的那个键同样要送到 service
// （manager 的非空即拒绝见 `usage_removed_dimension_test.go`）。
func TestUsageSummaryRemovedAgentKeyReachesService(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))
	env, code := do(t, r, "GET", "/v1/mate/usage/summary?agent_id=agent-stub&group_by=day", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("信封不符（拒绝对不对是 manager 的事）: http=%d env=%+v", code, env)
	}
	if svc.usageQuery.AgentID != "agent-stub" || svc.usageQuery.GroupBy != "day" {
		t.Fatalf("已下线的查询键应原样透传给 service: %+v", svc.usageQuery)
	}
}

// TestAgentsEndpointGone `/agents*` 必须**整片不存在**（v19：agent 概念并入 runtime）。
//
// 与 `TestRuntimesEndpointGone` 同一个理由：用户面零 agent 选择入口，历史路径不许再回 200
// ——静默 200 会让前端以为它还在册。
func TestAgentsEndpointGone(t *testing.T) {
	r := newRestEngine(stubDeps(t, &stubSvc{}))
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/mate/agents"},
		{"POST", "/v1/mate/agents"},
		{"PATCH", "/v1/mate/agents/a1"},
		{"DELETE", "/v1/mate/agents/a1"},
		{"POST", "/v1/mate/agents/a1/start"},
		{"POST", "/v1/mate/agents/a1/stop"},
		{"POST", "/v1/mate/agents/a1/default"},
		{"GET", "/v1/mate/agents/a1/capabilities"},
		{"GET", "/v1/mate/agents/a1/diagnostics"},
	} {
		if _, http := do(t, r, tc.method, tc.path, ""); http == 200 {
			t.Fatalf("%s %s 不该再可用：agent 概念已并入 runtime", tc.method, tc.path)
		}
	}
}

// TestRuntimesEndpointGone `GET /runtimes` 必须**不存在**（用户面零 runtime 选择入口）。
func TestRuntimesEndpointGone(t *testing.T) {
	r := newRestEngine(stubDeps(t, &stubSvc{}))
	_, code := do(t, r, "GET", "/v1/mate/runtimes", "")
	if code == 0 {
		t.Fatal("GET /v1/mate/runtimes 不该再可用：底座由构建期固定")
	}
}

// TestRuntimeDiagnosticsOmitsRemovedAgentIdentity 诊断面**逐字**不许出现 agent 身份字段。
//
// 这一条与上面两条方向相反：那两条钉"传输层不吞掉历史键"，这条钉"新读面不回带被撤掉的概念"
// （形状判据，避免它从别的字段缝回来）。
func TestRuntimeDiagnosticsOmitsRemovedAgentIdentity(t *testing.T) {
	svc := &stubSvc{diagResp: &dtoruntime.DiagnosticsResp{
		State: "stopped", Runtime: "stub", RuntimeName: "Stub",
		StderrSource: "none", StderrTail: []string{}, CheckedAt: "2026-09-09T00:00:00Z",
	}}
	r := newRestEngine(stubDeps(t, svc))
	env, http := do(t, r, "GET", "/v1/mate/runtime/diagnostics", "")
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	body := string(env.Data)
	for _, banned := range []string{`"agent_id"`, `"agent"`, `"name":"`} {
		if strings.Contains(body, banned) {
			t.Fatalf("诊断面不该出现 %s: %s", banned, body)
		}
	}
}
