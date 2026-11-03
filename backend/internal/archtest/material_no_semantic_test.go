package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterialHasNoSemanticSearch（本期范围守卫 M-5，2026-11-03）。
//
// 本期范围拍板（设计 .dsh/docs/specs/2026-11-03-material-library-design.md §0/§7）：
// **只做资料库、不做知识库**——资料库只做「存 / 找（关键词子串）/ 取 / 回流」，**不含**
// 语义向量检索 / embedding / RAG 问答。
//
// 这条守卫把「一句人工记忆」变成「会红的测试」：资料库域（`internal/domain/material`）、
// 用例层（`svcmaterial`，P1 落地后）、agent 接入面（`materialmcp`，P4 落地后）的生产源码里，**不得出现** embedding / vector / semantic / rag / chunks 这类
// 语义检索标识符。要加知识库必须**先删改本守卫并走范围评审**，不能顺手塞回来。
//
// 剥注释后扫描（注释里引用设计文档说明「不做」是允许的，见 stripLineComments）；判据 fail-closed：
// 一个扫描根都找不到即失败（避免目录改名后空转）。
func TestMaterialHasNoSemanticSearch(t *testing.T) {
	roots := []string{
		filepath.Join(backendRoot(t), "internal", "domain", "material"),
		filepath.Join(backendRoot(t), "internal", "service", "svcmaterial"),
		filepath.Join(backendRoot(t), "internal", "infra", "materialmcp"),
	}

	// 语义检索标识符（小写化后子串匹配）。刻意不匹配裸 `search`（资料库有**关键词**检索，
	// 函数名 `SearchKeywords` 合法）；只禁语义/向量族词。
	forbidden := []string{
		"embedding",
		"vector",
		"semantic",
		"rag",
		"chunks",
	}

	scannedFiles := 0
	scannedRoots := 0
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			// svcmaterial / materialmcp 尚未落地（P1/P4）：跳过那个根，但至少要扫到 domain/material。
			continue
		}
		scannedRoots++
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
			scannedFiles++
			for _, word := range forbidden {
				if strings.Contains(src, word) {
					t.Errorf("%s 出现语义检索标识符 %q——本期范围「只做资料库、不做知识库」"+
						"（语义向量检索 / embedding / RAG 问答整块不做）。要加知识库先删改本守卫并走范围评审。",
						path, word)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s: %v", root, err)
		}
	}

	if scannedRoots == 0 || scannedFiles == 0 {
		t.Fatalf("一个资料库源码文件都没扫到（scannedRoots=%d scannedFiles=%d）——"+
			"目录改名/搬迁后本守卫会空转，故 fail-closed", scannedRoots, scannedFiles)
	}
}
