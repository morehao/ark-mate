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
// 判据四段（都 fail-closed）：
//  1. `Layered.Resolve` 里四层出现的**顺序**必须是 env → managed → dotenv_cwd → dotenv_home
//     （锚在实现体内的首次出现位置，解析不到函数即失败）；
//  2. 除 `pkg/credentials/**` 外，全仓 Go 源码不得读 `.env` 文件（`.env` 字面量与文件读取同文件共现）；
//  3. 除 `pkg/credentials/**` 外，同一个文件不得同时点名两层 `Source*` 常量（同时点名两层的人，
//     必然在文件里自己判了顺序）；确需登记的进白名单并写明理由；
//  4. 四个 `Source` 取值常量在全仓**只声明一次**（`Source<词> Source = "…"` 恰好四处，且都在
//     `pkg/credentials`）——谁复述一份词表谁就漂移。
func TestCredentialLayeringOrderIsSingleSource(t *testing.T) {
	root := backendRoot(t)
	creds := filepath.Join(root, "pkg", "credentials")
	if _, err := os.Stat(creds); err != nil {
		t.Fatalf("凭据包不存在：%s（%v）——守卫失去对象", creds, err)
	}

	// ① 顺序锚在 Resolve 的实现体内。
	src := readSourceFor(t, filepath.Join(creds, "credentials.go"))
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

	// ②③④ 全仓扫描：跳过守卫自身、凭据包、以及 `_test.go`。
	//
	// 为什么跳过测试：夹具**必须**能造出"两层都有值"的局面（否则测不到优先级），也必须能写一份
	// `.env`（否则测不到 dotenv 那两层）。这条判据管的是**产品路径**——测试里复制一份瀑布流不会
	// 改变生产行为，而把它写进生产代码才会。
	repoRoot := filepath.Dir(root)
	goFiles := goFilesUnder(t, filepath.Join(root, "internal"))
	goFiles = append(goFiles, goFilesUnder(t, filepath.Join(root, "pkg"))...)
	if len(goFiles) < 100 {
		t.Fatalf("只扫到 %d 个 Go 文件——路径写错了？（fail-closed）", len(goFiles))
	}
	// 词表从凭据包**自取**（`Source<词> Source = "<值>"`），再拿"同名同值"去全仓找复述：
	// 别的包另有自己的 `Source` 家族（`pkg/config` 的 `SourceFile`/`SourceFlag`/`SourceEnv` 是
	// 「这份配置从哪来」，其中 `SourceEnv` 与凭据词表**同名不同值**），按名字扫会误伤，
	// 按"同名 + 同值"扫才只认这一份词表。
	pairRe := regexp.MustCompile(`(Source\w+)\s+Source\s*=\s*"([^"]*)"`)
	credsPairs := map[string]string{}
	for _, m := range pairRe.FindAllStringSubmatch(src, -1) {
		credsPairs[m[1]] = m[2]
	}
	if len(credsPairs) != 4 {
		t.Fatalf("`pkg/credentials/credentials.go` 里应当恰好声明四个 `Source` 取值常量"+
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
	decls := map[string][]string{}
	var dotenvReaders, twoLayerFiles []string
	for _, path := range goFiles {
		rel, _ := filepath.Rel(repoRoot, path)
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, "_test.go") ||
			strings.HasPrefix(rel, "backend/internal/archtest/") ||
			strings.HasPrefix(rel, "backend/pkg/credentials/") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", rel, err)
		}
		clean := stripLineComments(string(raw))
		if dotenvLiteral.MatchString(clean) && readCall.MatchString(clean) {
			dotenvReaders = append(dotenvReaders, rel)
		}
		named := 0
		for _, name := range order {
			if strings.Contains(clean, name) {
				named++
			}
		}
		if named >= 2 && !allowedByLayeringWhitelist(t, rel) {
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
			"dotenv 的解析与「读哪一份、以什么优先级」都是层序本身——它只有一处实现（`pkg/credentials`）。"+
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
		t.Fatalf("`Source` 取值常量只许在 `pkg/credentials` 里声明一次，别处又声明了：\n  %s\n"+
			"（复述词表 = 两条必然漂移的事实：面板与 doctor 会按各自那份解释同一个值）",
			strings.Join(out, "\n  "))
	}
}

// allowedByLayeringWhitelist 命中白名单即跳过（前缀匹配）——登记必须写明理由。
func allowedByLayeringWhitelist(t *testing.T, rel string) bool {
	t.Helper()
	for _, w := range layeringWhitelist {
		if !strings.HasPrefix(rel, w.path) {
			continue
		}
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %s 的理由是空的——登记必须能回答「为什么这里必须自己点名两层」", w.path)
		}
		return true
	}
	return false
}

// layeringWhitelist 允许同时点名两层 `Source*` 的文件（相对仓库根路径前缀）。
//
// 目前是空的：正确的用法是拿 `Resolve` 返回的 `Resolution.Source` 去比较**一个**值
// （例如 `SourceManaged` 才允许面板编辑），而不是在别处重新点名两层。
var layeringWhitelist = []struct {
	path   string
	reason string
}{
	{
		path: "backend/internal/manager/credentials.go",
		reason: "**库与镜像的对账，不是第二份层序**：四层顺序仍由 `Layered.Resolve` 一处实现，" +
			"这里只把「库」并进托管那一层做权威裁决（两边都有时以库为准；镜像里有而库里没有 = 镜像" +
			"陈旧 → **不采用**，否则在面板上清掉的密钥会被一份旧镜像悄悄撤销）。它点名 `SourceEnv`" +
			"只为说出「是哪个环境变量命中的」（读面必须解释得清「为什么我改的面板不生效」），" +
			"点名 `SourceManaged` 只为上面那条对账——**都不参与判层与层的先后**",
	},
}

// resolveFuncBodyFor 取出 `func (l *Layered) Resolve(` 的函数体（到下一个顶层 `}` 为止）。
//
// 刻意只按最外层大括号配平，不做 Go 语法解析：判据要看的是"四个层名在实现体里的先后"，
// 用词法配对足够，而且**解析不到就失败**（改名/搬迁会让守卫静默空转）。
func resolveFuncBodyFor(t *testing.T, src string) string {
	t.Helper()
	start := strings.Index(src, "func (l *Layered) Resolve(")
	if start < 0 {
		t.Fatalf("`pkg/credentials/credentials.go` 里找不到 `func (l *Layered) Resolve(`——" +
			"四层回落的顺序判据失去了对象（fail-closed：改名请同步本守卫）")
	}
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
