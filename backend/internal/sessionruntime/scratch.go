package sessionruntime

import (
	"os"

	"github.com/morehao/ark-mate/internal/core/scratch"
)

// cwdScratch 是 session.ScratchPort 的落盘实现。
//
// 二期（候选 B）：过程产物根落在**平台托管区** `<MATE_WORKSPACE_DIR>/.scratch`（root），每会话一个
// `<root>/<session_id>/` 子目录——物理上完全在会话 cwd（交付面）之外，交付目录零残留。要允许
// `workspace-write` 沙箱往这里写，需把 root 并入 dsh sandbox-policy 的 additionalWritableRoots
// （见 dsh/profile.go 的 sandboxWritableRootsPatch + assembly/prepare_runtime.go）。
//
// 兜底（root 为空，如代建关闭或建根失败）：退回一期语义，落在 `<cwd>/.scratch/<session_id>/`（cwd 内）。
//
// 归属：只做「建目录 / 清理」两件事的 IO；路径与判定（纯逻辑）在 internal/core/scratch。
// 清理只对该约定子树动手，绝不触碰交付目录里的用户文件。
type cwdScratch struct {
	// root 平台托管的过程产物根（`<workspaceDir>/.scratch`）。空 = 退回 cwd 内 `.scratch`。
	root string
}

// sessionDir 解析某会话的 scratch 目录：root 非空 → `<root>/<sid>`；否则 `<cwd>/.scratch/<sid>`。
// 返回空串表示参数非法（不臆造路径）。
func (c cwdScratch) sessionDir(cwd, sessionID string) string {
	if c.root != "" {
		return scratch.PathUnder(c.root, sessionID)
	}
	return scratch.PathFor(cwd, sessionID)
}

// Ensure 幂等创建会话 scratch 目录。失败返回错误（调用方 WARN，不阻断建会话）。
func (c cwdScratch) Ensure(cwd, sessionID string) error {
	p := c.sessionDir(cwd, sessionID)
	if p == "" {
		return nil
	}
	return os.MkdirAll(p, 0o700)
}

// Clean 删除会话 scratch 目录（不存在 → nil，no-op）。只删该会话的那一层子目录。
func (c cwdScratch) Clean(cwd, sessionID string) error {
	p := c.sessionDir(cwd, sessionID)
	if p == "" {
		return nil
	}
	return os.RemoveAll(p)
}
