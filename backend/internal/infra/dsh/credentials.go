// 本文件是 dsh 的**原生凭据面**：`<$DSH_HOME>/.credentials.yaml` 的读-改-写（M3.5）。
//
// 事实源是随包载荷里的官方文档 `@deepseek-ai/dsh-credentials-local/README.zh.md`（0.2.1-rc.2），
// 平台只依赖它写明的形态，不猜：
//
//	version: 1
//	refs:                       # 按**环境变量名**存放密钥值
//	  MATE_X_AB12CD34_API_KEY: sk-…
//	records:                    # 各插件自己的凭据（<owner>/<id>）
//	  llm-pi-ai/openai-codex: {kind: grant, payload: …}
//
// 三条官方口径决定了本文件的实现形态：
//
//   - **可以直接编辑文件**，`watch` 默认 true（改盘即热重载）⇒ 平台的写入能立刻生效（B9）；
//   - 0.2.1 起查找优先级是「存储文档 > 继承环境 > 两个 `.env`」（0.2.0 是 env 赢）⇒ 同一 runtime
//     **绝不**同时注入同名 env：两条通道同时开，总有一条是静默死值，且没有任何报错（R11/纪律③）；
//   - 权限必须只对属主可读：官方在**启动与重载**都拒绝"可被属主之外读取"的文档，
//     错误文案指向 `chmod 600` ⇒ 权限不合不是"更松"而是**拒启**，所以这条是 fail-loud（纪律①）。
//
// 于是平台侧的三条硬要求直接落在这里：
//
//	① **读-改-写，绝不整份覆盖**：文档是**共享面**（还有 `records:` 与用户自己的 refs），
//	   整份覆盖会把用户自己的密钥抹掉（契约③ 的粒度例外）；
//	② **只动平台自己写过的条目**：靠引用名模板的**字面前缀**（`MATE_`）识别平台命名空间，
//	   其余条目、注释、空行逐字保留；
//	③ **临时文件 + 原子 rename + 落盘后读回校验**（权限与内容都要读回）。
//
// 密钥面纪律（契约⑤）：**密钥值只许出现在这份文档里**——绝不进日志、错误文案、事件、返回值，
// 也绝不进"清单"那一路的字节（内联内容会进 `ps eww`）。本文件里的错误文案只带**键名与权限位**，
// 不带任何值（与 dsh 自己的凭据诊断口径一致：「键名可以安全打印，值不行」）。
package dsh

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/resource"
)

// syncCredentials 把 `Set.Providers` 的密钥写进原生凭据文档（读-改-写），并回带凭据面的结论。
//
// 落点优先取 `Set.CredentialDocPath`（**编排层的决定**：它按 `CredentialSpec.Tested` 做二选一，
// 选了原生文档才会给出路径与明文密钥），退回 `Options.CredentialDocPath`（声明值）是为了空集撤回
// ——空集时 `Set` 里没有供应商，编排层可能不再给出路径，而"平台删光供应商"这件事恰恰必须把
// 平台自己写过的 refs 条目清掉。
//
// 返回的 `CredentialOutcome` 与清单结论**分开报**（ADR-14）：凭据可热生效（next_run），
// 清单要新进程（next_process）——合并成一个 effect 必然说谎。
func (s *syncer) syncCredentials(set resource.Set) (resource.CredentialOutcome, error) {
	path := s.credDocPath
	if p := strings.TrimSpace(set.CredentialDocPath); p != "" {
		path = p
	}
	if path == "" {
		// 没有落点 = 本 adapter 不落盘凭据。**不冒充** env 通道：平台按 `FallbackEnv` 注入 env
		// 是编排层的事，adapter 只如实报"我没写任何凭据面"。
		return resource.CredentialOutcome{
			Channel: resource.ChannelNone,
			Status:  resource.StatusUntested,
			Where:   "无（未提供原生凭据文档落点）",
			Effect:  resource.EffectNextProcess,
			Tested:  false,
		}, nil
	}

	desired := map[string]string{}
	for _, p := range set.Providers {
		if p.Ref == "" || p.APIKey == "" {
			continue // 只有引用名没有值是 env 通道的形态：此时**不该**往文档里写空值（空值会被拒）
		}
		desired[p.Ref] = p.APIKey
	}
	changed, err := writeCredentialRefs(path, desired, managedRefMatcher(s.refPattern))
	if err != nil {
		return resource.CredentialOutcome{}, err
	}
	status := resource.StatusApplied
	if !changed {
		status = resource.StatusUnchanged
	}
	return resource.CredentialOutcome{
		Channel: resource.ChannelNativeDoc,
		Status:  status,
		Where:   path + "（底座私有凭据文档，权限 0600；dsh 的 watch 默认热重载）",
		Effect:  resource.EffectNextRun,
		// dsh 的原生凭据文档通道在 **0.2.1-rc.2** 上实测 7/7 PASS（H6 探针，2026-10-18）。
		// 版本绑定：载荷升级时由 `Spec.Credential.Tested` 先置回 false，编排层据此**不再走 native_doc**
		// （不给路径、不给明文），这条路径自然就不会被走到。
		Tested: true,
	}, nil
}

