package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionruntimeResolvesAttachmentsViaPortOnly（G3）`internal/sessionruntime` 的**生产源码**
// 不得 import `internal/persistence/dao`——附件解析必须经 `domain/session.AttachmentResolver` 端口。
//
// 挡的是「绕过端口直接查库」：`attachments` 的读法一旦在 sessionruntime 里直接走 dao，就把
// 「谁拥有附件解析」这件事摊到了两层，而"附件归属/越权/门禁"的 fail-closed 判据会跟着分叉。
// （sessionruntime 在层表上确实**可以** import dao，所以这条要单列一条判据——层表挡不住它。）
//
// fail-closed：扫不到任何 .go 即失败（路径/包被改名时让守卫红，而不是静默空转）。
func TestSessionruntimeResolvesAttachmentsViaPortOnly(t *testing.T) {
	dir := filepath.Join(backendRoot(t), "internal", "sessionruntime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 %s 失败：%v（守卫失锚）", dir, err)
	}
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		scanned++
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("读 %s: %v", e.Name(), rerr)
		}
		src := stripLineComments(string(b))
		if strings.Contains(src, `"github.com/morehao/ark-mate/internal/persistence/dao"`) {
			t.Errorf("sessionruntime/%s import 了 persistence/dao——附件解析必须经 AttachmentResolver 端口（G3），不得直接查库", e.Name())
		}
	}
	if scanned == 0 {
		t.Fatalf("%s 下没扫到任何非测试 .go：守卫失锚", dir)
	}
}
