package dsh

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/resource"
)

// ——— A5：dsh 的同步链路与 v11 的**逐字节**等价性（ADR-9/ADR-12） ———
//
// 判据分两部分（设计 §行为基线与等价性验证）：
//
//	A 部分（未点名的一切）：供应商按键排序、缩进、尾换行、`- id: acp` / `- id: llm-pi-ai` 的条目形态、
//	  `displayName`、`api`、`baseURL`、`models[]` 的顺序与 `name` 的 YAML 转义 —— **必须逐字节相同**；
//	B 部分（本方案点名的偏差，以新形态为期望）：`apiKeyEnv` 由 `envKeyOf(type)`（每个 type 一个名字）
//	  换成**逐供应商**的引用名（修 ADR-16 的真实缺陷）。
//
// A 部分的证明方式：把冻结的 v11 字节按**仅 B 部分**改写一遍，再与新 adapter 的输出逐字节比对。
// 改写函数只允许做 B 部分那一件事——多做一件比对就红。
//
// 另外两处**未迁移字段**（`compat` 与模型的 `extra`）在本期没有出口，夹具因此刻意不带它们；
// 它们由 `TestGoldenDshCompatHasNoV12Exit` 单独**机器登记**，不混进 A 部分。

var updateGolden = flag.Bool("update", false, "重新生成 golden 文件（评审时人工过一遍 diff）")

const goldenRefPattern = "MATE_%s_API_KEY"

type goldenFixtureProvider struct {
	ID      string
	Name    string // 平台展示名——**刻意不下发**（见 TestGoldenDshProjectsRouteIDAsDisplayName）
	Type    string // v11 的 `type`（只喂给冻结的 v11 生成器）
	BaseURL string
}

type goldenFixtureModel struct {
	ID         string // 平台模型 id
	ProviderID string
	ModelID    string
	Name       string
	Default    bool
}

// ≥3 个供应商 × 每个 2 条模型（其中 1 条设默认），含 `name` 与多字节字符（覆盖 YAML 转义）。
// 两个 `type` 都是 `openai` 的供应商是**刻意的**：v11 会让它们共用 `OPENAI_API_KEY`（ADR-16）。
var (
	goldenFixtureProviders = []goldenFixtureProvider{
		{ID: "alpha-gw", Name: "内网网关 Alpha", Type: "openai", BaseURL: "https://alpha.example.com/v1"},
		{ID: "beta.gw", Name: "备用网关", Type: "openai", BaseURL: "https://beta.example.com/openai/v1"},
		{ID: "zeta", Name: "Zeta 直连", Type: "deepseek", BaseURL: "https://zeta.example.com/v1"},
	}
	goldenFixtureModels = []goldenFixtureModel{
		{ID: "m-alpha-1", ProviderID: "alpha-gw", ModelID: "gpt-4o-mini", Name: "小模型·中文"},
		{ID: "m-alpha-2", ProviderID: "alpha-gw", ModelID: "gpt-4o", Name: `大模型 "A" \ 备用`},
		{ID: "m-beta-1", ProviderID: "beta.gw", ModelID: "qwen:7b", Name: "qwen #7b", Default: true},
		{ID: "m-beta-2", ProviderID: "beta.gw", ModelID: "llama/3-8b", Name: ""},
		{ID: "m-zeta-1", ProviderID: "zeta", ModelID: "deepseek-chat", Name: "DeepSeek 对话"},
		{ID: "m-zeta-2", ProviderID: "zeta", ModelID: "deepseek-reasoner", Name: "推理\t模型"},
	}
)

const goldenFixtureDefaultModel = "m-beta-1"

