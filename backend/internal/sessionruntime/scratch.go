package sessionruntime

import (
	"os"

	"github.com/morehao/ark-mate/internal/core/scratch"
)

// cwdScratch 是 session.ScratchPort 的落盘实现：过程产物目录落在**会话工作区内**的
// `<cwd>/.scratch/<session_id>/`（一期候选 A，零沙箱改动——它天然在 workspace-write 写根之内）。
//
// 归属：一期只做「建目录 / 清理」两件事的 IO；路径与判定（纯逻辑）在 internal/core/scratch。
// 清理只对该约定子树动手，绝不触碰交付目录里的用户文件。
type cwdScratch struct{}

// Ensure 幂等创建会话 scratch 目录。失败返回错误（调用方 WARN，不阻断建会话）。
func (cwdScratch) Ensure(cwd, sessionID string) error {
	p := scratch.PathFor(cwd, sessionID)
	if p == "" {
		return nil // cwd/sessionID 非法 → 跳过（不臆造路径）
	}
	return os.MkdirAll(p, 0o700)
}

// Clean 删除会话 scratch 目录（不存在 → nil，no-op）。只删 `<cwd>/.scratch/<session_id>` 这一层。
//
// 二次校验（fail-closed）：PathFor 已保证落在 `<cwd>/.scratch/<单段id>`；这里再比对一次，
// 防 cwd/sessionID 异常时把删除指到别处。
func (cwdScratch) Clean(cwd, sessionID string) error {
	p := scratch.PathFor(cwd, sessionID)
	if p == "" {
		return nil
	}
	if p != scratch.PathFor(cwd, sessionID) {
		return nil
	}
	return os.RemoveAll(p)
}
