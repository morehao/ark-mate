package acp

// V1 · resume 一致性黑盒验证（M1 出口闸）。
//
// 这是 ark-mate 侧唯一缺的那块「可重复证明」：**杀掉 worker 进程 → 用同一个原生 session id
// 重新 resume → 会话上下文 100% 还在**。它验证的是 DSH 的原生持久层（`session.vN.jsonl.zstd`
// 仅追加日志）——而 DSH 的契约是「resume 在写租约下从日志恢复上下文，不回放历史 update」
// （见 dsh-acp/README.md 的 session/resume 行）。所以「上下文有没有丢」不能靠「resume 后收到
// 了历史事件」来判（resume 本就不回放），只能用**行为**判：resume 之后发一轮需要「记得上一轮」的
// prompt，看模型侧有没有真的拿到上一轮。
//
// 为此本测试挂一个**本地假 provider**（OpenAI 兼容 `/v1/chat/completions`）：
//   - 第 1 轮 prompt 里埋一个**金丝雀 token**（`MEMTOKEN_<nonce>`），假 provider 把它记下并正常回一句；
//   - 杀进程、起新进程、`session/resume` 同一 id；
//   - 第 2 轮 prompt 问「上一轮的金丝雀是什么」，假 provider 检查**这一次收到的完整 messages 历史**里
//     有没有那只金丝雀：有 → 回 `CANARY_OK:` + token；没有 → 回 `CANARY_MISSING`。
// 于是「第 2 轮响应含 CANARY_OK 且 token 一致」= DSH resume 确实从日志恢复了上一轮上下文。
//
// 默认跳过；仅 `DSH_RESUME_PROBE=1` 时跑（与 upgrade_probe_test.go 同款纪律——真拉 dsh 进程，
// 不该在常规 `go test ./...` 里跑）。100 次循环由 `DSH_RESUME_PROBE_ITERS` 控（默认 100）。
//
// 环境变量：
//   DSH_RESUME_PROBE=1            显式开启
//   DSH_PROBE_PAYLOAD=<abs>       dsh 载荷目录（含 node_modules/@deepseek-ai/dsh/lib/bin.js）
//   DSH_PROBE_TMP=<abs>           隔离工作根（其下建私有 HOME/DSH_HOME，绝不碰用户 ~/.dsh）
//   DSH_RESUME_PROBE_ITERS=<n>    循环次数（默认 100）
//
// 隔离纪律（docs/agents/isolation.md）：私有 HOME + 私有 DSH_HOME，全在 DSH_PROBE_TMP 下，
// 收尾杀进程 + 清目录。

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

// fakeProvider 是本地 OpenAI 兼容假上游：只实现 `/v1/chat/completions` 与 `/v1/models`。
// 它按「这一次请求的完整 messages 历史里有没有金丝雀 token」决定第 2 轮的回答。
type fakeProvider struct {
	srv      *http.Server
	baseURL  string
	mu       sync.Mutex
	lastMsgs []fakeMsg // 最近一次请求里收到的历史（断言用）
	canary   string    // 本轮的期望金丝雀（由测试每次设置）
	calls    int
}

type fakeMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
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

// handleChat 解析 OpenAI chat 请求，按历史里是否含金丝雀给出确定性回答。
// 支持流式（stream:true → SSE `data:` 块 + finish_reason + [DONE]）与非流式两种，
// 因为 dsh 的 openai-completions 客户端默认走流式（非流式会报 "Stream ended without finish_reason"）。
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

	// 把整段历史拼成文本，找金丝雀。
	var hist strings.Builder
	for _, m := range req.Messages {
		hist.WriteString(msgText(m.Content))
		hist.WriteString("\n")
	}
	var answer string
	if canary != "" && strings.Contains(hist.String(), canary) {
		answer = "CANARY_OK:" + canary
	} else {
		answer = "CANARY_MISSING"
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
		"id":      "chatcmpl-probe",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "fake-echo",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": answer},
			"finish_reason": "stop",
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

func (fp *fakeProvider) setCanary(c string) {
	fp.mu.Lock()
	fp.canary = c
	fp.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeModelPatch 生成指向假 provider 的 dsh `--patch` overlay（复用平台 buildPatch 的产物形状）。
func writeModelPatch(t *testing.T, path, providerBaseURL string) {
	t.Helper()
	// 直接手写最小 patch（与 internal/infra/dsh/config.go buildPatch 的 YAML 形状一致）：
	// `- id: acp` 定默认 provider（路由键），`- id: llm-pi-ai` 声明 providers.<route>。
	// 平台 buildPatch 未导出，此处按同一形状内联（字段名与 config.go:455-492 对齐）。
	const route = "probe-route-1"
	// 关键：裸 `acp` template 的 `- id: agent-default-model` 默认 pin `deepseek-official/deepseek-flash`
	// （需要真 key）。把它**改指**到本地假 provider，会话一起来就有默认模型，无需再选。
	// 同时在 `llm-pi-ai` 里声明这条自定义 route（api + baseURL + apiKeyEnv + models 齐备）。
	patch := fmt.Sprintf(`- id: agent-default-model
  config:
    provider: %q
    model: %q
- id: acp
  config:
    provider: %q
    model: %q
- id: llm-pi-ai
  config:
    providers:
      %q:
        api: "openai-completions"
        baseURL: %q
        apiKeyEnv: %q
        models:
          - id: "fake-echo"
            name: "假回声"
            input: [text]
`, route, "fake-echo", route, "fake-echo", route, providerBaseURL, "MATE_PROBE_API_KEY")
	if err := os.WriteFile(path, []byte(patch), 0o600); err != nil {
		t.Fatalf("写模型 patch 失败：%v", err)
	}
}

// probeDial 在私有 HOME/DSH_HOME 上起一个 dsh 进程并拨通其 ACP 端点。
//
// 用 **`dsh --profile acp`**（dsh 随包模板，首用自动初始化）：它不需要 mate-acp 那套 office
// 覆写（office 缺包只该是 warning，但为让探针聚焦 resume 语义、不受 office 打包差异干扰，
// 这里用裸 acp 模板——resume / 持久化 / 日志恢复都是 base 能力，与 office 无关）。
// 模型路由由 `--patch` 注入（指向本地假 provider）。onEvent 收归一化事件。
func probeDial(t *testing.T, payload, home, cwd, patchPath string, onEvent func(Event)) (*Client, *StdioTransport) {
	t.Helper()
	// 用 PATH 上的 `dsh`（构建期固定版本，安装位置与 payload 对齐）；`--profile acp` 用裸模板。
	// 载荷 bin.js 在部分环境以裸文件方式启动不解析 ESM 裸依赖（实测），故直接用 `dsh` 启动器。
	dshHome := filepath.Join(home, "runtime", "dsh")
	tp := NewStdioTransport("dsh", []string{"--profile", "acp", "--patch", patchPath}, []string{
		"HOME=" + home,
		"DSH_HOME=" + dshHome,
		"MATE_PROBE_API_KEY=probe-fake-key",
	})
	tp.SetDir(cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: onEvent,
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, Always: true}
		},
		DialTimeout:   60 * time.Second,
		ClientName:    "mate-resume-probe",
		ClientVersion: "0",
	})
	if err != nil {
		t.Fatalf("Dial mate-acp 失败：%v（stderr=%v）", err, tp.StderrTail())
	}
	return cl, tp
}

