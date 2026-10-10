package opencode

import (
	"encoding/json"
	"sort"
)

// 本文件是 **v11 生成器的冻结副本**（逐字抄自 `git show 358ab27:backend/internal/manager/runtime_config.go`
// 的 `buildOpencodeJSON` 及其输入类型），只在测试里存在。
//
// 为什么要冻结一份而不是继续引用活代码：活的那份在 M2 之后被删除（知识下沉到 adapter），而等价性
// 判据（A4/ADR-9/ADR-12）需要一个**不会随时间漂移**的基线。冻结副本的忠实性由测试自己钉住：
// `TestGoldenOpencode` 断言它对本夹具的产出与 `testdata/opencode-v11.json` 逐字节相同——谁"顺手修"
// 了这个副本，测试立刻红。
//
// 另外**不要**给它加新特性（例如把 package 换成官方原生名）：它是历史，不是待改进的代码。

const v11GeneratorVersion = 1

// v11Model v11 的模型输入（`runtimeConfigModel` 的冻结形）。
type v11Model struct {
	ModelID    string
	Name       string
	Extra      map[string]any
	IsDefault  bool
	ProviderID string
}

// v11Provider v11 的供应商输入（`providerProjection` 的冻结形）。
type v11Provider struct {
	ID     string
	Type   string
	HasKey bool
	Body   map[string]any
	// Fragment `projections.<runtime>` 片段（可为 nil）。
	Fragment map[string]any
}

// v11EnvKeyOf provider type → 注入的环境变量名（`backend/internal/manager/providers.go` 的冻结形）。
//
// 它正是 ADR-16 记的那个真实缺陷的来源：**每个 type 一个名字**，而 `providers` 表对 `type` 无唯一
// 约束 → 两个同 type 的供应商共用一把密钥。
func v11EnvKeyOf(typ string) string {
	if typ == "" {
		return ""
	}
	switch typ {
	case "openai", "anthropic", "deepseek", "openrouter", "groq":
		return up(typ) + "_API_KEY"
	}
	return up(typ) + "_API_KEY"
}

func up(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

type v11ConfigDoc struct {
	Schema    string                    `json:"$schema"`
	Providers map[string]v11ProviderDoc `json:"providers"`
	Model     string                    `json:"model,omitempty"`
	Ownership map[string]any            `json:"_arkMate,omitempty"`
}

type v11ProviderDoc struct {
	Name     string                 `json:"name,omitempty"`
	Env      []string               `json:"env,omitempty"`
	Package  string                 `json:"package,omitempty"`
	Settings map[string]any         `json:"settings,omitempty"`
	Models   map[string]v11ModelDoc `json:"models,omitempty"`
	Options  map[string]any         `json:"options,omitempty"`
}

type v11ModelDoc map[string]any

// buildV11OpencodeJSON v11 的 `buildOpencodeJSON`（逐字冻结；连注释里的理由都保持原样）。
func buildV11OpencodeJSON(models []v11Model, providers map[string]v11Provider) ([]byte, error) {
	doc := v11ConfigDoc{
		Schema:    "https://opencode.ai/config.json",
		Providers: map[string]v11ProviderDoc{},
		Ownership: map[string]any{"manager": "ark-mate", "generator_version": v11GeneratorVersion},
	}
	routes := make([]string, 0, len(providers))
	for id := range providers {
		routes = append(routes, id)
	}
	sort.Strings(routes)

	for _, routeID := range routes {
		p := providers[routeID]
		pd := v11ProviderDoc{Name: routeID, Models: map[string]v11ModelDoc{}}
		if k := v11EnvKeyOf(p.Type); k != "" {
			pd.Env = []string{k}
		}
		if frag := p.Fragment; frag != nil {
			pd.Package = v11BodyString(frag, "package")
			switch {
			case v11BodyString(frag, "baseURL") != "":
				pd.Settings = map[string]any{"baseURL": v11BodyString(frag, "baseURL")}
			case v11BodyString(frag, "settings_baseURL") != "":
				pd.Settings = map[string]any{"baseURL": v11BodyString(frag, "settings_baseURL")}
			default:
				if st, ok := frag["settings"].(map[string]any); ok {
					pd.Settings = st
				}
			}
			opts := map[string]any{}
			if opt, ok := frag["options"].(map[string]any); ok {
				for k, v := range opt {
					opts[k] = v
				}
			}
			if api := v11BodyString(frag, "api"); api != "" {
				opts["api"] = api
			}
			if compat, ok := frag["compat"].(map[string]any); ok {
				opts["compat"] = compat
			}
			if len(opts) > 0 {
				pd.Options = opts
			}
		}
		doc.Providers[routeID] = pd
	}

	defaultRoute, defaultModel := "", ""
	for _, m := range models {
		pd, ok := doc.Providers[m.ProviderID]
		if !ok {
			continue
		}
		if pd.Models == nil {
			pd.Models = map[string]v11ModelDoc{}
		}
		md := v11ModelDoc{}
		if m.Name != "" {
			md["name"] = m.Name
		}
		for k, v := range m.Extra {
			if k == "name" || k == "model_id" || k == "hidden" || k == "pinned" || k == "is_default" {
				continue
			}
			md[k] = v
		}
		pd.Models[m.ModelID] = md
		doc.Providers[m.ProviderID] = pd
		if m.IsDefault {
			defaultRoute, defaultModel = m.ProviderID, m.ModelID
		}
	}
	if defaultRoute != "" {
		doc.Model = defaultRoute + "/" + defaultModel
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func v11BodyString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
