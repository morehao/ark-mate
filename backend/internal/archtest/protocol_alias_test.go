package archtest

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ——— G6：协议包的类型别名/转发只许协议侧自用（2026-10-22，决策 v32）———
//
// 背景：`infra/protocol/{acp,mcp}` 把 `core/protocol` 的载荷类型、事件常量与函数转发成同名的
// `Event` / `Tool` / `CallResult` / `GatewayBridgeArgs` …，让**协议实现自己的代码**读起来自然
// （实测：协议包内部 362 处不带前缀的引用，包外生产代码只有 3 个文件）。
//
// 这条转发本身不是第二事实源（`type X = protocol.X` 只有一处定义、零新行为），但它制造了
// **第二个出口**：包外任何一处 `acp.ToolCallInfo` / `mcp.GatewayCredCwdEnv` 都会把一个只需要
// 值类型的消费方绑到"协议实现"上——而那正是 A1/A2 刚刚拆掉的那种耦合，只是绕过了别名。
// G2 只挡内圈三目录 import `infra`；装配面/驱动面/测试不在它的扫描面里，于是这条边**今天没有判据**。
//
// 判据形状：先**从转发声明本身**解析出别名集合（不手抄一份名单——手抄的名单与声明漂移时
// 判据会静默空转），再扫包外生产代码里 `<import 本地名>.<别名>` 的出现次数。
// 白名单：只有两个协议包自己（它们正是这套读法的使用者）。
// fail-closed 两侧：别名集合为空 / 扫到的协议包导入方为 0 → 都 Fatal。

// protocolAliasDirs 承载转发的两个协议绑定包（相对 backend 根）。
var protocolAliasDirs = []string{"internal/infra/protocol/acp", "internal/infra/protocol/mcp"}

// aliasDeclRe 匹配转发声明：`Event = protocol.Event` / `type GatewayScope = protocol.GatewayScope`。
var aliasDeclRe = regexp.MustCompile(`(?m)^\s*(?:type\s+)?([A-Z]\w*)\s*=\s*protocol\.`)

// aliasFuncRe 匹配一行转发函数：`func GatewayBridgeArgs(sc GatewayScope) []string { return protocol.GatewayBridgeArgs(sc) }`。
//
// 只认"函数体就是一行 `return protocol.X`"这一种形态——真正的实现不在此列。
var aliasFuncRe = regexp.MustCompile(`(?m)^func ([A-Z]\w*)\([^)]*\)[^{]*\{\s*return protocol\.[A-Z]`)

// importSpecRe 从 import 块里取（可选别名 + 路径）。
var importSpecRe = regexp.MustCompile(`^\s*(?:([A-Za-z_]\w*)\s+)?"([^"]+)"\s*$`)

