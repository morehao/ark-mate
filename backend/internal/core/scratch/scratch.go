// Package scratch 定义「会话过程产物目录」的**约定与纯判定**（无 IO、无 internal 业务依赖）。
//
// 背景：office 等子 skill 要求把构建脚本/中间产物和成品一起留在任务工作区，于是过程产物会落在
// 交付目录里；同一工作区被后续会话复用时，模型会把这些陈旧脚本当现成捷径复用、绕过校验链
// （复盘 ses_01M6ZKC9...）。解法是**按产物性质分层**：交付面 = 会话 cwd（共享、持久、用户可见）；
// 过程面 = 会话私有的 `.scratch/<session_id>/`（临时、可弃、平台建平台清）。过程产物进 scratch，
// 就不再出现在交付目录里，下一会话物理上看不到——「捡到残留脚本」这条路径从根上不存在。
//
// 本包只做**纯判定与路径构造**（不 import 任何 internal 业务包、不碰磁盘），落盘 IO 由装配层的
// 适配器实现（经 domain/session.Scratch 端口注入）。这样 `domain`(L2) 可安全引用 `core`(L1)。
package scratch

import (
	"path/filepath"
	"strings"
)

// DirName 过程产物目录名（会话 cwd 下的隐藏子目录）。以 `.` 前缀使其在文件浏览/资料库默认被隐藏。
const DirName = ".scratch"

// PathFor 返回某会话在其工作区 cwd 下的 scratch 目录绝对路径：`<cwd>/.scratch/<sessionID>`。
//
// cwd 或 sessionID 为空时返回空串（调用方据此跳过，不做任何落盘）。sessionID 必须是单段
// （不含路径分隔符），否则返回空串——防止 `../` 之类的注入把删除指到别处。
func PathFor(cwd, sessionID string) string {
	cwd = strings.TrimSpace(cwd)
	sessionID = strings.TrimSpace(sessionID)
	if cwd == "" || sessionID == "" || !isSingleSegment(sessionID) {
		return ""
	}
	return filepath.Join(cwd, DirName, sessionID)
}

// RootFor 返回某会话工作区 cwd 下 scratch 的**根**（`<cwd>/.scratch`，不含会话段）。
// cwd 为空返回空串。
func RootFor(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	return filepath.Join(cwd, DirName)
}

// IsScratchRelPath 判断「相对会话 cwd 的展示路径」是否落在 `.scratch/` 子树内。
//
// rel 是 `SafeRelPath` 产出的相对路径（如 `.scratch/<sid>/x.py`）；首段为 `.scratch` 即视为过程产物。
// 抽取层据此拒绝把 scratch 里的文件登记进产物库（过程文件永不入库）。
//
// 兼容两种分隔符（`/` 与 `\`，Windows）：只判**首段**，不看深层。
func IsScratchRelPath(rel string) bool {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return false
	}
	// 统一分隔符后取首段。
	rel = strings.ReplaceAll(rel, "\\", "/")
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	return first == DirName
}

// OrphanCandidates 从一组「cwd 下 `.scratch` 根的子目录名」里筛出**孤儿**会话 id：
// 保留 `isLive(id)==true` 的（存活或存量有效会话，其 scratch 可能被恢复继续复用，绝不删）；
// 其余（如硬删/迁移遗留、查无此会话）视为孤儿，可清理。
//
// 纯函数：不做 IO，不碰磁盘；是否存在由调用方经 isLive 判断后传入。
func OrphanCandidates(sessionIDs []string, isLive func(id string) bool) []string {
	out := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		id = strings.TrimSpace(id)
		if id == "" || !isSingleSegment(id) {
			continue
		}
		if isLive != nil && isLive(id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// isSingleSegment 判断 s 是否为单个路径段（不含分隔符、不是 `.`/`..`、非空）。
func isSingleSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	return true
}
