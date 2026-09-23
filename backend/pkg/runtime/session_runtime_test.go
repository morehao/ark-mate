package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-forge/pkg/acl"
)

// dialTestSession 用自建 responder（acp_testserver_test.go）取一条**活的** acl.Session，
// 供投影身份/行为断言使用（echo 删除后不再有进程内 stdio 对端，改走 ws 远程形态）。
func dialTestSession(t *testing.T, r *testACPResponder) (*acl.Client, *acl.Session) {
	t.Helper()
	tr, err := acl.NewRemoteTransport(acl.RemoteSpec{Transport: "ws", Endpoint: serveResponderWS(t, r)})
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cl, err := acl.Dial(ctx, tr, acl.ClientOptions{
		RequestPermission: func(_ context.Context, _ acl.PermissionRequest) acl.PermissionDecision {
			return acl.PermissionDecision{Allowed: true}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close(); _ = tr.Close() })

	sess, err := cl.NewSession(context.Background(), acl.SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return cl, sess
}

// TestTwoProjectionConstructors 验证 opencode / deepseekharness 两投影各自携带
// §6.2 Spec 身份（ID/ManagesOwnContext），共用同一「活会话」实现。
func TestTwoProjectionConstructors(t *testing.T) {
	_, sess := dialTestSession(t, newTestACPResponder())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 两个投影共享同一活会话做身份断言（ID/自管上下文取自各自 Spec）。
	for _, tc := range []struct {
		id     string
		build  func(*acl.Session) Runtime
		ownCtx bool
	}{
		{"opencode", func(s *acl.Session) Runtime { return NewOpencodeRuntime(s) }, true},
		{"deepseekharness", func(s *acl.Session) Runtime { return NewDeepseekHarnessRuntime(s) }, true},
	} {
		rt := tc.build(sess)
		if rt.ID() != tc.id {
			t.Fatalf("%s 投影 ID 不符: %q", tc.id, rt.ID())
		}
		if rt.ManagesOwnContext() != tc.ownCtx {
			t.Fatalf("%s 投影 ManagesOwnContext 不符: %v", tc.id, rt.ManagesOwnContext())
		}
		if rt.ProviderSessionID() != sess.ID() {
			t.Fatalf("%s 投影 ProviderSessionID 不符", tc.id)
		}
		// 模型切换行为：两投影共用同一实现，按**会话自报的配置项**（category=model）判定。
		// 该会话未广告任何配置项 → 一律 unsupported_model（不臆造 env/provider 回落路径）。
		_, err := rt.ApplyModel(ctx, ModelConfig{Model: "x"})
		unsupported := false
		if ae, ok := acl.As(err); ok {
			unsupported = strings.HasPrefix(ae.Code, "unsupported_")
		}
		if !unsupported {
			t.Fatalf("%s ApplyModel 应报 unsupported_*，got %v", tc.id, err)
		}
		if len(rt.ConfigOptions()) != 0 {
			t.Fatalf("%s 未广告配置项却拿到 %+v", tc.id, rt.ConfigOptions())
		}
	}
}

// TestNewProjectionDispatcher 分发器（Orchestrator 采用接缝）：两投影按 Spec.ID 路由、
// 未知 id 返回 nil；构造不触碰会话（nil sess 亦返回非 nil 实例，方法调用方负责非 nil）。
func TestNewProjectionDispatcher(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"opencode", true},
		{"deepseekharness", true},
		{"unknown-rt", false},
	} {
		spec := Spec{ID: tc.id}
		rt := NewProjection(spec, nil)
		if tc.want {
			if rt == nil || rt.ID() != tc.id {
				t.Fatalf("%s 应分发到投影（ID=%s），got %+v", tc.id, tc.id, rt)
			}
			if !rt.ManagesOwnContext() {
				t.Fatalf("%s 投影应自管上下文", tc.id)
			}
		} else if rt != nil {
			t.Fatalf("未知 runtime %q 应返回 nil，got %+v", tc.id, rt)
		}
	}
	// echo 已从内置清单与分发器移除（M0）：它的 ID 必须落到「未知」分支，而不是残留别名。
	if rt := NewProjection(Spec{ID: "echo"}, nil); rt != nil {
		t.Fatalf("echo 已删除，分发器不应再认识它: %+v", rt)
	}
}
