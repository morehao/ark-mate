package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/credential"

	"github.com/morehao/ark-mate/internal/domain/provider"
	provrow "github.com/morehao/ark-mate/internal/domain/provider/row"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
)

// 本文件是 provider 密钥写面/装载面的用例（v20 形态）。
//
// v19 及更早，密钥的落点是"库里的引用 + 盘上的密文"两份，于是这一组用例围着加解密句柄、
// 主密钥与密文文件转。v20 把这些全折进**一处**：`providers.key` 一列（memory 模式下是托管
// 文档 `<MATE_HOME>/credentials.json` 那一条），`key_ref` 退化成派生量。于是本文件现在钉的是：
//
//	装载    旧档案里的明文密钥被搬进密钥面，文件里只剩派生引用；
//	写面    写进库 → 内存 → 镜像（镜像失败只 WARN，不回滚库）；
//	清除    同一个写入点，库与镜像都要跟着清；
//	判据    库那一列非空是"有密钥"的**唯一**判据——镜像里有而库里没有的那份**不采用**。
//
// 2026-10-11 那条"装配早期问一次密钥句柄就把错结论钉死整个进程"的回归守卫（旧
// `TestProviderVaultEarlyCallDoesNotPoisonAnchor`）随旧机制一并撤销：那条缓存在结构上没了，
// 而它真正护住的顺序问题（凭据面必须先于第一次 runtime 同步装配）改由
// `internal/bootstrap/app` 的镜像锚点用例在**真实装配路径**上钉住。

// writeLegacyProviders 写一份「旧版本」的明文 providers.json。
func writeLegacyProviders(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readProvidersFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "providers.json"))
	if err != nil {
		t.Fatalf("读 providers.json: %v", err)
	}
	return string(b)
}

// readMirrorValues 读托管镜像（`<home>/credentials.json`）的 `引用 → 值` 映射。
func readMirrorValues(t *testing.T, home string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(credential.CredentialsPath(home))
	if err != nil {
		t.Fatalf("读托管文档: %v", err)
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("托管文档应是 `引用 → 值` 的 JSON 对象: %v", err)
	}
	return values
}

// TestProviderKeyMovedFromFileToStoreOnLoad 装载时把旧档案里的**明文**密钥搬进密钥面：
// 文件里明文消失、只留派生引用；镜像（memory 模式的事实源）拿到值；注入仍可用；再装载幂等。
func TestProviderKeyMovedFromFileToStoreOnLoad(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	const secret = "sk-legacy-secret-abc"
	writeLegacyProviders(t, dir,
		`{"default":{"id":"default","type":"openai","model":"gpt-4o","key":"`+secret+`"}}`)

	svc := providerTestSvc(t, dir)

	// 1) 明文从文件里消失，只留引用（引用是**派生量**：`providers/<id>`）。
	raw := readProvidersFile(t, dir)
	if strings.Contains(raw, secret) {
		t.Fatalf("providers.json 仍有明文: %s", raw)
	}
	if !strings.Contains(raw, "providers/default") {
		t.Fatalf("providers.json 应只留引用: %s", raw)
	}
	// 2) 搬运的落点是密钥面（memory 模式 = 托管文档），文件里那份明文不再被读。
	if got := readMirrorValues(t, home)["providers/default"]; got != secret {
		t.Fatalf("托管文档里应有这条密钥，得到 %q", got)
	}
	// 3) 注入仍可用（内存里是解出来的明文），has_key 不变。
	if got := planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "default"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DEFAULT_") || !strings.HasSuffix(got[0], "_API_KEY="+secret) {
		t.Fatalf("搬运后注入不符: %v", got)
	}
	items, _ := planesOf(svc).Providers.ListProviders(context.Background())
	if len(items) != 1 || !items[0].HasKey {
		t.Fatalf("has_key 应保持 true: %+v", items)
	}
	// 4) 幂等：新 Service 再装载 → 文件字节不变（搬运只在"文件里还有明文"时发生）。
	svc2 := providerTestSvc(t, dir)
	if got := readProvidersFile(t, dir); got != raw {
		t.Fatalf("二次装载应幂等: %s", got)
	}
	if got := planesOf(svc2).Providers.ProviderEnvFor(runtimetest.ID, "default"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DEFAULT_") || !strings.HasSuffix(got[0], "_API_KEY="+secret) {
		t.Fatalf("重启后注入不符: %v", got)
	}
	// 5) 同一份托管文档被两个 Service 读写：值不重复搬、也不被覆盖成空。
	if got := readMirrorValues(t, home)["providers/default"]; got != secret {
		t.Fatalf("二次装载后托管文档里的值应原样在，得到 %q", got)
	}
}

