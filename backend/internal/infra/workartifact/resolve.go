// Package workartifact 从平台的工具事件里**旁路**识别「dsh 工具声明/写出的工作区文件」，
// 并把「相对路径」安全解析为「会话 cwd 内的绝对路径」——供上层做产物快照登记。
//
// 识别两类来源：
//   - write/edit 工具写出的文件（一次一个，见 ResolveToolWrite）；
//   - present 工具声明的交付物（一次可多个，见 ResolveToolPresent）。office 等经 Python exec
//     产出的 .pptx/.docx/... 平台不认 write/edit，靠 present 显式声明补登记（P0 链收口）。
//
// **零侵入铁律**（方案 .dsh/docs/plans/2026-11-04-artifact-auto-extraction-plan.md §2.5）：
// 本包只做**纯函数**：读 dsh **已经上报**的 `protocol.ToolCallInfo`、做字符串/路径判断，
// 不 import 任何底座 adapter、不 spawn 进程、不读写磁盘（路径在盘上的真实读写由装配层的抽取器做）。
// 因此「dsh 改了 wire 形状」→ 这里认不出 → 上层不抽（WARN），绝不影响 dsh。
//
// 识别判据来自 R1 实探（.dsh/docs/notes/2026-11-04-r1-tool-rawinput-probe-notes.md）与 present 源码：
//   - dsh 的 write/edit 工具 `title` 分别是 "write" / "edit"（`kind` 恒为 "other"，不可用）；
//   - `rawInput.file_path` 带路径（write 还带 content，edit 带 old/new_string）；
//   - present 工具 `title` 是 "present"，`rawInput.files` 是 `[{ path, description? }]` 数组
//     （dsh ACP 桥把 title 取成工具名，present 未设自定义 title）。
//
// **路径形态的修正（2026-11-04 真机核对）**：R1 探针当时让模型「写 r1_probe.txt」，于是看到的是
// 相对串；真会话里模型几乎总给**绝对路径**（本次 10/10 次 write/edit 全是绝对路径，P2 上线后
// 一条产物都没登记成——这些绝对路径全被旧判据 `filepath.IsAbs → 跳过` 静默丢弃）。所以本包
// **两种形态都认**：相对路径按 cwd 拼接，绝对路径直接判定是否落在 cwd 内；落在外面才拒（WARN）。
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
	// titlePresent 是 dsh `present` 工具的 ACP 上报 title。
	//
	// **事实来源**（0.2.2-rc.1）：dsh 的 ACP 桥把工具生命周期 title 取成「工具名」——
	// `packages/acp/src/index.js` 的 `toolCallUpdate()` 写死 `title: event.data.name`；而 present 插件
	// （`@deepseek-ai/dsh-tool-present`）用 `defineTool({ name: "present", ... })` 注册，未设自定义 title。
	// 故上报的 `ToolCallInfo.Title` 恒为字符串 "present"。其 rawInput = `JSON.parse(arguments)`，即
	// present 的入参对象 `{ files: [{ path, description? }, ...] }`（path 为相对 cwd 或绝对路径）。
	titlePresent = "present"
)

// rawInputPathKey 是 dsh write/edit 的 rawInput 里存放文件路径的键（R1 实测键名）。
const rawInputPathKey = "file_path"

// rawInputFilesKey 是 dsh present 的 rawInput 里存放文件列表的键（见 present 插件 input schema）。
const rawInputFilesKey = "files"

