package sessionruntime

// manager 测试夹具的唯一建造点。
//
// 此前包内有三个各自拼装的脚手架（newTestRig / testReapHarness / newSearchRig），
// 外加约二十处测试里手写的 `NewService(...) + NewOrchestrator(...) + t.Cleanup(orch.Close)`
// 组合。它们的差异其实只有四个开关：注册表装哪些 runtime、要不要事件总线、要不要 sqlite
// 库、预置哪些 agent。散在多处时，"关闭顺序"这类全局约定（编排器必须先于库关闭）就没人
// 负责，加一个资源也容易只在某一个 rig 里登记清理。
//
// 这里收成一个 newRig + 三个具名预设：**登记清理与接线只有一处**。三个预设保留原签名，
// 让既有调用点零改动——预设名本身也是意图文档（"这是跑 reaper 的架子"）。
//
// 为什么组合留在包内、而 internal/testutil 只放通用夹具：manager 的测试是内部测试包
// （`package manager`），内部测试包不能 import 一个反向 import manager 的包
// （`import cycle not allowed in test`）。详见 internal/testutil 的包注释。

import (
	"context"

	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/mechanism/credential"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/ark-mate/pkg/credentials"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// providerTestSvc 建一个三 spec 的注册表（dsh + 合成的 rtx + 无凭据声明的 stub），并把
// provider 目录设到 providerDir。不拨真实子进程。
//
// 第二、三个 spec 里，rtx 由 `internal/testutil/runtimetest` **合成**（见那里的包注释）：它带着
// "走 env 通道交付密钥"的凭据声明与"`<provider>/<model>` 形态"的模型值实现——这两条链路在真实
// adapter 里原本由第二个真实 adapter 承担，它下线后**只由这个夹具覆盖**。dsh 那一份**直接取真实
// adapter 的**（不手抄路径/模板：手抄会把底座的私有知识带进内核，守卫会判红，而且迟早漂移）。
//
// v19 起它只做"注册表 + provider 目录"两件事：agent 定义面撤销后，没有定义要播种了。
func providerTestSvc(t *testing.T, providerDir string) *Service {
	t.Helper()
	real := runtime.AllBuiltinRegistry()
	dshSpec, ok := real.Get("dsh")
	if !ok {
		t.Fatal("默认注册表里应有 dsh")
	}
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "dsh", Name: "dsh", Credential: dshSpec.Credential},
		runtimetest.Spec(runtime.LaunchSpec{}),
		runtime.Spec{ID: "stub", Name: "Stub"},
	)
	svc := NewService(reg, eventbus.New())
	if providerDir != "" {
		// 与装配期同一条路径：先装配凭据分层（镜像落在 provider 目录的父目录下，即"mate home"），
		// 再装载 `providers.json`（它顺带登记定义目录）。顺序与 `internal/app` 逐条同形——
		// 反过来，memory 模式下装载时读到的密钥就会按"凭据面未装配"处理。
		svc.planes.Credentials.SetLayered(newTestCredentials(t, filepath.Dir(providerDir)))
		svc.planes.Providers.LoadProviderDefinitions(providerDir)
	}
	return svc
}

// newTestCredentials 装配一份**密闭**的四层凭据面：镜像落在 home 下，"继承环境"那一层
// 恒空（不读测试机自己的环境变量——真实装配读 `os.Getenv`，那会让断言随开发机的环境漂移）。
//
// 想验"继承环境遮住面板里那条"的用例自己给 `Env` 换一个取值函数（见 providers_key_test.go）。
func newTestCredentials(t *testing.T, home string) *credentials.Layered {
	t.Helper()
	cwd := filepath.Join(home, "cwd")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	l, _, err := credential.OpenCredentialSource(home, cwd)
	if err != nil {
		t.Fatalf("装配凭据面: %v", err)
	}
	l.Env = func(string) string { return "" }
	return l
}

