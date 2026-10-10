package opencode

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/resource"
)

// ——— A4：opencode 的同步链路与 v11 的**逐字节**等价性（ADR-9/ADR-12） ———
//
// 判据分两部分（设计 §行为基线与等价性验证）：
//
//	A 部分（未点名的一切）：供应商按键排序、缩进、尾换行、顶层 `$schema`、`_arkMate` 归属标记、
//	  `models.<id>.name`、顶层 `model = "<route>/<model>"` —— **必须逐字节相同**；
//	B 部分（本方案点名的偏差，以新形态为期望）：
//	  ① 不再生成 `options` 块（`options` 是 opencode **V1** 的成员名，V2 禁止混入）；
//	  ② `package` 换成**官方原生**字符串（不再是 V1 裸 npm 名）；
//	  ③ 密钥出处由 `envKeyOf(type)` 变成常量化 + **逐供应商**的引用名（修 ADR-16 的真实缺陷）。
//
// A 部分怎么证明"逐字节"？**不是**直接把两份字节相比（那必然不等），而是：把冻结的 v11 字节按
// **仅 B 部分**改写一遍，再与新 adapter 的输出逐字节比对。改写函数只允许做上面三件事——
// 它一旦多做一件事，比对就会红，所以"未点名处"的等价性是机器判的，不是靠人读 diff。

var updateGolden = flag.Bool("update", false, "重新生成 golden 文件（评审时人工过一遍 diff）")

const goldenRefPattern = "MATE_%s_API_KEY"

// goldenFixtureProvider 夹具里的一个供应商。
type goldenFixtureProvider struct {
	ID      string
	Name    string // 平台展示名——**刻意不下发**（见 TestGoldenOpencodeProjectsRouteIDAsName）
	Type    string // v11 的 `type`（只喂给冻结的 v11 生成器）
	BaseURL string
}

type goldenFixtureModel struct {
	ID         string // 平台模型 id
	ProviderID string
	ModelID    string // 发给上游的模型 id
	Name       string
	Default    bool
}

// ≥3 个供应商 × 每个 2 条模型（其中 1 条设默认），含 `name` 与多字节字符。
//
// 两个 `type` 都是 `openai` 的供应商是**刻意的**：v11 的 `envKeyOf(type)` 会让它们共用
// `OPENAI_API_KEY`（同一把密钥命中两条 route，ADR-16 记的真实缺陷），而 v12 的逐供应商引用名
// 必须给出两个不同的名字——这就是 B 部分第 ③ 条的回归。
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

// goldenFixtureDefaultModel 默认模型是**平台模型 id**（不是上游 id，也不是 route+model 短形式）。
const goldenFixtureDefaultModel = "m-beta-1"

