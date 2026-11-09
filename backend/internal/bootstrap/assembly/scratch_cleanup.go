package assembly

import (
	"context"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/core/scratch"
	"github.com/morehao/golib/glog"
)

// startScratchOrphanCleanup 启动时清理「孤儿」过程产物目录：`<cwd>/.scratch/<session_id>` 里，
// session_id 在会话表中**查无此会话**的那些（硬删/迁移遗留）。存活/归档会话的 scratch 一律保留——
// 归档清是一回事（发生在归档那一刻，见 svcsession.Meta），这里只兜底清理真正无主的目录。
//
// 非 critical、失败仅 WARN：清理过程产物是尽力而为的卫生动作，不能因为它耽误启动。
// 只对**已知会话 cwd** 下的 `.scratch` 动手，绝不扫描或删除交付目录里的用户文件。
func (as *assembly) startScratchOrphanCleanup(ctx context.Context) error {
	if as.st == nil {
		return nil // memory 模式无会话表 → 无从判定，跳过（不猜）
	}
	sessions, err := as.st.ListSessions(ctx, nil) // archived=nil：含归档，全部算「已知」
	if err != nil {
		glog.Warnw(ctx, "scratch 孤儿清理：读会话表失败，跳过", "err", err)
		return nil // 非 critical，绝不因清理失败拒绝启动
	}
	live := make(map[string]bool, len(sessions))
	cwds := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		if s.ID != "" {
			live[s.ID] = true
		}
		if cwd := s.Cwd; cwd != "" {
			cwds[cwd] = true
		}
	}
	for cwd := range cwds {
		as.cleanOrphanScratchUnder(ctx, cwd, live)
	}
	return nil
}

// cleanOrphanScratchUnder 扫描某个 cwd 下的 `.scratch/*`，删掉「名字不在 live 集合」的子目录。
func (as *assembly) cleanOrphanScratchUnder(ctx context.Context, cwd string, live map[string]bool) {
	root := scratch.RootFor(cwd)
	if root == "" {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return // 没有 .scratch 目录是常态（os.IsNotExist 也走这里），静默
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	for _, id := range scratch.OrphanCandidates(ids, func(sid string) bool { return live[sid] }) {
		target := filepath.Join(root, id)
		// 二次校验：目标必须是 root 的直接子目录（fail-closed，防异常名把删除指到别处）。
		if filepath.Dir(target) != root {
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			glog.Warnw(ctx, "scratch 孤儿清理：删除失败（跳过）", "path", target, "err", err)
			continue
		}
		glog.Infow(ctx, "scratch 孤儿清理：已删除无主过程产物目录", "path", target)
	}
}
