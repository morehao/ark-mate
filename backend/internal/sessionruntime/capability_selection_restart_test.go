package sessionruntime

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// TestCapabilitySelectionSurvivesRestart M3-2 的端到端验收：
// 「新建时钉住的选定」与「重启后恢复出的选定」必须是**同一份**，指纹也必须逐字相同。
//
// 这是代际绑定最容易被漏掉的一环：进程内存里的选定当然一致（同一个变量），
// 而真实的中断几乎都是重启（容器重启、升级、崩溃）。漏掉落库或漏掉恢复时读回，
// 表现是"重启后 MCP 少了一个"或"会话被挂到另一个代际"——不报错、不留痕。
func TestCapabilitySelectionSurvivesRestart(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	setBuildRuntime(t, "stub") // 夹具只注册 stub：底座必须与它一致
	spec := stubACPSpec(t)
	spec.McpTransports = []string{"stdio"} // 桩 runtime 只承载 stdio（保守形态）
	reg := runtime.NewRegistry(spec)
	cwd := t.TempDir()

	// —— 进程一：建会话并钉住 "只要 git"（未钉的对照组 = 全量，才有区分度）——
	st1 := testutil.OpenStore(t, dbPath)
	svc1 := NewService(reg, eventbus.New())
	svc1.EnableStore(st1)
	registerStdioMcps(t, svc1)
	orch1 := NewOrchestrator(svc1, reg)

	pinned := &session.CapabilitySelection{Mcps: ptrOf([]string{"git"})}
	res, err := orch1.startSessionOn(ctx, session.StartOptions{Cwd: cwd, Selection: pinned})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	convID := res.SessionID
	rec1 := svc1.sessionPlane.Rec(convID)
	if rec1 == nil || rec1.Fingerprint == "" {
		t.Fatalf("钉住选定的会话必须有非空指纹: %+v", rec1)
	}
	// 未声明选定时指纹为空（缺省路径零回归）——对照组。
	if fp0 := svc1.sessionPlane.CapabilityFingerprint("stub", nil); fp0 != "" {
		t.Fatalf("未声明应为空指纹，got %q", fp0)
	}
	defInject := names(directMcp(svc1.McpServersFor("stub", nil)))
	if len(defInject) != 2 {
		t.Fatalf("对照组（未声明）应注入 2 条直连，got %v", defInject)
	}

	// 落库形态：选定明细与指纹都要写进 sessions 行。
	row, ok, err := st1.GetSession(ctx, convID)
	if err != nil || !ok {
		t.Fatalf("读库: ok=%v err=%v", ok, err)
	}
	if row.CapabilityJSON != session.MarshalCapabilitySelection(pinned) {
		t.Fatalf("capability_json 落库 = %q，期望 %q", row.CapabilityJSON, session.MarshalCapabilitySelection(pinned))
	}
	if row.CapabilityFingerprint != rec1.Fingerprint {
		t.Fatalf("capability_fingerprint 落库 = %q，内存 = %q", row.CapabilityFingerprint, rec1.Fingerprint)
	}
	baseKey := runtimeconn.ConnKey("stub", nil, nil)
	genKey1 := runtimeconn.GenerationKey(baseKey, rec1.Fingerprint, "")

	// 进程退出。
	orch1.Close()
	if err := st1.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}

	// —— 进程二：重启恢复 ——
	st2 := testutil.OpenStore(t, dbPath)
	defer func() { _ = st2.Close() }()
	svc2 := NewService(reg, eventbus.New())
	svc2.EnableStore(st2)
	// MCP 定义的事实源是能力目录（不是库）：重启后由 `mcp/*.json` 重新装载。
	// 只恢复库、不装载定义，会得到"库里有选定、注册表里没有 server"的假象。
	registerStdioMcps(t, svc2)
	if _, _, err := svc2.SessionPlane().Restore(ctx); err != nil {
		t.Fatalf("RestoreFromStore: %v", err)
	}
	rec2 := svc2.sessionPlane.Rec(convID)
	if rec2 == nil {
		t.Fatal("重启后会话记录应有（库是权威元数据）")
	}
	// ① 选定明细原样读回。
	if rec2.CapabilityJSON != session.MarshalCapabilitySelection(pinned) {
		t.Fatalf("重启后选定明细失真: %q", rec2.CapabilityJSON)
	}
	// ② 指纹逐字相同 → 会话回到**同一代际**（否则每次重启都新起一个进程）。
	if rec2.Fingerprint != rec1.Fingerprint {
		t.Fatalf("重启后指纹漂移: %q → %q", rec1.Fingerprint, rec2.Fingerprint)
	}
	if genKey2 := runtimeconn.GenerationKey(baseKey, rec2.Fingerprint, ""); genKey2 != genKey1 {
		t.Fatalf("重启后代际键漂移:\n%s\n%s", genKey1, genKey2)
	}
	// ③ 恢复路径算出的注入集 = 会话钉住的那一份（不是"现在的默认"）。
	restored := names(directMcp(svc2.McpServersFor("stub", session.ParseCapabilitySelection(rec2.CapabilityJSON))))
	if !sameSet(restored, []string{"git"}) {
		t.Fatalf("重启后注入集应等于钉住的选定，got %v", restored)
	}
	if sameSet(restored, defInject) {
		t.Fatal("注入集不该等于未声明的默认全量（这条断言要能区分开，否则它什么也没验）")
	}
	// 恒定的网关条目在重启前后**逐字**相同（args 里的 scope 与会话钉住的白名单都来自会话记录，
	// 与进程无关）——它一旦漂移，重启就会换代际。
	gw1 := gatewayMcp(svc1.McpServersFor("stub", session.ParseCapabilitySelection(rec1.CapabilityJSON)))
	gw2 := gatewayMcp(svc2.McpServersFor("stub", session.ParseCapabilitySelection(rec2.CapabilityJSON)))
	if gw1 == nil || gw2 == nil {
		t.Fatal("两侧都应有恒定的网关条目")
	}
	if strings.Join(gw1.Args, " ") != strings.Join(gw2.Args, " ") || gw1.Command != gw2.Command {
		t.Fatalf("重启后网关条目漂移：%v → %v", gw1.Args, gw2.Args)
	}
}

// registerStdioMcps 给一个 Service 装上两个 stdio MCP 定义（模拟 `mcp/*.json` 装载）。
func registerStdioMcps(t *testing.T, svc *Service) {
	t.Helper()
	for _, id := range []string{"fs", "git"} {
		if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{
			ID: id, Name: id, Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// sameSet 两个 id 集是否相同（与顺序无关）。
func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
