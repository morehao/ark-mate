package archtest

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestArtifactDoesNotImportMaterial（A-1）：产物域与用例层**不得** import 资料库域/case 层。
//
// 依赖方向单向：产物是「会话/工具产出的登记册」，资料库是「用户组织」；两者通过
// library_nodes.provenance_id 引用相连，但代码依赖只能「资料库 → 产物」，不能反向。
// 反向 import 会让产物域被组织语义污染，也破坏 svcmaterial 跨域编排的唯一入口。
func TestArtifactDoesNotImportMaterial(t *testing.T) {
	roots := []string{
		filepath.Join(backendRoot(t), "internal", "domain", "artifact"),
		filepath.Join(backendRoot(t), "internal", "service", "svcartifact"),
	}
	forbidden := []string{
		"internal/domain/material",
		"internal/service/svcmaterial",
	}
	scanned := 0
	fset := token.NewFileSet()
	for _, root := range roots {
		paths, err := filepathGlobGo(root)
		if err != nil {
			t.Fatalf("扫 %s: %v", root, err)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			scanned++
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("解析 %s: %v", path, err)
			}
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				for _, fb := range forbidden {
					if strings.Contains(p, fb) {
						t.Errorf("%s import 了 %s —— 产物域/用例层不得依赖资料库域（A-1，依赖单向）", path, p)
					}
				}
			}
		}
	}
	// fail-closed：一个产物源文件都没扫到 → 目录改名会让守卫空转。
	if scanned == 0 {
		t.Fatalf("一个产物源文件都没扫到——domain/artifact 或 svcartifact 目录改名了？")
	}
}

// filepathGlobGo 返回某目录下全部 .go 文件（非递归；本仓 domain/svc 目录都是平铺）。
func filepathGlobGo(dir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dir, "*.go"))
}
