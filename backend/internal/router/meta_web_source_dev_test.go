//go:build !release

package router_test

// wantWebSource 默认构建（不内嵌前端资产）下 `/meta` 的期望资产来源。
//
// 空串 = 该字段在 JSON 里省略（`omitempty`）：这时核心根本不服务前端资产，
// 报 builtin/dir 都是撒谎。release 构建见 meta_web_source_release_test.go。
const wantWebSource = ""