// ptrOf 取指针的通用小工具（白名单/名字这类"可选字段"在夹具里到处要）。
//
// 它原本住在已被删除的 agent 夹具里；形状与 `provider_store_test.go` 的 `ptr[T]` 相同，
// 但那个名字是本包既有用例的自留地，不合并以免动到无关文件。
func ptrOf[T any](v T) *T { return &v }

// setBuildRuntime 把构建期底座设成 id，用例结束自动还原。
//
// 夹具用的是 stub-only 注册表（id="stub"），而 runtime.SelectedRuntime() 是编译期常量
// （默认 dsh）——不覆盖它，任何 ResolveRuntime 都会报"未装配"。
func setBuildRuntime(t *testing.T, id string) {
	t.Helper()
	old := runtime.BuildRuntime
	runtime.BuildRuntime = id
	t.Cleanup(func() { runtime.BuildRuntime = old })
}

// rigConfig 夹具的差异面（零值即"最裸的架子"：空注册表、无总线、无库）。
type rigConfig struct {
	stub  bool // 注册表装测试专用 stub runtime（false = 空注册表）
	bus   bool // 建事件总线（false = 传 nil，纯列表/纯状态用例不需要它）
	store bool // 开临时 sqlite 库并 EnableStore
}

// rig 一次夹具建造的全部产物。用结构体而不是多返回值：预设各自只取需要的字段，
// 而新增一个产物（比如 transcript 目录）不会波及任何调用点。
type rig struct {
	Svc  *Service
	Orch *Orchestrator
	Bus  *eventbus.Bus
	St   *store.Store
}

// newRig 建造夹具：注册表 → 总线 → Service → （可选库）→ agent → Orchestrator，并登记清理。
//
// **清理顺序是这里唯一的全局约定，且靠 t.Cleanup 的后进先出表达**：
// Orchestrator 的清理最后登记、最先执行，库的清理最先登记、最后执行——先停编排器再关库，
// 避免在途写库落到已关闭的句柄上。原先三个脚手架各自手写这个顺序，漏一个就是偶发报错。
func newRig(t *testing.T, mutate ...func(*rigConfig)) *rig {
	t.Helper()
	cfg := rigConfig{}
	for _, m := range mutate {
		m(&cfg)
	}

	reg := runtime.NewRegistry()
	if cfg.stub {
		reg = testutil.StubRegistry()
		// 注册表里只有 stub，而底座是构建期常量：不把它设成 stub，本夹具上任何
		// 「解析底座」都会报 runtime_not_found（那不是这些用例要验的东西）。
		setBuildRuntime(t, "stub")
	}
	var bus *eventbus.Bus
	if cfg.bus {
		bus = eventbus.New()
	}

	svc := NewService(reg, bus)

	var st *store.Store
	if cfg.store {
		st = testutil.OpenStore(t, testutil.TempDBPath(t))
		svc.EnableStore(st)
		t.Cleanup(func() { _ = st.Close() })
	}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	return &rig{Svc: svc, Orch: orch, Bus: bus, St: st}
}

// beginRun 复刻 `Orchestrator.Prompt` 的**开轮三连**（v22，设计 §D2）：
// 发布 `run.started` → 落 running 行 → 发布用户消息。
//
// 为什么直发事件的用例必须用它：`runs` 收口的窗口起点**由开轮行给出**（`first_seq`），没有那
// 一行就没有窗口——终态事件到达时物化只会打一条 WARN 然后跳过。走 `Prompt` 要真起 runtime，
// 所以这里把三连按 Prompt 里的顺序原样复刻一份（顺序本身就是契约：开轮行要写 first_seq，
// 而 first_seq 就是 `run.started` 的位次）。
func beginRun(t *testing.T, svc *Service, sessionID, text string) (runID string, started, user eventbus.Event) {
	t.Helper()
	runID = objid.New(objid.Run)
	started = svc.sessionPlane.Publish(session.BusRunStarted, sessionID, map[string]any{
		"session_id": sessionID, "run_id": runID, "runtime": "stub", "text": text,
	})
	svc.sessionPlane.OpenRun(sessionID, runID, text, started.Seq)
	user = svc.sessionPlane.Publish(session.BusUserMessage, sessionID, map[string]any{
		"session_id": sessionID, "runtime": "stub", "text": text,
	})
	return runID, started, user
}