// promptAndDrain 发一轮 prompt，等它返回（Prompt 阻塞至 run 结束）；只关心 stop reason 与错误。
func promptAndDrain(ctx context.Context, sess protocol.Session, text string) error {
	_, _, err := sess.Prompt(ctx, protocol.TextParts(text))
	return err
}

// sawCanaryInLast 判断假 provider 最近一次收到的请求历史里是否含金丝雀。
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

// selectModel 把会话的模型选到 dsh 目录里第一条候选（category=model）。裸 `acp` profile 起会话
// 后没有默认 provider/model，必须先选一次，否则 prompt 报 "no provider/model"。
// resume 之后同样要重选（原生恢复未必带回该选择——平台侧 ConnectSession 也做这个 best-effort）。
func selectModel(ctx context.Context, sess protocol.Session) error {
	var modelOpt *protocol.ConfigOption
	for i := range sess.ConfigOptions() {
		o := sess.ConfigOptions()[i]
		if o.Category == "model" {
			modelOpt = &o
			break
		}
	}
	if modelOpt == nil {
		// 没有 model 配置项 = 用 agent-default-model 的默认（本探针已在 patch 里改指假 provider）。
		return nil
	}
	if len(modelOpt.Values) == 0 {
		return fmt.Errorf("model 配置项没有候选值（patch 里的 route 没生效？current=%q）", modelOpt.CurrentValue)
	}
	_, err := sess.SetConfigOption(ctx, modelOpt.ID, modelOpt.Values[0].Value)
	return err
}

func TestResumeContextConsistencyV1(t *testing.T) {
	payload, tmp := resumeProbeEnv(t)
	iters := 100
	if s := strings.TrimSpace(os.Getenv("DSH_RESUME_PROBE_ITERS")); s != "" {
		fmt.Sscanf(s, "%d", &iters)
	}

	fp := startFakeProvider(t)

	// 每个迭代用独立的私有 home，互不污染；一次迭代内两段进程共享同一 home（= 同一 DSH_HOME，
	// 日志落在同一本地卷 → 第 2 个进程 resume 才能读到第 1 个进程写的日志）。
	passed := 0
	for i := 0; i < iters; i++ {
		i := i
		func() {
			home := filepath.Join(tmp, fmt.Sprintf("iter-%03d", i), "home")
			_ = os.RemoveAll(home)
			// 隔离纪律：每轮结束清掉本轮私有 HOME/DSH_HOME（进程已由 Close 回收）。
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
			cl1, _ := probeDial(t, payload, home, cwd, patchPath, nil)
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
			if err := promptAndDrain(ctx1, sess1, "记住这个金丝雀："+canary+"。只回 OK 即可。"); err != nil {
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
			// **杀进程**（模拟 worker 崩溃/回收）：关连接即回收子进程；日志已 flush 到本地卷。
			_ = cl1.Close()

			// —— 第 2 段进程：resume 同一 session id + 问金丝雀。要求第 2 次请求的历史里
			//      **没有直接给过金丝雀的新 prompt 文本**，金丝雀只能来自 resume 恢复的日志。 ——
			ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel2()
			cl2, _ := probeDial(t, payload, home, cwd, patchPath, nil)
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
			// 这一轮 prompt **故意不含金丝雀**：金丝雀若出现在上游历史里，只可能是 resume 从日志恢复的。
			if err := promptAndDrain(ctx2, sess2, "上一轮我给你的金丝雀 token 是什么？原样回给我。"); err != nil {
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
	t.Logf("V1 通过：%d/%d 次杀进程→同 sessionId resume，上下文 100%% 一致", passed, iters)
}
