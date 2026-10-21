package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P1a-2 定案：事件出口（唯一铸号点）归 `domain/session` ———
//
// 计划附录 A 早先把 `publish.go` 归到 `mechanism/publish`（"机制：事件身份唯一铸号点 + 事件出口"）。
// P3b-2a 实际把它搬进了 `domain/session`，**这条口径必须在 P4 之前定案**——否则下一个人照着
// 附录 A 建一个 `mechanism/publish`，会把"事件的唯一铸号点"从会话域搬走。
//
// 定案的结论是**留在会话域**，理由是它与机制层的定义不符（机制层是"被 ≥3 个域共享的、无业务规则
// 的跨域设施"）：
//
//   - 它**写 `events` 表**（事务内取会话内位次）并**读会话记录**（标题自动推导、runs 收口）——
//     这两件事都是会话域的不变量，不是跨域设施；
//   - 它同时是**事件身份的唯一铸号点**（`objid.New(objid.Event)`）与 transcript 旁路、脱敏闸门的
//     汇口：搬进机制层等于让机制层认识 `SessionRec`，而机制层守卫
//     （`TestMechanismMustNotImportUpward`）明令它不得 import `domain/*`。
//
// 判据两段，fail-closed：
//
//	① 运行时唯一铸号点所在的文件必须在 `internal/domain/session`（迁移里的历史回填除外——
//	   那是历史数据的搬运，不是第二条运行时铸号路径，G2 的 ⑦′ 已单独钉它的分部）；
//	② `internal/adapters/outbound/mechanism/` 下不得出现 `publish` / `event_sink` 这类包（附录 A 的旧口径若被照做，
//	   一定会落在这种目录名上）。
//
// 反证（实跑）：把 `publish.go` 从 `domain/session` 挪到 `mechanism/publish/` → 红（两段都红）。

// runtimeMintRe 运行时的事件铸号点（与 G2 的判据同源）。
var runtimeMintRe = regexp.MustCompile(`objid\.New\(objid\.Object\)|objid\.New\(objid\.Event\)`)

// TestEventExitStaysInSessionDomain 见文件头：事件出口（唯一铸号点）必须留在会话域。
func TestEventExitStaysInSessionDomain(t *testing.T) {
	root := backendRoot(t)
	internalDir := filepath.Join(root, "internal")
	files := productionGoFiles(t, internalDir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", internalDir)
	}

	var runtimeMint []string
	for _, path := range files {
		// 迁移里的历史回填不算运行时铸号路径（G2 的 ⑦′ 单独钉它的分部）。
		if strings.Contains(filepath.Base(path), "migrate_v") {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, path))
		if runtimeMintRe.MatchString(src) {
			runtimeMint = append(runtimeMint, filepath.ToSlash(rel(root, path)))
		}
	}
	if len(runtimeMint) == 0 {
		t.Fatal("全树找不到运行时的事件铸号点（`objid.New(objid.Event)`）：判据失锚（铸号点被改名/搬走了？）")
	}
	for _, f := range runtimeMint {
		if !strings.HasPrefix(f, "internal/domain/session/") {
			t.Errorf("运行时的事件铸号点出现在 %s：它必须留在 `internal/domain/session`（P1a-2 定案）。\n"+
				"它写 `events` 表（事务内取会话内位次）并读会话记录，是会话域的不变量；"+
				"搬进机制层等于让机制层认识会话类型，而机制层守卫明令它不得 import `domain/*`。\n"+
				"计划附录 A 里那条「mechanism/publish」是**已被推翻的旧口径**（见 §3.1.53）。", f)
		}
	}

	// ② `mechanism/` 下不得出现附录 A 那种包名（照做旧口径一定会落在这两个名字上）。
	mechDir := filepath.Join(internalDir, "adapters", "outbound", "mechanism")
	for _, sub := range subdirs(t, mechDir) {
		name := filepath.Base(sub)
		if name == "publish" || name == "event_sink" {
			t.Errorf("`internal/adapters/outbound/mechanism/%s` 存在：事件出口**不在机制层**（P1a-2 定案）。\n"+
				"它写 `events` 表并读会话记录——那是会话域的不变量，不是\"被 ≥3 个域共享的跨域设施\"。\n"+
				"请把实现留在 `internal/domain/session`（`publish.go`）。", name)
		}
	}
}
