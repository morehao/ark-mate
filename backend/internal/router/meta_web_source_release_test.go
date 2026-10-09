//go:build release

package router_test

// wantWebSource release 构建下 `/meta` 的期望资产来源（`//go:embed dist` 命中 → builtin）。
//
// 给了 `--web-dir` 时它会变成 `dir`（显式目录优先且**不回落内嵌**，设计 §6.1）。
const wantWebSource = "builtin"
