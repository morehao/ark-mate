package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/dto/dtoconversation"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/ark-forge/pkg/runtime"
	"github.com/morehao/golib/gerror"
)

// bootFailSpec 一个**启动即失败**的底座：往 stderr 写 lines 行诊断后立刻退出，
// 不完成 initialize 握手（lines 控制规模，用来验证"信封里的尾巴有上限"）。
//
// 为什么不是 stubACPSpec：那个桩会正常握手，而这里要的正是"底座根本没起来"这条路径。
func bootFailSpec(t *testing.T, lines int) runtime.Spec {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "boom.py")
	src := `import os, sys
n = int(os.environ.get("BOOT_LINES", "2"))
for i in range(n):
    sys.stderr.write("boom 第 %d 行：底座启动失败诊断\n" % i)
sys.stderr.write("OPENAI_API_KEY=sk-1234567890abcdefghijklmn\n")
sys.stderr.flush()
sys.exit(3)
`
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatalf("写失败桩失败: %v", err)
	}
	return runtime.Spec{ID: "stub", Name: "Stub",
		Launch: runtime.LaunchSpec{Command: "python3", Args: []string{script},
			Env: []string{fmt.Sprintf("BOOT_LINES=%d", lines)}},
		CapabilityScope: runtime.CapabilityScopeProcess}
}

// TestConnectFailureSurfacesStderrTail 底座起不来时，**信封文案必须带底座 stderr 的诊断尾巴**。
//
// 钉的是一个真实排障场景（2026-10-09 实测）：从含保留键 `.env` 的目录起核心 → dsh 会话建不起来，
// 而 REST 只回 `50001 Internal error`——那是 ACP SDK 对传输失败的通用文案（JSON-RPC -32603 的
// 固定 message，见 acp-go-sdk errors.go），真因（`dsh: <dir>/.env sets "DSH_VERSION"…`）只躺在
// 核心日志里。acl 其实早就采到了 stderr 尾巴（`acl.Error.StderrTail`），是 acl→信封那一跳把它丢了。
//
// 三条断言各有分工：①真因在文案里（可用性）、②量纲有上限（msg 会进错误气泡/日志/截图，而 acl 侧
// 的尾巴上限是 200 行——两者不是一个量纲）、③密钥被脱敏（stderr 常直接打印 key）。
func TestConnectFailureSurfacesStderrTail(t *testing.T) {
	// 写 3000 行：信封里只应出现最后 5 行（其中最后一行是带密钥的那行）。
	spec := bootFailSpec(t, 3000)
	reg := runtime.NewRegistry(spec)
	svc := NewService(reg, eventbus.New())
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", Runtimes: []string{"stub"}, State: AgentStateStopped}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	_, err := svc.CreateConversation(context.Background(),
		dtoconversation.ConversationCreateReq{AgentID: "a1", Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("底座起不来时建会话必须失败")
	}
	envelope := code.FromError(err)
	var ge gerror.Error
	if !errors.As(envelope, &ge) {
		t.Fatalf("信封错误必须带业务码（否则客户端拿到 -1）：%T %v", envelope, envelope)
	}
	// 具体码记的是"今天这条链的落点"：SDK 的 -32603 不在 Classify 的 switch 里 → ClassUnknown
	// → fromACL 的默认分支 UpstreamFailed。哪天有人把 -32603 归到 ClassService/ClassConnection，
	// 这里会变成 50201——那是一次有意的契约调整，测试该跟着改。
	if ge.Code != code.UpstreamFailed {
		t.Fatalf("码应为 %d（上游执行失败），got %d: %s", code.UpstreamFailed, ge.Code, ge.Msg)
	}
	msg := ge.Msg
	if !strings.Contains(msg, "底座 stderr 尾部") {
		t.Fatalf("文案里应带底座 stderr 尾巴（否则用户只有一句 SDK 通用文案）: %q", msg)
	}
	if !strings.Contains(msg, "boom 第 2999 行") {
		t.Fatalf("应带**最后**一行诊断（真因通常就在最后）: %q", msg)
	}
	if strings.Contains(msg, "boom 第 2995 行") {
		t.Fatalf("信封文案不该带满 200 行（acl 侧上限）: %q", msg)
	}
	if n := strings.Count(msg, " | "); n != 4 {
		t.Fatalf("应恰好带 5 行（4 个分隔符），got %d: %q", n, msg)
	}
	if !strings.Contains(msg, "OPENAI_API_KEY=***") || strings.Contains(msg, "sk-1234567890") {
		t.Fatalf("stderr 尾巴里的密钥必须脱敏: %q", msg)
	}
	if msg == "Internal error" {
		t.Fatal("文案不该只剩 SDK 那句通用话")
	}
}