// writeCredentialRefs 读-改-写凭据文档，返回是否真的改动了字节。
//
// 四种形态：
//   - 文档不存在 + 没有要写的条目 → 什么都不做（**绝不**造一份空文档：无版本形态会被 dsh 按名拒绝）；
//   - 文档不存在 + 有条目 → 新建（`version: 1` + `refs:`）；
//   - 文档存在 + 内容无需变化 → 不重写字节，但仍然**读回校验权限**（宽于 0600 会让 dsh 拒启）；
//   - 其余 → 临时文件 + 原子 rename，落盘后读回校验。
func writeCredentialRefs(path string, desired map[string]string, managed func(string) bool) (bool, error) {
	prev, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("读原生凭据文档失败（%s）：%w", path, err)
	}
	next := patchRefsSection(string(prev), desired, managed)
	switch {
	case !exists && next == "":
		return false, nil
	case exists && next == string(prev):
		return false, verifyCredentialDoc(path, string(prev))
	}
	if !exists {
		next = "version: 1\n\n" + next
	}
	// 先把父目录收紧到 0700（**在文件出现之前**）：目录宽着的话，文件刚 rename 过去就已经可被
	// 同机他人列举/读取，而 dsh 对"可被属主之外读取"的文档是**拒启**——所以这一步失败就不该落盘。
	if err := chmodDir(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("收紧原生凭据文档父目录权限失败（%s）：%w", filepath.Dir(path), err)
	}
	if err := writeFileAtomic(path, []byte(next), 0o600); err != nil {
		return false, fmt.Errorf("写原生凭据文档失败（%s）：%w", path, err)
	}
	if err := verifyCredentialDoc(path, next); err != nil {
		return false, err
	}
	return true, nil
}

// verifyCredentialDoc 落盘后读回校验：权限（POSIX）与内容都必须与预期一致。
//
// 为什么必须读回而不是相信 `write` 的返回值：dsh 对权限不对的文档是**拒启**，而权限可能被
// umask、父目录的默认 ACL、或别的工具改掉——只写不验等于把"拒启"留给用户去发现。
// Windows 没有可检查的 mode，按 dsh 自己的口径**跳过而不是伪造**（设计 §实测事实 的残余项）。
func verifyCredentialDoc(path, want string) error {
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("凭据文档落盘后读不回（%s）：%w", path, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			return fmt.Errorf("凭据文档权限为 %04o，必须是 0600——dsh 会**拒绝启动**（%s）", perm, path)
		}
		di, err := os.Stat(filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("凭据文档父目录读不回（%s）：%w", filepath.Dir(path), err)
		}
		if perm := di.Mode().Perm(); perm&0o077 != 0 {
			return fmt.Errorf("凭据文档父目录权限为 %04o，不得对属主之外开放（须 0700）：%s", perm, filepath.Dir(path))
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("凭据文档落盘后读不回（%s）：%w", path, err)
	}
	if string(got) != want {
		// **刻意不回显内容**：错误文案会进响应/日志，而这份文档里就是密钥。
		return fmt.Errorf("凭据文档落盘后读回内容与预期不一致（%s）：不展示内容以避免密钥外泄", path)
	}
	return nil
}

