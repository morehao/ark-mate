package dsh

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/resource"
)

// ——— A5：dsh 的同步链路与 v11 的**逐字节**等价性（ADR-9/ADR-12） ———
//
// 判据分两部分（设计 §行为基线与等价性验证）：
//
//	A 部分（未点名的一切）：供应商按键排序、缩进、尾换行、`- id: acp` / `- id: llm-pi-ai` 的条目形态、
//	  `api`、`baseURL`、`models[]` 的顺序与 `name` 的 YAML 转义 —— **必须逐字节相同**；
//	B 部分（本方案点名的偏差，以新形态为期望）：
//	  B1 `apiKeyEnv` 由 `envKeyOf(type)`（每个 type 一个名字）换成**逐供应商**的引用名（修 ADR-16 的真实缺陷）；
//	  B2 每个模型条目**多一行** `input: [text]` / `[text, image]`（v13/M6 的 ADR-12：平台**总是**声明
//	     输入模态，所以"默认 text"是明确值而不是"没设"——代价是覆盖底座 catalog，见 config.go 的注释）；
//	  B3 文件名（v24–v25 的路由键隔离，见 spec §5.1）：① `providers.<k>` 的键与 `- id: acp` 的
//	     `provider:` 值都加平台保留前缀 `mate-`；② `displayName` 从"路由键"改成**供应商展示名**
//	     （空则回落路由键）——即"这一个字段的取值不再是路由键本身"。
//
// A 部分的证明方式：把冻结的 v11 字节按**仅 B 部分**改写一遍，再与新 adapter 的输出逐字节比对。
// 改写函数只允许做 B 部分那几件事——多做一件比对就红。
//
// 文件名保留 `dsh-v12.patch.yml`：它命名的是**等价性序列**（"v11 基线 → 当前形态"），不是当前
// 版本号——B2 于 v13 加入时就沿用了这个名字，B3 同样。
//
// 另外两处**未迁移字段**（`compat` 与模型的 `extra`）在本期没有出口，夹具因此刻意不带它们；
// 它们由 `TestGoldenDshCompatHasNoV12Exit` 单独**机器登记**，不混进 A 部分。

var updateGolden = flag.Bool("update", false, "重新生成 golden 文件（评审时人工过一遍 diff）")

const goldenRefPattern = "MATE_%s_API_KEY"

type goldenFixtureProvider struct {
	ID   string
	Name string // 平台展示名——v24 起**下发**为 `displayName`（见 TestGoldenDshProjectsProviderNameAsDisplayName）
	Type string // v11 的 `type`（只喂给冻结的 v11 生成器）
	// BaseURL 端点。空串 = 不写 `baseURL` 行（用来覆盖"这个字段可以缺席"这条形态）。
	BaseURL string
}

type goldenFixtureModel struct {
	ID         string // 平台模型 id
	ProviderID string
	// Model 发给上游的模型名（v24 起这一列叫 `model`；这里跟到平台词，v11 的 `ModelID` 是历史词）。
	Model   string
	Name    string
	Default bool
	// Modalities 输入模态（nil = 默认 `[text]`）。夹具里**两类都要有**，否则 B2 的两种形态
	// 只被覆盖到一种——而"显式 text 覆盖了 catalog"与"用户勾了图片"是两件必须分别可见的事。
	Modalities []string
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
		{ID: "m-alpha-1", ProviderID: "alpha-gw", Model: "gpt-4o-mini", Name: "小模型·中文",
			Modalities: []string{"text", "image"}},
		{ID: "m-alpha-2", ProviderID: "alpha-gw", Model: "gpt-4o", Name: `大模型 "A" \ 备用`},
		{ID: "m-beta-1", ProviderID: "beta.gw", Model: "qwen:7b", Name: "qwen #7b", Default: true},
		{ID: "m-beta-2", ProviderID: "beta.gw", Model: "llama/3-8b", Name: ""},
		{ID: "m-zeta-1", ProviderID: "zeta", Model: "deepseek-chat", Name: "DeepSeek 对话"},
		{ID: "m-zeta-2", ProviderID: "zeta", Model: "deepseek-reasoner", Name: "推理\t模型"},
	}
)