// v11FixtureInputs 把夹具翻译成冻结 v11 生成器的输入。
//
// 模型按 `ModelID` 排序——这是 v11 的 `collectRuntimeConfigInput` 的真实输入形态（它会对模型做一次
// 全局 `ModelID` 升序），也是 v12 逐 route 过滤后的数组顺序。**不排就会让数组顺序变**，那属于 A 部分。
//
// `compat` 是 v11 独有、v12 没有出口的字段（设计 §载体差异的「未迁移字段」清单），所以它由调用方
// 显式给出（主 golden 传 nil）；不给参数就不动全局夹具，避免用例之间互相污染。
func v11FixtureInputs(compat map[string]map[string]any) ([]v11Model, map[string]v11Provider) {
	models := make([]v11Model, 0, len(goldenFixtureModels))
	for _, m := range goldenFixtureModels {
		models = append(models, v11Model{
			ModelID:    m.ModelID,
			Name:       m.Name,
			ProviderID: m.ProviderID,
			IsDefault:  m.Default,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelID < models[j].ModelID })
	providers := map[string]v11Provider{}
	for _, p := range goldenFixtureProviders {
		frag := map[string]any{
			"api":     "openai-completions",
			"baseURL": p.BaseURL,
		}
		if c, ok := compat[p.ID]; ok && len(c) > 0 {
			frag["compat"] = c
		}
		providers[p.ID] = v11Provider{ID: p.ID, Type: p.Type, HasKey: true, Fragment: frag}
	}
	return models, providers
}

// goldenSet 把同一份夹具翻译成 v12 的端口输入（`resource.Set`）。
func goldenSet() resource.Set {
	providers := make([]resource.Provider, 0, len(goldenFixtureProviders))
	for _, p := range goldenFixtureProviders {
		providers = append(providers, resource.Provider{
			ID:       p.ID,
			Name:     p.Name,
			Protocol: resource.ProtocolOpenAI,
			BaseURL:  p.BaseURL,
			APIKey:   "sk-fixture-" + p.ID,
			Ref:      resource.RefFor(goldenRefPattern, p.ID),
		})
	}
	models := make([]resource.Model, 0, len(goldenFixtureModels))
	for _, m := range goldenFixtureModels {
		models = append(models, resource.Model{ID: m.ID, ProviderID: m.ProviderID, ModelID: m.ModelID, Name: m.Name})
	}
	return resource.Set{
		Runtime:      ID,
		Providers:    providers,
		Models:       models,
		DefaultModel: goldenFixtureDefaultModel,
	}
}

// TestGoldenDsh A5：新 adapter 的产出 == golden，且 A 部分（把 v11 字节按**仅 B 部分**改写后）
// 与新产出逐字节相同。
func TestGoldenDsh(t *testing.T) {
	models, providers := v11FixtureInputs(nil)
	v11Bytes, err := buildV11DshPatchYAML(models, providers)
	if err != nil {
		t.Fatalf("冻结的 v11 生成器失败：%v", err)
	}
	v12Bytes, err := buildPatch(goldenSet())
	if err != nil {
		t.Fatalf("新生成器失败：%v", err)
	}

	oldPath := filepath.Join("testdata", "dsh-v11.patch.yml")
	newPath := filepath.Join("testdata", "dsh-v12.patch.yml")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(oldPath, v11Bytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newPath, v12Bytes, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已重写 golden：%s / %s", oldPath, newPath)
	}

	if want := readGolden(t, oldPath); want != string(v11Bytes) {
		t.Errorf("冻结的 v11 生成器产出与 %s 不一致——v11 基线的忠实性被破坏：\n%s",
			oldPath, firstDiff(want, string(v11Bytes)))
	}
	rewritten := rewriteDshV11ToV12(t, string(v11Bytes))
	if rewritten != string(v12Bytes) {
		t.Errorf("A 部分（未点名处）出现差异——按纪律必须以 golden 为准、把 adapter 改到一致：\n%s",
			firstDiff(rewritten, string(v12Bytes)))
	}
	if want := readGolden(t, newPath); want != string(v12Bytes) {
		t.Errorf("新 adapter 的产出与 %s 不一致（B 部分以新形态为期望）：\n%s",
			newPath, firstDiff(want, string(v12Bytes)))
	}
}

// TestGoldenDshBPartPresent 夹具必须真的覆盖到 B 部分（否则等价性判据是空转）。
func TestGoldenDshBPartPresent(t *testing.T) {
	models, providers := v11FixtureInputs(nil)
	v11Bytes, err := buildV11DshPatchYAML(models, providers)
	if err != nil {
		t.Fatal(err)
	}
	old := string(v11Bytes)
	v12Bytes, err := buildPatch(goldenSet())
	if err != nil {
		t.Fatal(err)
	}
	got := string(v12Bytes)

	// B：引用名逐供应商唯一（v11 的两个 openai 型供应商共用 OPENAI_API_KEY）。
	if c := strings.Count(old, `apiKeyEnv: "OPENAI_API_KEY"`); c != 2 {
		t.Fatalf("夹具没有覆盖 B：v11 的两个 openai 型供应商应共用 OPENAI_API_KEY（实测命中 %d 次）", c)
	}
	refs := apiKeyEnvValues(got)
	if len(refs) != len(goldenFixtureProviders) {
		t.Fatalf("B 未落地：应有 %d 个引用名，实际 %v", len(goldenFixtureProviders), refs)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r] {
			t.Errorf("B 未落地：引用名 %q 命中两条 route（ADR-16 的缺陷回归了）", r)
		}
		seen[r] = true
	}
	for _, p := range goldenFixtureProviders {
		if want := resource.RefFor(goldenRefPattern, p.ID); !seen[want] {
			t.Errorf("B 未落地：供应商 %s 的引用名应为 %q，实际 %v", p.ID, want, refs)
		}
	}
	// `api` 固定为 openai-completions，`baseURL` 来自资源字段（A 部分：与 v11 逐字相同）。
	for _, p := range goldenFixtureProviders {
		if !strings.Contains(got, "        baseURL: "+strconv.Quote(p.BaseURL)+"\n") {
			t.Errorf("供应商 %s 的 baseURL 未按资源字段下发", p.ID)
		}
	}
	if c := strings.Count(got, "        api: \"openai-completions\"\n"); c != len(goldenFixtureProviders) {
		t.Errorf("每个 route 都应写固定 api（实测 %d 处，应有 %d 处）", c, len(goldenFixtureProviders))
	}
	// 默认模型：`Set.DefaultModel` 是**平台 id**，必须解析成上游 id 与它挂靠的 route。
	if !strings.Contains(got, "    provider: \"beta.gw\"\n    model: \"qwen:7b\"\n") {
		t.Error("默认模型未按 `DefaultModel`（平台 id）解析成 route + 上游 id")
	}
}

