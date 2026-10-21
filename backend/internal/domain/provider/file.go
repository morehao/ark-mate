package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/platform/code"
)

// ——— providers.json 的磁盘形状与词法校验（纯规则，v3.2 刀 8）———
//
// 导入/导出**编排**在 `internal/adapters/outbound/transfer`（它要碰 store 与 mechanism）；这里只留
// "这个文件长什么样、id 合不合法"——那两条是域自己的词汇，编排与用例层都要用。

const ProvidersFileName = "providers.json"

// ProviderFile providers.json 的磁盘形状。
//
// Key 字段**只读**：用于把 v20 之前的明文档案搬进托管文档；之后一律不再写出（写它等于把
// 明文放回一个可被复制/备份的文件里，而那正是 v20 想去掉的那份重复）。
type ProviderFile struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Note     string `json:"note,omitempty"`
	KeyRef   string `json:"key_ref,omitempty"`
	Key      string `json:"key,omitempty"`
	// Type/Projections 是 v11 的遗留（导入时按 v11 → v12 字段映射转换，见 provider_import.go）。
	Type        string         `json:"type,omitempty"`
	Projections map[string]any `json:"projections,omitempty"`
}

// V11FileBaseURL 从 v11 的文件档案里推导端点（`projections.*.baseURL` 优先，其次 type 表）。
func V11FileBaseURL(d *ProviderFile) string {
	keys := make([]string, 0, len(d.Projections))
	for k := range d.Projections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		frag, ok := d.Projections[k].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := frag["baseURL"].(string); ok && v != "" {
			return v
		}
	}
	if ep, ok := v11KnownEndpoints[strings.ToLower(d.Type)]; ok {
		return ep
	}
	return ""
}

// v11KnownEndpoints v11 的 `type` → 端点（**冻结副本**：`internal/adapters/outbound/sqlite/store/migrate_v12.go`
// 里有一份同源的，那是迁移用的历史快照，这里是导入（逃生门）用的；两处都描述历史，不复用活代码）。
var v11KnownEndpoints = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"deepseek":   "https://api.deepseek.com/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"groq":       "https://api.groq.com/openai/v1",
}

// BodyString 从已解析的 JSON 对象里取一个字符串键（缺失/类型不符 → 空串）。
//
// v13 之后它**只服务文件载体与导入路径**（`providers.json` 的条目仍是自由 JSON）；
// DB 行是列，不经过这里。
func BodyString(body map[string]any, key string) string {
	v, ok := body[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// ValidateProviderID id 词法校验（它进托管文档的键 `providers/<id>` → 必须过引用校验，
// 防的是一个带 `/` 或 `..` 的 id 把别的条目覆盖掉）。
func ValidateProviderID(id string) error {
	if id == "" {
		return code.NewError(code.ClassRequest, "invalid_provider_id", "provider id 必填", nil)
	}
	if strings.TrimSpace(id) != id || len(id) > 64 {
		return code.NewError(code.ClassRequest, "invalid_provider_id", fmt.Sprintf("provider id %q 非法（仅 a-zA-Z0-9._-，1-64 字符，且不含首尾空白）", id), nil)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return code.NewError(code.ClassRequest, "invalid_provider_id", fmt.Sprintf("provider id %q 非法（仅 a-zA-Z0-9._-，1-64 字符）", id), nil)
		}
	}
	if id == "." || id == ".." {
		return code.NewError(code.ClassRequest, "invalid_provider_id", fmt.Sprintf("provider id %q 非法（不得为 . 或 ..）", id), nil)
	}
	return nil
}
