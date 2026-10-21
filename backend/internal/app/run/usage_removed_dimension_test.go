package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/contract/dtousage"
	"github.com/morehao/ark-mate/internal/app/usage"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/golib/gerror"
)

// TestUsageSummaryRejectsRemovedAgentDimension 用量读面遇到已下线的查询键 `agent_id` 必须**显式拒绝**
// （40001 `agent_selection_removed`），而不是静默忽略。
//
// 为什么宁可报错：静默忽略的后果不是"少了一个维度"，而是"用户以为那次筛选发生过"——他会拿着一份
// 其实是全量的数字当作筛过的结果。会话身份 v19 只剩 `runtime_id`，而底座由构建期固定 ⇒ 按它聚合
// 只有一桶，这个维度整体下线。
//
// 为什么单列一个文件：判据本体（`rejectRemovedUsageDimension`）与它的用例都必须写出那个历史键的
// 名字，而 `usage_test.go` / `usage.go` 在「会话身份只剩 runtime_id」结构判据的扫描面附近，保持
// 零命中更干净。这里是**显式拒绝面**的唯一用例落点。
//
// 请求直接构造（这个字段只服务查询串，没有 JSON 标签；控制器从 `c.Query` 填它）。
func TestUsageSummaryRejectsRemovedAgentDimension(t *testing.T) {
	ctx := context.Background()
	svc := newServiceForTest(testutil.StubRegistry(), eventbus.New())
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer st.Close()
	enableStore(svc, st)

	// 拒绝面在**用例层**（v3.2 刀 9）：已下线的键是"请求形状"的事，领域只收它要用的维度。
	usageSvc := usage.New(sessionPlaneOf(svc))
	_, err := usageSvc.Summary(ctx, dtousage.UsageQueryReq{AgentID: "stub"})
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 40001 || !strings.Contains(ge.Msg, "[agent_selection_removed]") {
		t.Fatalf("期望 40001/agent_selection_removed，got %v", err)
	}

	// 反证：没有这个键时照常出数（拒绝不能误伤正常查询）。
	if _, err := usageSvc.Summary(ctx, dtousage.UsageQueryReq{From: "2000-01-01", To: "2000-01-02"}); err != nil {
		t.Fatalf("不带已下线键应照常出数: %v", err)
	}
}
