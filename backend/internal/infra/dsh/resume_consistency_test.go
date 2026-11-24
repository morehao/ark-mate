package dsh

// V1 · resume 一致性黑盒验证（M1 出口闸）+ 会话日志落本地卷实证。
//
// 与同目录 `upgrade_probe_test.go` 同款：**dsh 启动级探针，默认跳过**，只在 `DSH_RESUME_PROBE=1`
// 时跑；用**本包真实的 `buildPatch`** 生成模型 patch（不手抄 YAML 形状——手抄会随 buildPatch 演进
// 静默漂移），把随包/安装的 dsh 以 `--profile acp --patch <真实产物>` 拉起，驱动完整会话生命周期。
//
// 验什么（M1 出口闸「单租户单 worker 起停+resume 闭环、上下文一致性 100%」+ M1-2「日志落本地卷」）：
//  1. `session/new` 建会话 → 首轮 prompt 埋一个金丝雀 token（`MEMTOKEN_…`）；
//  2. **杀进程**（Close 回收子进程，模拟 worker 崩溃/回收）；断言该会话的 DSH JSONL 日志
//     `session.v*.jsonl*` 确已落到本地卷（`<DSH_HOME>/sessions/...`）——M1-2 的实证；
//  3. 起新进程 → `session/resume` **同一原生 sessionId** → 第二轮 prompt **故意不含金丝雀**；
//  4. 判据：本地假 provider 这一次收到的上游请求历史里**仍含金丝雀**——第二轮 prompt 本身没给过它，
//     故它只能来自 DSH 从本地日志恢复的上下文。=> resume 后上下文 100% 一致。
//
// 为什么用「金丝雀出现在上游历史」而不是「resume 收到了历史事件」来判上下文：DSH 的 `session/resume`
// 契约是「还原日志、**不回放**旧 update」（见 dsh-acp/README.md 的 session/resume 行）。所以「上下文有没有
// 丢」只能用**行为**判——resume 后发一轮需要「记得上一轮」的 prompt，看模型侧是否真拿到了上一轮。
//
// 假 provider：本地 OpenAI 兼容 `/v1/chat/completions`（支持流式 SSE，dsh 默认走流式）。它按「这一次请求
// 的完整 messages 历史里有没有金丝雀」给确定性回答。
//
// 环境变量（与 upgrade_probe 对齐）：
//   DSH_RESUME_PROBE=1         显式开启
//   DSH_PROBE_PAYLOAD=<abs>    dsh 载荷目录（仅用于定位/存在性校验；实际启动用 PATH 上的 `dsh`）
//   DSH_PROBE_TMP=<abs>        隔离工作根（其下建私有 HOME/DSH_HOME，绝不碰用户 ~/.dsh）
//   DSH_RESUME_PROBE_ITERS=<n> 循环次数（默认 100）
//
// 隔离纪律（docs/agents/isolation.md）：私有 HOME + 私有 DSH_HOME，全在 DSH_PROBE_TMP 下；收尾杀进程 + 清目录。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/core/resource"
	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
)

func resumeProbeEnv(t *testing.T) (payload, tmp string) {
	t.Helper()
	if os.Getenv("DSH_RESUME_PROBE") != "1" {
		t.Skip("resume 一致性探针默认跳过：设 DSH_RESUME_PROBE=1 显式运行")
	}
	payload = strings.TrimSpace(os.Getenv("DSH_PROBE_PAYLOAD"))
	tmp = strings.TrimSpace(os.Getenv("DSH_PROBE_TMP"))
	if payload == "" || tmp == "" {
		t.Fatal("DSH_PROBE_PAYLOAD / DSH_PROBE_TMP 必须都设置")
	}
	if _, err := os.Stat(filepath.Join(payload, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")); err != nil {
		t.Fatalf("载荷 bin.js 不存在：%v", err)
	}
	return payload, tmp
}

// fakeMsg 是 OpenAI chat 历史里的一条（判金丝雀用）。
type fakeMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// fakeProvider 是本地 OpenAI 兼容假上游：只实现 `/v1/chat/completions` 与 `/v1/models`。
type fakeProvider struct {
	srv      *http.Server
	baseURL  string
	mu       sync.Mutex
	lastMsgs []fakeMsg
	canary   string
	calls    int
}

func startFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("假 provider 监听失败：%v", err)
	}
	fp := &fakeProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": "fake-echo", "object": "model", "owned_by": "probe"}},
		})
	})
	mux.HandleFunc("/v1/chat/completions", fp.handleChat)
	fp.srv = &http.Server{Handler: mux}
	fp.baseURL = "http://" + ln.Addr().String() + "/v1"
	go func() { _ = fp.srv.Serve(ln) }()
	t.Cleanup(func() { _ = fp.srv.Close() })
	return fp
}

