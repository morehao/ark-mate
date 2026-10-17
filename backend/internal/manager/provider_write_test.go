package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"github.com/morehao/ark-mate/pkg/resource"
)

// ——— v11 provider 写面：CRUD 的失败面、写面顺序与补偿、生效回执 ———
//
// A1 的四条端点 × 成功/冲突/不存在/被引用，错误码逐条等于设计表：
//
//	已存在          → 40901 provider_exists
//	不存在（PATCH） → 40401 provider_not_found
//	被引用（DELETE）→ 40901 provider_in_use + referenced_by_models[]（见 model_list_test.go）
//	id 非法         → 40001 invalid_provider_id

func TestProviderWriteFaceCRUD(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	r := newRig(t) // 带编排器（生效面回执需要它）
	svc := r.Svc
	// 凭据面：memory 模式下托管文档就是密钥的事实源（与装配期同一条路径）。
	svc.creds.SetLayered(newTestCredentials(t, home))
	svc.providerPlane.SetProviderDir(dir)

	key := "sk-abc"
	// ① POST 成功。
	resp, err := svc.providerPlane.CreateProvider(ctx, func() dtoprovider.ProviderCreateReq {
		r := providerReq("deepseek-main", &key)
		r.Note = ptr("主账号")
		return r
	}())
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if resp.ID != "deepseek-main" || resp.Protocol != "openai" || resp.BaseURL == "" || !resp.HasKey || resp.Note != "主账号" {
		t.Fatalf("新建响应不符: %+v", resp)
	}
	if resp.Revision != 0 {
		t.Fatalf("memory 模式的 revision 应为 0（文件载体没有版本号），got %d", resp.Revision)
	}
	// ② POST 冲突 → provider_exists(40901)。
	if _, err := svc.providerPlane.CreateProvider(ctx, providerReq("deepseek-main", nil)); errCode(err) != "provider_exists" {
		t.Fatalf("重复新建应 provider_exists，got %q（%v）", errCode(err), err)
	}
	// ③ id 非法 → invalid_provider_id。
	for _, id := range []string{"", "../x", "a/b"} {
		if _, err := svc.providerPlane.CreateProvider(ctx, providerReq(id, nil)); errCode(err) != "invalid_provider_id" {
			t.Fatalf("id %q 应 invalid_provider_id，got %q", id, errCode(err))
		}
	}
	// ④ PATCH 不存在 → provider_not_found(40401)。
	if _, err := svc.providerPlane.UpdateProvider(ctx, "nope", dtoprovider.ProviderPatchReq{BaseURL: ptr("https://api.example.com/v1")}); errCode(err) != "provider_not_found" {
		t.Fatalf("PATCH 不存在应 provider_not_found，got %q（%v）", errCode(err), err)
	}

	// ⑤ PATCH 只改 note → effect=none（不驱动行为的字段不许说"下一轮生效"）。
	done, err := svc.providerPlane.UpdateProvider(ctx, "deepseek-main", dtoprovider.ProviderPatchReq{Note: ptr("改了备注")})
	if err != nil {
		t.Fatalf("PATCH note: %v", err)
	}
	if done.Note != "改了备注" || done.Effect == nil || done.Effect.Effect != "none" {
		t.Fatalf("只改 note 应回 effect=none: %+v", done.Effect)
	}
	if done.Effect.Channel != "dial_env" {
		t.Fatalf("通道应为 dial_env: %+v", done.Effect)
	}

	// ⑥ PATCH 端点 → effect=next_run（驱动行为的字段）。
	changed, err := svc.providerPlane.UpdateProvider(ctx, "deepseek-main", dtoprovider.ProviderPatchReq{BaseURL: ptr("https://api.other.example/v1")})
	if err != nil {
		t.Fatalf("PATCH base_url: %v", err)
	}
	if changed.Effect == nil || changed.Effect.Effect != "next_run" {
		t.Fatalf("改端点应回 effect=next_run: %+v", changed.Effect)
	}
	// 引用名**只跟 id 有关**（逐供应商派生）——这正是 A12 要的不变量。
	// 这里用纯函数断言（夹具的注册表是空的，`providerEnvFor` 需要一个声明了 `RefPattern`
	// 的 runtime 才有意义；那条路径由 `TestProviderRegistryWriteOnceReadNever` 端到端覆盖）。
	ref := resource.RefFor("MATE_%s_API_KEY", "deepseek-main")
	if !strings.HasPrefix(ref, "MATE_DEEPSEEK_MAIN_") || !strings.HasSuffix(ref, "_API_KEY") {
		t.Fatalf("v12 的引用名应是逐供应商派生的：%q", ref)
	}
	if resource.RefFor("MATE_%s_API_KEY", "deepseek_main") == ref {
		t.Fatalf("规范化会碰撞的两个 id 必须靠哈希后缀区分开：%q", ref)
	}

	// ⑦ DELETE 幂等：第一次 true，第二次 false 且 code=0（不报错）。
	del, err := svc.providerPlane.DeleteProvider(ctx, "deepseek-main")
	if err != nil || !del.Deleted {
		t.Fatalf("删除应成功: %+v err=%v", del, err)
	}
	again, err := svc.providerPlane.DeleteProvider(ctx, "deepseek-main")
	if err != nil || again.Deleted {
		t.Fatalf("重复删除应幂等（deleted=false, code=0）: %+v err=%v", again, err)
	}
	// ⑧ 删除**顺带清掉密钥面那一条**（v20：不留"库/文档里有值、档案没了"的半截状态）。
	//    内存模式的事实源是托管文档，所以断言它；残留会由回执的 `credentials_left` 说明，
	//    并由 `mate doctor` 的镜像对账段列出来。
	if _, ok := readMirrorValues(t, home)["providers/deepseek-main"]; ok {
		t.Fatal("删除档案应清掉托管文档里那一条")
	}
	if del.CredentialsLeft != "" {
		t.Fatalf("清干净了就不该报残留: %q", del.CredentialsLeft)
	}
	// ⑨ 「被引用 → provider_in_use」不在这里测：引用者是**模型条目**，而模型清单需要 sqlite
	// 载体（memory 模式下结构上不存在任何引用者）。该分支由 `model_list_test.go` 的
	// `TestModelListDeleteProviderInUse` 在真库上钉住。
	//
	// v19 之前这里靠"agent 上的 provider 字段"造引用；agent 面撤销后那条路径没有了。
}

