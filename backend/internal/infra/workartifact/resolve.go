// Package workartifact 从平台的工具事件里**旁路**识别「write/edit 工具写出的工作区文件」，
// 并把「相对路径」安全解析为「会话 cwd 内的绝对路径」——供上层做产物快照登记。
//
// **零侵入铁律**（方案 .dsh/docs/plans/2026-11-04-artifact-auto-extraction-plan.md §2.5）：
// 本包只做**纯函数**：读 dsh **已经上报**的 `protocol.ToolCallInfo`、做字符串/路径判断，
// 不 import 任何底座 adapter、不 spawn 进程、不读写磁盘（路径在盘上的真实读写由装配层的抽取器做）。
// 因此「dsh 改了 wire 形状」→ 这里认不出 → 上层不抽（WARN），绝不影响 dsh。
//
// 识别判据来自 R1 实探（.dsh/docs/notes/2026-11-04-r1-tool-rawinput-probe-notes.md）：
//   - dsh 的 write/edit 工具 `title` 分别是 "write" / "edit"（`kind` 恒为 "other"，不可用）；
//   - `rawInput.file_path` 是**相对 cwd** 的路径（write 还带 content，edit 带 old/new_string）。
package workartifact

import (
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/artifact"
	"github.com/morehao/ark-mate/internal/domain/workspace"
)

// 工具的 title 白名单（R1 实测值）。只认这两个；其余一律不抽（保守，不猜）。
const (
	titleWrite = "write"
	titleEdit  = "edit"
)

// rawInputPathKey 是 dsh write/edit 的 rawInput 里存放文件路径的键（R1 实测键名）。
const rawInputPathKey = "file_path"

// ResolvedToolWrite 是一次「工具写了哪个文件」的解析结果。
type ResolvedToolWrite struct {
	// RelPath 相对会话 cwd 的路径（dsh 原样给出，已 Clean，未做越界外的任何解释）。
	RelPath string
	// Origin 来源：write → tool_write，edit → tool_edit。
	Origin artifact.Origin
}

// ResolveToolWrite 从一次工具事件里识别「write/edit 落文件」。
//
// 返回 ok=false 表示**不是**一次可抽取的写文件（title 不在白名单 / rawInput 无 file_path /
// 形状不符）。这是**纯判定**，不做路径越界检查（那由 SafeRelPath 用 cwd 判定）。
//
// 只认 `title ∈ {write, edit}`：R1 实测 `kind` 恒为 "other"，不能作判据；`title` 是当前
// dsh 的 wire 事实，若将来变了 → 这里认不出 → 不抽（零侵入口径 C3，不让 dsh 为平台改）。
func ResolveToolWrite(info protocol.ToolCallInfo) (ResolvedToolWrite, bool) {
	var origin artifact.Origin
	switch info.Title {
	case titleWrite:
		origin = artifact.OriginToolWrite
	case titleEdit:
		origin = artifact.OriginToolEdit
	default:
		return ResolvedToolWrite{}, false
	}
	raw := rawInputPath(info.Input)
	if raw == "" {
		return ResolvedToolWrite{}, false
	}
	// 只接受相对路径：绝对路径的写入不是「会话工作区产物」的常规形态，且越界风险高——保守跳过。
	// （相对路径仍是唯一常态，R1 实测即 "../r1_probe.txt" 这种相对串。）
	if filepath.IsAbs(raw) {
		return ResolvedToolWrite{}, false
	}
	return ResolvedToolWrite{RelPath: filepath.Clean(raw), Origin: origin}, true
}

// rawInputPath 从 rawInput（JSON 解码后的 any，通常是 map[string]any）里取 file_path。
//
// 只认 map[string]any 这一种形状；认不出返回空串（不猜、不弱转）。
func rawInputPath(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m[rawInputPathKey]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// SafeRelPath 把「相对 cwd 的路径」解析成「cwd 内的绝对路径」，越界即拒。
//
// cwd 必须是绝对路径（会话 cwd，见 model.SessionEntity.Cwd）。判定复用 workspace.WithinRoots：
// 它做 EvalSymlinks 真身解析 + filepath.Rel，能挡住 `../` 与符号链接逃逸（macOS /var→/private/var
// 这类真身差异也已处理）。返回 (abs, rel, err)：
//   - abs: 绝对路径（cwd 拼接后 Clean，未经真身解析——读文件仍用 abs，与 dsh 视角一致）；
//   - rel: 相对 cwd 的 slanch 化路径（用于展示/聚簇）。
func SafeRelPath(cwd, relPath string) (abs string, rel string, err error) {
	if !filepath.IsAbs(cwd) {
		return "", "", &PathError{Reason: "cwd_not_absolute", Path: cwd}
	}
	cleanRel := filepath.Clean(relPath)
	if cleanRel == "." || filepath.IsAbs(cleanRel) {
		return "", "", &PathError{Reason: "path_not_relative_file", Path: relPath}
	}
	abs = filepath.Join(cwd, cleanRel)
	// 越界判定：path 必须落在 cwd 这一个根之内（WithinRoots 空根视为放行，故这里必须显式给 [cwd]）。
	if !workspace.WithinRoots(abs, []string{cwd}) {
		return "", "", &PathError{Reason: "path_escape", Path: relPath}
	}
	return abs, filepath.ToSlash(cleanRel), nil
}

// PathError 是解析类失败（越界 / cwd 非绝对 / 非相对文件路径）。
//
// 携带稳定 Reason 便于上层 WARN 分类（比如 `path_escape` 要能在日志里一眼区分于普通解析失败）。
type PathError struct {
	Reason string
	Path   string
}

func (e *PathError) Error() string {
	return "workartifact: " + e.Reason + ": " + e.Path
}
