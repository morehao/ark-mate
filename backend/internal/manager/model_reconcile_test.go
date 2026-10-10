package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— T8 对账 · T5 的 `cli_patch_arg` 通道 · dsh 校验门 ———

// seedCatalog 往内存目录缓存里塞一份**伪造的运行时事实**（对账的另一半）。
//
// 这是把"运行时广告了什么"变成可构造输入的唯一干净办法：目录缓存本来就是内存态，
// 而 `ModelCatalog` 在无活会话时读它（source=cache）。用真会话造这一侧会让单测依赖真底座，
// 而本用例要钉的是**差集算法**本身。
func seedCatalog(svc *Service, agentID, runtimeID string, values ...acl.ConfigOptionValue) {
	svc.catalog.put(agentID, runtimeID, []acl.ConfigOption{{
		ID: "model", Name: "Model", Category: acl.CategoryModel, Type: "select",
		Values: values,
	}})
}

// TestModelReconcileExactDiff A14 ②：对账闭环——构造差集，断言报出的**正好**是差的那几条。
//
// 三条断言（计划 A14 的原文）：
//
//	① advertised 与 declared 一致 → 两侧差集都为空；
//	② 去掉一条 → `declared_not_advertised` **恰好是它**；
//	③ 多一条 → `advertised_not_declared` **恰好是它**（**恰好相等，不是包含**）。
func TestModelReconcileExactDiff(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	r.svc.agents["a1"].Runtimes = []string{"opencode"}
	m1 := r.createModel(t, "opencode", "p1", "m-1")
	m2 := r.createModel(t, "opencode", "p1", "m-2")

	// ① 两侧一致 → 差集都为空。
	seedCatalog(r.svc, "a1", "opencode",
		acl.ConfigOptionValue{Value: m1.Value},
		acl.ConfigOptionValue{Value: m2.Value},
	)
	diff, err := r.svc.reconcileModels(ctx, "opencode", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.DeclaredNotAdvertised) != 0 || len(diff.AdvertisedNotDeclared) != 0 {
		t.Fatalf("两侧一致时差集应为空: %+v", diff)
	}

	// ② 底座那边少一条 → declared_not_advertised 恰好是 m-2。
	seedCatalog(r.svc, "a1", "opencode", acl.ConfigOptionValue{Value: m1.Value})
	diff, _ = r.svc.reconcileModels(ctx, "opencode", "a1")
	if len(diff.DeclaredNotAdvertised) != 1 || diff.DeclaredNotAdvertised[0].Value != m2.Value {
		t.Fatalf("差集应恰好是 m-2: %+v", diff.DeclaredNotAdvertised)
	}
	if len(diff.AdvertisedNotDeclared) != 0 {
		t.Fatalf("另一侧应为空: %+v", diff.AdvertisedNotDeclared)
	}
	// opencode 上的严重度：声明了却没进目录，通常是配置没被读到（error）。
	if diff.DeclaredNotAdvertised[0].Severity != severityError {
		t.Fatalf("opencode 的 declared_not_advertised 应为 error: %+v", diff.DeclaredNotAdvertised[0])
	}

	// ③ 底座多一条（内置目录常态）→ advertised_not_declared 恰好是它，且是 info。
	seedCatalog(r.svc, "a1", "opencode",
		acl.ConfigOptionValue{Value: m1.Value},
		acl.ConfigOptionValue{Value: m2.Value},
		acl.ConfigOptionValue{Value: "opencode/builtin-x"},
	)
	diff, _ = r.svc.reconcileModels(ctx, "opencode", "a1")
	if len(diff.AdvertisedNotDeclared) != 1 || diff.AdvertisedNotDeclared[0].Value != "opencode/builtin-x" {
		t.Fatalf("多出来的应恰好是内置那条: %+v", diff.AdvertisedNotDeclared)
	}
	if diff.AdvertisedNotDeclared[0].Severity != severityInfo {
		t.Fatalf("opencode 上底座自带目录的条目是常态（info）: %+v", diff.AdvertisedNotDeclared[0])
	}
	if diff.AdvertisedNotDeclared[0].Route != "opencode" {
		t.Fatalf("路由前缀应取自 value 的第一个 `/` 之前: %+v", diff.AdvertisedNotDeclared[0])
	}
}

