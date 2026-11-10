package apppkg

import (
	"fmt"
	"strconv"
	"strings"
)

// ——— 版本门槛的语义化版本比较（WP-C8）———
//
// 分发/版本治理要比「包要求的最低核心版本」与「当前核心版本」的先后。本仓没有引第三方 semver 库
// （go.mod 里没有），这一小段比较就是够用的最小实现——只做**排序**（major.minor.patch），不做范围表达式。
//
// 口径（刻意从简，写在注释里免得下一个读的人以为是 bug）：
//   - 接受可选前缀 `v`（`v1.2.3` / `1.2.3` 都行）；
//   - 主三段 major.minor.patch；缺省的段按 0 补齐（`1` == `1.0.0`，`1.2` == `1.2.0`）；
//   - **预发布/构建后缀（`-rc.1` / `+build`）在排序里被忽略**：本期只有单机单操作员、包由操作者自己
//     放入，预发布语义没有分发场景；忽略后缀 = 「`1.2.3-rc.1` 视为 `1.2.3`」，若要严格 semver 排序
//     再收紧（留 C8 之后）。这里判断只影响「是否满足最低版本」，不影响包能否被解析（那是 manifest 的事）。
//   - 无法解析（如核心 `dev` 构建、纯非数字串）→ 返回 error，由调用方**fail-closed**处置（不能把
//     "版本说不清"当成"满足要求"——那正是「装错版本却静默运行」的形态）。

// semanticVersion 是一个解析后的三段版本。
type semanticVersion struct {
	major, minor, patch int
}

// parseSemanticVersion 解析 `v?X(.Y(.Z)?)?`，返回三段版本。无法解析 → error（fail-closed）。
func parseSemanticVersion(s string) (semanticVersion, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return semanticVersion{}, fmt.Errorf("空版本串")
	}
	raw = strings.TrimPrefix(raw, "v")
	// 去掉预发布/构建后缀（只取 '-' 或 '+' 之前的部分做排序；后缀语义见文件头）。
	if i := strings.IndexAny(raw, "-+"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" {
		return semanticVersion{}, fmt.Errorf("版本串只有后缀、无数字段: %q", s)
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 3 {
		return semanticVersion{}, fmt.Errorf("版本段超过三段: %q", s)
	}
	var nums [3]int
	for i, p := range parts {
		if p == "" {
			return semanticVersion{}, fmt.Errorf("版本段为空: %q", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semanticVersion{}, fmt.Errorf("版本段 %q 不是非负整数: %q", p, s)
		}
		nums[i] = n
	}
	return semanticVersion{major: nums[0], minor: nums[1], patch: nums[2]}, nil
}

// compareSemanticVersion 返回 a 相对 b 的顺序：a<b → -1，a==b → 0，a>b → +1。
func compareSemanticVersion(a, b semanticVersion) int {
	if a.major != b.major {
		return cmpInt(a.major, b.major)
	}
	if a.minor != b.minor {
		return cmpInt(a.minor, b.minor)
	}
	return cmpInt(a.patch, b.patch)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