// TestGoldenDshCompatHasNoV12Exit 记录一处**未迁移字段**：v11 会把 `projections.<runtime>.compat`
// 序列化成 `compat: {…}`，而 v12 的 `resource.Provider` 没有这个字段（设计 §载体差异把它登记进
// 「未迁移字段」清单）。
//
// 这里不是等价性判据，而是**如实登记**：拿掉那一行之后，两份产出逐字节相同——于是"compat 消失"
// 是一件被点名、被机器看见的事，而不是混进 A 部分的静默差异。
func TestGoldenDshCompatHasNoV12Exit(t *testing.T) {
	models, providers := v11FixtureInputs(map[string]map[string]any{"alpha-gw": {"systemRole": "system"}})
	v11Bytes, err := buildV11DshPatchYAML(models, providers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v11Bytes), `        compat: {"systemRole":"system"}`+"\n") {
		t.Fatalf("v11 应当把 compat 原样写成 YAML flow mapping，实际：\n%s", v11Bytes)
	}
	v12Bytes, err := buildPatch(goldenSet())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v12Bytes), "compat:") {
		t.Error("v12 的输出里出现了 compat——本期它没有出口（§载体差异）")
	}
	// 把 v11 的那一行删掉，再按 B 部分改写 → 应与 v12 逐字节相同。
	stripped := dropYAMLLine(t, string(v11Bytes), "compat:")
	if got, want := rewriteDshV11ToV12(t, stripped), string(v12Bytes); got != want {
		t.Errorf("compat 是本例唯一的差异，其余字节必须相同：\n%s", firstDiff(want, got))
	}
}

// TestGoldenDshProjectsRouteIDAsDisplayName 记录一条**刻意的**选择：`displayName` 取路由 id，
// 不取供应商展示名（理由同 opencode 的 `name`：它属于 A 部分，v11 写的就是路由 id）。
func TestGoldenDshProjectsRouteIDAsDisplayName(t *testing.T) {
	b, err := buildPatch(goldenSet())
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, p := range goldenFixtureProviders {
		if !strings.Contains(got, "      \""+p.ID+"\":\n        displayName: \""+p.ID+"\"\n") {
			t.Errorf("route %s 的 displayName 应为路由 id（展示名 %q 不下发）", p.ID, p.Name)
		}
	}
}

