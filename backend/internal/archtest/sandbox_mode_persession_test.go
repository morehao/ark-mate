package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **原生权限模式按会话下发** 的守卫（P1-f 下半 / ADR-16① 的退化形态）。
//
// 探针实测 `/permission` **命令面在 ACP 上不存在**（底座从不发 `available_commands_update`），
// 所以"一个进程内按会话切换模式"这条路走不通。唯一可行的退化形态是：
//
//	模式 → 进程 env（`DSH_PERMISSION_MODE`）→ **每模式一个暖进程**
//
// 那条链条上有三处可以静默断掉，且都不报错：
//
//	① **模式没进暖进程键**：后建的会话跑在先进那个进程的模式里——用户以为选了只读，实际是完全访问；
//	② **模式没落库**：重启后 `NativeMode` 变空 → 键少一维 → 该会话悄悄换到"不注入原生权限"的
//	   进程上，沙箱边界跟着变，而界面上没有任何一行会说这件事变过；
//	③ **adapter 的 `Env()` 没人喊**：那段映射写得再完整也不生效（本轮之前它正是死代码）。
//
// 判据四段，都 fail-closed。

// TestNativeModeIsDeliveredPerSession 见文件头。
func TestNativeModeIsDeliveredPerSession(t *testing.T) {
	root := backendRoot(t)

	// ① 模式必须是**基础键**的一维（与 provider env 同一层），且注入的 env 与算键的 env 同源。
	pool := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "outbound", "runtimeconn", "pool.go")))
	keyFor, ok := goFuncBody(pool, "func (pl *Pool) KeyFor(")
	if !ok {
		t.Fatalf("`runtimeconn/pool.go` 里找不到 `KeyFor(`")
	}
	if !strings.Contains(keyFor, "nativeEnvFor(") {
		t.Fatalf("`KeyFor` 没有取原生权限 env：**模式不进暖进程键** ⇒ 后建的会话跑在先进那个" +
			"进程的模式里——用户以为选了只读，实际是完全访问")
	}
	if !strings.Contains(keyFor, "ConnKey(spec.ID, env, nativeEnv)") {
		t.Fatalf("原生权限 env 没有进 `ConnKey(`：它是**基础键**的一维（回答「这个进程的沙箱边界在哪」），" +
			"混进代际维会让诊断时分不清是哪一维变了")
	}
	if !strings.Contains(keyFor, "append(append([]string{}, env...), nativeEnv...)") {
		t.Fatalf("`KeyInputs.Env` 不是「算键那一份 + 原生 env」：注入与算键必须**同一次取值**，" +
			"分开取会得到「键说共享、env 却是另一套」的静默分叉")
	}

	// ② 模式必须落库 + 装载复原（"写了但不复原"是这一族最典型的静默失效）。
	mig := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "outbound", "sqlite", "store", "migrate_v29.go")))
	if !strings.Contains(mig, "ADD COLUMN native_mode TEXT NOT NULL DEFAULT ''") {
		t.Fatalf("v29 没有加 `native_mode` 列（或默认值不是空串）：空串 = 不注入，是刻意的平台缺省")
	}
	persist := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "persist.go")))
	// 写面与读面各出现一次（`NativeMode:` 两处）。
	if n := strings.Count(persist, "NativeMode:"); n < 2 {
		t.Fatalf("`persist.go` 里 `NativeMode:` 只出现 %d 次（写面与恢复面各需一次）："+
			"漏了恢复那一处，重启后模式变空 → 该会话悄悄换到「不注入原生权限」的进程上", n)
	}

	// ③ 恢复路径必须把它带进 `acquire`（不是只放进记录）。
	start := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "lifecycle.go")))
	if !strings.Contains(start, "l.acquire(ctx, spec, fingerprint, rec.NativeMode)") {
		t.Fatalf("恢复路径的 `acquire` 没有带 `rec.NativeMode`：重启后模式不参与算键，" +
			"沙箱边界跟着变，而界面上没有任何一行会说这件事变过")
	}

	// ④ adapter 的 `Env()` 必须有**生产调用点**（本轮之前它是死代码）。
	poolPort := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "run", "runtime_pool.go")))
	if !strings.Contains(poolPort, "adapter.Env(mode, \"\", nil)") {
		t.Fatalf("`runtime_pool.go` 里没有 `adapter.Env(`：原生权限投影会退回死代码——" +
			"映射写得再完整，没人喊它就不生效")
	}
	// 空模式必须短路（不调 adapter）：`''` 是**平台级开关**，不属于任何底座。
	if !strings.Contains(poolPort, `if mode == "" {`) {
		t.Fatalf("`NativePermissionEnv` 没有对空模式短路：`''` 的语义是「不注入原生配置」，" +
			"那是平台级开关，不该交给某个底座去解释")
	}
}
