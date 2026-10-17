package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是 **v26.1「本地记住的模型必须是平台行 id」** 的守卫（2026-10-15）。
//
// 挡的失败模式（当天实测，用户原话是"我已经配置了一个模型，为什么在下拉列表中没有呢"）：
// 本地持久键 `mate.web.lastModel` 里留着 v26 之前的**运行时 value**（dsh 形如
// `["yygu","deepseek-flash"]`）。它在界面上与合法值**长得一模一样**，发出去则被平台以 40401 拒；
// 而当时那条"清掉旧值"的自愈被**目录为空**挡在门外（`if (!model || modelOptions.length === 0) return`），
// 于是：旧值永远留着 → 每次发送都被拒 → 下拉里一个候选都没有、用户无从替换。
//
// 判据四段（都 fail-closed：解析不到即失败）：
//
//	① 形状判据只有一份，且与后端守卫**逐字同形**（`mdl_` + 26 位 Crockford）——两份各写各的，
//	   漂了之后"前端放行、后端拒"会变成一个只有用户能看见的 bug；
//	② 本地值**读入时**就判形状（`useState` 初始化器），不合法值进不了状态；
//	③ 自愈 effect 里的形状判据**不许排在目录闸门后面**（那正是这次死锁的形状）；
//	④ 出口（提交路径）再判一次形状——形状不对的值一个字都不外发。
func TestLocalModelMemoryIsShapeGated(t *testing.T) {
	typesPath := filepath.Join(frontendRoot(t), "packages", "types", "src", "model.ts")
	types := readStripped(t, typesPath)

	// ① 形状判据：前端那份必须与后端守卫里那一份**逐字相同**。
	lit := regexp.MustCompile(`PLATFORM_MODEL_ID_PATTERN\s*=\s*/([^/\n]+)/`)
	m := lit.FindStringSubmatch(types)
	if m == nil {
		t.Fatalf("%s 里找不到 `PLATFORM_MODEL_ID_PATTERN = /…/` 字面量——"+
			"改名/搬迁会让这条守卫静默空转（fail-closed）", typesPath)
	}
	if want := sessionModelIDForm.String(); m[1] != want {
		t.Fatalf("两侧的形状判据不同源：前端 %q，后端 %q——"+
			"前端放行而后端拒的形状不对值，用户只能从一次 40401 里知道", m[1], want)
	}
	if !strings.Contains(types, "isPlatformModelID") {
		t.Fatalf("%s 里找不到 `isPlatformModelID`（形状判据必须以它对外）", typesPath)
	}

	panelPath := filepath.Join(frontendRoot(t), "packages", "console", "src", "components", "StartPanel.tsx")
	panel := readStripped(t, panelPath)

	// ② 读入时就判形状：初始化器只认那个"形状合法才返回"的读取函数。
	if !strings.Contains(panel, "useState<string | null>(readStoredModel)") {
		t.Fatalf("%s 的 `model` 初值不再经 `readStoredModel`（形状合法的本地值才进状态）——"+
			"形状不对的值一旦进状态，界面上它与合法值长得一模一样", panelPath)
	}
	readBody := arrowBody(t, panel, "const readStoredModel = (): string | null => {")
	if !strings.Contains(readBody, "isPlatformModelID(") {
		t.Fatal("`readStoredModel` 里没有形状判据：本地值会原样进状态（旧形态的运行时 value 照样能用）")
	}
	if !strings.Contains(readBody, "localStorage.removeItem(LAST_MODEL_KEY)") {
		t.Fatal("`readStoredModel` 判出形状不对时必须**就地清掉**那个键——" +
			"只把它当作 null 返回，下次打开还会再读一遍同一个坏值")
	}

	// ③ 自愈 effect：形状判据不许排在目录闸门后面（目录为空是"还不知道"，不是"可以继续用坏值"）。
	effect := useEffectBodyWith(t, panel, "LAST_MODEL_KEY")
	shapeAt := strings.Index(effect, "isPlatformModelID(")
	gateAt := strings.Index(effect, "modelOptions.length")
	if shapeAt < 0 {
		t.Fatal("自愈 effect 里没有形状判据：目录为空时（`modelOptions.length === 0`）旧值会永远留着，" +
			"而每次发送都被 40401 拒——2026-10-15 事故的形状")
	}
	if gateAt >= 0 && gateAt < shapeAt {
		t.Fatal("自愈 effect 里形状判据排在 `modelOptions.length` 之后：目录为空时那句 early return " +
			"会先把它挡掉（死锁），旧值从此清不掉")
	}
	if !strings.Contains(effect, "localStorage.removeItem(LAST_MODEL_KEY)") {
		t.Fatal("自愈 effect 判出形状不对时必须清掉那个键（否则状态清了、盘上还在）")
	}

	// ④ 出口再判一次：形状不对的值一个字都不外发。
	sendBody := arrowBody(t, panel, "const send = async () => {")
	if !strings.Contains(sendBody, "isPlatformModelID(model)") {
		t.Fatal("`send` 里没有形状闸门：状态里万一进了形状不对的值，" +
			"这一次提交就会把它发出去换回一个 40401（平台只接受自己声明过的模型）")
	}
}

// arrowBody 返回 `const <名字> = (…) => {` 这类箭头函数的函数体（大括号配对）。
//
// 找不到那个签名即 fatal：改名会让判据**静默失效**（比红更糟）。模板串里的 `${…}` 是配对的，
// 不干扰计数；抽出来的体太短也判失败——那说明配对逻辑没作用在预期的那段代码上。
func arrowBody(t *testing.T, src, head string) string {
	t.Helper()
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("找不到 %q——改名/搬迁会让这条守卫静默空转", head)
	}
	open := strings.Index(src[i:], "{")
	if open < 0 {
		t.Fatalf("%q 的签名里没有 `{`（解析不到函数体）", head)
	}
	start := i + open
	depth := 0
	for j := start; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				body := src[start : j+1]
				// 下限只用来抓"配对逻辑根本没作用在预期那段代码上"（例如 head 落进了一个字符串）：
				// 这个文件里有正当的小 effect（`{ void loadModelCatalog() }` 就是 33 字节），
				// 门槛抬高会把好代码判红、把守卫逼成噪声。
				if len(body) < 8 {
					t.Fatalf("%q 的函数体只解析出 %d 字节——大括号配对看起来失效了", head, len(body))
				}
				return body
			}
		}
	}
	t.Fatalf("%q 的函数体没有闭合的大括号", head)
	return ""
}

// useEffectBodyWith 返回**提到 `marker`（如持久键名）的那个** `useEffect(() => {` 的函数体。
//
// 按内容定位而不是按出现次序：这个文件里有多个 effect，按序号取会在插进一个 effect 之后
// **静默换对象**（守卫照旧绿，判的却是别人）。
func useEffectBodyWith(t *testing.T, src, marker string) string {
	t.Helper()
	head := "useEffect(() => {"
	scanned := 0
	rest := src
	for {
		i := strings.Index(rest, head)
		if i < 0 {
			break
		}
		scanned++
		body := arrowBody(t, rest[i:], head)
		if strings.Contains(body, marker) {
			return body
		}
		rest = rest[i+len(head):]
	}
	t.Fatalf("找不到提到 %q 的 `useEffect(() => {`（共扫过 %d 个 effect）——"+
		"守卫的对象不见了，不能静默变绿", marker, scanned)
	return ""
}
