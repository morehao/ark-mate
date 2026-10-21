package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pkgDocRe 抓包注释首行的包名（`// Package <名字> …`）。
//
// 只认这一种形态：Go 的约定是包注释以 `Package <name>` 开头，`go doc` / pkg.go.dev 会把那个名字
// 当作文档标题。别处的 `Package` 字样（正文里提到"Package"）不受影响。
var pkgDocRe = regexp.MustCompile(`^// Package ([A-Za-z_][A-Za-z0-9_]*)`)

// TestPackageDocNamesMatch 包注释里的包名必须与 `package <名字>` 子句**逐字相同**。
//
// 为什么需要它：`// Package svcsession …` 写在 `package session` 上方，编译、vet、测试全过，
// 但 `go doc` 与 pkg.go.dev 会把那个**不存在的名字**当成文档标题——读者照着去找
// `internal/svcsession`，那里什么都没有。2026-10-21 C3 4.2 把 `svc` 前缀从包名去掉时，8 个包注释
// 没跟着改，而**没有任何判据会红**（`go vet` 只比对 `package` 子句本身，不读注释）。
//
// 判据：扫 `backend/` 下每个 .go 文件，若包注释首行匹配 `// Package X`，则 X 必须等于该文件的
// `package` 子句。fail-closed：一条包注释都没扫到 → Fatal（扫描根失效的最坏表现是"绿但没查"）。
func TestPackageDocNamesMatch(t *testing.T) {
	root := backendRoot(t)
	checked := 0
	for _, dir := range goPackagesUnder(t, root) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读目录 %s 失败: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
			if err != nil {
				t.Fatalf("解析 %s 失败: %v", path, err)
			}
			if file.Doc == nil || len(file.Doc.List) == 0 {
				continue
			}
			m := pkgDocRe.FindStringSubmatch(file.Doc.List[0].Text)
			if m == nil {
				continue
			}
			checked++
			if m[1] != file.Name.Name {
				t.Errorf("%s：包注释写的是 `Package %s`，实际子句是 `package %s`"+
					"（`go doc` 会照抄注释里的名字，读者会去找一个不存在的包）",
					rel(root, path), m[1], file.Name.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("一条 `// Package …` 包注释都没扫到：扫描根（backend/）失效，判据失去对象")
	}
}
