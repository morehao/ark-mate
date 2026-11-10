package apppkg

import (
	"regexp"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// capabilityAppProbe 是走 `capability.ValidateApp` 时的最小探针 App：apppkg 只借用它的 id /
// api_version 词法判定，不引入 capability.App 的全部字段。字段非空即够。
type capabilityAppProbe struct {
	ID         string
	Name       string
	APIVersion string
}

// capabilityAppAPI 是核心暴露的应用 API 主版本（复用 `capability.AppAPI`，不另立常量）。
var capabilityAppAPI = capability.AppAPI

// validateViaCapability 把探针转成 capability.App 并调用它的唯一闸门，从而**复用**其 id / api_version
// 词法判定（不在本包重写）。返回的 error 可能是字母 id 错或 api_version 错，调用方各自包装。
func validateViaCapability(p *capabilityAppProbe) error {
	a := &capability.App{ID: p.ID, Name: p.Name, APIVersion: p.APIVersion}
	if err := capability.ValidateApp(a); err != nil {
		return err
	}
	// 回填规范化后的 api_version（capability 会在空时补 "^1"）。
	p.APIVersion = a.APIVersion
	return nil
}

// caretMajorRE 只提取 caret 的 major 段（用于**兼容性比较**）。caret **语法**完全交给
// `capability.ValidateApp`（见 validateManifestAPIVersion）；这里不重复判语法，只取已确认合法的串里的
// major 数字做「是否与核心主版本一致」的比较。
var caretMajorRE = regexp.MustCompile(`^\^(\d+)`)

// apiVersionCompatibleWithCore 判定一个（已确认 caret 合法的）版本范围是否与核心主版本兼容：
// caret 的 major 必须等于核心 API 的 major（如核心 1.x 时，`^1`/`^1.5` 兼容，`^2` 不兼容）。
//
// 与 `capability.ValidateApp` 的运行期口径同源：它只验语法，兼容性由调用方据同一 major 判定。
func apiVersionCompatibleWithCore(v string) bool {
	m := caretMajorRE.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	return m[1] == capabilityAppAPIMajor()
}

// capabilityAppAPIMajor 取核心 API 主版本段（如 "1.0" → "1"）。
func capabilityAppAPIMajor() string {
	m := caretMajorRE.FindStringSubmatch("^" + capabilityAppAPI)
	if m == nil {
		// AppAPI 形如 "1.0"，理论上必然匹配 ^<digits>；这里 fail-closed 返回空串 → 永不兼容。
		return ""
	}
	return m[1]
}
