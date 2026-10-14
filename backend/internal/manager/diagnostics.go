package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/redact"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// maxDiagStderrLines 外露的 stderr 诊断行数上限（超出保留最近若干行）。
const maxDiagStderrLines = 20

// runtimeLastErr 底座最近一次失败（进程起不来/握手失败/轮次失败）的诊断快照。
//
// v19 之前这是**每 agent 一份**的 map；单底座下它天然只有一份，于是收成一个字段——
// 顺带消灭了"同一底座的两份错误记录谁更新"这类无意义状态。
type runtimeLastErr struct {
	At         time.Time
	SessionID  string
	Code       string
	Class      string
	Details    string
	StderrTail []string
}

// recordRuntimeError 记录底座最近一次失败（含 stderr 诊断尾巴）。
func (s *Service) recordRuntimeError(sessionID string, ae *acl.Error, tail []string) {
	if ae == nil {
		return
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	s.lastErr = &runtimeLastErr{
		At:         time.Now().UTC(),
		SessionID:  sessionID,
		Code:       ae.Code,
		Class:      string(ae.Class),
		Details:    redact.String(ae.Details),
		StderrTail: acl.TailLines(tail, maxDiagStderrLines),
	}
}

// ambiguityNote 是一条「归属歧义」留痕（时间 + 事实描述）。
type ambiguityNote struct {
	At     time.Time
	Detail string
}

// noteScopeAmbiguous 记一条「归属歧义」留痕（elicitation 无法归属会话：wire `_meta` 未给 scope
// 且连接上无唯一在途 run）。覆盖最近一条（重复事件不堆积），随 RuntimeDiagnostics 的
// remediation 外露——这是「提问卡为什么没出现」的用户可见入口。
func (s *Service) noteScopeAmbiguous(detail string) {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	s.ambiguous = &ambiguityNote{At: time.Now().UTC(), Detail: detail}
}

// scopeNote 读取归属歧义留痕。
func (s *Service) scopeNote() (ambiguityNote, bool) {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if s.ambiguous == nil {
		return ambiguityNote{}, false
	}
	return *s.ambiguous, true
}

// runtimeLastError 读取底座的最近失败快照。
func (s *Service) runtimeLastError() (runtimeLastErr, bool) {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if s.lastErr == nil {
		return runtimeLastErr{}, false
	}
	return *s.lastErr, true
}

// RuntimeDiagnostics 返回底座的诊断快照（A3：stderr 诊断尾巴外露）。
//
// stderr_tail 来源优先级：live（当前连接子进程的实时尾巴）→ last_error（上次失败时采集的尾巴）
// → 空（无连接且无失败记录）。stderr_source 标明来源，前端/排障不必猜。
//
// 身份只有一个：`runtime`（构建期固定）。v19 之前这条的诊断对象是"某个 agent"，
// 而它内部取的运行时身份**本来就是** `runtime.SelectedRuntime()`——收口只是把外壳去掉。
func (s *Service) RuntimeDiagnostics(ctx context.Context) (*dtoruntime.DiagnosticsResp, error) {
	spec, specErr := s.selectedSpec()
	runtimeID := runtime.SelectedRuntime()
	rb := s.runtimeBindingSnapshot()

	resp := &dtoruntime.DiagnosticsResp{
		State:        rb.State,
		Runtime:      runtimeID,
		StderrTail:   []string{},
		CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		StderrSource: "none",
	}
	if resp.State == "" {
		resp.State = AgentStateStopped
	}
	specOK := specErr == nil
	if specOK {
		resp.Command = spec.Launch.Command
		resp.RuntimeName = spec.Name
	}
	if s.orch != nil {
		if cli, pid, live := s.orch.LiveClient(runtimeID); live {
			resp.Connected = true
			resp.PID = pid
			if tail := acl.TailLines(cli.StderrTail(), maxDiagStderrLines); len(tail) > 0 {
				resp.StderrTail = tail
				resp.StderrSource = "live"
			}
		}
	}
	if le, ok := s.runtimeLastError(); ok {
		resp.LastError = &dtoruntime.LastErrorResp{
			At:         le.At.Format(time.RFC3339),
			SessionID:  le.SessionID,
			Code:       le.Code,
			Class:      le.Class,
			Details:    le.Details,
			StderrTail: le.StderrTail,
		}
		if len(resp.StderrTail) == 0 && len(le.StderrTail) > 0 {
			resp.StderrTail = le.StderrTail
			resp.StderrSource = "last_error"
		}
	}
	resp.Remediation = s.remediationFor(ctx, spec, specOK, resp.Connected, resp.StderrTail, resp.LastError != nil)
	// 归属歧义留痕（若有）：诊断里给事实 + 可执行动作，而不是让提问卡无痕消失。
	if n, ok := s.scopeNote(); ok {
		resp.Remediation = append(resp.Remediation, fmt.Sprintf(
			"%s（%s）。若该 runtime 会发 elicitation 且存在多会话并发在途：让底座在 elicitation 的 `_meta` 里带 sessionId（宿主零 fork 读取，见 pkg/acl/scope.go），或串行等待提问。",
			n.Detail, n.At.Format(time.RFC3339)))
	}
	return resp, nil
}

// clearRuntimeLastError 清除最近失败快照（例如用户手动重启底座后：同一进程里不该继续回显旧错）。
func (s *Service) clearRuntimeLastError() {
	s.diagMu.Lock()
	s.lastErr = nil
	s.diagMu.Unlock()
}

// remediationFor 推导底座当前的可执行修复步骤（诊断面给「怎么修」，而不是只有报错）。
//
// 事实来源（按序合并、去重）：
//  1. runtime 未装配 / 已装配但启动命令不可执行（判定见 pkg/runtime/installed.go）；
//  2. **将被注入**的供应商档案悬空或无密钥（`CredentialProblem` 原文，已含出路）；
//  3. 已连接时底座自报的鉴权方式（caps.AuthGuidance，如「在终端执行 xxx 登录」）；
//  4. 最近失败的 stderr 呈鉴权特征 → 指向供应商档案/底座登录；
//  5. 该 runtime 声明的 SetupSteps（安装/登录步骤）——仅在未连接或已有问题时并入，
//     健康快照不掺安装说明。
//
// 不臆造命令：文本只来自上述事实来源。
func (s *Service) remediationFor(ctx context.Context, spec runtime.Spec, specOK, connected bool,
	stderr []string, hasLastErr bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(step string) {
		step = strings.TrimSpace(step)
		if step == "" || seen[step] {
			return
		}
		seen[step] = true
		out = append(out, step)
	}

	if !specOK {
		add(fmt.Sprintf("runtime %q 未装配：它由**构建期**注入（`-X pkg/runtime.BuildRuntime`），"+
			"请重新构建该二进制，或在 .ark-mate/config/runtimes.json 里声明同 id 的远程端点", runtime.SelectedRuntime()))
	} else if !runtime.Installed(spec) {
		add(fmt.Sprintf("runtime %q 的启动命令 %q 当前不可执行：安装该底座并确保它在 PATH 中（或把配置改成绝对路径）",
			spec.ID, spec.Launch.Command))
	}
	if err := s.CredentialProblem(ctx); err != nil {
		if ae, ok := acl.As(err); ok && ae != nil {
			add(ae.Details)
		}
	}
	if connected {
		for _, g := range s.authGuidanceFor() {
			desc := g.Description
			if desc == "" {
				desc = g.Name
			}
			if len(g.Command) > 0 {
				desc += fmt.Sprintf("（命令 %s）", strings.Join(g.Command, " "))
			}
			add("底座声明的鉴权方式：" + desc)
		}
	}
	if hasLastErr && looksLikeAuthFailure(stderr) {
		add("底座报告鉴权失败：给「模型」里默认那条模型的供应商补密钥，或先在底座内完成登录")
	}
	if specOK && len(spec.SetupSteps) > 0 && (!connected || hasLastErr || len(out) > 0) {
		for _, step := range spec.SetupSteps {
			add(step)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// looksLikeAuthFailure stderr 是否呈鉴权失败特征（保守匹配关键词，避免把普通报错误判成鉴权问题）。
func looksLikeAuthFailure(lines []string) bool {
	keywords := []string{"401", "403", "unauthorized", "authentication", "api key", "api_key", "invalid_api_key"}
	for _, l := range lines {
		t := strings.ToLower(l)
		for _, k := range keywords {
			if strings.Contains(t, k) {
				return true
			}
		}
	}
	return false
}

// LiveClient 返回某 runtime 当前连接（未断开时）；pid 为子进程号（0 = 未登记）。
func (o *Orchestrator) LiveClient(runtimeID string) (*acl.Client, int, bool) {
	o.mu.Lock()
	var rc *runtimeConn
	for _, c := range o.conns {
		if runtimeID != "" && c.spec.ID != runtimeID {
			continue
		}
		if c.client != nil {
			rc = c
			break
		}
	}
	o.mu.Unlock()
	if rc == nil {
		return nil, 0, false
	}
	select {
	case <-rc.client.Done():
		return nil, 0, false
	default:
		return rc.client, rc.pid, true
	}
}
