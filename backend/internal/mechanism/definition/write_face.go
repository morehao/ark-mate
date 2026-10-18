package definition

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/morehao/ark-mate/internal/code"
)

// ——— 定义写面的原文处理（skill / mcp / automation 三域共用**一份**实现） ———
//
// 这三件把"写面的原文 JSON 对象"变成落盘层要的 patch：请求体的键与类型在这里被解释**一次**。
// 各域的定义写面（`domain/skill.Plane.WriteSkill`、mcp 写面、automation 写面）都经它，**不许各写一份**
// ——两份原文处理会让同一个请求在不同域上有不同的键语义（最典型的是 `null`：一处当"删键"，
// 一处当"写一个 null 值"），而这两种解释装载后**看起来一样**，只在手读文件时才显形。
//
// 归属理由：它们描述的是**定义载体**（`capability.DefinitionFile` 的 patch 形状与参数类错误出口），
// 与任何业务规则无关，且被 ≥3 个域共用——放进某一个域会让另外两个域反向 import 它
// （`domain/*` 同层零 import 由守卫钉住；`skill` 与 `mcp` 更不许互相认识）。
//
// 与包注释里那句"不负责对外 DTO 与错误码"的关系：这里**不碰** `dtocapability`/`dtoautomation`
// 这类对外 DTO，`RawPatch`/`RawString` 只认 `map[string]json.RawMessage`；`InvalidParamErr`
// 与同文件的 `InvalidDefinitionIDErr` 同形（都是定义写面的**参数类**出路，40000），
// 是机制层已有的形态，不是新开的口子。

// RawPatch 把写面的原文 JSON 对象转成落盘层要的 patch。
//
// `null` 在这里被解释成**删掉这个键**（而不是写一个 null 值）：定义文件的键都是 omitempty 语义，
// 写 null 与删键在装载后等价，但磁盘上留一个 null 会让手读文件的人以为"这个键有值"。
func RawPatch(req map[string]json.RawMessage) map[string]any {
	out := make(map[string]any, len(req))
	for k, raw := range req {
		t := bytes.TrimSpace(raw)
		if len(t) == 0 || string(t) == "null" {
			out[k] = nil
			continue
		}
		// json.RawMessage 实现 MarshalJSON → DefinitionFile 原样写入这一段字节：
		// 嵌套对象/数组不会在"解出结构体再序列化"的过程中丢键或变形。
		out[k] = raw
	}
	return out
}

// RawString 取原文对象里某个键的字符串值（不存在/为 null → ok=false）。类型不符 → 40000。
func RawString(req map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := req[key]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return "", false, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", true, InvalidParamErr(fmt.Sprintf("字段 %q 须为字符串", key))
	}
	return v, true, nil
}

// InvalidParamErr 参数类写面错误（缺失/类型/取值不合法）→ 40000。
//
// 与"语义被拒"（40001，如正文扫描 critical）分开：前者改参数就好，后者要改内容。
func InvalidParamErr(msg string) error { return code.Error(code.InvalidParam, msg) }
