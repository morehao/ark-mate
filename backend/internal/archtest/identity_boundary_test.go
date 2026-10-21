package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pkgidentity "github.com/morehao/ark-mate/internal/platform/identity"
	productidentity "github.com/morehao/ark-mate/internal/domain/identity"
	"github.com/morehao/ark-mate/internal/infra/dsh"
)

// ——— 产品身份的五条静态守卫 ———
//
// 这一族守的都是**静默失效**：身份交付写错不会让任何既有测试变红，只会让"用户问你是谁"
// 这句最直白的问题得到另一个答案。
//
//	① 文案里混进执行引擎名 → 模型把运行时名当自己的名字（本方案要修的病本身）；
//	② 文案里混进模板标记 → 某底座的 persona 是模板，未知变量在**首轮组装**时抛错
//	   （启动日志一切正常，每次会话都失败）；
//	③ 实测版本与载荷 pin 分叉 → 升级了底座却继续把"未在新版本上验证过"的结论当事实；
//	④ 产品名与界面不一致 → 界面写 ark-mate、身份写别的；两处都不报错；
//	⑤ 启动期跑 `--dump-config-schema` → 用户已拍板不在启动路径上花这个预算。
//
// 另外一条前端守卫（`TestAboutPanelShowsProductWithoutEngineWords`）钉住「关于」面板
// **不出现执行引擎词**——它是 A4 的机器判据。

// identityRuntimeWords 身份文案里不得出现的执行引擎名（大小写不敏感）。
//
// 已下线底座的名字刻意保留：判据的语义是"身份文本不得引用任何底座"，不是"仓库里还有谁"。
var identityRuntimeWords = []string{
	"dsh", "deepseek harness", "opencode", "codebuddy", "claude",
}

// TestIdentityPromptHasNoRuntimeNames ①：身份文案（渲染结果）里不得出现执行引擎名。
//
// 判据落在**渲染结果**而不是源文件上：注释里出现底座名是正常的（说明为什么这么写），
// 真正要拦的是会被发给模型的那段字。
func TestIdentityPromptHasNoRuntimeNames(t *testing.T) {
	spec := productidentity.Render()
	lowered := strings.ToLower(spec.SystemPrompt + "\n" + spec.UserContext)
	for _, w := range identityRuntimeWords {
		if strings.Contains(lowered, w) {
			t.Fatalf("身份文案里出现了执行引擎名 %q：身份是产品人格，不是运行时的别名；"+
				"用户问「你是什么模型/运行时」时应当**区分**产品与执行层，而不是把执行层名当成自己的名字", w)
		}
	}
	if spec.Name != "ark-mate" {
		t.Fatalf("产品名应为 ark-mate，得到 %q", spec.Name)
	}
}

// TestIdentityPromptHasNoTemplateBraces ②：身份正文不得含模板标记。
//
// 这条判据的存在理由来自实测：某底座的 persona 是 Nunjucks 模板，未知 `{{x}}` **在首轮组装时**
// 才抛错——不是启动时。所以它必须能在启动期静态回答，否则表现是"每次会话都失败而启动正常"。
func TestIdentityPromptHasNoTemplateBraces(t *testing.T) {
	spec := productidentity.Render()
	for name, text := range map[string]string{"系统提示段": spec.SystemPrompt, "首轮块": spec.UserContext} {
		if text == "" {
			t.Fatalf("%s 为空：本判据没有对象（fail-closed）", name)
		}
		if pkgidentity.HasTemplateBraces(text) {
			t.Fatalf("%s 含模板花括号（`{{` / `}}`）：底座的 persona 是模板，未知变量会在首轮组装时抛错", name)
		}
		if strings.Contains(text, "{%") || strings.Contains(text, "%}") {
			t.Fatalf("%s 含模板标签（{%% / %%}）", name)
		}
	}
}

