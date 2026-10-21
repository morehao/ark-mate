package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// TestMcpSecretResolution §13.3 M1/M6：定义文件里只有引用，注入时才解析成明文；
// 库里的密钥缺失 / 环境变量未设 → **不注入该 server**（不静默塞空密钥）。
//
// v20 起 `store:<ns>/<name>` 指向库里的 `mcp_secrets` 一张表（memory 模式是托管文档），
// 引用串本身就是库的主键（**整串存**，不剥前缀）。
func TestMcpSecretResolution(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "providers")
	svc := providerTestSvc(t, dir)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	enableStore(svc, st)

	const ref = "store:mcp/fs-token"
	const token = "sk-mcp-secret-xyz"
	if err := st.SetMcpSecret(context.Background(), ref, token); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FS_AUTH_HEADER", "Bearer from-env")

	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		Env:           map[string]string{"FS_ROOT": "/tmp/root"},
		SecretEnv:     map[string]string{"FS_TOKEN": ref},
		Headers:       map[string]string{"X-Client": "mate"},
		SecretHeaders: map[string]string{"Authorization": "env:FS_AUTH_HEADER"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "broken", Name: "broken", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"FS_TOKEN": "store:mcp/not-there"},
	}); err != nil {
		t.Fatal(err)
	}

	// 注入：引用解析成明文，且明文/密钥两路合并到同一字典。
	got := directMcp(svc.McpServersFor("stub", nil))
	if len(got) != 1 || got[0].Name != "fs" {
		t.Fatalf("解析失败的 server 不该注入，应只剩 fs: %+v", got)
	}
	if got[0].Env["FS_TOKEN"] != token || got[0].Env["FS_ROOT"] != "/tmp/root" {
		t.Fatalf("env 注入不符: %+v", got[0].Env)
	}
	if got[0].Headers["Authorization"] != "Bearer from-env" || got[0].Headers["X-Client"] != "mate" {
		t.Fatalf("header 注入不符: %+v", got[0].Headers)
	}

	// 读模型：明文键给键名，密钥侧**只给键名**（连引用都不回），并标出解析不了的键。
	list, err := mcpCatalog(svc).ListMcpServers()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		env, secEnv, secHdr, unresolved []string
	}{}
	for _, m := range list {
		byID[m.ID] = struct {
			env, secEnv, secHdr, unresolved []string
		}{m.EnvKeys, m.SecretEnvKeys, m.SecretHeaderKeys, m.UnresolvedSecretKeys}
	}
	fs := byID["fs"]
	if len(fs.env) != 1 || fs.env[0] != "FS_ROOT" {
		t.Fatalf("env_keys 不符: %+v", fs.env)
	}
	if len(fs.secEnv) != 1 || fs.secEnv[0] != "FS_TOKEN" {
		t.Fatalf("secret_env_keys 不符: %+v", fs.secEnv)
	}
	if len(fs.secHdr) != 1 || fs.secHdr[0] != "Authorization" {
		t.Fatalf("secret_header_keys 不符: %+v", fs.secHdr)
	}
	if len(fs.unresolved) != 0 {
		t.Fatalf("fs 的引用都能解析，不该有 unresolved: %+v", fs.unresolved)
	}
	if broken := byID["broken"]; len(broken.unresolved) != 1 || broken.unresolved[0] != "FS_TOKEN" {
		t.Fatalf("库里没有那条密钥时应报 unresolved: %+v", broken.unresolved)
	}

	// 读模型里绝不能出现密钥值/引用体（守住"只回 key"）：序列化整个响应体来查。
	raw := dumpMcpServers(t, svc)
	for _, leak := range []string{token, ref, "Bearer from-env", "from-env"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("读模型泄漏 %q：%s", leak, raw)
		}
	}
}

