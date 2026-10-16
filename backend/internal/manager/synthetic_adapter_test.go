package manager

import (
	"context"
	"encoding/json"
	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestSyntheticAdapterIsFirstClass 「adapter 层还在」这句话的**可执行证据**。
//
// 断言的对象是一个框架**从未听说过**的底座（`runtimetest.ID` = `rtx`，刻意不在
// `AllBuiltinSpecs()` 里、也不在三份隔离表的任何一份里）。它走完整条真实链路：
//
//	① 隔离声明 → 注册表投影 → 子进程 env（`Spec.Isolation` 是 adapter 的，父包只做机械投影）；
//	② 原生权限投影 → 内核按注册表第三张表取端口 → 子进程 env（内核不认识 `RTX_PERMISSION_MODE`）；
//	③ 清单交付端口 → 编排层调用 adapter 的 `SyncModels`（契约③：空集也必须调）；
//	④ 模型值投影端口 → 内核按第四张表取端口（它不认识 `<provider>/<model>` 这个形态）；
//	⑤ 真子进程：①② 的结果**真的落在进程 env 上**。
//
// 为什么值得一条这样的判据：产品口径是"接一个新底座 = 新建一个目录 + 写一个 `xxxSpec()`"，
// 而框架里长回一个 `switch runtimeID`（或按 id 回查声明的表）**不会让任何别的测试变红**：
// 内置底座照样全绿，只是下一个底座接不进来。这条判据把"下一个底座"提前拿出来跑一遍，
// 于是那句口径从**声明**变成**被执行过的事实**。
func TestSyntheticAdapterIsFirstClass(t *testing.T) {
	t.Helper()
	if len(runtime.AllBuiltinSpecs()) == 0 {
		t.Fatal("内置清单为空：本条判据的对照面没了")
	}
	for _, s := range runtime.AllBuiltinSpecs() {
		if s.ID == runtimetest.ID {
			t.Fatalf("合成底座 %s 不该出现在内置清单里——一旦出现，它就变成「第二个内置底座」，"+
				"「框架没见过它」这个唯一的证明力就没了", runtimetest.ID)
		}
	}

	// 构建期底座 = 合成底座（产品约束：用户不能选 runtime，装配只注册选中的那一个）。
	setBuildRuntime(t, runtimetest.ID)

	// 桩 ACP 子进程（真 python3），并把"平台实际注入的 env"落盘。
	envLog := filepath.Join(t.TempDir(), "child-env.jsonl")
	t.Setenv("STUB_ACP_ENV_LOG", envLog)
	reg, syncer := runtimetest.NewRegistry(stubACPSpec(t).Launch)

	// ① 隔离：装机期投影（生产里这一步在 `internal/app` 的装配最前面）。
	home := t.TempDir()
	applied := reg.ApplyIsolationEnv(home)
	if !reflect.DeepEqual(applied, []string{runtimetest.ID}) {
		t.Fatalf("隔离应落在合成底座上，实际 %v", applied)
	}
	spec, ok := reg.Get(runtimetest.ID)
	if !ok {
		t.Fatalf("注册表里应有 %s", runtimetest.ID)
	}
	wantEnv := map[string]string{
		runtimetest.HomeKey:  filepath.Join(home, "runtime", "rtx"),
		runtimetest.CacheKey: filepath.Join(home, "cache", "rtx-plugins"),
		// 公共 npm 面：所有本地子进程都给。
		"npm_config_cache":  filepath.Join(home, "npm", "cache"),
		"npm_config_prefix": filepath.Join(home, "npm", "global"),
	}
	for k, v := range wantEnv {
		want := k + "=" + v
		if !containsEnv(spec.Launch.Env, want) {
			t.Errorf("隔离声明 %q 没被投影进 Launch.Env=%v", want, spec.Launch.Env)
		}
	}
	// 隔离变量必须来自**这份 Spec 自己的声明**（`IsolationEnvFor`），而不是按 id 回查内置清单：
	// 回查的话上面两条 `RTX_*` 一条都不会出现（合成 id 不在内置清单里）。
	if got := runtime.IsolationEnvFor(spec, home); len(got) != len(wantEnv) {
		t.Errorf("IsolationEnvFor 应产出 %d 条，实际 %v", len(wantEnv), got)
	}

	svc := NewService(reg, eventbus.New())
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	svc.EnableStore(st)
	t.Cleanup(func() { _ = st.Close() })
	svc.providerDir = filepath.Join(t.TempDir(), "agents")

	// ② 原生权限：内核按注册表第三张表取端口，**不认识** `RTX_PERMISSION_MODE` 这个名字。
	//
	// v19：平台的权限策略来源（agent 定义）已撤销，所以这里直接调**端口契约函数**
	// `interaction.NativePermissionEnv`——它仍是"内核只做转发"这条契约的判据（见 native_permission_test.go）。
	pol := &interaction.PermissionPolicy{NativeMode: interaction.NativeModeFullAccess, Tools: map[string]string{"bash": "deny"}}
	env, _ := interaction.NativePermissionEnv(reg, runtimetest.ID, pol)
	if len(env) != 2 {
		t.Fatalf("合成底座应产出 2 条原生权限 env（模式 + 逐工具项），实际 %v", env)
	}
	if !containsEnv(env, runtimetest.PermissionEnvKey+"="+interaction.NativeModeFullAccess) {
		t.Fatalf("模式没被转发到合成底座自己的变量名上: %v", env)
	}
	if !containsEnv(env, runtimetest.InlineConfigKey+"=bash=deny") {
		t.Fatalf("逐工具项没被投影: %v", env)
	}
	// 反证：同一个策略在**没有原生权限端口**的底座上不该注入。
	if got, _ := interaction.NativePermissionEnv(reg, "stub", pol); len(got) != 0 {
		t.Fatalf("未登记原生权限面的底座不该注入，实际 %v", got)
	}

	// ③ 清单交付端口：编排层必须走到 adapter 的 SyncModels（空集也是）。
	//
	// 装配期（`EnableStore`）本来就会同步一次，所以记基线再比增量——
	// 断言绝对次数会把"装配期那次"算成重复调用，也会掩盖真正要验的"控制流走到了端口"。
	before := syncer.Calls
	if before == 0 {
		t.Fatal("装配期（EnableStore）应当已经调过一次端口——没调说明这一段链接根本没接上")
	}
	if _, err := svc.SyncRuntime(context.Background(), runtimetest.ID); err != nil {
		t.Fatalf("合成底座的清单同步不该报错: %v", err)
	}
	if syncer.Calls != before+1 {
		t.Fatalf("显式同步应让端口多被调用 1 次，实际 %d → %d", before, syncer.Calls)
	}
	if syncer.Last.Runtime != runtimetest.ID {
		t.Fatalf("端口应收到注册表键 %q，实际 %q", runtimetest.ID, syncer.Last.Runtime)
	}

	// ④ 模型值投影：内核按第四张表取端口，**不认识** `<provider>/<model>` 这个形态。
	//
	// 这段知识此前写在内核的 `switch runtimeID` 里（一个 `case` 一个内置底座 id），
	// 接第三个底座就得回去加 case。判据两侧都钉：合成底座拿到它自己的形态；而**没有登记**
	// 这一维的底座如实报 `model_value_unsupported`，内核不替它猜。
	got, err := modelValueFor(reg, runtimetest.ID, "acme", "acme-v1")
	if err != nil || got != "acme/acme-v1" {
		t.Fatalf("合成底座的模型值形态应由它自己声明（<provider>/<model>），实际 %q err=%v", got, err)
	}
	if _, err := modelValueFor(reg, "stub", "acme", "acme-v1"); errCode(err) != "model_value_unsupported" {
		t.Fatalf("未登记模型值形态的底座应报 model_value_unsupported（不猜形状），got %q", errCode(err))
	}
	// 反向：dsh 的形态（JSON 数组）**不在**这份注册表里——它由 dsh 自己登记。
	// v25：第一段是**派生路由键**（`mate-` + 平台供应商 id），不再是平台 id 本身。
	if v, err := modelValueFor(runtime.AllBuiltinRegistry(), runtime.DefaultBuildRuntime, "p", "m"); err != nil ||
		v != `["mate-p","m"]` {
		t.Fatalf("内置底座的形态应由它的 adapter 声明，实际 %q err=%v", v, err)
	}

	// ⑤ 真子进程：上面这些 env **真的**到了进程里。
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	res, err := orch.startSessionOn(context.Background(), StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("用合成底座建会话（= 真拨号 + 拉起桩子进程）: %v", err)
	}
	if strings.TrimSpace(res.SessionID) == "" {
		t.Fatal("应拿到会话 id")
	}

	childEnv := readChildEnv(t, envLog)
	if len(childEnv) == 0 {
		t.Fatalf("桩子进程没落盘 env（%s）：第 ④ 段就没验到东西", envLog)
	}
	for k, v := range wantEnv {
		if childEnv[k] != v {
			t.Errorf("子进程 env %s = %q，期望 %q（隔离投影没落到真进程上）", k, childEnv[k], v)
		}
	}
	// 原生权限 env **不在这里**断言到子进程：v19 起平台没有权限策略来源，真实拨号路径上
	// `interaction.NativePermissionEnv` 拿到的永远是 nil 策略 → 一条都不注入（第 ② 段已把端口契约钉住）。
	// 反过来说，若这里的子进程仍然出现 `RTX_PERMISSION_MODE`，那才是"某处又凭空造出了策略"。
	if v, ok := childEnv[runtimetest.PermissionEnvKey]; ok && v != "" {
		t.Errorf("没有策略来源时不该注入原生权限 env，子进程却拿到 %s=%q", runtimetest.PermissionEnvKey, v)
	}
}

// containsEnv 判断 env 数组里是否有 `KEY=VALUE` 这一项（全等，不做前缀匹配）。
func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// readChildEnv 读桩子进程落盘的那份 env（JSONL，取第一行即可）。
func readChildEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	out := map[string]string{}
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("子进程 env 落盘不是合法 JSON（%q）: %v", line, err)
	}
	return out
}
