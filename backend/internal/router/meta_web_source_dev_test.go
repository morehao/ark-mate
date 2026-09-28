//go:build !release

package router_test

// wantWebSource 默认构建下 `/meta` 的期望资产来源。
//
// **M2-1 起这条期望的理由变了**：不再"因为构建 tag 而永远没有资产"，而是因为测试装配
// （`newEngine()`）没有给 `--web-dir`、且默认构建没有内嵌资产 → 解析不到资产 → 该字段省略。
// 一旦给了目录，默认构建同样会报 `dir`（见 web_test.go 的目录用例）。
const wantWebSource = ""