// dumpMcpServers 把 MCP 列表响应序列化成 JSON（用于"读模型不得泄漏值/引用"的字符串断言）。
func dumpMcpServers(t *testing.T, svc *Service) string {
	t.Helper()
	list, err := mcpCatalog(svc).ListMcpServers()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMcpSecretResolutionFailures 三类解析失败都给得出原因（env 未设 / 没有库那一层 / 引用非法），
// 且都不注入——不静默塞空密钥让 server 带着空凭证跑。
func TestMcpSecretResolutionFailures(t *testing.T) {
	// 1) env 引用未设。
	svc := providerTestSvc(t, filepath.Join(t.TempDir(), "providers"))
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "s1", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"TOKEN": "env:MATE_TEST_UNSET_VAR"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := directMcp(svc.McpServersFor("stub", nil)); len(got) != 0 {
		t.Fatalf("env 未设时不该注入: %+v", got)
	}
	if keys := planesOf(svc).McpSecrets.UnresolvedKeys(capability.McpServer{SecretEnv: map[string]string{"TOKEN": "env:MATE_TEST_UNSET_VAR"}}); len(keys) != 1 {
		t.Fatalf("应报 unresolved: %+v", keys)
	}

	// 2) 没有库（也没有托管文档）：明确指出"库里没有这条密钥"，而不是当成"密钥为空"。
	noStore := newServiceForTest(runtime.NewRegistry(runtime.Spec{ID: "stub"}), nil)
	if err := planesOf(noStore).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "s2", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"TOKEN": "store:mcp/x"},
	}); err != nil {
		t.Fatal(err)
	}
	_, _, problems := planesOf(noStore).McpSecrets.Resolve(capability.McpServer{
		SecretEnv: map[string]string{"TOKEN": "store:mcp/x"},
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "托管文档") {
		t.Fatalf("应报「找不到这条密钥」并说明落点: %+v", problems)
	}
	if len(planesOf(noStore).McpSecrets.UnresolvedKeys(capability.McpServer{SecretEnv: map[string]string{"TOKEN": "store:mcp/x"}})) != 1 {
		t.Fatal("没有库与托管文档时该键应报 unresolved")
	}

	// 3) 失败原因里不得含密钥值（日志不该成为泄漏面）：库里那一条在解析失败时**不出现**。
	home := t.TempDir()
	st3 := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st3.Close() }()
	svc3 := providerTestSvc(t, filepath.Join(home, "providers"))
	enableStore(svc3, st3)
	const leaked = "sk-should-never-appear"
	if err := st3.SetMcpSecret(context.Background(), "store:mcp/present", leaked); err != nil {
		t.Fatal(err)
	}
	// 引用另一条**不存在**的：失败原因只含键名与引用，绝不含任何密钥值。
	_, _, problems = planesOf(svc3).McpSecrets.Resolve(capability.McpServer{
		SecretEnv: map[string]string{"TOKEN": "store:mcp/wrong"},
	})
	if len(problems) != 1 {
		t.Fatalf("库里没有的引用应解析失败: %+v", problems)
	}
	if strings.Contains(problems[0], leaked) {
		t.Fatalf("失败原因泄漏了密钥值: %v", problems[0])
	}
	if !strings.Contains(problems[0], "TOKEN") || !strings.Contains(problems[0], "store:mcp/wrong") {
		t.Fatalf("失败原因应含键名与引用: %v", problems[0])
	}
	// 出路必须是用户**今天真能做到**的那一条：库里 `mcp_secrets` 的值这一批还没有写入口
	// （REST/CLI/面板都没有），所以"把值写进库"不是出路——写了等于让用户去找一个不存在的入口。
	if !strings.Contains(problems[0], "env:<ENV_NAME>") {
		t.Fatalf("失败原因必须给出可执行的那条出路（改引用为 env:）: %v", problems[0])
	}
	for _, banned := range []string{"把值写进库", "mcp_secrets"} {
		if strings.Contains(problems[0], banned) {
			t.Fatalf("出路不得指向不存在的入口（%q）: %v", banned, problems[0])
		}
	}
	// 旧前缀在**运行时**读到就是坏数据：它既不是 `store:` 也不是 `env:`，词法层就拒了
	// （翻译只发生在导入档案文件那一步，判据见 `internal/platform/capability` 的翻译点用例；这里不写字面量，
	// 免得把已退役的词带回 manager 的源码里）。
	if err := capability.ValidateSecretRef("storex:mcp/legacy"); err == nil {
		t.Fatal("认不出的前缀应在词法层被拒")
	}
}

// TestSecretRefLexiconMatchesStore v20 的一致性命中面：**定义体里写的引用串 = 库里那一行的主键**。
//
// 为什么这条还在（v19 之前它比的是"能力数据面的词法"对"密文层的词法"）：v20 把密钥搬进
// `mcp_secrets` 之后，"引用"不再是某个机制内部的路径，而是**库的主键本身**——剥前缀、改写、
// 归一化都会让写进去的那一条与注入时查的那一条对不上，而那种分叉**不会让任何一条用例变红**
// （两边各自自洽）。所以判据横跨两层：词法层放行的串，经库存一遍再读出来必须**逐字节相同**，
// 且真能解析出值。
func TestSecretRefLexiconMatchesStore(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()

	for _, ref := range []string{
		"store:providers/default", "store:mcp/fs-token", "store:a/B", "store:a.b-c_d/e",
	} {
		if err := capability.ValidateSecretRef(ref); err != nil {
			t.Fatalf("词法层应放行 %q: %v", ref, err)
		}
		if err := st.SetMcpSecret(ctx, ref, "v-"+ref); err != nil {
			t.Fatalf("写库 %q: %v", ref, err)
		}
		v, ok, err := st.McpSecret(ctx, ref)
		if err != nil || !ok || v != "v-"+ref {
			t.Fatalf("引用串必须整串存取（不得剥前缀）：v=%q ok=%v err=%v", v, ok, err)
		}
	}
	// 词法层拒的串也不该能"绕过"：写进去读出来是两个不同的键（对照面，说明整串就是身份）。
	if err := st.SetMcpSecret(ctx, "store:", "weird"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.McpSecret(ctx, "store:"); !ok {
		t.Fatal("库不判词法，只认整串；词法拒绝发生在写面前一层")
	}
	refs, err := st.ListMcpSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		if r.Ref != capability.SecretRefStorePrefix+strings.TrimPrefix(r.Ref, capability.SecretRefStorePrefix) {
			t.Fatalf("库里的引用串被改写过: %q", r.Ref)
		}
	}
}