// chmodDir 目录不存在则创建，存在则收紧权限（幂等）。
func chmodDir(dir string, perm os.FileMode) error {
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	return os.Chmod(dir, perm)
}

// managedRefMatcher 由引用名模板派生出"哪些 refs 键是平台自己写的"这一判据。
//
// 平台命名空间 = 模板里 `%s` **之前**的字面前缀（默认 `MATE_%s_API_KEY` → `MATE_`）。
// 为什么用前缀而不是记住"上次写了哪些"：凭据文档是共享面，平台必须能在**空集**（这次没有任何
// 供应商）时仍然认出自己写过的条目——那正是契约③的空集撤回；而记住一份清单要额外的持久状态，
// 且会与"用户手工编辑过这份文档"这件事冲突。代价是平台命名空间被显式保留（ADR-16/R13：
// 引用名走 `MATE_*` 就是为了不与用户 shell 里的"知名"变量（如 `OPENAI_API_KEY`）撞名）。
//
// 前缀为空（模板形如 `%s_API_KEY`）时**一个都不匹配**：认不出来的东西宁可不删——删错就是抹掉
// 用户自己的密钥，不可恢复（§安全 纪律⑤）。
func managedRefMatcher(pattern string) func(string) bool {
	p := strings.TrimSpace(pattern)
	if p == "" {
		p = DefaultRefPattern
	}
	if i := strings.Index(p, "%s"); i >= 0 {
		head := p[:i]
		if head == "" {
			return func(string) bool { return false }
		}
		return func(k string) bool { return strings.HasPrefix(k, head) }
	}
	return func(k string) bool { return k == p }
}

