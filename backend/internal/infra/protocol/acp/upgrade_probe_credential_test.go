package acp

// 一次性升级探针（0.2.1-rc.2 → 0.2.2-rc.1）· 凭据热生效（H6 核心）。
//
// 默认跳过；`DSH_UPGRADE_PROBE=1` 显式运行。验证 0.2.2-rc.1 上「原生凭据文档 → 热生效」的核心事实：
// 同一进程内把文档的 refs 从 key-A 改成 key-B（平台式临时文件 + 原子 rename），**下一轮请求**的
// Authorization 就是 key-B（未重启）。用一个本地 mock OpenAI 端点捕捉 Authorization 头取证。
//
// 只覆盖 H6 里可由探针稳定复现的核心一条（热生效 + 文档赢），权限/回写等静态事实由代码单测钉住。
//
// 环境变量：
//   DSH_UPGRADE_PROBE=1
//   DSH_PROBE_PAYLOAD=<abs>
//   DSH_PROBE_TMP=<abs>

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/protocol"
)

// capturedAuths 逐请求记录 mock 端点看到的 Authorization 头。
type capturedAuths struct {
	mu    sync.Mutex
	auths []string
}

func (c *capturedAuths) record(a string) {
	c.mu.Lock()
	c.auths = append(c.auths, a)
	c.mu.Unlock()
}
func (c *capturedAuths) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.auths))
	copy(out, c.auths)
	return out
}

// startMockOpenAI 起一个只回最小 chat.completions SSE 的 mock，返回 baseURL、捕捉器、关闭函数。
func startMockOpenAI(t *testing.T, cap *capturedAuths) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock listen 失败：%v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		// 让 listModels 能列到我们的模型（route 才能进目录）。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "probe-model-a", "object": "model"}},
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		cap.record(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		bw := bufio.NewWriter(w)
		chunk := map[string]any{
			"id": "chatcmpl-probe", "object": "chat.completion.chunk", "model": "probe-model-a",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": "ok"}, "finish_reason": "stop"}},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(bw, "data: %s\n\n", b)
		fmt.Fprint(bw, "data: [DONE]\n\n")
		_ = bw.Flush()
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/v1", func() { _ = srv.Close() }
}

func writeCredDoc(t *testing.T, path, key string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	content := "version: 1\n\nrefs:\n  MATE_PROBE_API_KEY: " + key + "\n"
	// 平台式原子写：临时文件 + rename，权限 0600。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatalf("写凭据临时文件失败：%v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename 凭据文档失败：%v", err)
	}
}

func TestUpgradeProbeCredentialHotReload(t *testing.T) {
	upgradeProbeSkip(t)
	payload := os.Getenv("DSH_PROBE_PAYLOAD")
	tmp := os.Getenv("DSH_PROBE_TMP")
	if payload == "" || tmp == "" {
		t.Fatal("DSH_PROBE_PAYLOAD / DSH_PROBE_TMP 必须设置")
	}
	var cap capturedAuths
	baseURL, closeSrv := startMockOpenAI(t, &cap)
	defer closeSrv()

	bin := filepath.Join(payload, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	home := filepath.Join(tmp, "cred-probe-home")
	_ = os.RemoveAll(home)
	cwd := filepath.Join(home, "cwd")
	_ = os.MkdirAll(cwd, 0o700)
	dshHome := filepath.Join(home, "dsh")

	// 凭据文档落到 $DSH_HOME/.credentials.yaml（与平台 NativeDoc 一致）。
	credPath := filepath.Join(dshHome, ".credentials.yaml")
	writeCredDoc(t, credPath, "key-A")

	// 模型 patch：自定义 route 指向 mock，apiKeyEnv = MATE_PROBE_API_KEY（引到凭据文档的 ref）。
	modelPatch := filepath.Join(home, "model.patch.yml")
	patchBody := "# probe model patch\n" +
		"- id: acp\n  config:\n    provider: \"mate-probe-cred\"\n    model: \"probe-model-a\"\n" +
		"- id: llm-pi-ai\n  config:\n    providers:\n      \"mate-probe-cred\":\n" +
		"        displayName: \"Probe Cred\"\n        api: \"openai-completions\"\n" +
		"        apiKeyEnv: \"MATE_PROBE_API_KEY\"\n        baseURL: " + fmt.Sprintf("%q", baseURL) + "\n" +
		"        models:\n          - id: \"probe-model-a\"\n            input: [text]\n"
	if err := os.WriteFile(modelPatch, []byte(patchBody), 0o600); err != nil {
		t.Fatal(err)
	}

	node, err := exec.LookPath("node")
	if err != nil {
		node = "node"
	}
	// 注意：**不**在进程 env 里给 MATE_PROBE_API_KEY（那会走 env 通道，违反"文档赢/只走文档"）。
	// 只靠 DSH_HOME 下的凭据文档提供值，从而测到的就是"文档 → 请求头"这条通道。
	tp := NewStdioTransport(node, []string{bin, "--profile", "acp", "--patch", modelPatch}, []string{
		"HOME=" + home,
		"DSH_HOME=" + dshHome,
	})
	tp.SetDir(cwd)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cl, err := Dial(ctx, tp, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, Always: true}
		},
		DialTimeout:   60 * time.Second,
		ClientName:    "mate-cred-probe",
		ClientVersion: "0",
	})
	if err != nil {
		t.Fatalf("Dial 失败：%v（stderr=%v）", err, tp.StderrTail())
	}
	t.Cleanup(func() { _ = cl.Close() })

	sess, err := cl.NewSession(ctx, protocol.SessionOptions{Cwd: cwd})
	if err != nil {
		t.Fatalf("NewSession 失败：%v", err)
	}
	pctx, pcancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer pcancel()
	if _, _, err := sess.Prompt(pctx, protocol.TextParts("ping-1")); err != nil {
		t.Fatalf("第一轮 Prompt 失败：%v（stderr=%v）", err, tp.StderrTail())
	}
	first := cap.snapshot()
	if len(first) == 0 {
		t.Fatalf("第一轮没有打到 mock 端点（凭据/路由没接通）")
	}
	if first[0] != "Bearer key-A" {
		t.Fatalf("第一轮 Authorization=%q，期望 Bearer key-A（文档未被使用）", first[0])
	}

	// 热更：同进程内把文档 refs 从 key-A 改成 key-B（原子 rename），不重启。
	writeCredDoc(t, credPath, "key-B")
	// 给底座的文件 watch 一点时间生效。
	time.Sleep(1500 * time.Millisecond)

	pctx2, pcancel2 := context.WithTimeout(context.Background(), 45*time.Second)
	defer pcancel2()
	if _, _, err := sess.Prompt(pctx2, protocol.TextParts("ping-2")); err != nil {
		t.Fatalf("第二轮 Prompt 失败：%v", err)
	}
	all := cap.snapshot()
	if len(all) < 2 {
		t.Fatalf("第二轮没有打到 mock 端点，只看到 %d 次", len(all))
	}
	if all[len(all)-1] != "Bearer key-B" {
		t.Fatalf("第二轮 Authorization=%q，期望 Bearer key-B（热生效未成立，文档变化未反映到下一轮）", all[len(all)-1])
	}
	if !strings.EqualFold(all[0], "Bearer key-A") {
		t.Fatalf("第一轮头被影响：%q", all[0])
	}
	t.Logf("凭据热生效成立：第一轮 %q → 第二轮（同进程、未重启）%q", all[0], all[len(all)-1])
}