// TestMcpLiveAndProbeCarryResolvedEnv 密钥解析必须到得了**探测面与调用面**，不能只有注入面有。
//
// 这条钉的是一个实测过的缺陷（2026-10-09）：`mcp.Server` 当时连 `Env` 字段都没有，探测/调用路径
// 于是既不传 env 也不传 secret_headers ⇒ 一个靠 env 拿凭据的 stdio server 会"会话里能用、
// `POST /mcps/:id/probe` 必然失败"（`GET /mcps` 长期红）。证据是同一个 server 两处 spawn 的
// 进程环境截然相反：探测路径 `core_pid=""`、ACP 注入路径 `core_pid="46579"`。
func TestMcpLiveAndProbeCarryResolvedEnv(t *testing.T) {
	home := t.TempDir()
	svc := providerTestSvc(t, filepath.Join(home, "providers"))
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	enableStore(svc, st)
	const ref = "store:mcp/live"
	const token = "sk-live-secret"
	if err := st.SetMcpSecret(context.Background(), ref, token); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIVE_HDR", "Bearer live")
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "live", Name: "live", Transport: capability.TransportStdio, Command: "/usr/bin/env",
		Env:           map[string]string{"PLAIN": "p"},
		SecretEnv:     map[string]string{"TOKEN": ref},
		SecretHeaders: map[string]string{"Authorization": "env:LIVE_HDR"},
	}); err != nil {
		t.Fatal(err)
	}

	// 1) 调用面（mcpLiveServer）：明文 + 解析后的密钥一起进 mcp.Server。
	got, err := planesOf(svc).Mcp.LiveServer("live")
	if err != nil {
		t.Fatalf("mcpLiveServer: %v", err)
	}
	if got.Env["PLAIN"] != "p" || got.Env["TOKEN"] != token {
		t.Fatalf("调用面未带解析后的 env: %+v", got.Env)
	}
	if got.Headers["Authorization"] != "Bearer live" {
		t.Fatalf("调用面未带解析后的 header: %+v", got.Headers)
	}

	// 2) 解析不了 → 当场报错（不拿空凭证去探），且是 40001（配置问题）而不是 50001。
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "broken", Name: "broken", Transport: capability.TransportStdio, Command: "/usr/bin/env",
		SecretEnv: map[string]string{"TOKEN": "store:mcp/not-there"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := planesOf(svc).Mcp.LiveServer("broken"); err == nil {
		t.Fatal("密钥解析失败时调用面应报错")
	} else if numericCode(err) != code.InvalidRequest {
		t.Fatalf("应是 40001（配置问题），got %d（%v）", numericCode(err), err)
	} else if !strings.Contains(err.Error(), "mcp_secret_unresolved") {
		t.Fatalf("错误里应有稳定 code，便于排障：%v", err)
	}
	if _, err := planesOf(svc).Mcp.Probe(context.Background(), "broken"); err == nil {
		t.Fatal("密钥解析失败时探测应报错")
	} else if numericCode(err) != code.InvalidRequest {
		t.Fatalf("探测路径同样应是 40001，got %d（%v）", numericCode(err), err)
	}
	// 失败要留痕（GET /mcps 的 last_probe_*），否则用户只看到一次性的响应。
	list, err := mcpCatalog(svc).ListMcpServers()
	if err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, m := range list {
		if m.ID == "broken" && m.LastProbeErr != "" {
			recorded = true
			if !strings.Contains(m.LastProbeErr, "mcp_secret_unresolved") {
				t.Fatalf("留痕应带稳定 code: %s", m.LastProbeErr)
			}
		}
	}
	if !recorded {
		t.Fatal("解析失败也要记 last_probe_error")
	}
}

// TestMcpSecretStaleMirrorNotAdopted 库里没有、镜像里有的那条 MCP 密钥**不采用**（同 provider 的判据）。
func TestMcpSecretStaleMirrorNotAdopted(t *testing.T) {
	home := t.TempDir()
	svc := providerTestSvc(t, filepath.Join(home, "providers"))
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	enableStore(svc, st)
	doc := planesOf(svc).Credentials.Doc()
	if doc == nil {
		t.Fatal("夹具应装配出托管文档")
	}
	if err := doc.Set("store:mcp/stale", "sk-stale"); err != nil {
		t.Fatal(err)
	}
	if _, err := planesOf(svc).McpSecrets.ResolveRef("store:mcp/stale"); err == nil {
		t.Fatal("库里没有这条密钥时不许采用镜像里那一份")
	} else if !strings.Contains(err.Error(), "不采用") {
		t.Fatalf("失败原因应说清为什么不采用镜像那份: %v", err)
	}
	// 同一引用在库里真有值时照常解析（对照面）。
	if err := st.SetMcpSecret(context.Background(), "store:mcp/stale", "sk-real"); err != nil {
		t.Fatal(err)
	}
	if v, err := planesOf(svc).McpSecrets.ResolveRef("store:mcp/stale"); err != nil || v != "sk-real" {
		t.Fatalf("库里那条才是权威: v=%q err=%v", v, err)
	}
}
