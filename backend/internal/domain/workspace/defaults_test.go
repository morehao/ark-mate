package workspace

import "testing"

// TestResolveSessionDefaults 合并链的三档语义（ADR-7 / v27）。
//
// 这一条钉的是**顺序**：顺序反了不会编译失败、也不会让任何别的测试变红，表现只是
// 「我明明在请求里写了，它却按默认值建了」——而那种偏差在界面上看起来像"设置没保存"。
func TestResolveSessionDefaults(t *testing.T) {
	ws := &WorkspaceDefaults{CapabilityJSON: `{"skills":["sk_ws"]}`, PermissionMode: "workspace-write"}

	// ① 都没写 → 取工作空间默认。
	got := ResolveSessionDefaults("", "", ws)
	if got.CapabilityJSON != ws.CapabilityJSON || got.PermissionMode != ws.PermissionMode {
		t.Fatalf("空请求应取工作空间默认，got %+v", got)
	}

	// ② 显式优先（**两档各自独立**：写了一个不该把另一个也让出去）。
	got = ResolveSessionDefaults(`{"skills":["sk_explicit"]}`, "", ws)
	if got.CapabilityJSON != `{"skills":["sk_explicit"]}` {
		t.Fatalf("显式能力集应优先，got %q", got.CapabilityJSON)
	}
	if got.PermissionMode != ws.PermissionMode {
		t.Fatalf("只写了能力集时权限模式仍应回落工作空间默认，got %q", got.PermissionMode)
	}

	// ③ 没有工作空间默认（nil / 空结构）→ 原样往上传空串（交给下游缺省）。
	if got = ResolveSessionDefaults("", "", nil); got.CapabilityJSON != "" || got.PermissionMode != "" {
		t.Fatalf("没有工作空间默认时不应凭空造值，got %+v", got)
	}
	if got = ResolveSessionDefaults("", "", &WorkspaceDefaults{}); got.CapabilityJSON != "" || got.PermissionMode != "" {
		t.Fatalf("工作空间默认是空串时不应凭空造值，got %+v", got)
	}
}