// TestProviderCreateDoesNotWriteKeyWhenRowExists H4 的 v20 形态（新建路径）：
// 载体里已经有同 id 的行 → `provider_exists`，且**一个字节的密钥都没写下去**。
//
// v19 的失败面比这小得多：那时密钥先落盘上密文、再提交载体，于是必须有一条补偿分支去删刚写的密文。
// v20 的顺序是「先建行、再写密钥列」，所以"载体没提交"根本走不到密钥写入那一步——这条用例钉的
// 正是这个结构性质（不是"补偿写得好"，而是"没有补偿要跑"）。
func TestProviderCreateDoesNotWriteKeyWhenRowExists(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.creds.SetLayered(newTestCredentials(t, home))
	svc.providerPlane.LoadProviderDefinitionsFromStore(ctx, dir)

	// 载体里先放一行（内存不知道）→ 写面读内存判定"不存在"，于是走新建 → CAS 未命中。
	if ok, err := st.UpsertProviderDefinition(ctx, store.ProviderColumns{ID: "ghost", Protocol: "openai"}, 0, "rest"); err != nil || !ok {
		t.Fatalf("预置行: ok=%v err=%v", ok, err)
	}
	key := "sk-ghost"
	_, err := svc.providerPlane.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "ghost", Protocol: "openai", APIKey: &key})
	if errCode(err) != "provider_exists" {
		t.Fatalf("CAS 未命中应翻成 provider_exists，got %q（%v）", errCode(err), err)
	}
	// 关键断言：密钥列**一次都没被写过**（也没有任何孤儿残留）。
	if v, found, kErr := st.ProviderKey(ctx, "ghost"); kErr != nil || !found || v != "" {
		t.Fatalf("载体没提交时不该写密钥列: v=%q found=%v err=%v", v, found, kErr)
	}
	// 托管文档：要么根本还没建（这条路径一次都没走到），要么建了但不含这条引用。
	if _, err := os.Lstat(credential.CredentialsPath(home)); err == nil {
		if values := readMirrorValues(t, home); len(values) != 0 {
			t.Fatalf("托管文档不该有残留: %+v", values)
		}
	}
	// 载体那一行保持原样（写面失败 = 一行一审计都不落）。
	row, found, _ := st.GetProviderDefinition(ctx, "ghost")
	if !found || row.Protocol != "openai" || row.BaseURL != "" || row.Revision != 1 {
		t.Fatalf("失败不该改到载体: found=%v row=%+v", found, row)
	}
}