// endRun 发布一个终态事件，载荷与真实路径同形（带该轮身份 `run_id`）。
func endRun(svc *Service, sessionID, runID, typ string, extra map[string]any) eventbus.Event {
	payload := map[string]any{"session_id": sessionID, "run_id": runID, "runtime": "stub"}
	for k, v := range extra {
		payload[k] = v
	}
	return svc.sessionPlane.Publish(typ, sessionID, payload)
}

// stubACPSpec 一个**真子进程**的最小 ACP responder（python3），只实现本包测试需要的两个方法：
//
//	initialize  → 协议版本 + 空能力声明
//	session/new → 回一个稳定可预期的 sessionId
//
// 为什么不用 `testutil.StubRegistry()`：那个 spec 没有 Launch，一旦测试真的走到"拨号"就会以
// `spawn_failed` 失败（它服务的是纯列表/状态类用例）。凡是需要**真连接**（连接池、暖进程复用、
// 停机拆连接）的用例都要真子进程——而"连接池里到底有几条连接"恰恰只能在真拨号之后才有意义。
//
// 需要定制 stderr/握手行为的用例（如诊断的实时尾巴）自带脚本，不复用本函数。
func stubACPSpec(t *testing.T) runtime.Spec {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "acp_stub.py")
	code := `import json, os, sys
n = 0
# STUB_ACP_ENV_LOG（可选）：进程一进来就把平台注入的 RTX_/MATE_/DSH_ 前缀 env 落成一行 JSON。
# 断言"平台把哪些 env 真的交给了子进程"只能看这里——进程是平台拉起的，隔离表与原生权限投影
# 的作用点都在子进程 env 上；只在管理侧断言等于没验（env 名字写错不会让任何东西变红）。
envlog = os.environ.get("STUB_ACP_ENV_LOG")
if envlog:
    try:
        with open(envlog, "a") as fh:
            keys = sorted(k for k in os.environ if k.startswith(("RTX_", "MATE_", "DSH_", "npm_config_")))
            fh.write(json.dumps({k: os.environ[k] for k in keys}) + "\n")
    except Exception:
        pass
# STUB_ACP_LOG（可选）：把每次 session/new 收到的参数按行追加成 JSONL。
# 断言"底座到底收到了哪套 MCP/能力"只能看这里——管理侧算得再对，下发的若是旧集合，
# 用户拿到的就是旧能力（M3 要消灭的正是这类静默失败）。
logpath = os.environ.get("STUB_ACP_LOG")
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    method, mid = msg.get("method"), msg.get("id")
    if mid is None:
        continue  # 通知：没有 id，无需响应
    if method == "initialize":
        res = {"protocolVersion": 1, "agentCapabilities": {}}
    elif method == "session/new":
        n += 1
        if logpath:
            try:
                with open(logpath, "a") as fh:
                    fh.write(json.dumps(msg.get("params") or {}) + "\n")
            except Exception:
                pass
        res = {"sessionId": "stub-session-%d" % n}
    else:
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid,
            "error": {"code": -32601, "message": "method not found: %s" % method}}) + "\n")
        sys.stdout.flush()
        continue
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": res}) + "\n")
    sys.stdout.flush()
`
	if err := os.WriteFile(script, []byte(code), 0o600); err != nil {
		t.Fatalf("写 ACP 桩脚本失败: %v", err)
	}
	// v12：夹具 spec 也带**凭据交付面声明**——"要不要把密钥注入子进程 env"由各底座自己的
	// 声明决定（ADR-4），没有声明的 runtime 平台一律不注入，`providerEnvFor` 会回 nil。
	return runtime.Spec{ID: "stub", Name: "Stub", Launch: runtime.LaunchSpec{Command: "python3", Args: []string{script}},
		Credential:      resource.CredentialSpec{RefPattern: "MATE_%s_API_KEY", FallbackEnv: true},
		CapabilityScope: runtime.CapabilityScopeProcess}
}

