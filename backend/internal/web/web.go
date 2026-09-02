//go:build release

package web

import "embed"

// Dist 内嵌的前端静态资源。dist/ 由仓库根 Makefile 的 `make web` 预置
// （从 frontend/apps/panel 构建产物拷贝而来），详见 doc.go。
//
//go:embed dist
var Dist embed.FS
