package apppkg

import (
	"fmt"

	"github.com/morehao/ark-mate/internal/version"
)

// ——— min core version 门槛（WP-C8）———
//
// 清单 `requires.min_core_version` 是「这个包至少需要多新的核心」。装载/分发时拿它与**当前核心版本**
// 比：核心版本低于要求 → 该包 FAILED、不装载，原因**明示**（不是静默跳过）。这是「版本门」在分发侧
// 的落点（设计 §17.2/§17.3；A+Slot 的 `api_version` 兼容门是「API 契约代」维度的对偶，二者互补：
// api_version 管「接口形状是否相容」，min_core_version 管「核心能力是否够新」）。
//
// **当前核心版本的唯一来源是 `internal/version.Version`**（构建期 `-ldflags` 注入，开发构建为 "dev"）。
// 这里**不硬编码**版本号、也不读 `capability.AppAPI`——后者是「应用 API 契约版本」不是「核心二进制
// 版本」，两者回答不同问题（见 `internal/version/version.go` 的注释分工）。

// CurrentCoreVersion 返回当前核心二进制版本（唯一来源 `version.Version`，如 "1.4.0"，开发构建 "dev"）。
func CurrentCoreVersion() string { return version.Version }

// CheckMinCoreVersion 校验一个清单的 `requires.min_core_version` 是否被 coreVersion 满足。
//
// 返回值语义：
//   - 清单未声明 `requires` 或 `min_core_version` 为空 → 视为不设门槛，返回 nil（通过）。
//   - 门槛声明了 → 双方都必须能解析为语义化版本，且 coreVersion >= minCoreVersion 才通过。
//   - **无法解析核心版本**（如 "dev"）或**无法解析门槛版本** → 返回 error（fail-closed）：不能把
//     「版本说不清」当成「满足要求」——那会让「装在过旧/未知核心上的包静默跑起来」。
//   - coreVersion 低于门槛 → 返回带「低于最低要求」措辞的 error，供上层标 FAILED 并明示。
//
// 纯函数：调用方给 coreVersion（装配期传 `CurrentCoreVersion()`；测试直接注入确定版本）。
func CheckMinCoreVersion(m *Manifest, coreVersion string) error {
	if m == nil {
		return fmt.Errorf("apppkg: 清单为空，无法校验版本门槛")
	}
	if m.Requires == nil || !hasMinCoreVersion(m.Requires.MinCoreVersion) {
		return nil // 不设门槛
	}
	required := m.Requires.MinCoreVersion

	reqVer, err := parseSemanticVersion(required)
	if err != nil {
		return fmt.Errorf("apppkg %q: requires.min_core_version %q 无法解析（%v）——拒绝装载（门槛写错不能当成通过）", m.ID, required, err)
	}
	coreVer, err := parseSemanticVersion(coreVersion)
	if err != nil {
		return fmt.Errorf("apppkg %q: 当前核心版本 %q 无法解析为语义化版本（%v），无法判定是否满足 min_core_version=%q——拒绝装载（fail-closed）", m.ID, coreVersion, err, required)
	}

	if compareSemanticVersion(coreVer, reqVer) < 0 {
		return fmt.Errorf("apppkg %q: 核心版本 %q 低于包要求的最低版本 %q——拒绝装载（请升级核心或使用适配该核心的包版本）", m.ID, coreVersion, required)
	}
	return nil
}

// hasMinCoreVersion 报告门槛串是否非空（空 = 不设门槛）。
func hasMinCoreVersion(v string) bool {
	for _, r := range v {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}
