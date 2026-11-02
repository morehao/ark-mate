package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/protocol"
)

// stubSession 是本包测试用的**会话桩**：投影只做身份与透传，不需要活的协议对端。
//
// 为什么不再拉一个真 ACP 会话：拨号/握手已随 P3-d 搬进执行面（`runtimeconn`），契约包的测试
// 不该再把协议绑定拖回来；投影要断言的是"身份来自入参 Spec、方法原样透传"，桩足够且更快。
type stubSession struct {
	id      string
	prompts []string
	cancels int
}

func (s *stubSession) ID() string                             { return s.id }
func (s *stubSession) Client() protocol.Conn                  { return nil }
func (s *stubSession) Close(context.Context) error            { return nil }
func (s *stubSession) ConfigOptions() []protocol.ConfigOption { return nil }
func (s *stubSession) SetConfigOption(context.Context, string, string) ([]protocol.ConfigOption, error) {
	return nil, nil
}
func (s *stubSession) Prompt(_ context.Context, parts []protocol.PromptPart) (string, error) {
	var text string
	for _, p := range parts {
		if p.Kind == protocol.PromptPartText || p.Kind == "" {
			text += p.Text
		}
	}
	s.prompts = append(s.prompts, text)
	return "end_turn", nil
}
func (s *stubSession) Cancel(context.Context) error { s.cancels++; return nil }

// newStubSession 造一条桩会话（id 固定，便于断言投影透传）。
func newStubSession() *stubSession { return &stubSession{id: "ses_test_1"} }

// TestSessionProjectionCarriesSpecIdentity 会话投影必须携带**传入那份 Spec** 的身份
// （ID / ManagesOwnContext），而不是按 id 查表得来的身份。
//
// 为什么这条是"adapter 层"的一部分：投影构造若又去回查 id，那么**手搓 Spec 的调用方**
// （测试夹具、下一个底座在进内置清单之前的形态）拿到的身份会与它手里那份 `Spec` 不一致，
// 而这种不一致只会在很远的地方以"模型切不过去""取消没生效"的形式表现出来。
func TestSessionProjectionCarriesSpecIdentity(t *testing.T) {
	sess := newStubSession()
	_ = sess
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