// patchRefsSection 只重写 `refs:` 分节：平台命名空间内的条目按 desired 增删改，
// 其余行（注释、`records:`、用户自己的 refs 条目、空行）**逐字保留**。
//
// 文本不变时返回的字符串与原文本**逐字节相同**——调用方据此判定"幂等重放、不重写字节"。
// 没有任何 `refs:` 分节时：desired 非空则追加一节，desired 为空则原样返回（绝不造空文档）。
func patchRefsSection(text string, desired map[string]string, managed func(string) bool) string {
	lines := strings.Split(text, "\n")
	idx := -1
	for i, ln := range lines {
		if isTopLevelYAMLKey(ln, "refs") {
			idx = i
			break
		}
	}
	if idx < 0 {
		if len(desired) == 0 {
			return text
		}
		return appendRefsSection(text, desired)
	}
	end := idx + 1
	for end < len(lines) {
		ln := lines[end]
		t := strings.TrimSpace(ln)
		switch {
		case t == "":
			// 空行只有"下一行仍属本节"时才算块内；否则它是分节之间的分隔，逐字保留。
			if end+1 < len(lines) && isIndentedLine(lines[end+1]) {
				end++
				continue
			}
		case strings.HasPrefix(t, "#"), isIndentedLine(ln):
			end++
			continue
		}
		break
	}

	var body []string
	emitted := map[string]bool{}
	dropped := false
	indent := "  "
	indentSeen := false
	for _, ch := range splitRefChunks(lines[idx+1 : end]) {
		if !indentSeen && ch.kind == refChunkEntry {
			indent = strings.Repeat(" ", ch.indent)
			indentSeen = true
		}
		if ch.kind == refChunkEntry && managed(ch.key) {
			// 平台自己的条目：在 desired 里就按新值重写**值**，它上方的注解逐字留下（纪律⑤要求
			// 保住注释——那是用户可能自己写的）；不在 desired 里就整条删除（连带它**直接上方**的
			// 注释——官方口径：直接位于某条目上方的注释属于该条目，随它一起删除）。
			if v, ok := desired[ch.key]; ok && !emitted[ch.key] {
				body = append(body, ch.comments...)
				body = append(body, strings.Repeat(" ", ch.indent)+ch.key+": "+yamlScalar(v))
				emitted[ch.key] = true
				continue
			}
			dropped = true
			continue
		}
		body = append(body, ch.lines...)
	}
	// 尚未出现的 desired 条目补在末尾（按名字排序：同输入同字节）。
	for _, k := range sortedRefKeys(desired, emitted) {
		body = append(body, indent+k+": "+yamlScalar(desired[k]))
	}

	out := append([]string{}, lines[:idx]...)
	if len(body) == 0 {
		if dropped {
			// 平台条目被撤光且没有别的条目：**不能**留一个裸 `refs:`（那是 null，属官方拒收的
			// "类型错误"）——显式写成空映射。
			out = append(out, "refs: {}")
		} else {
			out = append(out, lines[idx]) // 分节本来就是空的：原样保留，不改字节
		}
	} else {
		out = append(out, "refs:")
		out = append(out, body...)
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// appendRefsSection 在文档末尾追加一节（文档里原本没有 `refs:` 时）。
func appendRefsSection(text string, desired map[string]string) string {
	var b strings.Builder
	b.WriteString(text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	if text != "" {
		b.WriteString("\n")
	}
	b.WriteString("refs:\n")
	for _, k := range sortedRefKeys(desired, nil) {
		b.WriteString("  " + k + ": " + yamlScalar(desired[k]) + "\n")
	}
	return b.String()
}

// isTopLevelYAMLKey 判断某行是不是**顶层**的 `<key>:`（含 `refs: {}` 这种内联空值形态）。
func isTopLevelYAMLKey(ln, key string) bool {
	if isIndentedLine(ln) {
		return false
	}
	body := strings.TrimSpace(ln)
	if !strings.HasPrefix(body, key+":") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(body, key+":"))
	// 只认"没有值"或"内联空映射/空值"两种形态；`refs: # 注释` 也归入前者。
	return rest == "" || rest == "{}" || rest == "~" || rest == "null" || strings.HasPrefix(rest, "#")
}

// isIndentedLine 该行是否带前导空白（YAML 里"属于上一级"的形态）。
func isIndentedLine(ln string) bool {
	return strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t")
}

// refChunk 是 refs 分节里的一段连续行：一个条目（含它上方的注解、下方的续行）或其它原样行。
type refChunk struct {
	kind int // refChunkEntry | refChunkOther
	key  string
	// comments 条目**直接上方**的注释行（官方口径：它们属于该条目）。
	comments []string
	lines    []string
	indent   int
}

const (
	refChunkOther = iota
	refChunkEntry
)

// splitRefChunks 把 refs 分节的若干行切成"条目 / 其它"两类，供重写时逐段决定去留。
//
// 条目的边界靠缩进判定：键行（缩进 N）**吞掉**紧随其后、缩进更深的行（块标量或折行的多行值），
// 以及**紧邻其上方**的注释行（官方口径：直接位于某条目上方的注释属于该条目的注解）。
// 认不出"键: 值"形态的行（例如嵌套列表项）按其它处理、原样保留——宁可少动，不可乱动。
func splitRefChunks(lines []string) []refChunk {
	out := make([]refChunk, 0, len(lines))
	var pending []string // 紧邻上方的注释（可能属于下一条目）
	var flushPending func()
	flushPending = func() {
		if len(pending) > 0 {
			out = append(out, refChunk{kind: refChunkOther, lines: pending})
			pending = nil
		}
	}
	for i := 0; i < len(lines); {
		ln := lines[i]
		t := strings.TrimSpace(ln)
		switch {
		case t == "":
			flushPending()
			out = append(out, refChunk{kind: refChunkOther, lines: []string{ln}})
			i++
		case strings.HasPrefix(t, "#"):
			pending = append(pending, ln)
			i++
		default:
			indent := leadingSpaces(ln)
			if indent == 0 {
				flushPending()
				out = append(out, refChunk{kind: refChunkOther, lines: []string{ln}})
				i++
				continue
			}
			key, isEntry := refKeyOf(t)
			ch := refChunk{kind: refChunkOther, lines: []string{ln}, indent: indent}
			if isEntry {
				ch = refChunk{
					kind:     refChunkEntry,
					key:      key,
					indent:   indent,
					comments: append([]string{}, pending...),
				}
				ch.lines = append(append([]string{}, pending...), ln)
				pending = nil
			} else {
				flushPending()
			}
			i++
			// 续行：缩进更深且非空的行都属于本段（多行值）。
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" && leadingSpaces(lines[i]) > indent {
				ch.lines = append(ch.lines, lines[i])
				i++
			}
			out = append(out, ch)
		}
	}
	flushPending()
	return out
}

// refKeyOf 从一行里取出 YAML 键名；不是"键: 值"形态时 ok=false。
func refKeyOf(trimmed string) (string, bool) {
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") {
		return "", false
	}
	var key string
	if strings.HasPrefix(trimmed, `"`) || strings.HasPrefix(trimmed, `'`) {
		q := trimmed[0]
		end := strings.IndexByte(trimmed[1:], q)
		if end < 0 {
			return "", false
		}
		key = trimmed[1 : 1+end]
		rest := strings.TrimSpace(trimmed[2+end:])
		if !strings.HasPrefix(rest, ":") {
			return "", false
		}
		return key, true
	}
	i := strings.IndexByte(trimmed, ':')
	if i <= 0 {
		return "", false
	}
	key = strings.TrimSpace(trimmed[:i])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", false
	}
	return key, true
}