// TestGoldenDshEmptySetProducesNothing 契约③的前置：空清单**绝不能**走到生成器——
// dsh 对空 / 只有注释的 patch 是**拒启**（README 逐字）。生成器只会在"有内容"时被调用。
func TestGoldenDshEmptySetProducesNothing(t *testing.T) {
	// 生成器本身不做数目判断（那是 SyncModels 的职责），但它对"零模型 + 零供应商"的产出必须是
	// **只有注释的头两行**——而这正是 dsh 拒启的形态，所以 SyncModels 必须在它之前短路。
	b, err := buildPatch(resource.Set{})
	if err != nil {
		t.Fatal(err)
	}
	lines, items := 0, 0
	for _, ln := range strings.Split(string(b), "\n") {
		tt := strings.TrimSpace(ln)
		if tt == "" || strings.HasPrefix(tt, "#") {
			continue
		}
		lines++
		if strings.HasPrefix(tt, "- id:") {
			items++
		}
	}
	if lines != 0 || items != 0 {
		t.Fatalf("空 Set 的生成结果里有实体行（lines=%d items=%d）——它只能由 SyncModels 短路掉", lines, items)
	}
	if err := selfCheck(b); err == nil {
		t.Fatal("自校验必须拒绝'只有注释的 patch'（那会让 dsh 拒启），它却放过了")
	}
}

// ——— 改写与比对工具 ———

// rewriteDshV11ToV12 把 v11 的字节**只**按 B 部分改写：`apiKeyEnv` 的值换成逐供应商引用名。
//
// 按"出现顺序"配对（apiKeyEnv 行的顺序 = route 升序 = provider 对象键序），这样它顺带证明了
// "第 k 个 route 拿到第 k 个引用名"；按类型配对恰恰会掩盖 ADR-16 的那个缺陷。
func rewriteDshV11ToV12(t *testing.T, old string) string {
	t.Helper()
	routes := make([]string, 0, len(goldenFixtureProviders))
	for _, p := range goldenFixtureProviders {
		routes = append(routes, p.ID)
	}
	sort.Strings(routes)

	lines := strings.Split(old, "\n")
	idx := 0
	for i, ln := range lines {
		if !strings.HasPrefix(strings.TrimSpace(ln), "apiKeyEnv: ") {
			continue
		}
		if idx >= len(routes) {
			t.Fatalf("apiKeyEnv 行多于 route 数（第 %d 行）", i+1)
		}
		lines[i] = indentOf(ln) + "apiKeyEnv: " + yamlQuote(resource.RefFor(goldenRefPattern, routes[idx]))
		idx++
	}
	if idx != len(routes) {
		t.Fatalf("夹具覆盖不全：v11 产出里只有 %d 个 apiKeyEnv 行，应有 %d 个", idx, len(routes))
	}
	return strings.Join(lines, "\n")
}

// dropYAMLLine 删掉含某个标记的行（用于把"未迁移字段"从基线里单独摘出来比对）。
func dropYAMLLine(t *testing.T, text, marker string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	dropped := 0
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), marker) {
			dropped++
			continue
		}
		out = append(out, ln)
	}
	if dropped != 1 {
		t.Fatalf("标记 %q 应恰好命中 1 行，实际 %d 行", marker, dropped)
	}
	return strings.Join(out, "\n")
}

// apiKeyEnvValues 取出产出里所有 `apiKeyEnv` 的值（同一个确定性 YAML 的最小解析）。
func apiKeyEnvValues(data string) []string {
	out := []string{}
	for _, ln := range strings.Split(data, "\n") {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "apiKeyEnv: ") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(t, "apiKeyEnv: "))
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		}
		out = append(out, v)
	}
	return out
}

func indentOf(line string) string { return line[:len(line)-len(strings.TrimLeft(line, " "))] }

func readGolden(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 golden %s（首次生成用 `go test ./pkg/runtime/dsh -run Golden -update`）：%v", path, err)
	}
	return string(b)
}

// firstDiff 给出首个差异行（比对失败时最有用的一句话）。
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return "首个差异在第 " + strconv.Itoa(i+1) + " 行：\n  期望(改写后/golden): " + strconv.Quote(w) + "\n  实际            : " + strconv.Quote(g)
		}
	}
	return "（两份内容逐行相同）"
}
