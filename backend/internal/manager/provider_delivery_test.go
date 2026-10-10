package manager

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/pkg/object/objdelivery"
)

// TestProviderRespCarriesDeliveryAndApplicability `GET /providers` 的 M6 加法（纯加法，不动既有字段）：
// 每条档案回带 `delivery`（通道/生效时刻/人话 note）与逐**已注册 runtime** 的 `applicability`。
//
// 逐条断言而不是只看"字段非空"：这三样东西各自的失败模式不同——通道写错会让用户等错时刻、
// applicability 少一个 runtime 会让某个底座的可达性看不见、note 缺失会让界面只剩一个代号。
func TestProviderRespCarriesDeliveryAndApplicability(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir) // 已注册 runtime：deepseekharness / opencode / stub（升序）

	key := "sk-secret-abc"
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
		ID: "ds", Type: "deepseek", Key: &key,
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	items, err := svc.ListProviders()
	if err != nil || len(items) != 1 {
		t.Fatalf("ListProviders: %v (%d 条)", err, len(items))
	}
	resp := items[0]

	// ① delivery：provider 凭据恒为 dial_env / next_turn，note 按实际注入变量名具体化。
	if resp.Delivery.Channel != objdelivery.ChannelDialEnv {
		t.Errorf("delivery.channel 应为 %q，得到 %q", objdelivery.ChannelDialEnv, resp.Delivery.Channel)
	}
	if resp.Delivery.Effect != objdelivery.EffectNextTurn {
		t.Errorf("delivery.effect 应为 %q，得到 %q", objdelivery.EffectNextTurn, resp.Delivery.Effect)
	}
	if !strings.Contains(resp.Delivery.Note, "DEEPSEEK_API_KEY") {
		t.Errorf("delivery.note 应带实际注入变量名 DEEPSEEK_API_KEY，得到 %q", resp.Delivery.Note)
	}

	// ② applicability：逐个已注册 runtime 一条，推得出变量名 → ok/projected 且 reason 带变量名。
	wantRuntimes := []string{"deepseekharness", "opencode", "stub"}
	if len(resp.Applicability) != len(wantRuntimes) {
		t.Fatalf("applicability 应覆盖 %d 个已注册 runtime，得到 %+v", len(wantRuntimes), resp.Applicability)
	}
	for i, rt := range wantRuntimes {
		a := resp.Applicability[i]
		if a.Runtime != rt {
			t.Errorf("applicability[%d].runtime 应为 %q（升序），得到 %q", i, rt, a.Runtime)
		}
		if !a.OK || a.Status != objdelivery.StatusProjected {
			t.Errorf("%s 应 ok/projected，得到 %+v", rt, a)
		}
		if !strings.Contains(a.Reason, "DEEPSEEK_API_KEY") {
			t.Errorf("%s 的 reason 应带具体变量名，得到 %q", rt, a.Reason)
		}
	}

	// ③ wire 形状：`delivery` 是对象、`applicability` 是数组，键名逐字是 channel/effect/note 与
	//    runtime/ok/status/reason（前端按这套形状读）。
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	delivery, ok := wire["delivery"].(map[string]any)
	if !ok {
		t.Fatalf("delivery 应是对象: %s", raw)
	}
	for _, k := range []string{"channel", "effect", "note"} {
		if _, exists := delivery[k]; !exists {
			t.Errorf("delivery 缺键 %q: %s", k, raw)
		}
	}
	list, ok := wire["applicability"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("applicability 应是非空数组: %s", raw)
	}
	first, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("applicability 的元素应是对象: %s", raw)
	}
	for _, k := range []string{"runtime", "ok", "status"} {
		if _, exists := first[k]; !exists {
			t.Errorf("applicability 元素缺键 %q: %s", k, raw)
		}
	}
}

// TestProviderApplicabilityNotConfiguredWhenTypeEmpty type 为空（推不出注入变量名）→ 每个 runtime
// 一条 not ok/`not_configured`，且 reason 必须**可执行**（说清去哪儿补什么）。
//
// 路径用 PATCH 把 type 清空来构造：POST 会拒绝空 type，而 PATCH 允许（清空历史 type 是真实存在的
// 存量形态），正是这条分支要覆盖的场景。
func TestProviderApplicabilityNotConfiguredWhenTypeEmpty(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	key := "sk-secret-abc"
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
		ID: "legacy", Type: "openai", Key: &key,
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	empty := ""
	if _, err := svc.UpdateProvider(ctx, "legacy", dtoprovider.ProviderPatchReq{Type: &empty}); err != nil {
		t.Fatalf("UpdateProvider(清空 type): %v", err)
	}

	items, err := svc.ListProviders()
	if err != nil || len(items) != 1 {
		t.Fatalf("ListProviders: %v (%d 条)", err, len(items))
	}
	resp := items[0]
	// type 推不出 → note 退回表里的 <TYPE>_API_KEY 占位（不许编出一个变量名）。
	if !strings.Contains(resp.Delivery.Note, "<TYPE>_API_KEY") {
		t.Errorf("type 为空时 note 应保留占位，得到 %q", resp.Delivery.Note)
	}
	if len(resp.Applicability) == 0 {
		t.Fatal("applicability 不应为空（还没有任何已注册 runtime 才是合法空）")
	}
	for _, a := range resp.Applicability {
		if a.OK || a.Status != objdelivery.StatusNotConfigured {
			t.Errorf("%s 应 not ok/not_configured，得到 %+v", a.Runtime, a)
		}
		if a.Reason == "" {
			t.Errorf("%s 的 reason 必须可执行，得到空串", a.Runtime)
		}
	}
	// 档案自身的问题面照旧（缺 type 的可执行出路在 problems 里），两块信息不互相替代。
	if len(resp.Problems) == 0 {
		t.Error("type 为空也应在 problems 里给出可执行出路")
	}
}
