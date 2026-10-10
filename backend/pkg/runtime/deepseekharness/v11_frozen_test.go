package deepseekharness

import (
	"encoding/json"
	"fmt"
	"sort"
)

// 本文件是 **v11 生成器的冻结副本**（逐字抄自 `git show 358ab27:backend/internal/manager/runtime_config.go`
// 的 `buildDshPatchYAML` / `yamlQuote` / `yamlEscape` 及其输入类型），只在测试里存在。
//
// 为什么要冻结一份：活的那份在 M3 之后被删除（知识下沉到 adapter），而等价性判据（A5/ADR-9/ADR-12）
// 需要一个**不会随时间漂移**的基线。冻结副本的忠实性由测试自己钉住：`TestGoldenDsh` 断言它对本夹具
// 的产出与 `testdata/dsh-v11.patch.yml` 逐字节相同——谁"顺手修"了这个副本，测试立刻红。

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
	ID       string
	Type     string
	HasKey   bool
	Body     map[string]any
	Fragment map[string]any
}

// v11EnvKeyOf provider type → 注入的环境变量名（`backend/internal/manager/providers.go` 的冻结形）。
func v11EnvKeyOf(typ string) string {
	if typ == "" {
		return ""
	}
	return v11Upper(typ) + "_API_KEY"
}

func v11Upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

// buildV11DshPatchYAML v11 的 `buildDshPatchYAML`（逐字冻结）。
func buildV11DshPatchYAML(models []v11Model, providers map[string]v11Provider) ([]byte, error) {
	var b []byte
	b = append(b, []byte("# ark-mate 生成的模型清单 overlay（平台自有文件；经 dsh --patch 递入）\n")...)
	b = append(b, []byte("# 归属标记：本文件只有平台会写；dsh 自己的 cordis.patch.yml 不在此列。\n")...)
	for _, m := range models {
		if !m.IsDefault {
			continue
		}
		b = append(b, []byte("- id: acp\n  config:\n")...)
		b = append(b, fmt.Sprintf("    provider: %s\n    model: %s\n", v11YamlQuote(m.ProviderID), v11YamlQuote(m.ModelID))...)
	}
	if len(providers) > 0 {
		routes := make([]string, 0, len(providers))
		for id := range providers {
			routes = append(routes, id)
		}
		sort.Strings(routes)
		for _, route := range routes {
			p := providers[route]
			b = append(b, []byte("- id: llm-pi-ai\n  config:\n    providers:\n")...)
			b = append(b, fmt.Sprintf("      %s:\n        displayName: %s\n", v11YamlQuote(route), v11YamlQuote(route))...)
			if frag := p.Fragment; frag != nil {
				if api := v11BodyString(frag, "api"); api != "" {
					b = append(b, fmt.Sprintf("        api: %s\n", v11YamlQuote(api))...)
				}
			}
			if k := v11EnvKeyOf(p.Type); k != "" {
				b = append(b, fmt.Sprintf("        apiKeyEnv: %s\n", v11YamlQuote(k))...)
			}
			if frag := p.Fragment; frag != nil {
				if base := v11BodyString(frag, "baseURL"); base != "" {
					b = append(b, fmt.Sprintf("        baseURL: %s\n", v11YamlQuote(base))...)
				}
				if compat, ok := frag["compat"].(map[string]any); ok && len(compat) > 0 {
					cb, err := json.Marshal(compat)
					if err != nil {
						return nil, fmt.Errorf("manager: 序列化 %s 的 compat 失败: %w", route, err)
					}
					b = append(b, fmt.Sprintf("        compat: %s\n", cb)...)
				}
			}
			b = append(b, []byte("        models:\n")...)
			for _, m := range models {
				if m.ProviderID != route {
					continue
				}
				b = append(b, fmt.Sprintf("          - id: %s\n", v11YamlQuote(m.ModelID))...)
				if m.Name != "" {
					b = append(b, fmt.Sprintf("            name: %s\n", v11YamlQuote(m.Name))...)
				}
			}
		}
	}
	return b, nil
}

func v11YamlQuote(s string) string { return `"` + v11YamlEscape(s) + `"` }

func v11YamlEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(b[1 : len(b)-1])
}

func v11BodyString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
