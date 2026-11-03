package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestArtifactNoSemanticTool（A-4，2026-11-03 产物溯源）：产物面与 MCP 工具面**不得**出现语义检索/知识库
// 相关标识符。
//
// 沿续「只做资料库、不做知识库」的范围拍板：产物是「登记/收录/溯源」，不是知识库入口；MCP 工具面
// 只有文件级工具（列目录/上传/导入），**没有** `search`/`semantic`/`embedding`/`rag` 一族。要加语义检索
// 必须先删改守卫并走范围评审。刻意不禁裸 `search`（资料库有**关键词**检索，合法）。
func TestArtifactNoSemanticTool(t *testing.T) {
	roots := []string{
		filepath.Join(backendRoot(t), "internal", "domain", "artifact"),
		filepath.Join(backendRoot(t), "internal", "service", "svcartifact"),
		filepath.Join(backendRoot(t), "internal", "infra", "materialmcp"),
	}
	forbidden := []string{"embedding", "vector", "semantic", "rag", "chunks"}

	scanned := 0
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		err = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := strings.ToLower(stripLineComments(string(raw)))
			scanned++
			for _, fb := range forbidden {
				if strings.Contains(src, fb) {
					t.Errorf("%s 出现语义检索标识符 %q——产物面/MCP 工具面不含语义检索（A-4，只做资料库不做知识库）", path, fb)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s: %v", root, err)
		}
	}
	if scanned == 0 {
		t.Fatalf("一个产物/MCP 源文件都没扫到——目录改名会让守卫空转（fail-closed）")
	}
}
