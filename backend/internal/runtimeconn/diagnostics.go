package runtimeconn

import (
	"time"
)

// maxDiagStderrLines 外露的 stderr 诊断行数上限（超出保留最近若干行）。
const MaxDiagStderrLines = 20

// LastErr 底座最近一次失败（进程起不来/握手失败/轮次失败）的诊断快照。
//
// v19 之前这是**每 agent 一份**的 map；单底座下它天然只有一份，于是收成一个字段——
// 顺带消灭了"同一底座的两份错误记录谁更新"这类无意义状态。
//
// P3a-2：本批只把**状态**搬进来（`diagnostics.go` 的组装面随后一批跟进）。
type LastErr struct {
	At         time.Time
	SessionID  string
	Code       string
	Class      string
	Details    string
	StderrTail []string
}

// AmbiguityNote 是一条「归属歧义」留痕（时间 + 事实描述）。
type AmbiguityNote struct {
	At     time.Time
	Detail string
}

// RecordError 记录底座最近一次失败（含 stderr 诊断尾巴）。
func (pl *Plane) RecordError(e LastErr) {
	pl.diagMu.Lock()
	defer pl.diagMu.Unlock()
	pl.lastErr = &e
}

// LastError 读取最近失败快照（无 → ok=false）。
func (pl *Plane) LastError() (LastErr, bool) {
	pl.diagMu.Lock()
	defer pl.diagMu.Unlock()
	if pl.lastErr == nil {
		return LastErr{}, false
	}
	return *pl.lastErr, true
}

// ClearLastError 清除最近失败快照（例如用户手动重启底座后：同一进程里不该继续回显旧错）。
func (pl *Plane) ClearLastError() {
	pl.diagMu.Lock()
	defer pl.diagMu.Unlock()
	pl.lastErr = nil
}

// NoteScopeAmbiguous 记一条「归属歧义」留痕（elicitation 无法归属会话：wire `_meta` 未给 scope
// 且连接上无唯一在途 run）。覆盖最近一条（重复事件不堆积），随 RuntimeDiagnostics 的
// remediation 外露——这是「提问卡为什么没出现」的用户可见入口。
func (pl *Plane) NoteScopeAmbiguous(detail string) {
	pl.diagMu.Lock()
	defer pl.diagMu.Unlock()
	pl.ambiguous = &AmbiguityNote{At: time.Now().UTC(), Detail: detail}
}

// ScopeNote 读取归属歧义留痕。
func (pl *Plane) ScopeNote() (AmbiguityNote, bool) {
	pl.diagMu.Lock()
	defer pl.diagMu.Unlock()
	if pl.ambiguous == nil {
		return AmbiguityNote{}, false
	}
	return *pl.ambiguous, true
}