// TestProviderUpdateWritesKeyAndClearRemovesIt 写面：写密钥 → 文件只留引用、密钥面拿到值；
// 空串清除 → 同一个写入点把库/镜像里的那条删掉（幂等）。
func TestProviderUpdateWritesKeyAndClearRemovesIt(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	svc := providerTestSvc(t, dir)

	key := "sk-live-secret-xyz"
	resp, err := planesOf(svc).Providers.CreateProvider(context.Background(), providerReq("ds", &key))
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if !resp.HasKey {
		t.Fatal("应 has_key=true")
	}
	raw := readProvidersFile(t, dir)
	if strings.Contains(raw, key) {
		t.Fatalf("providers.json 不应含明文: %s", raw)
	}
	if !strings.Contains(raw, "providers/ds") {
		t.Fatalf("应写引用: %s", raw)
	}
	if got := readMirrorValues(t, home)["providers/ds"]; got != key {
		t.Fatalf("托管文档应记下这条密钥，得到 %q", got)
	}
	if got := planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "ds"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DS_") || !strings.HasSuffix(got[0], "_API_KEY="+key) {
		t.Fatalf("注入不符: %v", got)
	}
	// 覆盖写同一档案：镜像里仍是 1 条（值被换成新的），新值生效。
	key2 := "sk-live-secret-2"
	if _, err := planesOf(svc).Providers.UpdateProvider(context.Background(), "ds", provider.PatchRequest{APIKey: &key2}); err != nil {
		t.Fatal(err)
	}
	if got := planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "ds"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DS_") || !strings.HasSuffix(got[0], "_API_KEY="+key2) {
		t.Fatalf("覆盖后注入不符: %v", got)
	}
	values := readMirrorValues(t, home)
	if len(values) != 1 || values["providers/ds"] != key2 {
		t.Fatalf("覆盖后镜像里应只有一条且是新值: %+v", values)
	}

	// 清除：has_key=false、env nil、镜像里那条没了。
	clear := ""
	if _, err := planesOf(svc).Providers.UpdateProvider(context.Background(), "ds", provider.PatchRequest{APIKey: &clear}); err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "ds") != nil {
		t.Fatal("清除后不应注入")
	}
	if values := readMirrorValues(t, home); len(values) != 0 {
		t.Fatalf("清除后镜像里不该留这一条: %+v", values)
	}
	items, _ := planesOf(svc).Providers.ListProviders(context.Background())
	for _, it := range items {
		if it.ID == "ds" && it.HasKey {
			t.Fatalf("清除后 has_key 应为 false: %+v", it)
		}
	}
	// 幂等：再清除一次不报错。
	if _, err := planesOf(svc).Providers.UpdateProvider(context.Background(), "ds", provider.PatchRequest{APIKey: &clear}); err != nil {
		t.Fatalf("重复清除应幂等: %v", err)
	}
}

