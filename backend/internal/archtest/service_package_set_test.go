package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ——— P4a「用例层包集 = ADR-2」守卫 ———
//
// P4a 的第一步不是搬实现，而是把 `service/*` 的**包集**钉成 ADR-2 的形状——因为
// "把实现上提到 `svc<领域>`"必须有一个正确的落点，落点错了搬进去的东西也要重搬：
//
//	`svcruntime` 一度同时装**三件不同的事**（供应商档案 / 模型清单与目录 / 底座身份与诊断），
//	于是 `ctrprovider` 只用到它的 5 个方法、`ctrmodel` 只用到 6 个、`ctrsystem` 只用到 1 个——
//	三个控制器各自抱着一个"大半用不上的大接口"。
//
// 现状（P4a 第一批收口后）：
//
//	svcsession    会话 + 检索 + 用量（`svcsearch`/`svcusage` 并入）
//	svcworkspace  工作空间
//	svcruntime    底座身份与诊断（**只剩 2 个方法**）
//	svcprovider   供应商档案（从 svcruntime 拆出）
//	svcmodel      模型清单与目录（从 svcruntime 拆出）
//	svccapability 能力定义
//	svcautomation 自动化
//	svclibrary    库读面
//	svcsystem     系统面（`svcexport` 并入）
//
// 三种失效都**不会让别的测试变红**：
//
//	① 旧名悄悄回潮（谁再建一个 `svcsearch` 装一个方法）——两个包表达同一件事，装配点给哪个都编译过；
//	② `svcruntime` 又长回 provider/model 的方法（拆开的意义当场消失，而调用方一个都不用改）；
//	③ 目标包被改名/删除（判据失锚，守卫静默空转）。
//
// 反证（实跑）：重建一个 `internal/service/svcsearch` → 红；把 `CreateProvider` 声明回
// `svcruntime` → 红。

// servicePackageSet ADR-2 定下的用例层包集（**恰好**这些）。
var servicePackageSet = []string{
	"svcautomation", "svccapability", "svclibrary", "svcmodel",
	"svcprovider", "svcruntime", "svcsession", "svcsystem", "svcworkspace",
}

// serviceRuntimeMethods `svcruntime` 允许声明的方法（多一个都红）。
var serviceRuntimeMethods = []string{"RuntimeIdentity", "RuntimeDiagnostics"}

// TestServicePackageSetFollowsADR2 见文件头。
func TestServicePackageSetFollowsADR2(t *testing.T) {
	dir := filepath.Join(backendRoot(t), "internal", "service")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 %s 失败：%v（这条守卫失去了检查对象）", dir, err)
	}
	var got []string
	for _, e := range entries {
		if e.IsDir() {
			got = append(got, e.Name())
		}
	}
	if len(got) == 0 {
		t.Fatalf("%s 下一个包都没有：判据失锚", dir)
	}
	sort.Strings(got)
	want := append([]string(nil), servicePackageSet...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("`internal/service` 的包集与 ADR-2 不一致：\n  实际 %v\n  期望 %v\n"+
			"三个单方法面（`svcsearch`/`svcusage`/`svcexport`）已并入 `svcsession`/`svcsystem`；"+
			"`svcprovider`/`svcmodel` 已从 `svcruntime` 拆出。\n"+
			"旧名回潮的后果是**两个包表达同一件事**，装配点给哪个都能编译过——"+
			"而\"收实现\"就没法判断该落到哪一边。", got, want)
	}
}

// TestServiceRuntimeStaysIdentityAndDiagnostics `svcruntime` 只许装底座身份与诊断。
func TestServiceRuntimeStaysIdentityAndDiagnostics(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "service", "svcruntime")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：判据失锚", dir)
	}
	allowed := map[string]bool{}
	for _, m := range serviceRuntimeMethods {
		allowed[m] = true
	}
	found := 0
	for _, path := range files {
		src := stripLineComments(readFileOrFatal(t, path))
		lines := strings.Split(src, "\n")
		inIface := false
		for _, l := range lines {
			if interfaceHeadRe.MatchString(l) {
				inIface = true
				continue
			}
			if inIface && l == "}" {
				inIface = false
				continue
			}
			if !inIface {
				continue
			}
			trimmed := strings.TrimSpace(l)
			if trimmed == "" || !strings.Contains(trimmed, "(") {
				continue
			}
			name := trimmed[:strings.Index(trimmed, "(")]
			found++
			if !allowed[name] {
				t.Errorf("`internal/service/svcruntime` 声明了 %q：这个包只装**底座身份与诊断**"+
					"（ADR-2，P4a）。\n"+
					"供应商档案属 `svcprovider`、模型清单与目录属 `svcmodel`——"+
					"三件事共用一个接口时，每个控制器都抱着一份大半用不上的大接口"+
					"（拆之前 `ctrprovider` 只用 5/14、`ctrmodel` 只用 6/14、`ctrsystem` 只用 1/14）。", name)
			}
		}
	}
	if found == 0 {
		t.Fatal("`svcruntime` 里一个接口方法都没扫到：接口被改名/搬走了？判据失锚等于空转。")
	}
}