// ResolvedToolWrite 是一次「工具写了哪个文件」的解析结果。
type ResolvedToolWrite struct {
	// Path dsh 原样给出的 file_path（相对 cwd 或绝对路径，已 Clean；越界判定在 SafeRelPath）。
	Path string
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
//
// 路径形态**不在这里筛选**：相对/绝对都原样交给 SafeRelPath——真机里绝对路径是常态，早先
// 「只收相对路径」的判据会让绝大多数产物静默消失（见包注释的 2026-11-04 修正）。
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
	return ResolvedToolWrite{Path: filepath.Clean(raw), Origin: origin}, true
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

// ResolvedToolPresent 是一次 `present` 工具事件的解析结果（可能声明多个文件）。
type ResolvedToolPresent struct {
	// Paths present rawInput.files 里逐个给出的文件路径（相对 cwd 或绝对路径，已 Clean 去空白；
	// 越界判定交给 SafeRelPath，与 write/edit 一致）。空元素（纯空白）已在解析时剔除。
	Paths []string
	// Origin 恒为 tool_present。
	Origin artifact.Origin
}

// ResolveToolPresent 从一次工具事件里识别「present 声明的交付物」。
//
// 返回 ok=false 表示**不是**一次可抽取的 present（title 不是 "present" / rawInput 无 files /
// 形状不符 / files 为空）。这是**纯判定**，不做路径越界检查（同 ResolveToolWrite，交 SafeRelPath）。
//
// present 与 write/edit 的最大区别是**一次可声明多文件**（present input schema 的 files 是数组）。
// files 的元素形状对多形态做**保守兼容**（尽量兼容 dsh 未来微调，但只解析能确定的字符串路径）：
//   - []string：`["a.pptx","b.docx"]`（数组元素直接是字符串）；
//   - []any，元素为 map，取 `path` 字段（present 官方 schema：`{ path, description? }`）；
//   - []any，元素为 map，取 `file_path` 字段（对 write/edit 同款键名的兜底兼容）；
//   - []any，元素为字符串。
//
// 任一元素认不出路径（非上述形态 / 空串）→ **该元素跳过、不猜**；全部认不出 → ok=false（不抽）。
// 这与「零侵入」一致：dsh 改了 wire 形状就自然不抽（上层 WARN/跳过），绝不影响 dsh。
func ResolveToolPresent(info protocol.ToolCallInfo) (ResolvedToolPresent, bool) {
	if info.Title != titlePresent {
		return ResolvedToolPresent{}, false
	}
	m, ok := info.Input.(map[string]any)
	if !ok {
		return ResolvedToolPresent{}, false
	}
	paths := presentFilePaths(m[rawInputFilesKey])
	if len(paths) == 0 {
		return ResolvedToolPresent{}, false
	}
	return ResolvedToolPresent{Paths: paths, Origin: artifact.OriginToolPresent}, true
}

// presentFilePaths 从 rawInput.files（any）解析出等长的文件路径切片（剔除空白项，Clean）。
//
// 多形态兼容见 ResolveToolPresent 注释。任何认不出的形状返回 nil（不猜）。
func presentFilePaths(input any) []string {
	switch v := input.(type) {
	case []string:
		return cleanPresentPaths(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			switch elem := item.(type) {
			case string:
				out = append(out, elem)
			case map[string]any:
				if s := presentElemPath(elem); s != "" {
					out = append(out, s)
				}
			}
		}
		return cleanPresentPaths(out)
	default:
		return nil
	}
}

// presentElemPath 从 present files 的单个对象元素里取路径：优先 `path`（官方 schema），
// 兜底 `file_path`（write/edit 同款键名，兼容可能的复用）；都不是字符串 → 空串。
func presentElemPath(elem map[string]any) string {
	for _, key := range []string{"path", rawInputPathKey} {
		if s, ok := elem[key].(string); ok {
			if t := strings.TrimSpace(s); t != "" {
				return t
			}
		}
	}
	return ""
}

// cleanPresentPaths 去空白 + Clean；空项剔除。返回 nil（当全部为空）时不制造「空的可抽取项」。
func cleanPresentPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, filepath.Clean(t))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SafeRelPath 把 write/edit 的 file_path 解析成「cwd 内的绝对路径」+「相对 cwd 的展示路径」，越界即拒。
//
// 两种入参形态都支持（真机两种都会出现，绝对路径更常见）：
//   - 相对路径 → 与 cwd 拼接；
//   - 绝对路径 → 原样使用，但必须落在 cwd 内（不在 → `path_outside_workspace`）。
//
// cwd 必须是绝对路径（会话 cwd，见 model.SessionEntity.Cwd）。判定复用 workspace.WithinRoots：
// 它做 EvalSymlinks 真身解析 + filepath.Rel，能挡住 `../` 与符号链接逃逸（macOS /var→/private/var
// 这类真身差异也已处理）。返回 (abs, rel, err)：
//   - abs: 绝对路径（Clean，未经真身解析——读文件仍用 abs，与 dsh 视角一致）；
//   - rel: 相对 cwd 的 slanch 化路径（用于展示/聚簇；工件表的 `path` 就是它）。
func SafeRelPath(cwd, filePath string) (abs string, rel string, err error) {
	if !filepath.IsAbs(cwd) {
		return "", "", &PathError{Reason: "cwd_not_absolute", Path: cwd}
	}
	cwd = filepath.Clean(cwd)
	clean := filepath.Clean(filePath)
	if clean == "." {
		return "", "", &PathError{Reason: "path_not_relative_file", Path: filePath}
	}
	if filepath.IsAbs(clean) {
		abs = clean
	} else {
		abs = filepath.Join(cwd, clean)
	}
	// 越界判定：path 必须落在 cwd 这一个根之内（WithinRoots 空根视为放行，故这里必须显式给 [cwd]）。
	if !workspace.WithinRoots(abs, []string{cwd}) {
		reason := "path_escape"
		if filepath.IsAbs(clean) {
			// 绝对路径不在工作区：不是「逃逸」（没有 ../ 语义），单独给一个原因便于日志区分。
			reason = "path_outside_workspace"
		}
		return "", "", &PathError{Reason: reason, Path: filePath}
	}
	relSlash, err := filepath.Rel(cwd, abs)
	if err != nil || relSlash == "." {
		return "", "", &PathError{Reason: "path_not_relative_file", Path: filePath}
	}
	return abs, filepath.ToSlash(relSlash), nil
}

// PathError 是解析类失败（越界 / 绝对路径在工作区外 / cwd 非绝对 / 不是文件路径）。
//
// 携带稳定 Reason 便于上层 WARN 分类（`path_escape` / `path_outside_workspace` 要能在日志里
// 一眼区分于普通解析失败——静默丢弃是 2026-11-04 那次「产物一条都没有」的排查成本来源）。
type PathError struct {
	Reason string
	Path   string
}

func (e *PathError) Error() string {
	return "workartifact: " + e.Reason + ": " + e.Path
}