// TestProviderUpdateKeepsKeyWhenContentCasFails H4 的 v20 形态（覆盖路径）：
// 内容 CAS 失败 → 整笔 PATCH 失败，且**密钥列一个字节都没动**（绝不出现"内容旧的、密钥新的"）。
//
// v20 的写面顺序是「内容列 CAS → 密钥列」：内容 CAS 先失败就返回，密钥写入根本没被走到。
// 这条判据比"密钥先写再补偿"更强——它不依赖补偿逻辑正确，而是**结构上没有半截状态**。
func TestProviderUpdateKeepsKeyWhenContentCasFails(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.creds.SetLayered(newTestCredentials(t, home))
	svc.providerPlane.LoadProviderDefinitionsFromStore(ctx, dir)

	old := "sk-old"
	if _, err := svc.providerPlane.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "p1", Protocol: "openai", APIKey: &old}); err != nil {
		t.Fatal(err)
	}
	// 让内存里的 revision 过期（模拟"有人在这中间改过载体"）：直接按当前版本再写一次载体。
	row, found, kErr := st.GetProviderDefinition(ctx, "p1")
	if kErr != nil || !found {
		t.Fatalf("读行: found=%v err=%v", found, kErr)
	}
	if ok, err := st.UpsertProviderDefinition(ctx, store.ProviderColumns{
		ID: "p1", Protocol: "openai", BaseURL: row.BaseURL,
	}, row.Revision, "rest"); err != nil || !ok {
		t.Fatalf("抬高版本: ok=%v err=%v", ok, err)
	}
	neu := "sk-new"
	// 一次 PATCH 同时改端点与密钥：内容 CAS 先失败，所以密钥写入不该发生。
	_, err := svc.providerPlane.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{
		BaseURL: ptr("https://api.other.example/v1"), APIKey: &neu,
	})
	if errCode(err) != "definition_changed" {
		t.Fatalf("CAS 未命中应 definition_changed，got %q（%v）", errCode(err), err)
	}
	if v, _, kErr := st.ProviderKey(ctx, "p1"); kErr != nil || v != old {
		t.Fatalf("内容 CAS 失败时密钥列必须原样（got %q err=%v）", v, kErr)
	}
	// 托管文档里也还是旧值。
	if got := readMirrorValues(t, home)["providers/p1"]; got != old {
		t.Fatalf("镜像里也应是旧值，得到 %q", got)
	}
	// 内存也没被改脏（写面的承诺：载体提交成功之前内存一个字段都不动）。
	if env := svc.providerPlane.ProviderEnvFor(runtimetest.ID, "p1"); len(env) != 1 || !strings.HasSuffix(env[0], "_API_KEY="+old) {
		t.Fatalf("失败的写面不该改内存: %v", env)
	}
}

