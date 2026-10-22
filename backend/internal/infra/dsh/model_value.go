package dsh

import "encoding/json"

// ModelValue dsh 的「模型值投影」实现（见 internal/runtime/model_value.go 的端口契约）。
//
// dsh 在 ACP 的 `modelValue()` 上要的是 `["<provider>","<model>"]` 这个 JSON 数组——一段**它自己的**
// 词表形态。这条知识此前写在内核的 `switch runtimeID` 里，2026-10-12 随第四张表搬到 adapter。
type ModelValue struct{}

// Value 把平台清单里的一条编成 dsh 认得的 JSON 数组。
//
// 第一段是**派生后的路由键**（`routeKey`，v25 起带平台保留前缀）——它不是平台 id 本身：ACP 拿这个
// 值去**找路由**，而路由是在配置里以派生键声明的。两处必须逐字同源，否则 `set_config_option`
// 找不到 provider，**所有选了模型的会话立刻起不来**（动态判据
// `TestDshRouteKeyAgreesBetweenPatchAndModelValue` 就是这条的机器判据）。
//
// 用 `json.Marshal` 而不是手工拼串：provider/model 里出现引号或反斜杠时手工拼会产出**看起来合法、
// 实际换了内容**的 JSON（连"少了一条"都判不出来）。Marshal 的错误分支在本形态里构造不出来
// （都是 string），但端口契约要求把"编不出来"如实交出去，所以不吞。
func (ModelValue) Value(providerID, model string) (string, error) {
	b, err := json.Marshal([]string{routeKey(providerID), model})
	if err != nil {
		return "", err
	}
	return string(b), nil
}
