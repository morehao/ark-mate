package runtime

import (
	"fmt"
	"sort"
	"strings"
)

// ——— provider 的**逐 runtime 声明**（H1/H2）：`providers.body.projections.<runtime>` ———
//
// 为什么需要它：平台清单要让一条自定义模型**真的能连通**，光写 "id + name" 不够——每个底座都要求
// 若干**运行时参数**（走哪个 wire 协议、打到哪个 baseURL、用哪个 package…），而这些参数与"哪条
// 凭据"无关、与"哪个底座"强相关。所以它们按 runtime 声明在凭据上：
//
//	projections: {
//	  "opencode": { "package": "@ai-sdk/openai-compatible", "baseURL": "…", "api": "openai-completions" },
//	  "deepseekharness": { "api": "openai-completions", "baseURL": "…" }
//	}
//
// 三条纪律：
//
//	① **未知键原样保留**（与 body 里其它未知键同一条纪律）：平台只校验它认识的字段，
//	   其余原样带走——将来的底座版本新增字段时，用户写进去的值不会因为平台不认识而消失。
//	② **类型错误必须报**：`api: 123` 这种写进去会让底座侧解析失败（dsh 侧甚至可能拒启），
//	   所以认识的字段要判类型；不认识的名字则放过（见①）。
//	③ **schema 由 adapter 声明**（本文件按 `Spec.ConfigProjection.Format` 派生），不是前端硬编码：
//	   前端只是把后端给的字段渲染成表单（H3），加一个底座不需要改前端。

// ProviderProjectionField 一条可声明的运行时参数。
type ProviderProjectionField struct {
	// Name 字段名（写进 `projections.<runtime>` 的键）。
	Name string `json:"name"`
	// Type string | object；仅用于提示与类型校验。
	Type string `json:"type"`
	// Required 该底座是否要求它（缺了会让模型连不上）。
	Required bool `json:"required"`
	// Note 一句话说明（前端当 placeholder/后缀，用户看到的就是它）。
	Note string `json:"note,omitempty"`
}

// ProviderProjectionSchema 某 runtime 的运行时参数 schema。
type ProviderProjectionSchema struct {
	Runtime string                   `json:"runtime"`
	Fields  []ProviderProjectionField `json:"fields"`
	Note    string                   `json:"note,omitempty"`
}

// 两个底座共有的字段（wire 协议与端点）。
var commonProjectionFields = []ProviderProjectionField{
	{Name: "api", Type: "string", Note: "wire 协议（dsh/pi-ai：openai-completions | openai-responses | anthropic-messages）；自定义 route 不写它会被判为不可用"},
	{Name: "baseURL", Type: "string", Note: "上游端点（自定义 route 必填；不写会打到厂商默认端点）"},
	{Name: "compat", Type: "object", Note: "兼容开关（不认识端点的网关按需填，例如系统提示词放哪个 role）"},
}

// ProviderProjectionSchemaFor 派生某 runtime 的 provider 投影 schema。
//
// `ok=false` = 该 runtime 不承载平台模型清单（未声明 ConfigProjection），
// 因此对它的 `projections.<runtime>` 没有意义（写面据此拒绝，而不是默默存一条没人看的声明）。
func ProviderProjectionSchemaFor(spec Spec) (ProviderProjectionSchema, bool) {
	if !spec.ConfigProjection.Declared() {
		return ProviderProjectionSchema{}, false
	}
	s := ProviderProjectionSchema{Runtime: spec.ID}
	switch spec.ConfigProjection.Format {
	case ConfigFormatOpencodeJSON:
		s.Fields = append([]ProviderProjectionField{
			{Name: "package", Type: "string", Note: "opencode 侧加载该 route 用的 npm 包（自定义 route 常常要）"},
			{Name: "options", Type: "object", Note: "透传给 opencode 的 provider options"},
		}, commonProjectionFields...)
		s.Note = "写到 opencode 的 providers.<route>：package / options / api / baseURL 与兼容开关"
	case ConfigFormatDshPatchYAML:
		s.Fields = append([]ProviderProjectionField{}, commonProjectionFields...)
		s.Note = "写到 dsh 的 patch：自定义 route 需要 api + baseURL + 非空 models（dsh 侧判不可用会拒绝该 route）"
	default:
		return ProviderProjectionSchema{}, false
	}
	return s, true
}

// ValidateProviderProjection 校验一条 `projections.<runtime>` 声明。
//
// 返回**非法字段**（名字 + 原因）而不是直接报错：调用方要把它们拼成一条可执行的出路。
// 未知键**不在这里拒绝**（见文件头纪律①）。
func ValidateProviderProjection(spec Spec, frag map[string]any) []string {
	if len(frag) == 0 {
		// 没声明就没有可校验的东西——**这一点很重要**：可达性那一面会对每条已注册 runtime 都调它，
		// 若空声明也返回"该 runtime 不承载模型清单"，那这条理由会出现在**每一个** runtime 上。
		return nil
	}
	schema, ok := ProviderProjectionSchemaFor(spec)
	if !ok {
		return []string{fmt.Sprintf("runtime %q 不承载平台模型清单（未声明 ConfigProjection），对它写运行时参数没有意义", spec.ID)}
	}
	byName := map[string]ProviderProjectionField{}
	for _, f := range schema.Fields {
		byName[f.Name] = f
	}
	var problems []string
	for name, val := range frag {
		f, known := byName[name]
		if !known {
			continue // 未知键原样保留（纪律①）
		}
		switch f.Type {
		case "string":
			if _, isStr := val.(string); !isStr {
				problems = append(problems, fmt.Sprintf("%s.%s 应为字符串，实际 %T", spec.ID, name, val))
			}
		case "object":
			if _, isMap := val.(map[string]any); !isMap {
				problems = append(problems, fmt.Sprintf("%s.%s 应为对象，实际 %T", spec.ID, name, val))
			}
		}
	}
	// 自定义 route 的"必填"是**软**要求：缺 baseURL 时我们只提示，不阻断（用户可能是先登记后补）。
	for _, f := range schema.Fields {
		if !f.Required {
			continue
		}
		if _, ok := frag[f.Name]; !ok {
			problems = append(problems, fmt.Sprintf("%s.%s 缺失（%s）", spec.ID, f.Name, f.Note))
		}
	}
	sort.Strings(problems)
	return problems
}

// ProjectionSummary 把一条投影声明渲染成一句话（`GET /providers` 的 applicability reason 用）。
func ProjectionSummary(frag map[string]any) string {
	if len(frag) == 0 {
		return ""
	}
	keys := make([]string, 0, len(frag))
	for k := range frag {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if s, ok := frag[k].(string); ok && s != "" {
			parts = append(parts, k+"="+s)
			continue
		}
		parts = append(parts, k)
	}
	return strings.Join(parts, "、")
}
