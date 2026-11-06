package acp

// 一次性升级探针（0.2.1-rc.2 → 0.2.2-rc.1）· ACP 侧。
//
// 默认跳过；仅在 `DSH_UPGRADE_PROBE=1` + 指定 -run 时跑。它用平台**真实的** ACP 客户端拨通
// 随包载荷（0.2.2-rc.1）的 `node bin.js --profile acp --patch <模型 patch>`，验证：
//   - D-3 / A4：`option[category=model]` 的 currentValue 由 `- id: acp` 那行决定；候选集含平台声明的
//     自定义 route 模型（动态目录不破）。
//   - D-1 / A6：`ListSessions` 在 0.2.2-rc.1 上不因 fork 子会话语义变化而失败（能被调用并返回）。
//
// 环境变量：
//   DSH_UPGRADE_PROBE=1
//   DSH_PROBE_PAYLOAD=<abs>                     载荷目录
//   DSH_PROBE_MODEL_PATCH=<abs>                 模型 patch 文件（由 dsh 包探针生成）
//   DSH_PROBE_TMP=<abs>                         隔离工作根

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/protocol"
)

func upgradeProbeSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("DSH_UPGRADE_PROBE") != "1" {
		t.Skip("升级探针默认跳过：设 DSH_UPGRADE_PROBE=1 显式运行")
	}
}

func upgradeProbeDial(t *testing.T) *Client {
	t.Helper()
	payload := os.Getenv("DSH_PROBE_PAYLOAD")
	patch := os.Getenv("DSH_PROBE_MODEL_PATCH")
	tmp := os.Getenv("DSH_PROBE_TMP")
	if payload == "" || patch == "" || tmp == "" {
		t.Fatal("DSH_PROBE_PAYLOAD / DSH_PROBE_MODEL_PATCH / DSH_PROBE_TMP 必须都设置")
	}
	bin := filepath.Join(payload, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("载荷 bin.js 不存在：%s：%v", bin, err)
	}
	home := filepath.Join(tmp, "acp-probe-home")
	_ = os.RemoveAll(home)
	if err := os.MkdirAll(filepath.Join(home, "cwd"), 0o700); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		node = "node"
	}
	tp := NewStdioTransport(node, []string{bin, "--profile", "acp", "--patch", patch}, []string{
		"HOME=" + home,
		"DSH_HOME=" + filepath.Join(home, "dsh"),
		// 平台 route 声明的 apiKeyEnv 无值时，底座把该 route 的模型目录按凭据过滤掉（0.2.1 起，
		// ModelCatalogNeedsCredential）——探针要看到候选集，必须给这条 route 一个可用凭据。
		"MATE_PROBE_API_KEY=probe-fake-key",
	})
	tp.SetDir(filepath.Join(home, "cwd"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cl, err := Dial(ctx, tp, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, Always: true}
		},
		DialTimeout:   45 * time.Second,
		ClientName:    "mate-upgrade-probe",
		ClientVersion: "0",
	})
	if err != nil {
		t.Fatalf("Dial 0.2.2-rc.1 失败：%v（stderr=%v）", err, tp.StderrTail())
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

func TestUpgradeProbeACPCatalog(t *testing.T) {
	upgradeProbeSkip(t)
	cl := upgradeProbeDial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	sess, err := cl.NewSession(ctx, protocol.SessionOptions{Cwd: filepath.Join(os.Getenv("DSH_PROBE_TMP"), "acp-probe-home", "cwd")})
	if err != nil {
		t.Fatalf("NewSession 失败：%v（stderr=%v）", err, cl.StderrTail())
	}
	opts := sess.ConfigOptions()
	modelOpt, ok := protocol.FindConfigOption(opts, protocol.CategoryModel)
	if !ok {
		t.Fatalf("会话没有 category=model 配置项（0.2.2-rc.1 ACP 目录读取可能变了）")
	}
	t.Logf("model currentValue=%q，候选 %d 条", modelOpt.CurrentValue, len(modelOpt.Values))
	for _, v := range modelOpt.Values {
		t.Logf("  候选 value=%q name=%q", v.Value, v.Name)
	}
	// D-3：currentValue 必须由 `- id: acp` 那行（provider=mate-prov-probe / model=probe-model-a）决定。
	if !strings.Contains(modelOpt.CurrentValue, "probe-model-a") {
		t.Fatalf("currentValue=%q 未反映 acp patch 声明的 probe-model-a（默认路由未生效）", modelOpt.CurrentValue)
	}
	// A4：候选集里应出现平台声明的自定义 route 模型（动态目录不破）。
	foundA, foundB := false, false
	for _, v := range modelOpt.Values {
		if strings.Contains(v.Value, "probe-model-a") {
			foundA = true
		}
		if strings.Contains(v.Value, "probe-model-b") {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Fatalf("候选集缺失平台声明的模型（a=%v b=%v）：值=%v", foundA, foundB, modelOpt.Values)
	}
	_ = sess.Close(ctx)
}

func TestUpgradeProbeACPForkListSessions(t *testing.T) {
	upgradeProbeSkip(t)
	cl := upgradeProbeDial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// D-1 / A6：先建一个会话，再 ListSessions，确认不因 fork 语义变化报错。
	if _, err := cl.NewSession(ctx, protocol.SessionOptions{Cwd: filepath.Join(os.Getenv("DSH_PROBE_TMP"), "acp-probe-home", "cwd")}); err != nil {
		t.Fatalf("NewSession 失败：%v", err)
	}
	sessions, err := cl.ListSessions(ctx, filepath.Join(os.Getenv("DSH_PROBE_TMP"), "acp-probe-home", "cwd"))
	if err != nil {
		t.Fatalf("ListSessions 失败（0.2.2-rc.1 A6 fork 语义可能破）：%v（stderr=%v）", err, cl.StderrTail())
	}
	t.Logf("ListSessions 返回 %d 条", len(sessions))
	for _, s := range sessions {
		t.Logf("  session id=%s", s.ID)
	}
}
