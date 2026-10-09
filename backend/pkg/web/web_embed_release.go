//go:build release

package web

import (
	"embed"
	"io/fs"
)

// embedded 是内嵌的前端产物（仅 `-tags release` 编译）。
//
// 注意：`//go:embed` 必须是**紧贴 var 的指令行**（`//go:` 与 `/` 之间不能有空格，也不能写成
// 文档注释里的一句引用）——首版把它写进说明文字里，编译器一声不响地嵌了个空目录，
// 表现是"构建成功但资产永远缺失"（release tag 的资产用例当场红）。
//
//go:embed dist
var embedded embed.FS

// Dist 返回内嵌的前端产物根（index.html 与 assets/ 所在的目录），第二个返回值为 false 表示
// **本构建没有内嵌资产**。
//
// 为什么要有第二个返回值而不是直接给 fs.FS：调用方（internal/router 的资产层）需要按
// 设计 §6.1 的优先级决策——显式目录优先、内嵌兜底、两者都没有则**拒绝启动**（退出码 3）。
// "没有"必须是一个可判定的信号，而不是 nil 检查（nil fs.FS 要到 ReadFile 时才炸）。
func Dist() (fs.FS, bool) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// dist 目录不存在（make web 未先拷贝产物即带 release 编译）：按"没有内嵌资产"处理，
		// 由调用方给出可读的启动错误，而不是在这里 panic 掉一个二进制。
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
