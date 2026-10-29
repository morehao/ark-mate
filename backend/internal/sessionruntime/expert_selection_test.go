package sessionruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— 专家：创建时选择 + 物化 + 只读回显（P0-2 验收） ———
//
// 这些用例建立在 `validateRig`（stub runtime：只承载 stdio、skill 投影就绪、网关关闭）之上：
//   - 可达 skill：ok-skill（off-skill 禁用、other-skill 白名单不含 stub）
//   - 可达 MCP：fs（stdio）；web（http）在 stub 上不可达
//   - 专家：直接注册进能力注册表（写面/装载另有用例覆盖）

// registerExpert 往注册表塞一条专家（测试快捷口）。
func registerExpert(t *testing.T, svc *Service, e capability.Expert) {
	t.Helper()
	if err := svc.planes.Capabilities.RegisterExpert(&e); err != nil {
		t.Fatalf("注册专家 %q: %v", e.ID, err)
	}
}

// TestCreateSessionWithExpertMaterializes A2：带专家的创建回显专家 + 物化后的技能/MCP；DB 行可读回。
func TestCreateSessionWithExpertMaterializes(t *testing.T) {
	svc, _, _ := validateRig(t)
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc.EnableStore(st)

	registerExpert(t, svc, capability.Expert{
		ID: "data-analyst", Name: "数据分析师", Skills: []string{"ok-skill"}, Mcps: []string{"fs"},
	})

	res, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Cwd:          t.TempDir(),
		Capabilities: &dtosession.CapabilitiesReq{Expert: ptrOf("data-analyst")},
	})
	if err != nil {
		t.Fatalf("带专家创建应成功: %v", err)
	}
	// 创建响应回显专家 + 物化列表。
	if res.Capabilities == nil {
		t.Fatal("带专家的创建响应必须回显 capabilities")
	}
	if res.Capabilities.Expert == nil || *res.Capabilities.Expert != "data-analyst" {
		t.Fatalf("响应 expert 不符: %+v", res.Capabilities.Expert)
	}
	if res.Capabilities.Skills == nil || !sameSet(*res.Capabilities.Skills, []string{"ok-skill"}) {
		t.Fatalf("响应 skills 应物化为 [ok-skill]，got %+v", res.Capabilities.Skills)
	}
	if res.Capabilities.Mcps == nil || !sameSet(*res.Capabilities.Mcps, []string{"fs"}) {
		t.Fatalf("响应 mcps 应物化为 [fs]，got %+v", res.Capabilities.Mcps)
	}

	// GET 详情同样回显（读的是落库快照）。
	detail, err := detailSvc(svc).GetSession(ctx, res.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if detail.Capabilities == nil || detail.Capabilities.Expert == nil || *detail.Capabilities.Expert != "data-analyst" {
		t.Fatalf("详情应回显 expert: %+v", detail.Capabilities)
	}
	if detail.Capabilities.Skills == nil || !sameSet(*detail.Capabilities.Skills, []string{"ok-skill"}) {
		t.Fatalf("详情 skills 不符: %+v", detail.Capabilities.Skills)
	}

	// DB 行是**具体 id 列表**（不是「跟随专家」的活引用）。
	row, ok, err := st.GetSession(ctx, res.ID)
	if err != nil || !ok {
		t.Fatalf("读库: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(row.CapabilityJSON, `"expert":"data-analyst"`) ||
		!strings.Contains(row.CapabilityJSON, `"ok-skill"`) ||
		!strings.Contains(row.CapabilityJSON, `"fs"`) {
		t.Fatalf("capability_json 应含专家与物化列表，got %q", row.CapabilityJSON)
	}
}

// TestCreateSessionExplicitEmptyBeatsExpertRecommendation A3：显式 `skills: []` + 专家推荐 3 个 → 0 个。
func TestCreateSessionExplicitEmptyBeatsExpertRecommendation(t *testing.T) {
	svc, _, _ := validateRig(t)
	ctx := context.Background()
	registerExpert(t, svc, capability.Expert{
		ID: "rec3", Name: "推荐三个", Skills: []string{"ok-skill", "off-skill", "other-skill"},
	})

	res, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Cwd: t.TempDir(),
		Capabilities: &dtosession.CapabilitiesReq{
			Expert: ptrOf("rec3"),
			Skills: ptrOf([]string{}), // 明确一个都不要
		},
	})
	if err != nil {
		t.Fatalf("显式空数组应能建会话: %v", err)
	}
	if res.Capabilities == nil || res.Capabilities.Skills == nil {
		t.Fatalf("显式 [] 必须回显为空数组而不是缺键: %+v", res.Capabilities)
	}
	if len(*res.Capabilities.Skills) != 0 {
		t.Fatalf("显式 [] 必须赢过专家推荐，got %v", *res.Capabilities.Skills)
	}
	// 响应里不该冒出被推荐却被显式覆盖的名字。
	for _, bad := range []string{"ok-skill", "off-skill", "other-skill"} {
		if strings.Contains(string(mustJSON(t, res.Capabilities)), bad) {
			t.Fatalf("显式 [] 时响应不该出现推荐项 %q", bad)
		}
	}
}

