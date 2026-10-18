package archtest

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件是 **工具事件载荷键白名单** 的守卫（S4 / v27）。
//
// 这一批给工具载荷加了两个键（`fail_reason` / `biz_code`），它们**不是** ACP 字段，
// 而是平台从工具结果正文里读出来的归类。两种腐化都不会让任何既有测试变红：
//
//	① **后端多写一个前端不读的键**：事件照常推、界面照常渲染，只是那个键永远没人读
//	   （或者下一个人照着它在客户端又实现一份推断）；
//	② **前端读了后端不写的键**：那个字段永远是 `undefined`，界面表现是"这个功能时好时坏"
//	   （取决于有没有人从别处塞值进来）。
//
// 判据两侧一起钉、都 fail-closed。

// TestToolPayloadKeysAreClosed 载荷白名单 == 前端 `StreamEntry` 的读取面（逐条相等）。
func TestToolPayloadKeysAreClosed(t *testing.T) {
	root := backendRoot(t)

	// ① 白名单声明在会话域的 `toolPayload` 旁边（P4a 第五十五刀随事件出口搬入），且 `toolPayload` 只写这些键。
	sink := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "eventsink.go")))
	if !strings.Contains(sink, "var ToolPayloadKeys = []string{") {
		t.Fatalf("eventsink.go 里找不到 `var ToolPayloadKeys = []string{`：工具载荷的键必须有白名单")
	}
	block := tsUnionBlock(sink, "var ToolPayloadKeys = []string{")
	if block == "" {
		t.Fatalf("解析不到 `ToolPayloadKeys` 的取值块（fail-closed）")
	}
	declared := map[string]string{} // wire 键 → 常量名
	for _, m := range regexp.MustCompile(`\bToolPayload[A-Z][a-zA-Z]*`).FindAllString(block, -1) {
		if m == "ToolPayloadKeys" { // 白名单变量自身不是键
			continue
		}
		// 常量 → 取值
		re := regexp.MustCompile(`(?m)^\s*` + m + `\s*=\s*"([a-z_]+)"`)
		lit := re.FindStringSubmatch(sink)
		if len(lit) != 2 {
			t.Fatalf("白名单里的 `%s` 找不到对应字面量（fail-closed）", m)
		}
		declared[lit[1]] = m
	}
	if len(declared) < 8 {
		t.Fatalf("白名单只解析出 %d 个键（want ≥ 8）：解析失效，守卫形同虚设", len(declared))
	}

	// ② `toolPayload` 的函数体里**不得**出现白名单之外的字符串键（`p["xxx"] = ...`）。
	body, ok := goFuncBody(sink, "func toolPayload(")
	if !ok {
		t.Fatalf("取不到 `toolPayload` 的函数体（fail-closed）")
	}
	for _, m := range regexp.MustCompile(`p\["([a-z_]+)"\]`).FindAllStringSubmatch(body, -1) {
		if _, ok := declared[m[1]]; !ok {
			t.Errorf("`toolPayload` 写了白名单之外的键 %q：白名单是**契约的一半**，"+
				"多写一个键不会让任何测试变红，只会让前端多一个永远没人读的字段（或者让下一个人"+
				"照着它在客户端又推一遍）", m[1])
		}
	}

	// ③ 调用方补的那两个键也必须在白名单里（它们在 `KindToolUpdate` 分支里直接写 map）。
	for _, want := range []string{"fail_reason", "biz_code"} {
		if _, ok := declared[want]; !ok {
			t.Errorf("白名单里没有 %q：它是这一批新增的对外键（平台侧归类），漏登记等于契约少一半", want)
		}
	}

	// ④ 前端读取面与它**逐条相等**（Go 读 TS）。
	fePath := filepath.Join(root, "..", "frontend", "packages", "console", "src", "store.ts")
	fe := stripTSComments(readFileOrFatal(t, fePath))
	toolCase := tsSwitchCase(fe, "case Ev.ToolUpdate: {")
	if toolCase == "" {
		t.Fatalf("%s 里找不到 `case Ev.ToolUpdate:` 分支（fail-closed）", fePath)
	}
	read := map[string]bool{}
	for _, m := range regexp.MustCompile(`payload\?\.([a-z_]+)`).FindAllStringSubmatch(toolCase, -1) {
		read[m[1]] = true
	}
	if len(read) == 0 {
		t.Fatalf("`case Ev.ToolUpdate:` 分支里一个 `payload?.<键>` 都没解析到（判据失效）")
	}
	// 只钉**一个方向**：前端读了后端不写的键 = 那个字段恒为 `undefined`。
	//
	// 反向（后端有、前端暂时不读）是**允许**的：新增一个键常常先给历史/导出/排障用，界面晚一轮
	// 才消费（`kind`/`input`/`output` 就是先落事件、后接会话流的工具行与详情）。把那一半也钉上，
	// 判据唯一的产物就是逼着下一个人给它们加白名单——而白名单一加，这条守卫对它真正要拦的
	// 方向也就松了。
	extra := make([]string, 0, 4)
	for k := range read {
		if _, ok := declared[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Fatalf("前端 `case Ev.ToolUpdate:` 读了后端白名单之外的键 %v：那些字段恒为 undefined，"+
			"界面表现是「这个功能时好时坏」（取决于有没有人从别处塞值进来）", extra)
	}
}

// tsSwitchCase 取 TS 里一个 `case` 的分支体（到下一个**同缩进** `case ` 或 `default:` 为止）。
//
// 为什么不用 Go 侧那个 `switchCaseBlock`：它按制表符缩进切分（Go 的 case 缩进一层），而 TS 的
// 缩进是空格——第一版直接用它，分支体一路吃到文件尾，于是把别的 case 里的键算成了"工具分支读了
// 白名单之外的键"（17 个假红）。**判据的分隔符必须与它读的那种代码的缩进习惯一致。**
func tsSwitchCase(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	rest := src[i+len(decl):]
	// 行首同缩进的 `case` / `default` 是分支边界；`}` 收尾也算。
	for _, sep := range []string{"\n    case ", "\n    default:", "\n  }"} {
		if j := strings.Index(rest, sep); j >= 0 && j < len(rest) {
			rest = rest[:j]
		}
	}
	return rest
}