// leadingSpaces 前导空格数（制表符按 1 计：文档是平台自己写的两空格缩进，不做 tab 展开）。
func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// sortedRefKeys desired 里尚未写出的键（升序；同输入同字节）。
func sortedRefKeys(desired map[string]string, emitted map[string]bool) []string {
	out := make([]string, 0, len(desired))
	for k := range desired {
		if emitted[k] {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// yamlScalar 渲染一个 YAML 标量值：能用裸标量就用裸的（与 dsh 自己的发射器同形，H6 探针实测
// 该形态被正常解析），不能就退到双引号（JSON 转义规则与 YAML 双引号标量一致）。
//
// 为什么要判形态而不是"总是加引号"：密钥值形态不定（可能含 `: `、`#`、换行、首尾空格），
// 裸写这些字符会让 YAML **解析失败**——而 dsh 对解析失败的文档在启动期是**拒启**。
func yamlScalar(s string) string {
	if plainScalarSafe(s) {
		return s
	}
	return yamlQuote(s)
}

// plainScalarSafe 判断一个值能否安全地裸写成 YAML 标量。
//
// 判据取**保守**一侧：拿不准就引用（引用永远合法，裸写不合法就是拒启）。
func plainScalarSafe(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	if strings.ContainsAny(s, "\n\r\t") {
		return false
	}
	if strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`,") {
		// `:` / `#` 等指示符在裸标量里有语义（`a: b` 会被当映射、` #` 会被当注释），
		// 而密钥里出现它们完全可能——一律退到引号形态。
		return false
	}
	switch s[0] {
	case '-', '?', ' ', '.', '<', '=', '~':
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off":
		return false
	}
	return true
}
