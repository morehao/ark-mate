package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件钉住 v20 的第三条不变量：**四层回落只有一处实现**（设计 §5）。
//
// 为什么这需要一条机器判据：层序（继承环境 > 托管文档 > `<cwd>/.env` > `<MATE_HOME>/.env`）是一条
// **产品语义**——"面板上写的密钥不该被陈旧 `.env` 顶掉""更具体的位置赢"。它的失效方式是**静默**的：
// 另一处再写一遍瀑布流，只要顺序稍有不同（比如先读 `.env` 再读托管文档），两个入口就会对同一个
// 引用给出**不同的值**，而两边各自的测试都会绿（各自测的是自己那套顺序）。同理，把 dotenv 的
// 解析或"读哪一份"复制到第二处，等于把这条语义复制成两份必然漂移的事实。
//
// **判据按符号定位，不钉文件路径**（P0）：这条不变量只该落在两处——层序的**唯一实现**
// （`internal/infra/credentials` 的 `Layered.Resolve`）与那个合法地"同时点名两层"的**对账消费者**
// （迁移后落 `mechanism/credential`，由符号 `ResolveProviderKey` 认领）。两处都按符号断言，
// 所以文件搬进哪个包都不影响判据；反过来，"在第二个地方又实现了一遍层序"会因为**符号声明数
// 变成 2** 而红。
//
// 判据五段（都 fail-closed）：
//  1. `Layered.Resolve` 全仓**恰好一处**（按符号找，不钉文件），且就落在 `internal/infra/credentials` 包里；
//     它的函数体里四层出现的**顺序**必须是 env → managed → dotenv_cwd → dotenv_home
//     （锚在实现体内的首次出现位置，解析不到函数即失败）；
//  2. 除 `internal/infra/credentials/**` 外，全仓 Go 源码不得读 `.env` 文件（`.env` 字面量与文件读取同文件共现）；
//  3. 除 `internal/infra/credentials/**` 外，同一个文件不得同时点名两层 `Source*` 常量（同时点名两层的人，
//     必然在文件里自己判了顺序）；豁免只按**符号**给——只有声明了白名单符号的那个文件拿得到；
//  4. 四个 `Source` 取值常量在全仓**只声明一次**（`Source<词> Source = "…"` 恰好四处，且都在
//     `internal/infra/credentials`）——谁复述一份词表谁就漂移；
//  5. 白名单符号全仓**恰好一处声明**：0 处 = 登记烂成了陈旧指针（豁免再也发不出去，而"唯一实现"
//     也没人再验），≥2 处 = 第二份对账实现也拿到了同一份豁免。
func TestCredentialLayeringOrderIsSingleSource(t *testing.T) {
	root := backendRoot(t)
	creds := filepath.Join(root, "internal", "infra", "credentials")
	if _, err := os.Stat(creds); err != nil {
		t.Fatalf("凭据包不存在：%s（%v）——守卫失去对象", creds, err)
	}
	repoRoot := filepath.Dir(root)
	goFiles := goFilesUnder(t, filepath.Join(root, "internal"))
	if len(goFiles) < 100 {
		t.Fatalf("只扫到 %d 个 Go 文件——路径写错了？（fail-closed）", len(goFiles))
	}

	// 产品路径 = 非测试文件（`_test.go` 里的夹具必须能造出"两层都有值"的局面，也必须能写一份
	// `.env`，否则测不到优先级与 dotenv 那两层；这条判据管的是**产品路径**——测试里复制一份
	// 瀑布流不会改变生产行为，而把它写进生产代码才会）。
	productGo := make([]string, 0, len(goFiles))
	relOf := make(map[string]string, len(goFiles))
	for _, path := range goFiles {
		rel, _ := filepath.Rel(repoRoot, path)
		rel = filepath.ToSlash(rel)
		relOf[path] = rel
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		productGo = append(productGo, path)
	}

	// ① 顺序锚在 `Layered.Resolve` 的实现体内——函数**按符号**定位（搬家不改判据），
	// 且要求全仓恰好一处：第二份 `Layered.Resolve` 就是"第二个地方又实现了一遍层序"。
	resolveFiles := goFilesDeclaring(t, productGo, layeredResolveDeclRE)
	if len(resolveFiles) != 1 {
		t.Fatalf("`Layered.Resolve` 全仓应当恰好一处实现，实际 %d 处：%v\n"+
			"四层回落的顺序是产品语义，第二份实现就是第二名事实源（fail-closed）",
			len(resolveFiles), relAll(repoRoot, resolveFiles))
	}
	if got := filepath.Dir(resolveFiles[0]); got != creds {
		t.Fatalf("`Layered.Resolve` 落在 %s，而它只许在凭据包（%s）里——"+
			"分层解析的唯一实现不能离开 `internal/infra/credentials`", got, creds)
	}
	src := readSourceFor(t, resolveFiles[0])
	body := resolveFuncBodyFor(t, src)
	order := []string{"SourceEnv", "SourceManaged", "SourceDotenvCwd", "SourceDotenvHome"}
	prev := -1
	for _, name := range order {
		idx := strings.Index(body, name)
		if idx < 0 {
			t.Fatalf("`Layered.Resolve` 里找不到 %s——四层回落的顺序判据失去了对象（fail-closed）", name)
		}
		if idx < prev {
			t.Errorf("`Layered.Resolve` 里 %s 出现在前一层之前：层序被改动了。"+
				"四层顺序是产品语义（继承环境 > 托管文档 > 项目 .env > 数据目录 .env），"+
				"改它必须同时改设计文档、README 与面板文案", name)
		}
		prev = idx
	}

	// ②③④⑤ 的扫描面：产品路径里再跳过守卫自身与凭据包（后者由 ① 单独钉）。
	scanned := make([]string, 0, len(productGo))
	for _, path := range productGo {
		rel := relOf[path]
		if strings.HasPrefix(rel, "backend/internal/archtest/") ||
			strings.HasPrefix(rel, "backend/internal/infra/credentials/") {
			continue
		}
		scanned = append(scanned, path)
	}

	// 词表从凭据包**自取**（`Source<词> Source = "<值>"`），再拿"同名同值"去全仓找复述：
	// 别的包另有自己的 `Source` 家族（`internal/config` 的 `SourceFile`/`SourceFlag`/`SourceEnv` 是
	// 「这份配置从哪来」，其中 `SourceEnv` 与凭据词表**同名不同值**），按名字扫会误伤，
	// 按"同名 + 同值"扫才只认这一份词表。
	pairRe := regexp.MustCompile(`(Source\w+)\s+Source\s*=\s*"([^"]*)"`)
	credsPairs := map[string]string{}
	for _, m := range pairRe.FindAllStringSubmatch(src, -1) {
		credsPairs[m[1]] = m[2]
	}
	if len(credsPairs) != 4 {
		t.Fatalf("凭据包里应当恰好声明四个 `Source` 取值常量"+
			"（「这把钥匙从哪层来」的对外词表，面板与 doctor 都读它），实际 %d 个：%v",
			len(credsPairs), credsPairs)
	}
	var declRe *regexp.Regexp
	{
		alts := make([]string, 0, len(credsPairs))
		for name, value := range credsPairs {
			alts = append(alts, regexp.QuoteMeta(name)+`\s+Source\s*=\s*"`+regexp.QuoteMeta(value)+`"`)
		}
		sort.Strings(alts)
		declRe = regexp.MustCompile(strings.Join(alts, "|"))
	}
	// `.env` 必须是**字符串字面量**（引号紧跟其后）：`t.env` / `mergeEnv` 这类标识符不算
	// ——第一版判据就是被 `cmd.Env = mergeEnv(os.Environ(), t.env)` 喂红的。
	dotenvLiteral := regexp.MustCompile(`\.env["` + "`" + `]`)
	readCall := regexp.MustCompile(`os\.ReadFile\(|os\.Open\(|bufio\.NewScanner\(|ioutil\.ReadFile\(`)
	// 豁免按**符号**认领，不按文件路径：符号搬家不改判据；而"在第二个文件里再抄一份对账"
	// 会让该符号的声明数变成 2 → 段 ⑤ 红。
	whitelistRE := make(map[string]*regexp.Regexp, len(layeringWhitelist))
	for _, w := range layeringWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %s 的理由是空的——登记必须能回答「为什么这里必须自己点名两层」", w.symbol)
		}
		whitelistRE[w.symbol] = declREFor(w.symbol)
	}
	declaredAt := map[string][]string{} // 白名单符号 → 全仓声明它的文件
	decls := map[string][]string{}
	var dotenvReaders, twoLayerFiles []string
	for _, path := range scanned {
		rel := relOf[path]
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", rel, err)
		}
		clean := stripLineComments(string(raw))
		if dotenvLiteral.MatchString(clean) && readCall.MatchString(clean) {
			dotenvReaders = append(dotenvReaders, rel)
		}
		exempt := ""
		for _, w := range layeringWhitelist {
			if whitelistRE[w.symbol].MatchString(clean) {
				declaredAt[w.symbol] = append(declaredAt[w.symbol], rel)
				exempt = w.symbol
			}
		}
		named := 0
		for _, name := range order {
			if strings.Contains(clean, name) {
				named++
			}
		}
		if named >= 2 && exempt == "" {
			twoLayerFiles = append(twoLayerFiles, rel)
		}
		for _, m := range declRe.FindAllString(clean, -1) {
			decls[m] = append(decls[m], rel)
		}
	}
	sort.Strings(dotenvReaders)
	sort.Strings(twoLayerFiles)

	if len(dotenvReaders) > 0 {
		t.Errorf("这些文件自己读 `.env`：\n  %s\n"+
			"dotenv 的解析与「读哪一份、以什么优先级」都是层序本身——它只有一处实现（`internal/infra/credentials`）。"+
			"要拿一把钥匙请调 `Layered.Resolve`，别自己拼回落链", strings.Join(dotenvReaders, "\n  "))
	}
	if len(twoLayerFiles) > 0 {
		t.Errorf("这些文件同时点名了两层以上的 `Source*` 常量：\n  %s\n"+
			"同一个文件里同时引用两层，几乎必然是自己在判顺序——顺序只许在 `Layered.Resolve` 里判",
			strings.Join(twoLayerFiles, "\n  "))
	}
	if len(decls) > 0 {
		out := make([]string, 0, len(decls))
		for decl, where := range decls {
			out = append(out, strings.TrimSpace(decl)+" @"+strings.Join(where, ","))
		}
		sort.Strings(out)
		t.Fatalf("`Source` 取值常量只许在 `internal/infra/credentials` 里声明一次，别处又声明了：\n  %s\n"+
			"（复述词表 = 两条必然漂移的事实：面板与 doctor 会按各自那份解释同一个值）",
			strings.Join(out, "\n  "))
	}
	// ⑤ 白名单符号的声明点全仓**恰好一处**：0 处 = 这条登记已经烂成陈旧指针（那条豁免永远
	// 发不出去，而"唯一实现"也没人再验）；≥2 处 = 第二份对账实现拿到了同一份豁免。
	for _, w := range layeringWhitelist {
		where := declaredAt[w.symbol]
		switch len(where) {
		case 1:
		case 0:
			t.Fatalf("白名单符号 `%s` 全仓一处都没声明——这条登记已经失去对象（陈旧指针，"+
				"fail-closed）：要么把它改指到真实存在的那一处，要么连同理由一起删掉", w.symbol)
		default:
			sort.Strings(where)
			t.Fatalf("白名单符号 `%s` 全仓声明了 %d 处：\n  %s\n"+
				"豁免只许给**唯一**那一处「库与镜像对账」；第二处拿到豁免就等于允许第二份凭据层序实现",
				w.symbol, len(where), strings.Join(where, "\n  "))
		}
	}
}