// TestProviderEffectIsSameSourceAsGenerationPlan C5/A2：生效面回执与代际裁决**同源**。
//
// 判据不是"返回了两个数字"，而是：改 `type` 之后
//
//	① 空闲会话的句柄代际键**真的换了**（下一轮拿到新 env）；
//	② 忙会话的裁决就是 `planGenerationSwitch` 给出的 `defer`（回执里的 deferred 与它逐条一致）。
func TestProviderEffectIsSameSourceAsGenerationPlan(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t) // stub ACP（真子进程）+ 真库
	key := "sk-old"
	if _, err := r.svc.providerPlane.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "p1", Protocol: "openai", APIKey: &key}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	// 注入哪条档案由**默认模型**那条清单决定（v19：agent 上的 provider 字段已撤销）。
	// 夹具直接插行：写面已搬进 `service/svcmodel`（见 `seedModelRow`）。
	r.seedModelRow(t, "p1", "m-1", true)

	idleID, idle := r.start(t, nil)
	busyID, busy := r.start(t, nil)

	// ② 忙的那条：有在途轮次。
	busy.mu.Lock()
	busy.busy = true
	busy.mu.Unlock()

	// 走 env 通道的底座上，改**密钥**才会换基础键（改了它才谈得上"下一轮拿到新 env"）。
	neu := "sk-new"
	changed, err := r.svc.providerPlane.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{APIKey: &neu})
	if err != nil {
		t.Fatalf("PATCH 密钥: %v", err)
	}
	eff := changed.Effect
	if eff == nil {
		t.Fatal("写面必须回带生效面（effect）")
	}
	if eff.Channel != "dial_env" || eff.Effect != "next_run" {
		t.Fatalf("通道/时刻不符: %+v", eff)
	}
	// v19：生效面只按**会话**计数（旧回执里的 applies_to_agents 随 agent 面撤销——
	// 引用者现在是挂靠在该供应商下的模型条目，见 `Refs.Models`）。
	if eff.AffectedSessions == 0 {
		t.Fatalf("应有受影响的会话: %+v", eff)
	}
	if eff.AffectedSessions != 2 || eff.Deferred != 1 {
		t.Fatalf("受影响/顺延计数不符: %+v", eff)
	}
	// 同源：逐会话调 planGenerationSwitch，裁决必须与回执一致。
	for _, tc := range []struct {
		id   string
		as   *ActiveSession
		want generationAction
	}{
		{idleID, idle, genActionSwitch},
		{busyID, busy, genActionDefer},
	} {
		rec := r.svc.sessionRec(tc.id)
		plan := r.orch.planGenerationSwitch(tc.as, rec)
		if !plan.PermChanged {
			t.Fatalf("%s: 改供应商端点必须让基础键变化（这正是'下一轮生效'的判据）", tc.id)
		}
		if plan.Action != tc.want {
			t.Fatalf("%s: 裁决应为 %s，got %s（%s）", tc.id, tc.want, plan.Action, plan.Reason)
		}
	}

	// ① 空闲会话在轮次边界真的换代际（新连接），忙的那条保持旧句柄。
	if got := r.boundary(t, idleID); got == idle {
		t.Fatal("空闲会话在轮次边界应迁到新代际（拿到新 env 的新进程）")
	}
	if got := r.boundary(t, busyID); got != busy {
		t.Fatal("有在途轮次的会话必须顺延（仍用旧句柄）")
	}
}