// TestDshIdentityPatchMatchesPayloadPin ③：身份交付面声明「已实测」时，版本必须等于载荷 pin。
//
// pin 的唯一事实源是 `frontend/apps/desktop/release.json` 的 `payloads[]`（与
// `TestDshTestedVersionMatchesPayloadPin` 读同一套事实，但那条读的是 package.json 的依赖 pin；
// 这里读的是**打包产物声明的版本**——两者分叉本身就是一种事故，所以故意分别钉）。
func TestDshIdentityPatchMatchesPayloadPin(t *testing.T) {
	path := filepath.Join(frontendRoot(t), "apps", "desktop", "release.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 release.json 失败（%s）：%v", path, err)
	}
	var doc struct {
		Payloads []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"payloads"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 release.json 失败（%s）：%v", path, err)
	}
	pin := ""
	for _, p := range doc.Payloads {
		if p.Name == dsh.ID {
			pin = strings.TrimSpace(p.Version)
			break
		}
	}
	if pin == "" {
		t.Fatalf("%s 的 payloads[] 里没有 %q：本判据没有对象（fail-closed）", path, dsh.ID)
	}

	if dsh.Identity.Tested {
		if dsh.Identity.TestedOnVersion != pin {
			t.Fatalf("身份交付面声明已实测（Tested=true），但 TestedOnVersion=%q 与载荷 pin=%q 不一致：\n"+
				"  升级载荷后必须重跑一次性 probe（临时 home + --dump-config-schema + 最小会话）并把这里改成新 pin；\n"+
				"  未重测时先把 Tested 置 false、TestedOnVersion 置空。",
				dsh.Identity.TestedOnVersion, pin)
		}
	} else if dsh.Identity.TestedOnVersion != "" {
		t.Fatalf("身份交付面 Tested=false 时 TestedOnVersion 必须为空（中间态自洽），得到 %q", dsh.Identity.TestedOnVersion)
	}
	if strings.TrimSpace(dsh.Identity.Note) == "" {
		t.Fatal("身份交付面必须写 Note：它记录实测结论与已知边界（写入权是用实测换的）")
	}
}

// TestProductIdentityMatchesUi ④：产品名在**界面与身份**两侧同源。
//
// 两处都不解析就失败（fail-closed）：改名/搬迁会让守卫静默空转，而空转的表现是绿的。
func TestProductIdentityMatchesUi(t *testing.T) {
	root := frontendRoot(t)

	// 页面标题（`apps/web/index.html` 的 <title>）。
	indexPath := filepath.Join(root, "apps", "web", "index.html")
	indexSrc := readFileOrFatal(t, indexPath)
	title := ""
	if i := strings.Index(indexSrc, "<title>"); i >= 0 {
		if j := strings.Index(indexSrc[i:], "</title>"); j >= 0 {
			title = indexSrc[i+len("<title>") : i+j]
		}
	}
	if title == "" {
		t.Fatalf("%s 里解析不到 <title>：本判据没有对象（fail-closed）", indexPath)
	}
	if !strings.Contains(title, productidentity.Name) {
		t.Fatalf("页面标题 %q 里没有产品名 %q：界面与身份必须同源", title, productidentity.Name)
	}

	// 左栏品牌（`NavRail.tsx` 的 `fg-rail-brand`）。
	navPath := filepath.Join(root, "packages", "console", "src", "components", "NavRail.tsx")
	navSrc := stripTSComments(readFileOrFatal(t, navPath))
	idx := strings.Index(navSrc, "fg-rail-brand")
	if idx < 0 {
		t.Fatalf("%s 里找不到 `fg-rail-brand`：本判据没有对象（fail-closed）", navPath)
	}
	// 品牌文字是 `ark` + `<span>·</span>` + `mate` 三段拼的，所以先剥掉 JSX 标签、再去空白，
	// 判据落在**渲染后的文字**上（整串匹配会随样式改动假红，只找 `ark` 又会漏掉后半段）。
	segment := navSrc[idx:]
	if end := strings.Index(segment, "</NavLink>"); end >= 0 {
		segment = segment[:end]
	}
	brand := strings.Join(strings.Fields(jsxTagRe.ReplaceAllString(segment, "")), "")
	if !strings.Contains(brand, "ark·mate") {
		t.Fatalf("左栏品牌渲染后应含 %q，得到 %q：界面与身份必须同源", "ark·mate", clip(brand, 160))
	}
}