// TestModelReconcileSeverityByRoute 差集严重度**按 route 分**（dsh 的两个路由语义完全不同）。
//
// 同一个差集在 pi-ai 路由上是"选了就报错"、在 deepseek 路由上只是"能力按 text-only 估"——
// 只报一个数字就会把两种后果说成一种。
func TestModelReconcileSeverityByRoute(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"deepseek-official", severityDegrade},
		{"llm-pi-ai-route", severityError},
		{"some-gateway", severityError},
	}
	for _, tc := range cases {
		if got := declaredNotAdvertisedSeverity("deepseekharness", tc.provider); got != tc.want {
			t.Errorf("route %q 的严重度应为 %q，got %q", tc.provider, tc.want, got)
		}
	}
	// 另一侧：opencode 的自带目录是 info（它的声明是追加，内置目录永远在候选里）。
	if got := reconcileAdvertisedNotDeclaredSeverity("opencode"); got != severityInfo {
		t.Fatalf("opencode 的 advertised_not_declared 默认非空、属常态: %q", got)
	}
	// 取值集合是**封闭**的（前端按它上色/分组，不能出现第四种）。
	if len(modelReconcileSeverities) != 3 {
		t.Fatalf("severity 取值集合应恰好三种: %v", modelReconcileSeverities)
	}
}

