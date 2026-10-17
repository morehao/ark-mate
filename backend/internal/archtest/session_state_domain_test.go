package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件钉住**会话运行期状态归会话域**（P4a 正文第五十五刀）。
//
// 这一刀把 `ActiveSession` / `Run` / 句柄表与三个半块（事件出口、排队投递、轮次收口）从
// `internal/manager` 搬进 `internal/domain/session`。失效方式全是**静默**的：
//
//	① **状态搬一半**：句柄表留在 manager、`Runner` 在域里各存一份 ⇒ 两份在途 / 两份队列，
//	   并发下表现为重复投递或丢投递，而编译、日志、单会话串行用例全部正常；
//	② **实现搬走了、转发/字段还在**：`manager` 里再长出一个 `o.sessions` 或直接写 `as.busy`，
//	   "这段实现到底在哪一层"重新不可见；
//	③ **端口没接上**：`Runner` 造出来了但 `Acquire` / `Busy` / `Diagnostics` 是 nil ——
//	   事件出口恒空、队列恒不投，只有端到端用例才现形（而且形似"模型没回消息"）。
//
// 判据三段（都 fail-closed）：域侧锚必须在；manager 生产代码零命中旧状态字段；接线点正判据。

// TestSessionStateLivesInSessionDomain 见文件头。
func TestSessionStateLivesInSessionDomain(t *testing.T) {
	root := backendRoot(t)

	// ① 域侧锚：状态、双索引、四个运行期动作都必须在会话域里。
	anchors := map[string][]string{
		filepath.Join("internal", "domain", "session", "active.go"): {
			"type ActiveSession struct {",
			"type Handles struct {",
			"func (as *ActiveSession) BeginRun(",
			"func (as *ActiveSession) FinishRun(",
			"func (as *ActiveSession) MarkDead(",
			"func (h *Handles) Put(",
			"func (h *Handles) ResolveProvider(",
			"func (h *Handles) ForClient(",
		},
		filepath.Join("internal", "domain", "session", "runner.go"): {
			"func (r *Runner) Prompt(",
			"func (r *Runner) executeRun(",
			"func (r *Runner) DeliverQueued(",
			"func (r *Runner) CancelRun(",
		},
		filepath.Join("internal", "domain", "session", "eventsink.go"): {
			"func (r *Runner) EventSink(",
		},
	}
	for rel, wants := range anchors {
		body := stripLineComments(readFileOrFatal(t, filepath.Join(root, rel)))
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s 里找不到 %q：运行期状态机必须整族在会话域里（搬一半就是两份在途/两份队列）",
					rel, want)
			}
		}
	}

	// ② manager 生产代码零命中旧状态：句柄表、双索引、私有字段都不许长回来。
	files := productionGoFiles(t, filepath.Join(root, "internal", "manager"))
	if len(files) == 0 {
		t.Fatal("`internal/manager` 下扫不到任何生产文件——判据失锚（目录改名/搬走了？）")
	}
	forbidden := []string{
		"type ActiveSession struct",
		"type Run struct",
		"sessions map[string]*",
		"byProvider map[string]string",
	}
	for _, path := range files {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s 里出现 %q：运行期状态已经归 domain/session，在 manager 再留一份就是第二个事实源"+
					"（两份在途/两份队列不会让任何编译或单会话用例变红）", rel(root, path), bad)
			}
		}
		// `o.sessions[...]` / `o.byProvider[...]` 形态的访问同样零容忍（夹具除外：本守卫只扫生产文件）。
		if strings.Contains(body, ".sessions[") || strings.Contains(body, ".byProvider[") {
			t.Errorf("%s 里仍按旧句柄表访问（`.sessions[` / `.byProvider[`）：请走 `handles.Get/Put/ResolveProvider`",
				rel(root, path))
		}
	}

	// ③ 接线正判据：只删旧字段不够——`Runner` 造出来却没接上时，事件出口恒空/队列恒不投。
	wiring := map[string][]string{
		filepath.Join("internal", "manager", "orchestrator.go"): {
			"session.NewHandles(",
			"session.NewRunner(",
			"Acquire:      o.acquireSession,",
			"Busy:         o.inflight,",
		},
		filepath.Join("internal", "manager", "connection.go"): {
			"o.runner.EventSink(",
			"o.handles.ForClient(",
		},
		filepath.Join("internal", "manager", "session_start.go"): {
			"o.handles.Put(",
		},
		filepath.Join("internal", "manager", "run.go"): {
			"o.runner.Prompt(",
			"o.runner.CancelRun(",
		},
	}
	for rel, wants := range wiring {
		body := stripLineComments(readFileOrFatal(t, filepath.Join(root, rel)))
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s 里找不到 %q：端口没接上时状态机照常构造，但事件出口/队列投递会静默失效",
					rel, want)
			}
		}
	}
}