// newTestRig 最裸的架子：空注册表 + 事件总线，无库、无 agent。
//
// 空注册表是有意的——用它的用例都不需要真的拉起 runtime（列表/状态/权限/transcript 等
// 面），注册表里多一个 spec 反而会让"解析不到 runtime 时怎么办"这类断言失去意义。
func newTestRig(t *testing.T) (*Service, *Orchestrator, *eventbus.Bus) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) { c.bus = true })
	return r.Svc, r.Orch, r.Bus
}

// testReapHarness 跑孤儿回收/空闲回收的架子：stub 注册表（构建期底座 = stub）。
// 无事件总线（回收路径不上报事件，只落运行时诊断）。
//
// v19 起没有"先造一个 agent 才能回收"这回事：进程归属就是 runtime，登记与回收只看它。
func testReapHarness(t *testing.T) (*Orchestrator, *Service) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) { c.stub = true })
	return r.Orch, r.Svc
}

// newSearchRig 跑跨会话检索/导出的架子：stub 注册表 + 事件总线 + 临时 sqlite 库。
//
// 总线与库都要：消息读模型的物化是「域事件 → store」这条链的产物，缺任一端都验不到真东西。
func newSearchRig(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) {
		c.stub, c.bus, c.store = true, true, true
	})
	return r.Svc, r.St
}

// transcriptPlane 取 Service 的会话域（**测试专用**：P3b-2b 把 transcript recorder 整体搬进
// 域后，`Service.tx` 字段就没了——这些装配级用例仍要经"真 Service + 真域"这条链造 transcript
// 夹具，所以由这里提供唯一的可达入口）。
//
// 它刻意是**测试文件里的包内助手**而不是 Service 的导出方法：生产装配不需要"把域拿出来"，
// 加一个导出 getter 等于给 P4b 留一条新的一行转发。
func transcriptPlane(svc *Service) *session.Plane { return svc.sessionPlane }

// seedModelRow 直接往库里插一条模型行（**绕开写面**），返回行 id。
//
// 写面（`CreateModel`/`UpdateModel`/`DeleteModel`）已搬进 `service/svcmodel`（P4a 正文第五刀）：
// 还留在 manager 的用例要的是"清单里有一条（甚至是默认那条）"这个**前提**，不是写面的行为
// ——写面自己的判据跟着实现走（`internal/service/svcmodel/declared_test.go`）。
func (r *genRig) seedModelRow(t *testing.T, providerID, model string, isDefault bool) string {
	t.Helper()
	ctx := context.Background()
	id := objid.New(objid.Model)
	ok, err := r.svc.planes.Resources.Store().CreateModelDefinition(ctx, store.ModelColumns{
		ID: id, ProviderID: providerID, Model: model, InputModalities: resource.ModalityText,
	}, definition.DefinitionWriteActor)
	if err != nil || !ok {
		t.Fatalf("插模型行 %s/%s: ok=%v err=%v", providerID, model, ok, err)
	}
	if isDefault {
		if err := r.svc.planes.Resources.Store().SetDefaultModelID(ctx, id); err != nil {
			t.Fatalf("置默认模型 %s: %v", id, err)
		}
	}
	return id
}

// setBusy 在测试里模拟「有/没有在途轮次」：走真实状态机入口，不直接写句柄的私有字段。
//
// 直接置位曾经可行（同在 `package manager`），但那让测试依赖「busy 只是一个 bool」这个
// 实现细节；搬进 `domain/session` 之后，状态的唯一写口就是 BeginRun/FinishRun。
func setBusy(as *session.ActiveSession, busy bool) {
	if busy {
		_, _, _ = as.BeginRun(context.Background())
		return
	}
	as.FinishRun()
}
