package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ——— P3a-2「连接键 / 代际键归属」守卫 ———
//
// `connKey` / `generationKey`（以及 `shortGenerationKeys` / `connCauseLabel` / `dedupeStrings`）
// 是**纯函数**，已落 `internal/runtimeconn`（P3a-2 第三刀）。
//
// 它们决定"哪些会话共享一个暖进程"，是执行面最要紧的一条判据。两份实现的后果**不是报错**，
// 而是静默分叉：迁移判据说"不用换进程"、而 `acquire` 换了 —— 表现为改了能力选择之后
// 一部分会话仍跑在旧配置的进程上，而所有既有测试照旧绿。
//
// 判据两段，fail-closed：
//
//	① `runtimeconn` 必须真的有那五个导出入口（改名 = 守卫失锚）；
//	② manager 侧不得再出现派生**本体**的痕迹（拼接形态、长度前缀、末段切分）。
//
// 反证（实跑）：把 `generationKey` 的长度前缀拼装搬回 `manager/connection.go` → 红。

// keyDerivationMarkers 派生本体的痕迹（只该出现在 `runtimeconn/keys.go`）。
var keyDerivationMarkers = []string{
	`"gen:" + strconv.Itoa(len(`,
	`strings.Join(parts, "\x00")`,
	`strings.LastIndex(k, ":")`,
}

// TestConnectionKeysLiveInRuntimeconn 见文件头。
func TestConnectionKeysLiveInRuntimeconn(t *testing.T) {
	root := backendRoot(t)
	src := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "runtimeconn", "keys.go")))
	for _, a := range []string{
		"func ConnKey(runtimeID string, envs ...[]string) string",
		"func GenerationKey(connKey, fingerprint, configDigest string) string",
		"func ShortGenerationKeys(keys []string) []string",
		"func ConnCauseLabel(intentional bool) string",
		"func DedupeStrings(in []string) []string",
	} {
		if !strings.Contains(src, a) {
			t.Errorf("internal/runtimeconn/keys.go 里找不到 %q：连接键/代际键的派生被改名或搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}

	// manager 侧：转发文件里不得再有派生本体。
	dir := filepath.Join(root, "internal", "manager")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", dir)
	}
	for _, path := range files {
		if filepath.Base(path) == "connection.go" {
			continue // 转发文件（只许是 `return runtimeconn.Xxx(...)`）
		}
		body := stripLineComments(readFileOrFatal(t, path))
		for _, m := range keyDerivationMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/manager/%s 里出现连接键/代际键的派生本体 %q：它**只有一份实现**，"+
					"在 `internal/runtimeconn/keys.go`（P3a-2 第三刀）。\n"+
					"两份的后果不是报错而是静默分叉：迁移判据说「不用换进程」而 acquire 换了——"+
					"表现为改了能力选择之后一部分会话仍跑在旧配置的进程上。", filepath.Base(path), m)
			}
		}
	}

	// connection.go 只许转发（不得出现上面的痕迹）。
	conn := stripLineComments(readFileOrFatal(t, filepath.Join(dir, "connection.go")))
	for _, m := range keyDerivationMarkers {
		if strings.Contains(conn, m) {
			t.Errorf("internal/manager/connection.go 里出现派生本体 %q：它只剩转发（见文件头注释）。", m)
		}
	}
}