// TestCreateSessionExpertSkipsUnreachableRecommendation A4：专家推荐里一个不可达 → 创建成功 + warnings 一条。
func TestCreateSessionExpertSkipsUnreachableRecommendation(t *testing.T) {
	svc, _, _ := validateRig(t)
	ctx := context.Background()
	registerExpert(t, svc, capability.Expert{
		ID: "mixed", Name: "混合推荐", Mcps: []string{"fs", "web"}, // web 在 stub 不可达（http）
	})

	res, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Cwd:          t.TempDir(),
		Capabilities: &dtosession.CapabilitiesReq{Expert: ptrOf("mixed")},
	})
	if err != nil {
		t.Fatalf("推荐里有不可达项不应阻断创建: %v", err)
	}
	if res.Capabilities == nil || res.Capabilities.Mcps == nil || !sameSet(*res.Capabilities.Mcps, []string{"fs"}) {
		t.Fatalf("可达推荐应物化为 [fs]，got %+v", res.Capabilities)
	}
	if len(res.Capabilities.Warnings) != 1 {
		t.Fatalf("应有一条被跳过的推荐告警，got %+v", res.Capabilities.Warnings)
	}
	w := res.Capabilities.Warnings[0]
	if w.Kind != "mcp" || w.ID != "web" || w.Reason == "" {
		t.Fatalf("告警应指明 mcp web 与原因，got %+v", w)
	}
}

// TestCreateSessionRejectsUnknownOrDisabledExpert A6：专家不存在 → 40401；禁用 → 40001 expert_disabled。
func TestCreateSessionRejectsUnknownOrDisabledExpert(t *testing.T) {
	svc, orch, _ := validateRig(t)
	ctx := context.Background()
	registerExpert(t, svc, capability.Expert{ID: "off", Name: "禁用专家", Enabled: boolPtr(false)})

	_, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Cwd: t.TempDir(), Capabilities: &dtosession.CapabilitiesReq{Expert: ptrOf("ghost")},
	})
	if err == nil || numericCode(err) != 40401 || !strings.Contains(err.Error(), "未知 expert") {
		t.Fatalf("未知专家应 40401（expert_not_found），got %v (code=%d)", err, numericCode(err))
	}

	_, err = svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Cwd: t.TempDir(), Capabilities: &dtosession.CapabilitiesReq{Expert: ptrOf("off")},
	})
	if err == nil || numericCode(err) != 40001 || !strings.Contains(err.Error(), "已被禁用") {
		t.Fatalf("禁用专家应 40001（expert_disabled），got %v (code=%d)", err, numericCode(err))
	}
	if n := orch.pool.Len(); n != 0 {
		t.Fatalf("专家校验失败不该建立连接，got %d", n)
	}
}

// TestCreateSessionExpertSnapshotDoesNotDrift A7：创建后改专家定义，再读会话：技能/MCP 不变。
func TestCreateSessionExpertSnapshotDoesNotDrift(t *testing.T) {
	svc, _, _ := validateRig(t)
	ctx := context.Background()
	registerExpert(t, svc, capability.Expert{ID: "snap", Name: "快照专家", Skills: []string{"ok-skill"}})

	res, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Cwd: t.TempDir(), Capabilities: &dtosession.CapabilitiesReq{Expert: ptrOf("snap")},
	})
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	// 改专家推荐（注册表覆盖）。
	registerExpert(t, svc, capability.Expert{ID: "snap", Name: "快照专家", Skills: []string{"other-skill"}})

	detail, err := detailSvc(svc).GetSession(ctx, res.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if detail.Capabilities.Skills == nil || !sameSet(*detail.Capabilities.Skills, []string{"ok-skill"}) {
		t.Fatalf("改专家定义后会话技能应保持创建时快照，got %+v", detail.Capabilities.Skills)
	}
}

