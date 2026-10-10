package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— M5b/M5c：T5 生成器的**逐字节**判据 + 自校验 + 落盘纪律 ———
//
// 生成是纯函数，所以这里能直接把输出钉成字面量：模板一改、键序一乱、转义一错，测试就红。
// 这是本方案里唯一"写坏就让底座起不来"的代码，字面量断言是刻意的（不是"看起来对"）。

// TestBuildOpencodeJSONByteExact A14 ①：opencode 生成物逐字节可钉。
func TestBuildOpencodeJSONByteExact(t *testing.T) {
	proj := runtime.ConfigProjectionPath{ConfigProjection: runtime.ConfigProjection{
		Format: runtime.ConfigFormatOpencodeJSON, Channel: runtime.ConfigChannelEnvContent,
	}}
	models := []runtimeConfigModel{
		{ModelID: "v4-flash", Name: "Flash", ProviderID: "deepseek-main", IsDefault: true},
		{ModelID: "v4-pro", ProviderID: "deepseek-main"},
	}
	providers := map[string]providerProjection{
		"deepseek-main": {ID: "deepseek-main", Type: "deepseek", HasKey: true,
			Fragment: map[string]any{"settings": map[string]any{"baseURL": "https://example.invalid/v1"}}},
	}
	got, err := buildRuntimeConfig(proj, models, providers)
	if err != nil {
		t.Fatalf("生成: %v", err)
	}
	want := `{
  "$schema": "https://opencode.ai/config.json",
  "providers": {
    "deepseek-main": {
      "name": "deepseek-main",
      "env": [
        "DEEPSEEK_API_KEY"
      ],
      "settings": {
        "baseURL": "https://example.invalid/v1"
      },
      "models": {
        "v4-flash": {
          "name": "Flash"
        },
        "v4-pro": {}
      }
    }
  },
  "model": "deepseek-main/v4-flash",
  "_arkMate": {
    "generator_version": 1,
    "manager": "ark-mate"
  }
}
`
	if string(got) != want {
		t.Fatalf("opencode 生成物与字面量不符:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// 逐字节可复现（map 迭代顺序不影响结果）。
	again, _ := buildRuntimeConfig(proj, models, providers)
	if string(again) != string(got) {
		t.Fatal("同输入两次生成必须逐字节相同（map 迭代顺序不得泄漏进产物）")
	}
	// 密钥绝不进产物：只有 `env` 引用。
	if strings.Contains(string(got), "sk-") || strings.Contains(string(got), "apiKey\"") {
		t.Fatalf("产物里不得出现密钥或 apiKey 字段: %s", got)
	}
	// `extra` 是"把这个条目原样写进底座"的口子（例如 `{"disabled":true}` 对齐底座原生的
	// "从选择器隐藏"）——所以它必须**被物化**，否则用户写进去的底座字段会静默消失。
	models[0].Extra = map[string]any{"disabled": true, "hidden": false, "model_id": "nope"}
	again2, _ := buildRuntimeConfig(proj, models, providers)
	var doc map[string]any
	if err := json.Unmarshal(again2, &doc); err != nil {
		t.Fatal(err)
	}
	entry := doc["providers"].(map[string]any)["deepseek-main"].(map[string]any)["models"].(map[string]any)["v4-flash"].(map[string]any)
	if entry["disabled"] != true {
		t.Fatalf("extra 的键应被物化（底座原生字段靠它表达）: %+v", entry)
	}
	// 平台自己的列/键不许由 extra 复写（否则平台记账与配置会分叉）。
	if _, ok := entry["model_id"]; ok {
		t.Fatalf("extra 不得复写平台的键（model_id）: %+v", entry)
	}
	if _, ok := entry["hidden"]; ok {
		t.Fatalf("hidden/pinned 是平台侧呈现偏好，**不下发**: %+v", entry)
	}
	if !strings.Contains(string(again2), `"v4-flash"`) {
		t.Fatal("清单条目本身必须仍然下发（隐藏不是删掉它）")
	}
}

// TestBuildDshPatchYAMLByteExact A14 ①：dsh 生成物逐字节可钉，且**不写 agent-default-model**。
func TestBuildDshPatchYAMLByteExact(t *testing.T) {
	proj := runtime.ConfigProjectionPath{ConfigProjection: runtime.ConfigProjection{
		Format: runtime.ConfigFormatDshPatchYAML, Channel: runtime.ConfigChannelCLIPatchArg,
	}}
	models := []runtimeConfigModel{
		{ModelID: "deepseek-v4-flash", ProviderID: "deepseek-official", IsDefault: true},
	}
	providers := map[string]providerProjection{
		"deepseek-official": {ID: "deepseek-official", Type: "deepseek", HasKey: true},
	}
	got, err := buildRuntimeConfig(proj, models, providers)
	if err != nil {
		t.Fatalf("生成: %v", err)
	}
	s := string(got)
	// ① 目标行是 `id: acp`（ACP 平面唯一消费模型的行）。
	if !strings.Contains(s, "- id: acp\n") {
		t.Fatalf("必须打 `id: acp` 这一行:\n%s", s)
	}
	// ② **绝不写 agent-default-model**（ACP 平面不消费它，写它等于空操作 + 第二个会漂移的事实）。
	if strings.Contains(s, "agent-default-model") {
		t.Fatalf("默认不写 agent-default-model（对 ACP 会话是空操作）:\n%s", s)
	}
	// ③ 默认模型落在 acp 行的 config 里。
	if !strings.Contains(s, `    provider: "deepseek-official"`+"\n"+`    model: "deepseek-v4-flash"`+"\n") {
		t.Fatalf("acp 行的 config 应带 provider/model:\n%s", s)
	}
	// ④ 路由声明走 llm-pi-ai 的 providers.<route>.models[]（整体替换语义 ⇒ 平台列出的就是全部）。
	if !strings.Contains(s, "- id: llm-pi-ai\n") || !strings.Contains(s, `          - id: "deepseek-v4-flash"`) {
		t.Fatalf("缺少 llm-pi-ai 路由声明:\n%s", s)
	}
	if !strings.Contains(s, `        apiKeyEnv: "DEEPSEEK_API_KEY"`) {
		t.Fatalf("apiKeyEnv 必须是引用（不是明文）:\n%s", s)
	}
	// 逐字节可复现 + 自校验通过。
	again, _ := buildRuntimeConfig(proj, models, providers)
	if string(again) != s {
		t.Fatal("同输入两次生成必须逐字节相同")
	}
	if err := selfCheckRuntimeConfig(proj, got); err != nil {
		t.Fatalf("自校验应通过: %v", err)
	}
}

// TestBuildRuntimeConfigEmptyListWritesNothing 空清单：**不生成任何东西**（不是"写一份空配置"）。
//
// 两个底座上空配置都是坏输入：opencode 目录空 → 起不了会话；dsh 空/只有注释的文件 → **拒启**。
func TestBuildRuntimeConfigEmptyListWritesNothing(t *testing.T) {
	for _, format := range []string{runtime.ConfigFormatOpencodeJSON, runtime.ConfigFormatDshPatchYAML} {
		proj := runtime.ConfigProjectionPath{ConfigProjection: runtime.ConfigProjection{Format: format}}
		data, err := buildRuntimeConfig(proj, nil, nil)
		if err != nil || data != nil {
			t.Fatalf("%s 空清单应返回 (nil, nil)，got len=%d err=%v", format, len(data), err)
		}
	}
	// 未知格式：报错，**不静默回落成空内容**（那等于把用户的清单静默清空）。
	proj := runtime.ConfigProjectionPath{ConfigProjection: runtime.ConfigProjection{Format: "toml"}}
	if _, err := buildRuntimeConfig(proj, []runtimeConfigModel{{ModelID: "m"}}, nil); err == nil {
		t.Fatal("未知格式必须报错")
	}
}

// TestSelfCheckCatchesTemplateBugs F5：故意注入模板 bug → 必须被抓。
func TestSelfCheckCatchesTemplateBugs(t *testing.T) {
	ocProj := runtime.ConfigProjectionPath{ConfigProjection: runtime.ConfigProjection{Format: runtime.ConfigFormatOpencodeJSON}}
	dshProj := runtime.ConfigProjectionPath{ConfigProjection: runtime.ConfigProjection{Format: runtime.ConfigFormatDshPatchYAML}}

	// opencode：v1 的 `provider`（单数）在 v2 上是静默的未知字段——自校验必须挡住这个形态。
	for _, bad := range []string{
		`{"$schema":"x","provider":{"a":{"models":{}}}}`,
		`{"$schema":"x","providers":{"a":{}}}`,
		`{"$schema":"x","providers":[]}`,
		``,
		`{"$schema":"x"`,
	} {
		if err := selfCheckRuntimeConfig(ocProj, []byte(bad)); err == nil {
			t.Fatalf("opencode 自校验应拒绝 %q", bad)
		}
	}
	// dsh：空 / 只有注释 / 只有 `---` 的文件都会让 dsh **拒启**。
	for _, bad := range []string{
		``,
		"# 只有注释\n# 再一行\n",
		"---\n",
		"[]\n",
		"id: acp\n",
	} {
		if err := selfCheckRuntimeConfig(dshProj, []byte(bad)); err == nil {
			t.Fatalf("dsh 自校验应拒绝 %q", bad)
		}
	}
	// 正例：合法的顶层数组。
	if err := selfCheckRuntimeConfig(dshProj, []byte("- id: acp\n  config:\n    model: \"m\"\n")); err != nil {
		t.Fatalf("dsh 正例应通过: %v", err)
	}
}

// configTestRig 一个带库 + 一个"声明了落点但未实测"的 runtime 的架子。
func configTestRig(t *testing.T, tested bool) (*genRig, string) {
	t.Helper()
	r := newGenRig(t)
	dir := t.TempDir()
	oc, ok := runtime.DefaultRegistry().Get("opencode")
	if !ok {
		t.Fatal("默认注册表里应有 opencode")
	}
	oc.Launch.Env = append(oc.Launch.Env, "OPENCODE_CONFIG_DIR="+filepath.Join(dir, "oc-config"))
	oc.ConfigProjection.Tested = tested
	if tested {
		oc.ConfigProjection.TestedOnVersion = "test-version"
	}
	r.svc.reg.Register(oc)
	return r, dir
}

// TestMaterializeUnwrittenWhenUntested F6/纪律⑤：未实测 → **生成候选但不落盘、不改启动参数**。
func TestMaterializeUnwrittenWhenUntested(t *testing.T) {
	ctx := context.Background()
	r, _ := configTestRig(t, false)
	key := "sk-x"
	if _, err := r.svc.CreateProvider(ctx, dtoproviderCreate("p1", "deepseek", key)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{
		Runtime: "opencode", ProviderID: "p1", ModelID: "m-1",
	}); err != nil {
		t.Fatal(err)
	}
	st, err := r.svc.MaterializeRuntimeConfig(ctx, "opencode", false)
	if err != nil {
		t.Fatalf("物化: %v", err)
	}
	if !st.Declared || st.Tested || st.Writable {
		t.Fatalf("未实测的落点应是 declared=true / tested=false / writable=false: %+v", st)
	}
	if st.Changed {
		t.Fatal("未实测时不得报告「已改变落盘内容」（它根本没落盘）")
	}
	if st.Digest == "" {
		t.Fatal("未实测也应给出候选摘要（探针与诊断要看「会写成什么」）")
	}
	if _, err := os.Stat(st.Path); !os.IsNotExist(err) {
		t.Fatalf("未实测时**一个字节都不该写**: %v", err)
	}
	// 候选内容仍在内存里可查（H6 探针的输入）。
	if cand := r.svc.RuntimeConfigCandidate("opencode"); cand == "" {
		t.Fatal("未实测时应保留候选内容（探针要用）")
	}
	// 启动参数也没被改（env_content 通道要等 writable）。
	for _, kv := range r.svc.specLaunchEnv(t, "opencode") {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG_CONTENT=") && kv != "OPENCODE_CONFIG_CONTENT=" {
			t.Fatalf("未实测时不该把内容注入 env: %q", kv)
		}
	}
}

// TestMaterializeWritesFileAndInjects 实测通过后的完整四步：
// 落盘（含 .bak 回退）→ 注入 env_content → 摘要入 meta → 并进代际键。
func TestMaterializeWritesFileAndInjects(t *testing.T) {
	ctx := context.Background()
	r, _ := configTestRig(t, true)
	key := "sk-x"
	if _, err := r.svc.CreateProvider(ctx, dtoproviderCreate("p1", "deepseek", key)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{
		Runtime: "opencode", ProviderID: "p1", ModelID: "m-1", IsDefault: ptr(true),
	}); err != nil {
		t.Fatal(err)
	}
	// 写面已经顺手物化过一次（tryMaterialize）。
	st, err := r.svc.MaterializeRuntimeConfig(ctx, "opencode", true)
	if err != nil {
		t.Fatalf("物化: %v", err)
	}
	if !st.Writable || st.ModelCount != 1 {
		t.Fatalf("实测通过后应可写: %+v", st)
	}
	data, err := os.ReadFile(st.Path)
	if err != nil {
		t.Fatalf("配置文件应已落盘: %v", err)
	}
	if !strings.Contains(string(data), `"m-1"`) {
		t.Fatalf("落盘内容应含清单条目: %s", data)
	}
	if got := sha256Hex(data); got != st.Digest {
		t.Fatalf("摘要应为落盘内容的 sha256: got=%s want=%s", st.Digest, got)
	}
	// 摘要入 meta。
	if e, ok := r.svc.runtimeConfigEntry(ctx, "opencode"); !ok || e.Digest != st.Digest ||
		e.GeneratorVersion != runtimeConfigGeneratorVersion {
		t.Fatalf("meta 摘要不符: %+v ok=%v", e, ok)
	}
	// 并进代际键：内存里的指纹与 meta 一致。
	if d := r.svc.runtimeConfigDigest("opencode"); d != st.Digest {
		t.Fatalf("内存里的配置指纹应与 meta 一致: %q vs %q", d, st.Digest)
	}
	// env_content 通道：同一份字节被注入子进程 env（**同键替换**，不是追加第二条）。
	env := r.svc.specLaunchEnv(t, "opencode")
	found := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG_CONTENT=") {
			found++
			if kv != "OPENCODE_CONFIG_CONTENT="+string(data) {
				t.Fatalf("注入的内容应与落盘字节逐字相同")
			}
		}
	}
	if found != 1 {
		t.Fatalf("注入键必须恰好一条（同键替换），实际 %d 条: %v", found, env)
	}
	// 幂等重放：内容没变 → 不重写（changed=false）。
	before, _ := os.Stat(st.Path)
	again, err := r.svc.MaterializeRuntimeConfig(ctx, "opencode", true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatal("内容相同时不该报告 changed")
	}
	after, _ := os.Stat(st.Path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("内容相同时不该重写文件（mtime 不该动）")
	}
}

