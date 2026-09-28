//go:build !release

package web

import "io/fs"

// Dist 在默认构建里恒为"无内嵌资产"：没有 `-tags release` 就没有嵌入。
//
// 这不是缺陷而是设计（设计 §6.1）：默认构建不依赖前端产物，因此**必须**由部署形态显式提供资产——
// 独立二进制用 `-tags release` 让资产进二进制，桌面形态由壳传 `--web-dir` 指向包内那一份。
// 两者都没有时核心拒绝启动（退出码 3），而不是起一个"能调 API 但打不开界面"的服务。
func Dist() (fs.FS, bool) { return nil, false }