// TestProviderStaleMirrorKeyNotAdopted 镜像里有、库里没有的那一份密钥**不采用**。
//
// 这是 v20 最容易静默出错的一条：镜像是一份可以自由复制、也可以被别的东西改的明文文件。
// 若解析时采用它，"在面板上清掉的密钥"会被一份陈旧镜像悄悄撤销（删除变成静默的 no-op），
// 而用户看到的是"我明明删了，它还在"。判据必须落在**库**那一列上。
//
// 同一现场还把另一条出口钉住：默认模型挂着这条没有密钥的档案时，`CredentialProblem` 报出
// 原因与可执行出路（库是权威面，出路是"在设置里补一次"）。
func TestProviderStaleMirrorKeyNotAdopted(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()

	svc := newServiceForTest(v12ProviderRegistry(), nil)
	enableStore(svc, st)
	planesOf(svc).Providers.SetProviderDir(dir)
	planesOf(svc).Credentials.SetLayered(newTestCredentials(t, home))
	// 库里一条**没有密钥**的档案（面板上刚清掉，或从来没设过）。
	if _, err := st.ImportProviderDefinition(ctx, store.ProviderColumns{
		ID: "default", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if created, err := st.CreateModelDefinition(ctx, provrow.ModelColumns{
		ID: "mdl_stale", ProviderID: "default", Model: "m-1", InputModalities: "text",
	}, "t"); err != nil || !created {
		t.Fatalf("造模型行: created=%v err=%v", created, err)
	}
	if err := st.SetMeta(ctx, model.MetaKeyDefaultModel, "mdl_stale"); err != nil {
		t.Fatal(err)
	}
	// 镜像里那份是陈旧的（库里已经被清掉了）。
	doc := planesOf(svc).Credentials.Doc()
	if doc == nil {
		t.Fatal("夹具应装配出托管文档")
	}
	if err := doc.Set("providers/default", "sk-stale-from-mirror"); err != nil {
		t.Fatal(err)
	}
	if n := planesOf(svc).Providers.LoadProviderDefinitionsFromStore(ctx, dir); n != 1 {
		t.Fatalf("应装载 1 条档案，得到 %d", n)
	}

	// ① 不注入：镜像里那一条不构成"这条档案有密钥"。
	if got := planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "default"); got != nil {
		t.Fatalf("库里没有密钥时不许采用镜像里那一份: %v", got)
	}
	// ② has_key 如实为 false，原因是"镜像陈旧"而不是"没有密钥"（两者处置不同）。
	items, err := planesOf(svc).Providers.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].HasKey {
		t.Fatalf("库里那一列为空 → has_key 应为 false: %+v", items)
	}
	problems := strings.Join(items[0].Problems, " ")
	if !strings.Contains(problems, "镜像") || !strings.Contains(problems, "不采用") {
		t.Fatalf("问题文案应说清「镜像里有而库里没有」这件事: %v", items[0].Problems)
	}
	// ③ 同一事实的另一条出口：默认模型挂着它时，出路指向「设置 · 模型」里重设一次。
	if err := svc.CredentialProblem(ctx); err == nil {
		t.Fatal("默认模型挂在没有密钥的档案上应报错")
	} else if !strings.Contains(err.Error(), "设置") {
		t.Fatalf("错误里要给出路: %v", err)
	}
	// ④ 镜像里那份**没有被回灌进库**（只读判据：权威在库）。
	if v, found, err := st.ProviderKey(ctx, "default"); err != nil || !found || v != "" {
		t.Fatalf("不该把镜像那份写回库: v=%q found=%v err=%v", v, found, err)
	}
}

// TestProviderInvalidIDRejected provider id 会进**文件路径与引用串**（`providers/<id>`）→
// 路径穿越必须被拒（词法校验，与载体无关）。
func TestProviderInvalidIDRejected(t *testing.T) {
	svc := providerTestSvc(t, filepath.Join(t.TempDir(), "providers"))
	key := "sk-x"
	for _, id := range []string{"../escape", "a/b", "a b", ".."} {
		if _, err := planesOf(svc).Providers.CreateProvider(context.Background(),
			providerReq(id, &key)); err == nil {
			t.Fatalf("非法 provider id 应被拒绝: %q", id)
		}
		// PATCH 走同一条词法校验。
		if _, err := planesOf(svc).Providers.UpdateProvider(context.Background(), id,
			provider.PatchRequest{APIKey: &key}); err == nil {
			t.Fatalf("非法 provider id 的 PATCH 应被拒绝: %q", id)
		}
	}
}