func TestProtocolAliasesStayInProtocolPackages(t *testing.T) {
	root := backendRoot(t)

	// ① 从声明解析别名集合（每包一份）。
	aliases := map[string]map[string]bool{}
	for _, dir := range protocolAliasDirs {
		names := map[string]bool{}
		abs := filepath.Join(root, filepath.FromSlash(dir))
		entries, err := os.ReadDir(abs)
		if err != nil {
			t.Fatalf("读不到协议包 %s: %v", dir, err)
		}
		files := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			files++
			src := stripGoComments(readFileOrFatal(t, filepath.Join(abs, e.Name())))
			for _, m := range aliasDeclRe.FindAllStringSubmatch(src, -1) {
				names[m[1]] = true
			}
			for _, m := range aliasFuncRe.FindAllStringSubmatch(src, -1) {
				names[m[1]] = true
			}
		}
		if files == 0 {
			t.Fatalf("%s 下扫到 0 个生产文件：判据失锚（目录改名/搬迁时请同步 protocolAliasDirs）", dir)
		}
		if len(names) == 0 {
			t.Fatalf("%s 里解析到 0 条转发声明（X = protocol.Y / 一行转发函数）：别名判据会静默空转", dir)
		}
		aliases[dir] = names
	}

	// ② 扫包外生产代码。
	type offender struct {
		pkg  string
		file string
		hits []string
	}
	var offenders []offender
	importers := 0
	scannedAlias := map[string]int{} // 别名 → 包外使用次数（用于正向断言）

	for _, p := range scanProductionPackages(t) {
		if isUnderAny(p.rel, protocolAliasDirs) {
			continue // 白名单：协议包自己
		}
		entries, err := os.ReadDir(p.dir)
		if err != nil {
			t.Fatalf("读不到包目录 %s: %v", p.rel, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src := stripGoComments(readFileOrFatal(t, filepath.Join(p.dir, e.Name())))
			locals := protocolImportLocals(src)
			if len(locals) > 0 {
				importers++
			}
			var hits []string
			seen := map[string]bool{}
			for dir, local := range locals {
				re := regexp.MustCompile(`\b` + regexp.QuoteMeta(local) + `\.([A-Z]\w*)`)
				for _, m := range re.FindAllStringSubmatch(src, -1) {
					if !aliases[dir][m[1]] || seen[local+"."+m[1]] {
						continue
					}
					seen[local+"."+m[1]] = true
					hits = append(hits, local+"."+m[1])
					scannedAlias[local+"."+m[1]]++
				}
			}
			if len(hits) > 0 {
				sort.Strings(hits)
				offenders = append(offenders, offender{pkg: p.rel, file: e.Name(), hits: hits})
			}
		}
	}

	if importers == 0 {
		t.Fatal("没有任何生产包 import 协议包：判据失锚（协议包改名/搬迁后这条会静默变绿）")
	}
	for _, o := range offenders {
		t.Errorf("%s/%s 用了协议包的转发名 %s：这台别名是**协议实现自己的读法**，不是第二个出口。\n"+
			"请改 import `internal/core/protocol` 并用 `protocol.X`（值类型的唯一事实源在那里）。\n"+
			"让装配面/驱动面认协议包，等于把 A1/A2 拆掉的耦合从别名这个后门接回来（决策 v31 ①、v32）。",
			o.pkg, o.file, strings.Join(o.hits, ", "))
	}

	// ③ 正向断言：别名确实还在被协议包自己用（否则这套转发已经该整块删掉，而不是留着）。
	// 这里只要求"至少一条别名在协议包内部有引用"——判据的目的是防后面那个出口，不是给前缀读法记分。
	usedInside := false
	for _, dir := range protocolAliasDirs {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		entries, _ := os.ReadDir(abs)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src := stripGoComments(readFileOrFatal(t, filepath.Join(abs, e.Name())))
			for name := range aliases[dir] {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(src) {
					usedInside = true
					break
				}
			}
		}
	}
	if !usedInside {
		t.Fatal("协议包内部一处都没用到这些转发名：别名已无收益，请整块删掉（本判据的前提消失）")
	}
}

// protocolImportLocals 从源码的 import 声明里取出
// 「协议包目录 → 本地名」，本地名 = 显式别名或路径末段。
func protocolImportLocals(src string) map[string]string {
	out := map[string]string{}
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import (") {
			inBlock = true
			continue
		}
		if inBlock && trimmed == ")" {
			inBlock = false
			continue
		}
		// 变量声明里也可能出现 `importAlias "..."` 形态的**字符串**，但只有 import 块与
		// 单行 import 才算；单行形态 `import x "path"` 一并覆盖。
		isImportLine := inBlock || strings.HasPrefix(trimmed, "import ")
		if !isImportLine {
			continue
		}
		spec := strings.TrimPrefix(trimmed, "import ")
		m := importSpecRe.FindStringSubmatch(" " + spec)
		if m == nil {
			continue
		}
		p := m[2]
		for _, dir := range protocolAliasDirs {
			if !strings.HasSuffix(p, "/"+dir) {
				continue
			}
			local := m[1]
			if local == "" {
				local = path.Base(p)
			}
			out[dir] = local
		}
	}
	return out
}