// TestProviderEffectNilWithoutOrchestrator 没有编排器时**不编数**（回 nil 而不是回 0）。
func TestProviderEffectNilWithoutOrchestrator(t *testing.T) {
	ctx := context.Background()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.providerPlane.SetProviderDir(filepath.Join(t.TempDir(), "providers"))
	if _, err := svc.providerPlane.CreateProvider(ctx, providerReq("p1", nil)); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.providerPlane.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{BaseURL: ptr("https://api.other.example/v1")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Effect != nil {
		t.Fatalf("没有编排器时不该编出生效面数字（宁可缺席）: %+v", resp.Effect)
	}
}

// TestProviderProblemsSurfaceBeforeSession G3：`problems[]` 让用户在开会话**之前**就看到问题。
func TestProviderProblemsSurfaceBeforeSession(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.providerPlane.SetProviderDir(filepath.Join(home, "providers"))
	svc.creds.SetLayered(newTestCredentials(t, home))
	if _, err := svc.providerPlane.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "no-key", Protocol: "openai"}); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.providerPlane.ListProviders(context.Background())
	if len(items) != 1 {
		t.Fatalf("列表面不符: %+v", items)
	}
	// v12 的问题清单不再有"本应注入哪个变量名"这回事（引用名是逐供应商派生的），
	// 但它必须仍然给出**可执行的出路**。
	if len(items[0].Problems) == 0 || !strings.Contains(strings.Join(items[0].Problems, " "), "密钥") {
		t.Fatalf("无密钥的供应商应给出可执行问题: %+v", items[0].Problems)
	}
	if !strings.Contains(strings.Join(items[0].Problems, " "), "设置 · 模型") {
		t.Fatalf("问题文案应指向设置 · 模型: %+v", items[0].Problems)
	}
	// 引用面是只读派生：**模型条目**一挂靠就出现；底座维度只有构建期那一个（引用者出现的
	// 前提下才谈得上"哪个底座拿得到它"）。memory 模式没有清单载体 → 结构上不可能有引用者
	//（`providerModelRefsReadOnly` 无库即空），所以显式断言这一条，而不是拿 agent 凑引用。
	setBuildRuntime(t, runtimetest.ID) // v12ProviderRegistry 只注册了合成底座
	items, _ = svc.providerPlane.ListProviders(context.Background())
	if len(items[0].Refs.Models) != 0 || len(items[0].Refs.Runtimes) != 0 {
		t.Fatalf("memory 模式不该声称有引用者: %+v", items[0].Refs)
	}
}

// TestCredentialProblemPointsAtModelSettings C6：两条文案的出路都指向「设置 · 模型」。
//
// v19：校验对象是**默认模型挂靠的那条档案**（agent 面撤销后，注入的来源只剩这一条链）。
func TestCredentialProblemPointsAtModelSettings(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	st := testutil.OpenStore(t, filepath.Join(home, "mate.db"))
	defer func() { _ = st.Close() }()
	svc := NewService(v12ProviderRegistry(), eventbus.New())
	svc.providerPlane.SetProviderDir(filepath.Join(home, "providers"))
	svc.EnableStore(st)

	defaultModel := func(t *testing.T, id, providerID string) {
		t.Helper()
		created, err := st.CreateModelDefinition(ctx, store.ModelColumns{
			ID: id, ProviderID: providerID, Model: "m-" + id, InputModalities: "text",
		}, "t")
		if err != nil || !created {
			t.Fatalf("造模型行: created=%v err=%v", created, err)
		}
		if err := st.SetDefaultModelID(ctx, id); err != nil {
			t.Fatalf("置默认模型: %v", err)
		}
	}

	// ① 悬空引用：默认模型挂到一条不存在的档案上。
	defaultModel(t, "mdl_gone", "gone")
	err := svc.CredentialProblem(ctx)
	if errCode(err) != "credential_missing" || !strings.Contains(err.Error(), "设置 · 模型") {
		t.Fatalf("悬空引用应报 credential_missing 并指向设置 · 模型: %v", err)
	}
	// ② 存在但无密钥。
	if _, err := svc.providerPlane.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "nokey", Protocol: "openai"}); err != nil {
		t.Fatal(err)
	}
	defaultModel(t, "mdl_nokey", "nokey")
	err = svc.CredentialProblem(ctx)
	if errCode(err) != "credential_missing" || !strings.Contains(err.Error(), "设置 · 模型") ||
		!strings.Contains(err.Error(), "nokey") {
		t.Fatalf("无密钥应报 credential_missing（指明是哪条供应商）并指向设置 · 模型: %v", err)
	}
}