// v11FixtureInputs 把同一份夹具翻译成冻结 v11 生成器的输入。
//
// `package` / `api` / `compat` 都填上：它们是 v11 真实产出的一部分，也是 B 部分①②要消掉的东西。
// `extra` 不填：v12 的模型没有这个字段（Q3 删除），见 TestGoldenOpencodeExtraHasNoV12Exit。
func v11FixtureInputs() ([]v11Model, map[string]v11Provider) {
	models := make([]v11Model, 0, len(goldenFixtureModels))
	for _, m := range goldenFixtureModels {
		models = append(models, v11Model{
			ModelID:    m.ModelID,
			Name:       m.Name,
			ProviderID: m.ProviderID,
			IsDefault:  m.Default,
		})
	}
	providers := map[string]v11Provider{}
	for _, p := range goldenFixtureProviders {
		providers[p.ID] = v11Provider{
			ID:     p.ID,
			Type:   p.Type,
			HasKey: true,
			Fragment: map[string]any{
				"package": "@ai-sdk/openai-compatible",
				"baseURL": p.BaseURL,
				"api":     "openai-completions",
				"compat":  map[string]any{"systemRole": "system"},
			},
		}
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

// TestGoldenOpencode A4：新 adapter 的产出 == golden，且 A 部分（把 v11 字节按**仅 B 部分**改写后）
// 与新产出逐字节相同。
func TestGoldenOpencode(t *testing.T) {
	models, providers := v11FixtureInputs()
	v11Bytes, err := buildV11OpencodeJSON(models, providers)
	if err != nil {
		t.Fatalf("冻结的 v11 生成器失败：%v", err)
	}
	v12Bytes, err := buildConfig(goldenSet())
	if err != nil {
		t.Fatalf("新生成器失败：%v", err)
	}

	oldPath := filepath.Join("testdata", "opencode-v11.json")
	newPath := filepath.Join("testdata", "opencode-v12.json")
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

	// ① 冻结副本必须忠实：谁"顺手修"了 v11 的生成器，这里立刻红。
	//    ⚠️ golden 是**先冻结 v11、再改生成器**（计划 T2.2 的顺序），所以这条断言是等价性判据的地基。
	if want := readGolden(t, oldPath); want != string(v11Bytes) {
		t.Errorf("冻结的 v11 生成器产出与 %s 不一致——v11 基线的忠实性被破坏：\n%s",
			oldPath, firstDiff(want, string(v11Bytes)))
	}
	// ② A 部分逐字节：v11 字节经**仅 B 部分**改写后，必须与新产出逐字节相同。
	rewritten := rewriteOpencodeV11ToV12(t, string(v11Bytes))
	if rewritten != string(v12Bytes) {
		t.Errorf("A 部分（未点名处）出现差异——按纪律必须以 golden 为准、把 adapter 改到一致：\n%s",
			firstDiff(rewritten, string(v12Bytes)))
	}
	// ③ 新产出的逐字节契约。
	if want := readGolden(t, newPath); want != string(v12Bytes) {
		t.Errorf("新 adapter 的产出与 %s 不一致（B 部分以新形态为期望）：\n%s",
			newPath, firstDiff(want, string(v12Bytes)))
	}
}

// TestGoldenOpencodeBPartPresent 夹具必须真的覆盖到 B 部分的每一处（否则等价性判据是空转）。
func TestGoldenOpencodeBPartPresent(t *testing.T) {
	models, providers := v11FixtureInputs()
	v11Bytes, err := buildV11OpencodeJSON(models, providers)
	if err != nil {
		t.Fatal(err)
	}
	old := string(v11Bytes)
	v12Bytes, err := buildConfig(goldenSet())
	if err != nil {
		t.Fatal(err)
	}
	got := string(v12Bytes)

	// B①：v11 有 options 块（内含 api / compat），v12 一个都没有。
	if !strings.Contains(old, `"options": {`) || !strings.Contains(old, `"compat": {`) {
		t.Fatal("夹具没有覆盖 B①：v11 产出里应当有 options/compat 块")
	}
	if strings.Contains(got, `"options"`) {
		t.Error("B①未落地：v12 的输出里仍有 options 块（V1 成员混进 V2 条目）")
	}
	for _, banned := range []string{`"api"`, `"compat"`} {
		if strings.Contains(got, banned) {
			t.Errorf("B①未落地：v12 的输出里仍出现 %s（它只该存在于 options 里，已随 options 一起取消出口）", banned)
		}
	}
	// B②：package 换成官方原生字符串。
	if !strings.Contains(old, `"@ai-sdk/openai-compatible"`) {
		t.Fatal("夹具没有覆盖 B②：v11 产出里应当是 V1 的裸 npm 包名")
	}
	if !strings.Contains(got, strconv.Quote(ProviderPackage)) {
		t.Errorf("B②未落地：v12 的 package 应是 %q", ProviderPackage)
	}
	if strings.Contains(got, "@ai-sdk/") {
		t.Error("B②未落地：v12 的输出里仍有 V1 的裸 npm 包名")
	}
	// B③：引用名逐供应商唯一。
	if c := strings.Count(old, `"OPENAI_API_KEY"`); c != 2 {
		t.Fatalf("夹具没有覆盖 B③：v11 的两个 openai 型供应商应共用 OPENAI_API_KEY（实测命中 %d 次）", c)
	}
	envs := envValues(t, got)
	if len(envs) != len(goldenFixtureProviders) {
		t.Fatalf("B③未落地：应有 %d 个引用名，实际 %v", len(goldenFixtureProviders), envs)
	}
	seen := map[string]bool{}
	for _, e := range envs {
		if seen[e] {
			t.Errorf("B③未落地：引用名 %q 命中两条 route（ADR-16 的缺陷回归了）", e)
		}
		seen[e] = true
	}
	for _, p := range goldenFixtureProviders {
		if want := resource.RefFor(goldenRefPattern, p.ID); !seen[want] {
			t.Errorf("B③未落地：供应商 %s 的引用名应为 %q，实际 %v", p.ID, want, envs)
		}
	}
}

// TestGoldenOpencodeProjectsRouteIDAsName 记录一条**刻意的**选择：底座的 `name` 取路由 id，
// 不取供应商展示名。
//
// 理由：`name` 属于 A 部分（未点名处）——v11 写的就是路由 id，而 B 部分只有三处。按纪律
// 「A 部分出现差异 → 以 golden 为准」，把展示名投出去会造出一个未点名的字节差异。
// 这条用例让该选择**机器可见**：将来真要把展示名投给底座，那是一次 ADR 级的新增 B 条目，
// 必须同时改这里、改 golden 头注释、并重跑端到端闸门。
func TestGoldenOpencodeProjectsRouteIDAsName(t *testing.T) {
	set := goldenSet()
	b, err := buildConfig(set)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	provs, _ := doc["providers"].(map[string]any)
	if len(provs) != len(goldenFixtureProviders) {
		t.Fatalf("providers 数量不符：%v", provs)
	}
	for _, p := range goldenFixtureProviders {
		pd, _ := provs[p.ID].(map[string]any)
		if pd == nil {
			t.Fatalf("缺少 route %s", p.ID)
		}
		if pd["name"] != p.ID {
			t.Errorf("route %s 的 name 应为路由 id %q（供应商展示名 %q 不下发），实际 %v",
				p.ID, p.ID, p.Name, pd["name"])
		}
	}
}

// TestGoldenOpencodeExtraHasNoV12Exit 记录另一处**未迁移字段**：v11 的 `extra` 会原样并进
// `models.<id>`，而 v12 的模型没有这个字段（Q3 删除，§载体差异把它登记进「未迁移字段」清单）。
//
// 这里不是等价性判据，而是**如实登记**：夹具因此刻意不带 extra，谁将来把它加回夹具，
// 这条用例会告诉他"v12 没有出口"，而不是让差异悄悄混进 A 部分。
func TestGoldenOpencodeExtraHasNoV12Exit(t *testing.T) {
	models, providers := v11FixtureInputs()
	models[0].Extra = map[string]any{"disabled": true}
	v11Bytes, err := buildV11OpencodeJSON(models, providers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v11Bytes), `"disabled": true`) {
		t.Fatal("v11 的 extra 应当原样进配置（这是它作为唯一私有键出口的形态）")
	}
	// v12：`resource.Model` 没有 extra，产出里自然没有它（同一份夹具去掉 extra 之后）。
	set := goldenSet()
	b, err := buildConfig(set)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"disabled"`) {
		t.Error("v12 的输出里出现了 extra 的键——本期它没有出口（Q3）")
	}
}

// ——— 改写与比对工具 ———

// rewriteOpencodeV11ToV12 把 v11 的字节**只**按 B 部分改写成 v12 应有的形态。
//
// 允许的三件事（多一件都会让 A 部分比对红，这正是它作为判据的意义）：
//  1. 删掉每个 provider 的 `options` 成员；
//  2. 把 `package` 的值换成官方原生字符串；
//  3. 把 `env` 数组里的 `<TYPE>_API_KEY` 换成该 route **逐供应商**的引用名。
//
// 为什么按"出现顺序"而不是按类型映射 env：env 块的顺序就是 provider 对象的键序（encoding/json
// 对 map 键排序），v11 与 v12 都是按 route id 升序 —— 用顺序配对才能证明"第 k 个 route 拿到第 k 个
// 引用名"，而按类型配对恰恰会掩盖 ADR-16 的那个缺陷。
func rewriteOpencodeV11ToV12(t *testing.T, old string) string {
	t.Helper()
	routes := make([]string, 0, len(goldenFixtureProviders))
	for _, p := range goldenFixtureProviders {
		routes = append(routes, p.ID)
	}
	sort.Strings(routes)

	lines := dropJSONMember(t, strings.Split(old, "\n"), "options")
	envIdx := 0
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		switch {
		case trimmed == `"env": [`:
			if i+1 >= len(lines) || envIdx >= len(routes) {
				t.Fatalf("env 块与 route 数量不匹配（第 %d 个，共 %d 个 route）", envIdx+1, len(routes))
			}
			// 数组里只有一个元素：v12 的形态与 v11 逐字节同形，只有值不同。
			lines[i+1] = indentOf(lines[i+1]) + strconv.Quote(resource.RefFor(goldenRefPattern, routes[envIdx]))
			envIdx++
		case strings.HasPrefix(trimmed, `"package": `):
			suffix := ""
			if strings.HasSuffix(trimmed, ",") {
				suffix = ","
			}
			lines[i] = indentOf(lines[i]) + `"package": ` + strconv.Quote(ProviderPackage) + suffix
		}
	}
	if envIdx != len(routes) {
		t.Fatalf("夹具覆盖不全：v11 产出里只有 %d 个 env 块，应有 %d 个", envIdx, len(routes))
	}
	return strings.Join(lines, "\n")
}

// dropJSONMember 删掉一个 `"<member>": {…}` 成员（含它的花括号配对；若它是最后一个成员，
// 还要去掉**前一个**成员行的尾逗号）。
//
// 只认"值是对象"的成员——本夹具里 options 就是这种形态，且它是 provider 结构的最后一个字段，
// 于是两种收尾（带逗号 / 不带逗号）都会走到。
func dropJSONMember(t *testing.T, lines []string, member string) []string {
	t.Helper()
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != `"`+member+`": {` {
			out = append(out, lines[i])
			continue
		}
		depth, j := 0, i
		for ; j < len(lines); j++ {
			depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			if depth == 0 {
				break
			}
		}
		if j >= len(lines) {
			t.Fatalf("成员 %q 的花括号没有闭合", member)
		}
		if !strings.HasSuffix(strings.TrimSpace(lines[j]), ",") {
			// 最后一个成员：把前一个非空行的尾逗号去掉，否则 JSON 不合法。
			for k := len(out) - 1; k >= 0; k-- {
				if strings.TrimSpace(out[k]) == "" {
					continue
				}
				out[k] = strings.TrimSuffix(out[k], ",")
				break
			}
		}
		i = j
	}
	return out
}

func indentOf(line string) string { return line[:len(line)-len(strings.TrimLeft(line, " "))] }

// envValues 取出产出里所有 `env` 数组的值（结构化解法：不依赖文本形态）。
func envValues(t *testing.T, data string) []string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(data), &doc); err != nil {
		t.Fatal(err)
	}
	provs, _ := doc["providers"].(map[string]any)
	keys := make([]string, 0, len(provs))
	for k := range provs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{}
	for _, k := range keys {
		pd, _ := provs[k].(map[string]any)
		arr, _ := pd["env"].([]any)
		for _, v := range arr {
			s, _ := v.(string)
			out = append(out, s)
		}
	}
	return out
}

func readGolden(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 golden %s（首次生成用 `go test ./pkg/runtime/opencode -run Golden -update`）：%v", path, err)
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
