package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenWorkspaceAttachmentStrings（G5）附件投递**不得写用户工作区**——实现源码里不得出现这些
// 形态：把附件物化到工作区的目录名，或"复制附件到会话 cwd"的动作。
//
// 设计 §5.3.2「不脏」：附件只落 `<MATE_HOME>/attachments`，prompt 里只有一行 handle（文档/大图）
// 或一个 image 块（小图），零工作区副作用。反证：加入工作区复制 → 红。
var forbiddenWorkspaceAttachmentStrings = []string{
	".mate-attachments",
	"mate-attachments/",
}

// TestAttachmentsNotWrittenToWorkspace（G5）backend/internal 非测试源码不得出现"把附件写进工作区"
// 的字面量。fail-closed：扫不到文件即失败。
func TestAttachmentsNotWrittenToWorkspace(t *testing.T) {
	root := filepath.Join(backendRoot(t), "internal")
	scanned := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "archtest" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := stripLineComments(string(b))
		for _, bad := range forbiddenWorkspaceAttachmentStrings {
			if strings.Contains(src, bad) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s 出现工作区物化字面量 %q——附件只落 <MATE_HOME>/attachments，不写用户工作区（G5）", rel, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 %s: %v", root, err)
	}
	if scanned == 0 {
		t.Fatalf("internal 下没扫到任何非测试 .go：守卫失锚")
	}
}