// TestSyncRuntimeConfigArgsIdempotent T5 的 `cli_patch_arg`：参数按 overlay 的存在性**幂等**增删。
func TestSyncRuntimeConfigArgsIdempotent(t *testing.T) {
	r, dir := configTestRig(t, false)
	spec, _ := r.svc.reg.Get("opencode")
	spec.ConfigProjection.Channel = "cli_patch_arg"
	spec.ConfigProjection.Format = "dsh_patch_yaml"
	spec.ConfigProjection.Filename = "mate-models.patch.yml"
	spec.ConfigProjection.Root = "${OPENCODE_CONFIG_DIR}"
	r.svc.reg.Register(spec)
	path := filepath.Join(dir, "oc-config", "mate-models.patch.yml")

	argsOf := func() []string {
		sp, _ := r.svc.reg.Get("opencode")
		return sp.Launch.Args
	}
	// ① 文件不存在 → 不出现 --patch。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	r.svc.SyncRuntimeConfigArgs()
	if strings.Contains(strings.Join(argsOf(), " "), patchFlag) {
		t.Fatalf("overlay 不存在时不该传 --patch: %v", argsOf())
	}
	// ② 只有注释的文件 → 仍然不传（dsh 会**拒启**）。
	if err := os.WriteFile(path, []byte("# 只有注释\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.svc.SyncRuntimeConfigArgs()
	if strings.Contains(strings.Join(argsOf(), " "), patchFlag) {
		t.Fatalf("只有注释的 overlay 会让 dsh 拒启，绝不能传: %v", argsOf())
	}
	// ③ 有内容 → 恰好一次，且路径**绝对**。
	if err := os.WriteFile(path, []byte("- id: acp\n  config: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.svc.SyncRuntimeConfigArgs()
	r.svc.SyncRuntimeConfigArgs() // 幂等：再来一次不重复
	args := argsOf()
	n := 0
	for i, a := range args {
		if a == patchFlag {
			n++
			if i+1 >= len(args) || !filepath.IsAbs(args[i+1]) {
				t.Fatalf("--patch 的路径必须绝对: %v", args)
			}
		}
	}
	if n != 1 {
		t.Fatalf("--patch 应恰好出现一次，实际 %d 次: %v", n, args)
	}
	// ④ 文件被删（恢复路径）→ 参数自动摘掉。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r.svc.SyncRuntimeConfigArgs()
	if strings.Contains(strings.Join(argsOf(), " "), patchFlag) {
		t.Fatalf("文件删掉后参数应自动摘掉（这就是「坏了就去掉参数」那条恢复路径）: %v", argsOf())
	}
}

// TestDshValidationGate 校验门的判据：exitCode==0 **且** diagnostics 为空 **且** complete==true。
//
// 用假 dsh（一段输出固定 JSON 的 shell 脚本）钉这三条——因为**只看退出码会漏掉最要命的一类错**：
// 目标 entry id 不存在时退出码是 0，只有一个 `level: warning` 的诊断。
func TestDshValidationGate(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-dsh.sh")
	// 脚本把 $FAKE_DUMP 原样打到 stdout（用它切换三种形态）。
	body := "#!/bin/sh\nprintf '%s' \"$FAKE_DUMP\"\nexit \"${FAKE_EXIT:-0}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewService(publisherRegistry(), nil)
	spec := runtimeSpecForGate("deepseekharness", script)

	cases := []struct {
		name string
		dump string
		exit string
		pass bool
	}{
		{"ok", `{"x-cordis":{"complete":true,"diagnostics":[]}}`, "0", true},
		// ④ 的负例：目标 id 不存在 → **诊断非空但退出码 0**。
		{"missing_entry", `{"x-cordis":{"complete":true,"diagnostics":[{"level":"warning","message":"patch: entry \"acpx\" not found"}]}}`, "0", false},
		{"incomplete", `{"x-cordis":{"complete":false,"diagnostics":[]}}`, "0", false},
		{"nonzero_exit", `{"x-cordis":{"complete":true,"diagnostics":[]}}`, "1", false},
		{"not_json", `dsh: some human output`, "0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_DUMP", tc.dump)
			t.Setenv("FAKE_EXIT", tc.exit)
			res, err := svc.validateDshPatchCandidate(context.Background(), spec, []byte("- id: acp\n"),
				dshValidator{command: script, args: []string{}, stagingRoot: t.TempDir()})
			if err != nil {
				t.Fatalf("跑校验门: %v", err)
			}
			if got := dshDumpPassed(res); got != tc.pass {
				t.Fatalf("判据不符：pass=%v，期望 %v（res=%+v）", got, tc.pass, res)
			}
		})
	}
}

// TestMaterializeDoesNotWriteWhenListEmpty 空清单 → 不写、不注入、不落 meta（两个底座通用）。
func TestMaterializeDoesNotWriteWhenListEmpty(t *testing.T) {
	ctx := context.Background()
	r, _ := configTestRig(t, true) // 可写
	st, err := r.svc.MaterializeRuntimeConfig(ctx, "opencode", true)
	if err != nil {
		t.Fatalf("空清单不该报错: %v", err)
	}
	if st.Changed || st.Digest != "" {
		t.Fatalf("空清单不该产生任何字节: %+v", st)
	}
	if !strings.Contains(st.Note, "平台清单为空") {
		t.Fatalf("空清单必须说明白为什么什么都不写: %+v", st)
	}
	if _, err := os.Stat(st.Path); !os.IsNotExist(err) {
		t.Fatalf("空清单不该写文件: %v", err)
	}
	// 清单非空之后才写。
	key := "sk-x"
	if _, err := r.svc.CreateProvider(ctx, dtoproviderCreate("p1", "deepseek", key)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "opencode", ProviderID: "p1", ModelID: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.Path); err != nil {
		t.Fatalf("有清单之后应落盘: %v", err)
	}
}

// runtimeSpecForGate 造一个"命令可替换"的 runtime spec（校验门用例用假 dsh）。
func runtimeSpecForGate(id, command string) runtime.Spec {
	return runtime.Spec{ID: id, Name: id, Launch: runtime.LaunchSpec{
		Command: command,
		Args:    []string{"--profile", "acp"},
		Env:     []string{"DSH_HOME=/tmp/active-dsh-home-must-not-be-used"},
	}}
}