// handleChat 解析 OpenAI chat 请求，按历史里是否含金丝雀给出确定性回答；支持流式 SSE（dsh 默认走流式）。
func (fp *fakeProvider) handleChat(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Messages []fakeMsg `json:"messages"`
		Stream   bool      `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)

	fp.mu.Lock()
	fp.lastMsgs = req.Messages
	fp.calls++
	canary := fp.canary
	fp.mu.Unlock()

	var hist strings.Builder
	for _, m := range req.Messages {
		hist.WriteString(msgText(m.Content))
		hist.WriteString("\n")
	}
	answer := "CANARY_MISSING"
	if canary != "" && strings.Contains(hist.String(), canary) {
		answer = "CANARY_OK:" + canary
	}

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		sendChunk := func(delta map[string]any, finish any) {
			chunk := map[string]any{
				"id": "chatcmpl-probe", "object": "chat.completion.chunk",
				"created": time.Now().Unix(), "model": "fake-echo",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		sendChunk(map[string]any{"role": "assistant"}, nil)
		sendChunk(map[string]any{"content": answer}, nil)
		sendChunk(map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
		return
	}

	writeJSON(w, map[string]any{
		"id": "chatcmpl-probe", "object": "chat.completion", "created": time.Now().Unix(), "model": "fake-echo",
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

func msgText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
					b.WriteString("\n")
				}
			}
		}
		return b.String()
	}
	return ""
}

func (fp *fakeProvider) setCanary(c string) { fp.mu.Lock(); fp.canary = c; fp.mu.Unlock() }

func (fp *fakeProvider) sawCanaryInLast(canary string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	var hist strings.Builder
	for _, m := range fp.lastMsgs {
		hist.WriteString(msgText(m.Content))
		hist.WriteString("\n")
	}
	return canary != "" && strings.Contains(hist.String(), canary)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeModelPatch 用**本包真实的 buildPatch** 生成指向假 provider 的模型 patch 文件。
// 不再手抄 YAML 形状——model 路由键派生（routeKey 前缀）、`- id: acp` 默认模型、providers 块
// 全部走生产代码，探针因此测的是**真实的模型交付路径**。
func writeModelPatch(t *testing.T, path, providerBaseURL string) {
	t.Helper()
	in := int64(8192)
	out := int64(2048)
	set := resource.Set{
		Runtime: ID,
		Providers: []resource.Provider{{
			ID: "prov-probe", Name: "Probe Provider",
			Protocol: resource.ProtocolOpenAI, BaseURL: providerBaseURL, Ref: "MATE_PROBE_API_KEY",
		}},
		Models: []resource.Model{{
			ID: "model-probe", ProviderID: "prov-probe", Model: "fake-echo", Name: "假回声",
			InputModalities: []string{resource.ModalityText}, MaxInputTokens: &in, MaxOutputTokens: &out,
		}},
		DefaultModel: "model-probe",
	}
	b, err := buildPatch(set)
	if err != nil {
		t.Fatalf("buildPatch 失败：%v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("写模型 patch 失败：%v", err)
	}
}

// probeDial 在私有 HOME/DSH_HOME 上起一个 dsh 进程并拨通其 ACP 端点。
// 用 PATH 上的 `dsh`（构建期固定版本）以 `--profile acp`（裸模板，首用自动初始化；resume/持久化
// 都是 base 能力，与 office 无关）启动，模型路由来自 buildPatch 生成的 `--patch`。
func probeDial(t *testing.T, home, cwd, patchPath string, onEvent func(acp.Event)) *acp.Client {
	t.Helper()
	tp := acp.NewStdioTransport("dsh", []string{"--profile", "acp", "--patch", patchPath}, []string{
		"HOME=" + home,
		"DSH_HOME=" + filepath.Join(home, "runtime", "dsh"),
		"MATE_PROBE_API_KEY=probe-fake-key",
	})
	tp.SetDir(cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cl, err := acp.Dial(ctx, tp, acp.ClientOptions{
		OnEvent: onEvent,
		RequestPermission: func(_ context.Context, _ acp.PermissionRequest) acp.PermissionDecision {
			return acp.PermissionDecision{Allowed: true, Always: true}
		},
		DialTimeout: 60 * time.Second, ClientName: "mate-resume-probe", ClientVersion: "0",
	})
	if err != nil {
		t.Fatalf("Dial mate-acp 失败：%v（stderr=%v）", err, tp.StderrTail())
	}
	return cl
}

// selectModel 把会话模型选到 dsh 目录第一条候选；没有 model 配置项 = 用 buildPatch 下发的默认（跳过）。
func selectModel(ctx context.Context, sess protocol.Session) error {
	var modelOpt *protocol.ConfigOption
	for _, o := range sess.ConfigOptions() {
		if o.Category == "model" {
			o := o
			modelOpt = &o
			break
		}
	}
	if modelOpt == nil {
		return nil
	}
	if len(modelOpt.Values) == 0 {
		return fmt.Errorf("model 配置项没有候选值（patch 里的 route 没生效？current=%q）", modelOpt.CurrentValue)
	}
	_, err := sess.SetConfigOption(ctx, modelOpt.ID, modelOpt.Values[0].Value)
	return err
}

// assertSessionLogOnLocalVolume 断言该会话的 DSH JSONL 日志已落到本地卷（DSH_HOME 下）。
// 只 glob（不重实现 DSH 的 projectKey/encodeSegment 路径编码，避免与其内部布局耦合）。
func assertSessionLogOnLocalVolume(t *testing.T, iter int, dshHome, sessionID string) {
	t.Helper()
	root := filepath.Join(dshHome, "sessions")
	found := false
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.Contains(p, sessionID) {
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, "session.v") && (strings.HasSuffix(base, ".jsonl") || strings.HasSuffix(base, ".jsonl.zstd")) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if !found {
		t.Fatalf("[iter %d] 会话日志没有落到本地卷：%s 下找不到 session %s 的 session.v*.jsonl* 文件（M1-2 未达成）", iter, root, sessionID)
	}
}

func TestResumeContextConsistencyV1(t *testing.T) {
	_, tmp := resumeProbeEnv(t)
	iters := 100
	if s := strings.TrimSpace(os.Getenv("DSH_RESUME_PROBE_ITERS")); s != "" {
		fmt.Sscanf(s, "%d", &iters)
	}

	fp := startFakeProvider(t)
	passed := 0
	for i := 0; i < iters; i++ {
		i := i
		func() {
			home := filepath.Join(tmp, fmt.Sprintf("iter-%03d", i), "home")
			_ = os.RemoveAll(home)
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(home)) })
			cwd := filepath.Join(home, "cwd")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatalf("建 cwd 失败：%v", err)
			}
			patchPath := filepath.Join(home, "probe-models.patch.yml")
			writeModelPatch(t, patchPath, fp.baseURL)
			canary := fmt.Sprintf("MEMTOKEN_%d_%d", i, time.Now().UnixNano())

			// —— 第 1 段进程：建会话 + 埋金丝雀 ——
			ctx1, cancel1 := context.WithTimeout(context.Background(), 120*time.Second)
			cl1 := probeDial(t, home, cwd, patchPath, nil)
			sess1, err := cl1.NewSession(ctx1, protocol.SessionOptions{Cwd: cwd})
			if err != nil {
				cancel1()
				_ = cl1.Close()
				t.Fatalf("[iter %d] NewSession 失败：%v", i, err)
			}
			sid := sess1.ID()
			if err := selectModel(ctx1, sess1); err != nil {
				cancel1()
				_ = cl1.Close()
				t.Fatalf("[iter %d] 建会话选模型失败：%v", i, err)
			}
			fp.setCanary(canary)
			if _, _, err := sess1.Prompt(ctx1, protocol.TextParts("记住这个金丝雀："+canary+"。只回 OK 即可。")); err != nil {
				cancel1()
				_ = cl1.Close()
				t.Fatalf("[iter %d] 第 1 轮 prompt 失败：%v", i, err)
			}
			if !fp.sawCanaryInLast(canary) {
				cancel1()
				_ = cl1.Close()
				t.Fatalf("[iter %d] 前置失真：第 1 轮假 provider 没收到金丝雀（探针自身失效）", i)
			}
			cancel1()
			_ = cl1.Close() // 杀进程（回收子进程）；日志已 flush 到本地卷

			// M1-2：日志确实落到了本地卷。
			assertSessionLogOnLocalVolume(t, i, filepath.Join(home, "runtime", "dsh"), sid)

			// —— 第 2 段进程：resume 同一 session id + 问金丝雀（prompt 不含它）——
			ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel2()
			cl2 := probeDial(t, home, cwd, patchPath, nil)
			defer func() { _ = cl2.Close() }()
			sess2, err := cl2.ResumeSession(ctx2, sid, protocol.SessionOptions{Cwd: cwd})
			if err != nil {
				t.Fatalf("[iter %d] ResumeSession(%s) 失败：%v", i, sid, err)
			}
			if sess2.ID() != sid {
				t.Fatalf("[iter %d] resume 返回的 session id 变了：%s → %s", i, sid, sess2.ID())
			}
			if err := selectModel(ctx2, sess2); err != nil {
				t.Fatalf("[iter %d] resume 后重选模型失败：%v", i, err)
			}
			if _, _, err := sess2.Prompt(ctx2, protocol.TextParts("上一轮我给你的金丝雀 token 是什么？原样回给我。")); err != nil {
				t.Fatalf("[iter %d] 第 2 轮 prompt 失败：%v", i, err)
			}
			if !fp.sawCanaryInLast(canary) {
				t.Fatalf("[iter %d] resume 后上下文不一致：第 2 轮上游请求历史里没有上一轮的金丝雀 %s"+
					"（第 2 轮 prompt 本身不含它 ⇒ 说明 DSH resume 没把日志恢复给模型）。", i, canary)
			}
			passed++
		}()
	}
	if passed != iters {
		t.Fatalf("V1 一致性未达标：%d/%d", passed, iters)
	}
	t.Logf("V1 通过：%d/%d 次杀进程→同 sessionId resume，上下文 100%% 一致；会话日志均落本地卷", passed, iters)
}
