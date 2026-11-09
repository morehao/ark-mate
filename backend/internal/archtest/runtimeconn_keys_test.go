package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ——— P3a-2「连接键 / 代际键归属」守卫 ———
//
// `connKey` / `generationKey`（以及 `shortGenerationKeys` / `connCauseLabel`）
// 是**纯函数**，已落 `internal/drivers/runtimeconn`（P3a-2 第三刀）。
//
// 它们决定"哪些会话共享一个暖进程"，是执行面最要紧的一条判据。两份实现的后果**不是报错**，
// 而是静默分叉：迁移判据说"不用换进程"、而 `acquire` 换了 —— 表现为改了能力选择之后
// 一部分会话仍跑在旧配置的进程上，而所有既有测试照旧绿。
//
// 判据两段，fail-closed：
//
//	① `runtimeconn` 必须真的有那四个导出入口（改名 = 守卫失锚）；
//	② manager 侧不得再出现派生**本体**的痕迹（拼接形态、长度前缀、末段切分）。
//
// 反证（实跑）：把 `generationKey` 的长度前缀拼装搬回 `manager/connection.go` → 红。

// keyDerivationMarkers 派生本体的痕迹（只该出现在 `runtimeconn/keys.go`）。
var keyDerivationMarkers = []string{
	`"gen:" + strconv.Itoa(len(`,
	`strings.Join(parts, "\x00")`,
	`strings.LastIndex(k, ":")`,
	// 第五十四刀：**取键点**也只许有一处（池的 `KeyFor`）。在 manager 里再算一次键——
	// 哪怕只是 `key = connKey(...)` 这一行——都会让"哪一把是权威键"重新有两个答案。
	// 判据只钉"自己拼键"的形态：调用 `o.pool.KeyFor(` 是**消费**那一处实现，恰恰是本刀要的形状。
	"ConnKey(",
	"GenerationKey(",
	"key = connKey(",
}

// TestConnectionKeysLiveInRuntimeconn 见文件头。
func TestConnectionKeysLiveInRuntimeconn(t *testing.T) {
	root := backendRoot(t)
	src := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "drivers", "runtimeconn", "keys.go")))
	for _, a := range []string{
		"func ConnKey(runtimeID string, envs ...[]string) string",
		"func GenerationKey(connKey, fingerprint, configDigest string) string",
		"func ShortGenerationKeys(keys []string) []string",
		"func ConnCauseLabel(intentional bool) string",
	} {
		if !strings.Contains(src, a) {
			t.Errorf("internal/drivers/runtimeconn/keys.go 里找不到 %q：连接键/代际键的派生被改名或搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}

	// 池侧：取键点在 `Pool.KeyFor`（与键的派生同包，且**只此一处**）。
	pool := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "drivers", "runtimeconn", "pool.go")))
	if !strings.Contains(pool, "func (pl *Pool) KeyFor(") {
		t.Error("internal/drivers/runtimeconn/pool.go 里找不到 `func (pl *Pool) KeyFor(`：取键点被改名/搬走了？" +
			"（这条守卫的判据挂在它身上，失锚等于空转）")
	}

	// manager 侧：**全部**生产文件（第五十四刀起不再有"转发文件"豁免——`connection.go` 里
	// 那五个键派生转发已随本刀删除）。
	dir := filepath.Join(root, "internal", "sessionruntime")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", dir)
	}
	for _, path := range files {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, m := range keyDerivationMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/sessionruntime/%s 里出现连接键/代际键的派生本体或取键点 %q：它**只有一份实现**，"+
					"在 `internal/drivers/runtimeconn`（P3a-2 第三刀派生、P4a 第五十四刀取键）。\n"+
					"两份的后果不是报错而是静默分叉：迁移判据说「不用换进程」而 acquire 换了——"+
					"表现为改了能力选择之后一部分会话仍跑在旧配置的进程上。", filepath.Base(path), m)
			}
		}
	}
}
