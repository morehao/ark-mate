// Package definition 是**能力定义的值层规则**：id 的合法性、写面原文（patch）的键语义、
// 定义写面的参数类错误出口、以及"定义被外部改动"的统一错误。
//
// 为什么单独成包（六边形对齐，v3.2 L1）：
//
//	这些规则被 ≥3 个领域（skill / mcp / automation）与用例层共用，但它们**只讲定义载体本身**
//	——不含任何存储、协议或装配知识。留在 `internal/drivers/mechanism`（L6 组合驱动）里，
//	领域层就得反向 import 机制层，`domain` 的出边被钉死的那条纪律就破了。
//
// 与 `internal/drivers/mechanism` 的分工：本包只放**纯规则**（无 I/O、无 store、无 acp 客户端），
// 机制层负责读写载体（repo / import / export），并**调用**本包而不是各写一份——两份原文处理会让
// 同一个请求在不同域上有不同的键语义（最典型的是 `null`：一处当"删键"，一处当"写一个 null 值"），
// 而这两种解释装载后看起来一样，只在手读文件时才显形。
package definition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/code"
)

// 三类能力定义各自的子目录名（`<CapabilityDir>/<subdir>/<id>.json`）。
const (
	CapSubSkills = "skills"
	CapSubMcps   = "mcp"
)

// ErrCapabilityUnavailable 能力数据面未装配（服务端形态问题，不是用户的参数问题）。
func ErrCapabilityUnavailable() error {
	return failure.NewError(failure.ClassService, "capability_unavailable", "能力数据面未装配", nil)
}

// ValidDefinitionID 定义 id 可否作为文件名。
//
// **这是一条安全边界，不只是格式校验**：id 来自 URL 路径与 body，直接拼进路径就成了目录穿越
// （`PUT /skills/..%2F..%2Fetc%2Fpasswd`）。上游 `RegisterSkill`/`RegisterMcpServer` 已经按
// 同一套规则（`nameRE`）拦过一道，但 `WriteDefinition` 也会被删除/迁移路径调用，所以在这里
// 再判一次——**最后一道防线放在真正做路径拼接的地方**。
func ValidDefinitionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return false
	}
	if filepath.Base(id) != id || id != filepath.Clean(id) {
		return false
	}
	return true
}

// InvalidDefinitionIDErr 非法 id 的错误（文案不回显原始字符串的长度细节，只说明规则）。
func InvalidDefinitionIDErr(Kind, id string) error {
	return failure.NewError(failure.ClassRequest, "invalid_param",
		fmt.Sprintf("%s id %q 不能作为文件名（须字母数字开头、≤64 位、不含路径分隔符）", Kind, id), nil)
}

// DefinitionPath 拼出 `dir/<id>.json`；id 非法即报错（见 ValidDefinitionID）。
func DefinitionPath(dir, Kind, id string) (string, error) {
	if !ValidDefinitionID(id) {
		return "", InvalidDefinitionIDErr(Kind, id)
	}
	return filepath.Join(dir, id+".json"), nil
}

// SortedPatchKeys 把 patch 的键排好序（写面日志与"改了哪几个键"的稳定输出都用它）。
func SortedPatchKeys(patch map[string]any) []string {
	out := make([]string, 0, len(patch))
	for k := range patch {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DefinitionChangedErr CAS 没中的统一出口（两个载体共用同一句话）。
//
// 语义与文案都必须与文件载体时代**逐字同义**：这不是"你的参数错了"（40001），而是
// "刷新后重试，你的改动没写入"（40901）——绝不 last-write-wins。
func DefinitionChangedErr(locator string) error {
	return failure.NewError(failure.ClassLimit, "definition_changed",
		fmt.Sprintf("能力定义 %s 在本次编辑期间被外部改动，请刷新后重试（你的改动未写入）", locator), nil)
}

// DefinitionWriteActor 变更审计里的"谁改的"。
//
// 当前没有鉴权、REST 写面是唯一调用者，所以按调用面记账；将来多了 CLI/导入等调用方，
// 各自传自己的标识（它们不走这个常量）。**刻意不从 ctx 里猜**：审计的价值恰恰在于它写的是
// 事实，猜出来的 actor 比"rest"更坏。
//
// 归在值层而不是机制层：它是**写面事实**的一部分，用例层（`svc*`）也要传它——
// 让用例层为了一个字符串去 import 机制层，就是把"谁在写"这件事也交给机制决定。
const DefinitionWriteActor = "rest"

// InvalidParamErr 参数类写面错误（缺失/类型/取值不合法）→ 40000。
//
// 与"语义被拒"（40001，如正文扫描 critical）分开：前者改参数就好，后者要改内容。
func InvalidParamErr(msg string) error { return code.Error(code.InvalidParam, msg) }

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