// TestWorkspaceDefaultExpertBoundary A11：capabilities 缺省时工作空间默认专家生效；capabilities:{} 时不生效。
func TestWorkspaceDefaultExpertBoundary(t *testing.T) {
	svc, _, _ := validateRig(t)
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc.EnableStore(st)

	wsDir := t.TempDir()
	if err := svc.Planes().Workspaces.SetWorkspacePolicy(wsDir, nil); err != nil {
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}
	if err := svc.Planes().Workspaces.EnsureDefaultWorkspace(); err != nil {
		t.Fatalf("EnsureDefaultWorkspace: %v", err)
	}
	def, ok, err := svc.Planes().Workspaces.DefaultWorkspace()
	if err != nil || !ok {
		t.Fatalf("取默认工作空间: ok=%v err=%v", ok, err)
	}
	if _, err := svc.Planes().Workspaces.PatchWorkspace(def.ID, workspace.PatchRequest{
		DefaultCapability: ptrOf(`{"expert":"data-analyst"}`),
	}); err != nil {
		t.Fatalf("设置工作空间默认能力: %v", err)
	}
	registerExpert(t, svc, capability.Expert{ID: "data-analyst", Name: "默认专家", Skills: []string{"ok-skill"}})

	// capabilities 缺省 → 工作空间默认带入专家。
	withDefault, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{})
	if err != nil {
		t.Fatalf("缺省能力应能建会话: %v", err)
	}
	recD := svc.sessionPlane.Rec(withDefault.ID)
	if recD == nil || !strings.Contains(recD.CapabilityJSON, "data-analyst") {
		t.Fatalf("capabilities 缺省时应带入工作空间默认专家，got %q", recD.CapabilityJSON)
	}

	// capabilities:{} → 声明了但什么都不限制，**不吃**工作空间默认专家。
	declared, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Capabilities: &dtosession.CapabilitiesReq{},
	})
	if err != nil {
		t.Fatalf("capabilities:{} 应能建会话: %v", err)
	}
	recE := svc.sessionPlane.Rec(declared.ID)
	if recE == nil || strings.Contains(recE.CapabilityJSON, "expert") {
		t.Fatalf("capabilities:{} 不应带入默认专家，got %q", recE.CapabilityJSON)
	}
}

// TestResolveExpertBuildsPersona 专家 instructions 非空时解析产出角色块（P1 注入的输入端）。
func TestResolveExpertBuildsPersona(t *testing.T) {
	svc, _, _ := validateRig(t)
	registerExpert(t, svc, capability.Expert{ID: "persona", Name: "人格专家", Instructions: "你是数据分析师，先确认口径"})

	res, err := svc.resolveExpert(context.Background(),
		&session.CapabilitySelection{Expert: ptrOf("persona")}, "")
	if err != nil {
		t.Fatalf("resolveExpert: %v", err)
	}
	if !strings.Contains(res.persona, session.ExpertRoleAttr) || !strings.Contains(res.persona, "先确认口径") {
		t.Fatalf("应产出含标记与 instructions 的角色块，got %q", res.persona)
	}
	// 无 instructions 的专家：不产出角色块（纯能力预设）。
	registerExpert(t, svc, capability.Expert{ID: "plain", Name: "纯预设"})
	res2, err := svc.resolveExpert(context.Background(),
		&session.CapabilitySelection{Expert: ptrOf("plain")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res2.persona != "" {
		t.Fatalf("无 instructions 不应产出角色块，got %q", res2.persona)
	}
}

// TestCreateSessionExpertMissingAfterDelete 读面在专家定义被删后标记 expert_missing（能力仍按快照生效）。
func TestCreateSessionExpertMissingAfterDelete(t *testing.T) {
	svc, _, _ := validateRig(t)
	ctx := context.Background()
	registerExpert(t, svc, capability.Expert{ID: "gone", Name: "会被删", Skills: []string{"ok-skill"}})

	res, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{
		Cwd: t.TempDir(), Capabilities: &dtosession.CapabilitiesReq{Expert: ptrOf("gone")},
	})
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	svc.planes.Capabilities.RemoveExpert("gone")

	detail, err := detailSvc(svc).GetSession(ctx, res.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if detail.Capabilities == nil || detail.Capabilities.Expert == nil || *detail.Capabilities.Expert != "gone" {
		t.Fatalf("应仍回显专家 id: %+v", detail.Capabilities)
	}
	if !detail.Capabilities.ExpertMissing {
		t.Fatalf("专家定义已删 → expert_missing 应为 true")
	}
	// 能力快照不受影响。
	if detail.Capabilities.Skills == nil || !sameSet(*detail.Capabilities.Skills, []string{"ok-skill"}) {
		t.Fatalf("删除专家不应影响已建会话的技能快照，got %+v", detail.Capabilities.Skills)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return b
}