// 主夹具里**没有**展示名为空的供应商——为什么那条回落链不放进来：主夹具同时喂给**冻结的 v11
// 生成器**，加一家供应商会把 `testdata/dsh-v11.patch.yml`（v11 基线本身）一起改写掉，而计划
// §4.M2.T4.5 要求它**一个字节都不许动**（"v11 基线"这份证据的价值就在于它是被评审过的那一份）。
// 回落链因此由 `TestGoldenDshDisplayNameFallsBackToRouteKey` 用一个**独立的小集合**覆盖。
const goldenFixtureDefaultModel = "m-beta-1"

// v11FixtureInputs 把夹具翻译成冻结 v11 生成器的输入。
//
// 模型按**上游模型名**排序——这是 v11 的 `collectRuntimeConfigInput` 的真实输入形态（它会对模型做一次
// 全局上游名升序），也是 v12 逐 route 过滤后的数组顺序。**不排就会让数组顺序变**，那属于 A 部分。
//
// `compat` 是 v11 独有、v12 没有出口的字段（设计 §载体差异的「未迁移字段」清单），所以它由调用方
// 显式给出（主 golden 传 nil）；不给参数就不动全局夹具，避免用例之间互相污染。
func v11FixtureInputs(compat map[string]map[string]any) ([]v11Model, map[string]v11Provider) {
	models := make([]v11Model, 0, len(goldenFixtureModels))
	for _, m := range goldenFixtureModels {
		models = append(models, v11Model{
			ModelID:    m.Model,
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
		models = append(models, resource.Model{
			ID: m.ID, ProviderID: m.ProviderID, Model: m.Model, Name: m.Name,
			// 端口契约是"总是带值"：nil 也要显式给 `[text]`（与编排层的行为一致）。
			InputModalities: goldenModalities(m),
		})
	}
	return resource.Set{
		Runtime:      ID,
		Providers:    providers,
		Models:       models,
		DefaultModel: goldenFixtureDefaultModel,
	}
}

// goldenModalities 夹具里一条模型的模态（nil = 默认 `[text]`）。
func goldenModalities(m goldenFixtureModel) []string {
	if len(m.Modalities) == 0 {
		return []string{resource.ModalityText}
	}
	return resource.NormalizeModalities(m.Modalities)
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
	rewritten := rewriteDshV11ToV24(t, string(v11Bytes))
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
	// B2：**每个**模型条目都有 `input`（不是"没设"），且两种形态都出现。
	if got := strings.Count(got, "            input: [text]\n"); got != len(goldenFixtureModels)-1 {
		t.Errorf("B2 未落地：应有 %d 条 `input: [text]`，实际 %d", len(goldenFixtureModels)-1, got)
	}
	if got := strings.Count(got, "            input: [text, image]\n"); got != 1 {
		t.Errorf("B2 未落地：应恰好 1 条 `input: [text, image]`（勾了图片的那条），实际 %d", got)
	}
	if c := strings.Count(got, "            input: ["); c != len(goldenFixtureModels) {
		t.Errorf("B2 未落地：每个模型条目都应写 input（实测 %d 处，应有 %d 处）", c, len(goldenFixtureModels))
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
	// 默认模型：`Set.DefaultModel` 是**平台 id**，必须解析成上游 id 与它挂靠的**派生路由键**。
	if !strings.Contains(got, "    provider: \"mate-beta.gw\"\n    model: \"qwen:7b\"\n") {
		t.Error("默认模型未按 `DefaultModel`（平台 id）解析成派生路由键 + 上游模型名")
	}

	// B3 的前置：v11 的产出里**确实有**这三类文本，否则 B3 的改写是空转
	// （等价性判据自己失效是最难查的一类绿：两边都不含被改写的形态，比对当然相等）。
	for _, needle := range []string{"    provider: \"", "        displayName: \"", "    providers:\n"} {
		if !strings.Contains(old, needle) {
			t.Fatalf("v11 产出里没有 %q——B3 的改写对象不存在，等价性判据在空转", needle)
		}
	}
	// B3 的落地形态（以新产出为期望）：键带保留前缀、展示名是供应商展示名（空则回落路由键）。
	for _, p := range goldenFixtureProviders {
		wantRoute := "      \"mate-" + p.ID + "\":\n"
		if !strings.Contains(got, wantRoute) {
			t.Errorf("B3 未落地：路由键应为 %q", "mate-"+p.ID)
		}
		wantName := fixtureDisplayName(p.ID, p.Name)
		if !strings.Contains(got, wantRoute+"        displayName: "+strconv.Quote(wantName)+"\n") {
			t.Errorf("B3 未落地：供应商 %s 的 displayName 应为 %q（展示名空则回落路由键）", p.ID, wantName)
		}
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
	if got, want := rewriteDshV11ToV24(t, stripped), string(v12Bytes); got != want {
		t.Errorf("compat 是本例唯一的差异，其余字节必须相同：\n%s", firstDiff(want, got))
	}
}

// TestGoldenDshProjectsProviderNameAsDisplayName 记录一条 v24 起**有意变更**的交付形态：
// `displayName` 交付**供应商展示名**（空则回落路由键），而不是路由键本身。
//
// 原断言（`TestGoldenDshProjectsRouteIDAsDisplayName`）钉的是"展示名属于 A 部分、v11 写的就是路由
// id"——那条纪律的理由是"v11 里没有供应商展示名这个概念"（`resource.Provider.Name` 是 v12 才进
// 夹具的字段）。v24 推翻它：展示名是用户在平台里起的名字，交付面把它写成路由键会让这个名字**在
// 底座侧完全不可见**（库里叫「内网网关 Alpha」，界面上显示 `alpha-gw`）。
//
// 代价如实写：这一行不再与 v11 逐字节相同——所以它进了 B 部分（B3），由
// `rewriteDshV11ToV24` 机械改写后参与 A 部分的比对，而不是被悄悄放过。
func TestGoldenDshProjectsProviderNameAsDisplayName(t *testing.T) {
	b, err := buildPatch(goldenSet())
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, p := range goldenFixtureProviders {
		if p.Name == "" {
			t.Fatalf("主夹具的供应商 %s 没有展示名：这条用例判的是「展示名被下发」，它必须每家都有名字", p.ID)
		}
		want := "      \"mate-" + p.ID + "\":\n        displayName: " + strconv.Quote(p.Name) + "\n"
		if !strings.Contains(got, want) {
			t.Errorf("route mate-%s 的 displayName 应为供应商展示名 %q", p.ID, p.Name)
		}
	}
	// 反向：`displayName` 不得再等于**不带前缀**的平台 id 或**带前缀的路由键**（那正是被推翻的旧形态）。
	for _, p := range goldenFixtureProviders {
		for _, banned := range []string{p.ID, "mate-" + p.ID} {
			if strings.Contains(got, "        displayName: \""+banned+"\"\n") {
				t.Errorf("route %s 的 displayName 退回了旧形态 %q——它必须来自供应商展示名", p.ID, banned)
			}
		}
	}
}

// TestGoldenDshDisplayNameFallsBackToRouteKey 覆盖回落链的另一半：供应商**没有**展示名时，
// `displayName` 回落**路由键**（DSH 语义逐字："defaults to the route key"）。
//
// 为什么单独一条、不放主夹具：见 `goldenFixtureProviders` 后面那段说明——主夹具喂给冻结的 v11
// 生成器，往里加一家供应商会把 v11 基线文件一起改写掉（计划 T4.5 不许）。
//
// 反证：把 `displayNameOf` 的回落改成空串 / 平台 id → 本用例红。
func TestGoldenDshDisplayNameFallsBackToRouteKey(t *testing.T) {
	b, err := buildPatch(resource.Set{
		Runtime: ID,
		Providers: []resource.Provider{
			{ID: "noname", Protocol: resource.ProtocolOpenAI, BaseURL: "https://n.example.com/v1"},
		},
		Models: []resource.Model{
			{ID: "m-noname", ProviderID: "noname", Model: "n-1", InputModalities: []string{resource.ModalityText}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "      \"mate-noname\":\n        displayName: \"mate-noname\"\n"
	if !strings.Contains(string(b), want) {
		t.Errorf("没有展示名时 displayName 应回落路由键：\nwant:\n%s\ngot:\n%s", want, b)
	}
}

// fixtureDisplayName 供应商在交付面上的**期望**展示名。
//
// 这里刻意**不调用** `displayNameOf`：判据若由被测实现自己生成，改了实现它也跟着改，等于没有判据
// （golden 的价值就在"期望是写死的字节"）。
func fixtureDisplayName(id, name string) string {
	if name != "" {
		return name
	}
	return "mate-" + id
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

// rewriteDshV11ToV24 把 v11 的字节**只**按 B 部分改写（B1/B2/B3），用于 A 部分的逐字节比对。
//
// B1：`apiKeyEnv` 的值换成逐供应商引用名。按"出现顺序"配对（apiKeyEnv 行的顺序 = route 升序 =
// provider 对象键序），这样它顺带证明了"第 k 个 route 拿到第 k 个引用名"；按类型配对恰恰会掩盖
// ADR-16 的那个缺陷。
// B2：给每个模型条目补一行 `input: [...]`（见 `insertGoldenModelInputs`）。
// B3：路由键加保留前缀 + `displayName` 换成供应商展示名（见 `rewriteGoldenRouteKeys`）。
//
// 顺序：B1 与 B3 都在"行级"上作业，但改的是互不相交的行；B2 是**插入**行，必须先做完再让 B3 扫
// ——否则 B3 会去认那些还不存在的行（`input` 行与 displayName/键行的前缀都不同，实际不会撞，但
// 依赖"不撞"比依赖"顺序"脆）。所以固定为 B1 → B2 → B3。
func rewriteDshV11ToV24(t *testing.T, old string) string {
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
	// B2：给**每个**模型条目补一行 `input: [...]`（v11 没有这个字段，所以它只能"插入"）。
	// 插入位置 = 该条目的末尾（下一个 `          - id: ` 之前，或文件末尾）——与 adapter
	// 把 `input` 写在 `name` 之后一致。
	return rewriteGoldenRouteKeys(t, insertGoldenModelInputs(t, strings.Join(lines, "\n")))
}

// rewriteGoldenRouteKeys 是 B3 的机械改写：① 路由键（`providers.<k>` 的键与 `- id: acp` 的
// `provider:` 值）加平台保留前缀 `mate-`；② `displayName` 从"路由键"换成供应商展示名（空则回落
// 路由键，与 `displayNameOf` 同口径）。
//
// 为什么按**行内的那个 id**配对而不是按"第 k 个"：这三个形态各自携带它属于哪条 route 的信息，
// 按出现顺序配对只会在夹具顺序变化时静默错位；而"行里的 id 不在夹具里"必须**响**——那正是配对不上
// 的情形（T4.1 的 `t.Fatalf`），静默漏改会让 A 部分的比对在两边都不含新形态时假绿。
func rewriteGoldenRouteKeys(t *testing.T, text string) string {
	t.Helper()
	names := map[string]string{}
	for _, p := range goldenFixtureProviders {
		names[p.ID] = p.Name
	}
	lines := strings.Split(text, "\n")
	keys, providers, displayNames := 0, 0, 0
	for i, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "      \"") && strings.HasSuffix(ln, "\":"):
			// ①a `providers.<k>:`（缩进 6）。
			id := strings.TrimSuffix(strings.TrimPrefix(ln, "      \""), "\":")
			if _, ok := names[id]; !ok {
				t.Fatalf("第 %d 行的路由键 %q 不在夹具里——B3 的改写配对不上（必须响，不能静默漏改）", i+1, id)
			}
			lines[i] = "      \"mate-" + id + "\":"
			keys++
		case strings.HasPrefix(ln, "    provider: \"") && strings.HasSuffix(ln, "\""):
			// ①b `- id: acp` 块的 `provider:` 值（缩进 4）。
			id := strings.TrimSuffix(strings.TrimPrefix(ln, "    provider: \""), "\"")
			if _, ok := names[id]; !ok {
				t.Fatalf("第 %d 行的 provider 值 %q 不在夹具里——B3 的改写配对不上", i+1, id)
			}
			lines[i] = "    provider: \"mate-" + id + "\""
			providers++
		case strings.HasPrefix(ln, "        displayName: \"") && strings.HasSuffix(ln, "\""):
			// ② `displayName:`（缩进 8）。
			id := strings.TrimSuffix(strings.TrimPrefix(ln, "        displayName: \""), "\"")
			name, ok := names[id]
			if !ok {
				t.Fatalf("第 %d 行的 displayName %q 不在夹具里——B3 的改写配对不上", i+1, id)
			}
			lines[i] = "        displayName: " + yamlQuote(fixtureDisplayName(id, name))
			displayNames++
		}
	}
	switch {
	case keys != len(goldenFixtureProviders):
		t.Fatalf("B3 的路由键改写覆盖不全：改了 %d 处，应有 %d 处（夹具/形态分叉）", keys, len(goldenFixtureProviders))
	case providers != 1:
		t.Fatalf("B3 的 `provider:` 改写覆盖不全：改了 %d 处，应有 1 处（默认模型那一行）", providers)
	case displayNames != len(goldenFixtureProviders):
		t.Fatalf("B3 的 displayName 改写覆盖不全：改了 %d 处，应有 %d 处", displayNames, len(goldenFixtureProviders))
	}
	return strings.Join(lines, "\n")
}

// insertGoldenModelInputs 在每个模型条目末尾插入 `input:` 行（B2 的机械改写）。
//
// 只认缩进 10 空格的 `- id: "<上游模型 id>"`（`- id: acp` 在缩进 0，不会被误命中）。
func insertGoldenModelInputs(t *testing.T, text string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines)+len(goldenFixtureModels))
	pending := ""
	flush := func() {
		if pending == "" {
			return
		}
		out = append(out, "            input: ["+strings.Join(modalitiesOfUpstream(pending), ", ")+"]")
		pending = ""
	}
	for _, ln := range lines {
		if strings.HasPrefix(ln, "          - id: ") {
			flush()
			pending = strings.Trim(strings.TrimSpace(strings.TrimPrefix(ln, "          - id: ")), `"`)
			out = append(out, ln)
			continue
		}
		// 条目的内容行缩进 12 空格；出现缩进更浅的非空行 = 这个条目结束了
		// （下一个 route 的键、下一条 `- id:`，或文件末尾）。
		if pending != "" && !strings.HasPrefix(ln, "            ") {
			flush()
		}
		out = append(out, ln)
	}
	flush()
	return strings.Join(out, "\n")
}

// modalitiesOfUpstream 上游模型名 → 夹具里那条模型声明的模态（找不到就按默认 `[text]`）。
func modalitiesOfUpstream(upstream string) []string {
	for _, m := range goldenFixtureModels {
		if m.Model == upstream {
			return goldenModalities(m)
		}
	}
	return []string{resource.ModalityText}
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
		t.Fatalf("读不到 golden %s（首次生成用 `go test ./internal/infra/dsh -run Golden -update`）：%v", path, err)
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
