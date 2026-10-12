package manager

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/ark-mate/pkg/object/objdelivery"
)

// TestProviderRespCarriesApplicability `GET /providers` 的两块派生只读面：
// **逐已注册 runtime 的可达性**（端点/凭据通道）与**本地段问题清单**。
//
// v12 删掉了 v11 的 `delivery` 字段（它描述的是"凭据恒为 dial_env / next_run"这条静态事实，
// 而 v12 的凭据通道**按 runtime 分面**：dsh 走原生文档可热生效、合成夹具 rtx 走 env——
// 一条静态声明表达不了它，说了就是说谎）。逐 runtime 的通道由 `applicability[].reason` 回答。
func TestProviderRespCarriesApplicability(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir) // 已注册 runtime：dsh / rtx / stub（升序）

	key := "sk-secret-abc"
	if _, err := svc.CreateProvider(ctx, providerReq("ds", &key)); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	items, err := svc.ListProviders(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("ListProviders: %v (%d 条)", err, len(items))
	}
	resp := items[0]

	// ① 逐已注册 runtime 一条，端点齐备 → ok/projected，reason 里带端点与该底座的凭据通道。
	wantRuntimes := []string{"dsh", runtimetest.ID, "stub"}
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
		if !strings.Contains(a.Reason, "https://api.example.com/v1") {
			t.Errorf("%s 的 reason 应带端点，得到 %q", rt, a.Reason)
		}
	}
	// ② 通道分面：同一个供应商在两个底座上走的通道**必须各自如实**——dsh 的原生凭据文档已实测
	//    （Credential.Tested），合成夹具 rtx 只有 env 一条通道（它的声明里 NativeDoc 为空）。
	for _, a := range resp.Applicability {
		switch a.Runtime {
		case "dsh":
			if !strings.Contains(a.Reason, "凭据文档") {
				t.Errorf("dsh 的凭据通道应报原生文档（已实测可热生效），得到 %q", a.Reason)
			}
		case runtimetest.ID:
			if !strings.Contains(a.Reason, "MATE_") {
				t.Errorf("%s 的凭据通道应报逐供应商的 env 引用名，得到 %q", runtimetest.ID, a.Reason)
			}
		}
	}

	// ③ wire 形状：`applicability` 是数组，元素键名逐字是 runtime/ok/status/reason。
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if _, exists := wire["delivery"]; exists {
		t.Errorf("v12 不该再有 delivery 字段（凭据通道按 runtime 分面，一条静态声明表达不了）: %s", raw)
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

// TestProviderApplicabilityNotConfiguredWhenBaseURLEmpty 缺端点 → 每个 runtime 一条
// not ok/`not_configured`，且 reason 必须**可执行**（说清去哪儿补什么）。
//
// v11 的判据是"type 推不推得出注入变量名"；v12 换成"有没有端点"——因为真正的失败模式变了：
// 缺 `base_url` 的供应商**不会被交付给任何底座**（它的模型一条也到不了底座），而这在 v11 里
// 根本不是一个可表达的形态（端点藏在逐底座的 projections 里）。
func TestProviderApplicabilityNotConfiguredWhenBaseURLEmpty(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	key := "sk-secret-abc"
	if _, err := svc.CreateProvider(ctx, providerReq("legacy", &key)); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	// 把端点清空（存量 v11 数据迁移后推不出端点的形态；PATCH 允许它，因为写面不该逼用户在
	// 一次编辑里就填完所有东西）。
	empty := ""
	if _, err := svc.UpdateProvider(ctx, "legacy", dtoprovider.ProviderPatchReq{BaseURL: &empty}); err != nil {
		t.Fatalf("UpdateProvider(清空端点): %v", err)
	}

	items, err := svc.ListProviders(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("ListProviders: %v (%d 条)", err, len(items))
	}
	resp := items[0]
	if resp.BaseURL != "" {
		t.Fatalf("端点应已清空: %q", resp.BaseURL)
	}
	if len(resp.Applicability) == 0 {
		t.Fatal("applicability 不应为空（还没有任何已注册 runtime 才是合法空）")
	}
	for _, a := range resp.Applicability {
		if a.OK || a.Status != objdelivery.StatusNotConfigured {
			t.Errorf("%s 应 not ok/not_configured，得到 %+v", a.Runtime, a)
		}
		if !strings.Contains(a.Reason, "base_url") {
			t.Errorf("%s 的 reason 必须指出缺的是端点，得到 %q", a.Runtime, a.Reason)
		}
	}
	// 档案自身的问题面照旧（缺端点的可执行出路在 problems 里），两块信息不互相替代。
	if len(resp.Problems) == 0 {
		t.Error("缺端点也应在 problems 里给出可执行出路")
	}
}