// TestMaterializeRetainsBackup 改清单 → 重新物化时保留上一份 `.bak-<digest 前 8>`。
func TestMaterializeRetainsBackup(t *testing.T) {
	ctx := context.Background()
	r, _ := configTestRig(t, true)
	key := "sk-x"
	if _, err := r.svc.CreateProvider(ctx, dtoproviderCreate("p1", "deepseek", key)); err != nil {
		t.Fatal(err)
	}
	m1, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "opencode", ProviderID: "p1", ModelID: "m-1"})
	if err != nil {
		t.Fatal(err)
	}
	st1, _ := r.svc.MaterializeRuntimeConfig(ctx, "opencode", false)
	// 再声明一条 → 内容变 → 上一份应被留成 .bak-<前 8 位>。
	if _, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{Runtime: "opencode", ProviderID: "p1", ModelID: "m-2"}); err != nil {
		t.Fatal(err)
	}
	st2, err := r.svc.MaterializeRuntimeConfig(ctx, "opencode", false)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Digest == st1.Digest {
		t.Fatal("改了清单，摘要应变")
	}
	if _, err := os.Stat(st2.Path + ".bak-" + st1.Digest[:8]); err != nil {
		t.Fatalf("应保留上一份 `.bak-<digest 前 8 位>`: %v", err)
	}
	_ = m1
}

// specLaunchEnv 读某 runtime 当前的启动 env（测试用；只看不改）。
func (s *Service) specLaunchEnv(t *testing.T, runtimeID string) []string {
	t.Helper()
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		t.Fatalf("runtime %q 未注册", runtimeID)
	}
	return spec.Launch.Env
}

// dtoproviderCreate 小工具：造一个新建凭据的请求（避免每处都写全字面量）。
func dtoproviderCreate(id, typ, key string) dtoprovider.ProviderCreateReq {
	return dtoprovider.ProviderCreateReq{ID: id, Type: typ, Key: &key}
}

var _ = eventbus.New
var _ = json.Marshal