// layeredResolveDeclRE 按**符号**认领 `(*Layered).Resolve` 的声明：接收者名不限、文件与目录
// 一概不钉（搬家不改判据），而第二处同名实现会被数出来。
var layeredResolveDeclRE = regexp.MustCompile(`func\s+\([^)]*\*Layered\)\s*Resolve\s*\(`)

// declREFor 生成"声明了该符号"的正则：包级函数 `func resolveProviderKey(` 与方法
// `func (s *X) resolveProviderKey(` 都算。**只认符号名**，不钉接收者类型与文件路径——
// 这既是搬家后判据仍然成立的原因，也是"第二份同名实现"仍然被数出来的原因。
func declREFor(symbol string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?` + regexp.QuoteMeta(symbol) + `\s*\(`)
}

// goFilesDeclaring 返回 files 里声明了 decl 的那些文件（升序）。
func goFilesDeclaring(t *testing.T, files []string, decl *regexp.Regexp) []string {
	t.Helper()
	var out []string
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", path, err)
		}
		if decl.MatchString(stripLineComments(string(raw))) {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// relAll 把绝对路径翻译成相对 base 的斜杠路径（只给报错文案用）。
func relAll(base string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(base, p)
		if err != nil {
			rel = p
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

// uniqueSourceFile 在 dir 下**递归**找出唯一一个"源码匹配 pattern"的非测试 Go 文件，返回其
// 绝对路径（跳过 `testdata`、`.` 开头的目录与 skipDirs 里的目录名）。
//
// 这是 P0 的通用手法：判据的事实源按**内容 / 符号**定位，不钉某个精确到文件的实现路径
// ——文件随重构搬进 `domain/*` / `mechanism/*` 之后判据照旧成立；而"第二处又抄了
// 一份"会因为命中数变成 2 而红（原来的写法只读钉死的那一个文件，根本看不见第二份）。
//
// 0 处或 ≥2 处都 fail-closed：判据失去唯一对象，比"挑错对象"或假绿好。
func uniqueSourceFile(t *testing.T, dir string, skipDirs map[string]bool, pattern, what string) string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	var hits []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir &&
				(skipDirs[d.Name()] || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if re.Match(raw) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 找 %s 失败: %v", dir, what, err)
	}
	sort.Strings(hits)
	if len(hits) != 1 {
		t.Fatalf("%s 应当恰好一处，实际 %d 处：\n  %s\n"+
			"判据按符号 / 内容定位（不钉文件路径），所以它必须唯一：0 处 = 判据失去对象"+
			"（fail-closed），≥2 处 = 有人又抄了一份",
			what, len(hits), strings.Join(relAll(filepath.Dir(dir), hits), "\n  "))
	}
	return hits[0]
}

// layeringWhitelist 允许同时点名两层 `Source*` 的**符号**（不是文件路径）。
//
// 登记的是"唯一那一处对账消费者"的符号：凭据机制包从 `manager` 搬到 `mechanism/credential`
// 之后这条登记仍然成立（符号跟着走），而"再抄一份对账"会因为该符号的声明数变成 2 而红。
// 目前只有一条：正确的用法是拿 `Resolve` 返回的 `Resolution.Source` 去比较**一个**值
// （例如 `SourceManaged` 才允许面板编辑），而不是在别处重新点名两层。
var layeringWhitelist = []struct {
	symbol string
	reason string
}{
	{
		// P3d-2 起它落 `internal/mechanism/credential`，并因为跨包转发的要求而**导出**
		// （`ResolveProviderKey`）——判据只认符号名，所以这里跟着改名，不钉路径。
		symbol: "ResolveProviderKey",
		reason: "**库与镜像的对账，不是第二份层序**：四层顺序仍由 `Layered.Resolve` 一处实现，" +
			"这里只把「库」并进托管那一层做权威裁决（两边都有时以库为准；镜像里有而库里没有 = 镜像" +
			"陈旧 → **不采用**，否则在面板上清掉的密钥会被一份旧镜像悄悄撤销）。它点名 `SourceEnv`" +
			"只为说出「是哪个环境变量命中的」（读面必须解释得清「为什么我改的面板不生效」），" +
			"点名 `SourceManaged` 只为上面那条对账——**都不参与判层与层的先后**",
	},
}

// resolveFuncBodyFor 取出 `Layered.Resolve` 的函数体（到下一个顶层 `}` 为止）。
//
// 函数的入口**按符号**定位（`layeredResolveDeclRE`），刻意只按最外层大括号配平、不做 Go 语法
// 解析：判据要看的是"四个层名在实现体里的先后"，用词法配对足够，而且**解析不到就失败**
// （改名/搬迁会让守卫静默空转）。
func resolveFuncBodyFor(t *testing.T, src string) string {
	t.Helper()
	loc := layeredResolveDeclRE.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("找不到 `Layered.Resolve` 的实现（按符号 `func (… *Layered) Resolve(` 定位）——" +
			"四层回落的顺序判据失去了对象（fail-closed：改名请同步本守卫）")
	}
	start := loc[0]
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("`Layered.Resolve` 的函数体读不到（找不到 `{`）")
	}
	depth := 0
	for i := start + open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("`Layered.Resolve` 的函数体没有配平的大括号（fail-closed）")
	return ""
}

// readSourceFor 读一个文件，读不到即失败（守卫的输入必须存在）。
func readSourceFor(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", path, err)
	}
	return string(raw)
}