// TestProviderKeyRejectedWithoutCredentialDoc 没有可写落点时，**带密钥的写入被拒绝**，
// 而不是"只留内存"。
//
// 为什么这条不能放宽：memory 模式下托管文档就是事实源，放行就等于面板回 `has_key=true`
// 而重启后密钥消失（一次静默的谎报）。判据三条：拒绝 + 一行内存都不留 + 不带密钥的档案照常可建。
func TestProviderKeyRejectedWithoutCredentialDoc(t *testing.T) {
	ctx := context.Background()
	// 未装配凭据面（数据目录没解析出来）→ 没有落点。
	svc := newServiceForTest(v12ProviderRegistry(), nil)
	key := "sk-memory-only"
	_, err := planesOf(svc).Providers.CreateProvider(ctx, providerReq("default", &key))
	if errCode(err) != "credentials_write_failed" {
		t.Fatalf("没有落点时应 credentials_write_failed，got %q（%v）", errCode(err), err)
	}
	// 出路必须可执行（用户要知道该修什么）。
	if !strings.Contains(err.Error(), credential.CredentialsFileName) {
		t.Fatalf("出路文案应指向托管文档: %v", err)
	}
	// 拒绝 = **一行内存都不留**（不是"半条档案"）。
	if items, _ := planesOf(svc).Providers.ListProviders(ctx); len(items) != 0 {
		t.Fatalf("被拒的写入不该留下任何档案: %+v", items)
	}
	// 不带密钥的档案照常可建（"先建后补密钥"是合法用法）。
	if _, err := planesOf(svc).Providers.CreateProvider(ctx, providerReq("default", nil)); err != nil {
		t.Fatalf("不带密钥时应该可以建: %v", err)
	}
	// PATCH 补密钥同样被拒（两条入口同一判据）。
	if _, err := planesOf(svc).Providers.UpdateProvider(ctx, "default", provider.PatchRequest{APIKey: &key}); errCode(err) != "credentials_write_failed" {
		t.Fatalf("PATCH 补密钥也应被拒，got %q（%v）", errCode(err), err)
	}
	// 重启 = 新 Service → 内存档案不复存在（没有任何落盘）。
	svc2 := newServiceForTest(v12ProviderRegistry(), nil)
	if got := planesOf(svc2).Providers.ProviderEnvFor(runtimetest.ID, "default"); got != nil {
		t.Fatalf("没有落点时不该有任何持久化: %v", got)
	}
}

// TestProviderEnvLayerShadowsStore 层序的事实：**继承环境**遮住密钥面那一条，且读面说得出来
// 是哪个环境变量遮的（用户会问"我明明在面板里改了，为什么没生效"）。
func TestProviderEnvLayerShadowsStore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()

	svc := newServiceForTest(v12ProviderRegistry(), nil)
	enableStore(svc, st)
	planesOf(svc).Providers.SetProviderDir(dir)
	l := newTestCredentials(t, home)
	const envValue = "sk-from-process-env"
	// 候选变量名由注册表的 `RefPattern` 派生（不是手抄常量）：夹具跟着它走，
	// 换模板时这条用例仍然验的是"继承环境遮住密钥面"这件事。
	envName := ""
	for _, n := range planesOf(newServiceForTest(v12ProviderRegistry(), nil)).Credentials.ProviderEnvKeyNames("default") {
		if strings.Contains(n, "DEFAULT") {
			envName = n
			break
		}
	}
	if envName == "" {
		t.Fatal("注册表应给 default 派生出一个环境变量名")
	}
	l.Env = func(name string) string {
		if name == envName {
			return envValue
		}
		return ""
	}
	planesOf(svc).Credentials.SetLayered(l)
	if _, err := st.ImportProviderDefinition(ctx, store.ProviderColumns{
		ID: "default", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if written, err := st.SetProviderKey(ctx, "default", "sk-from-store"); err != nil || !written {
		t.Fatalf("写库里的密钥: written=%v err=%v", written, err)
	}
	if n := planesOf(svc).Providers.LoadProviderDefinitionsFromStore(ctx, dir); n != 1 {
		t.Fatalf("应装载 1 条档案，得到 %d", n)
	}

	if got := planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "default"); len(got) != 1 || !strings.HasSuffix(got[0], "_API_KEY="+envValue) {
		t.Fatalf("继承环境应遮住密钥面那一条: %v", got)
	}
	items, err := planesOf(svc).Providers.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].KeySource != "env" {
		t.Fatalf("读面应说清生效的是环境那一层（key_source=env）: %+v", items[0])
	}
	// 是**哪个**环境变量遮的写在 problems 里（读面唯一说得出这件事的地方）。
	if problems := strings.Join(items[0].Problems, " "); !strings.Contains(problems, envName) ||
		!strings.Contains(problems, "遮住") {
		t.Fatalf("读面应说清是哪个环境变量遮的: %v", items[0].Problems)
	}
	if !items[0].HasKey {
		t.Fatalf("遮住之后仍然算「有密钥」：%+v", items[0])
	}
}

func strPtr(s string) *string { return &s }
