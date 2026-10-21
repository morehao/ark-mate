package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/infra/acp"
)

// dialTestSession 用自建 responder（acp_testserver_test.go）取一条**活的** acp.Session，
// 供投影身份/行为断言使用（echo 删除后不再有进程内 stdio 对端，改走 ws 远程形态）。
func dialTestSession(t *testing.T, r *testACPResponder) (*acp.Client, *acp.Session) {
	t.Helper()
	tr, err := acp.NewRemoteTransport(acp.RemoteSpec{Transport: "ws", Endpoint: serveResponderWS(t, r)})
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cl, err := acp.Dial(ctx, tr, acp.ClientOptions{
		RequestPermission: func(_ context.Context, _ acp.PermissionRequest) acp.PermissionDecision {
			return acp.PermissionDecision{Allowed: true}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close(); _ = tr.Close() })

	sess, err := cl.NewSession(context.Background(), acp.SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return cl, sess
}

// TestSessionProjectionCarriesSpecIdentity 会话投影必须携带**传入那份 Spec** 的身份
// （ID / ManagesOwnContext），而不是按 id 查表得来的身份。
//
// 为什么这条是"adapter 层"的一部分：投影构造若又去回查 id，那么**手搓 Spec 的调用方**
// （测试夹具、下一个底座在进内置清单之前的形态）拿到的身份会与它手里那份 `Spec` 不一致，
// 而这种不一致只会在很远的地方以"模型切不过去""取消没生效"的形式表现出来。
func TestSessionProjectionCarriesSpecIdentity(t *testing.T) {
	_, sess := dialTestSession(t, newTestACPResponder())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 刻意用**非内置 id**（`rt-alt`）：证明投影身份来自入参 Spec，而不是"认识这个 id"。
	for _, tc := range []struct {
		id     string
		ownCtx bool
	}{
		{"rt-alt", true},
		{"dsh", true},
		{"rt-direct", false},
	} {
		rt := NewProjection(Spec{ID: tc.id, ManagesOwnContext: tc.ownCtx}, sess)
		if rt == nil {
			t.Fatalf("%s 应拿到会话投影（投影构造不按 id 分派）", tc.id)
		}
		if rt.ID() != tc.id {
			t.Fatalf("%s 投影 ID 不符: %q", tc.id, rt.ID())
		}
		if rt.ManagesOwnContext() != tc.ownCtx {
			t.Fatalf("%s 投影 ManagesOwnContext 不符: %v", tc.id, rt.ManagesOwnContext())
		}
		if rt.RuntimeSessionID() != sess.ID() {
			t.Fatalf("%s 投影 RuntimeSessionID 不符", tc.id)
		}
		// 模型切换行为：两投影共用同一实现，按**会话自报的配置项**（category=model）判定。
		// 该会话未广告任何配置项 → 一律 unsupported_model（不臆造 env/provider 回落路径）。
		_, err := rt.ApplyModel(ctx, ModelConfig{Model: "x"})
		unsupported := false
		if ae, ok := failure.As(err); ok {
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

// TestNewProjectionNeedsNoDispatch 会话投影的构造**不得**按 id 分派。
//
// 这条判据钉的是"框架里不再有 `switch spec.ID`"这件事本身：投影是**按 Spec 参数化**的，
// 所以任何 id——内置的、远程的、测试现造的——都必须拿到同样形态的实例。
// 分派表一旦长回来（"只有这几个 id 才算投影"），表现是**新底座静默没有会话投影**：
// 取消不生效、模型切不过去，而每一处都只退化成"回退底层直连"，看不出来。
func TestNewProjectionNeedsNoDispatch(t *testing.T) {
	for _, id := range []string{"dsh", "rt-alt", "unknown-rt", "echo", ""} {
		rt := NewProjection(Spec{ID: id}, nil) // nil sess：构造不该触碰会话
		if rt == nil {
			t.Fatalf("id=%q 应拿到投影实例（构造不按 id 分派）", id)
		}
		if rt.ID() != id {
			t.Fatalf("id=%q 的投影身份应逐字来自入参 Spec，实际 %q", id, rt.ID())
		}
	}
}