// TestNoStartupDumpForIdentity ⑤：身份交付路径里不得出现启动期配置 dump。
//
// 用户已拍板：身份 patch 只有两个键、目标条目在 pin 版本上已验证，正确性由静态校验 + golden +
// 版本 pin 保证；真实底座校验只在一次性 probe/CI 里跑。把 dump 加回启动路径的代价是**每次
// 启动多一次秒级子进程**，而它不会让任何测试变红。
func TestNoStartupDumpForIdentity(t *testing.T) {
	path := filepath.Join(backendRoot(t), "internal", "infra", "dsh", "identity.go")
	src := stripGoComments(readFileOrFatal(t, path))
	// 判据落在"能不能拉起子进程"上，而不是那个 flag 的字面量：字面量在 Note 里出现是正常的
	// （它记录的正是"刻意不在启动期跑"这条决定），真正危险的是这三样——有了它们才有 dump 的能力。
	for _, banned := range []string{"dumpSchemaFlag", "exec.Command", "os/exec", "dshBin"} {
		if strings.Contains(src, banned) {
			t.Fatalf("%s 里出现了 %q：身份交付**刻意不在启动期跑底座级校验**"+
				"（正确性由静态校验 + golden + 版本 pin 保证，真实校验只在一次性 probe/CI 里跑）", path, banned)
		}
	}
}

// TestAboutPanelShowsProductWithoutEngineWords 「关于」面板不得出现执行引擎词（A4 的机器判据）。
//
// 三件事一起钉：
//   - 它读的是 `store.product`（产品面事实），**不读** `runtime`（诊断面事实）；
//   - 它渲染核心版本与产品身份版本（否则"关于"只剩一个名字）；
//   - 剥掉注释后的源码里不出现 `dsh` / `DeepSeek Harness` / `runtime` / `底座`。
//
// 为什么连 `runtime` 一起禁：那是本仓的代码词，出现在用户屏上就是"内部结构泄漏"；
// `/meta.runtime` 仍然存在，它的消费者是诊断页。
func TestAboutPanelShowsProductWithoutEngineWords(t *testing.T) {
	path := filepath.Join(frontendRoot(t), "packages", "console", "src", "components", "settings", "AboutPanel.tsx")
	src := stripTSComments(readFileOrFatal(t, path))

	for _, must := range []string{"s.product", "核心版本", "产品身份"} {
		if !strings.Contains(src, must) {
			t.Fatalf("%s 里找不到 %q：关于面板必须只读产品面事实，并同时显示核心版本与产品身份版本", path, must)
		}
	}
	// JSX 不渲染 Markdown：正文里的 `**强调**` 会把星号原样画给用户（2026-10-18 首次截图命中）。
	if strings.Contains(src, "**") {
		t.Fatalf("%s 的正文里出现了 `**`：JSX 不渲染 Markdown，星号会原样显示给用户", path)
	}
	lowered := strings.ToLower(src)
	for _, banned := range []string{"dsh", "deepseek harness", "runtime", "底座"} {
		if strings.Contains(lowered, banned) {
			t.Fatalf("%s 剥注释后仍出现 %q：关于面板只显示产品事实，执行引擎信息在诊断页/日志里"+
				"（用户已拍板：About 不显示执行引擎，也不提供高级信息展开）", path, banned)
		}
	}
}

// jsxTagRe 匹配 JSX/HTML 标签（剥标签后比较渲染文字）。
var jsxTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// clip 截断一段源码用于失败信息（避免把整份文件灌进测试输出）。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
