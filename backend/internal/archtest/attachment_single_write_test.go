package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// grepGoFiles 递归扫描 root 下所有 .go 文件，返回**剥注释后**包含 needle 的文件绝对路径。
func grepGoFiles(t *testing.T, root, needle string) []string {
	t.Helper()
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// 跳过 archtest 自身（它的判据字面量就写着这些 needle）。
			if info.Name() == "archtest" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(stripLineComments(string(b)), needle) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 %s 失败：%v", root, err)
	}
	return hits
}

// relTo 返回 path 相对 root 的路径（用于可读报错）。
func relTo(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}


// TestAttachmentSingleWriteEntry（G2）`attachments` 表的写入只有一条业务入口。
//
// 判据分两层（都在剥注释后的源码上扫）：
//  1. **DAO 层**：`s.attachments.Insert(` 只许出现在 `internal/persistence/store/attachments.go`
//     （store.InsertAttachment 是唯一写入原语）；别处再写一次 = 绕开唯一入口。
//  2. **用例层**：`InsertAttachment(` 的调用方只许是 `internal/service/svcattachment`（Upload 是唯一
//     上传入口）；别处（controller / 其它 svc / whatever）调它 = 出现第二写入点。
//
// 挡的是「上传入口偷偷多一个」：多一个直写点不会让任何行为测试变红，但「谁写了 attachments」
// 从此不止一处——孤儿清扫、去重、doctor 对账都得跟着猜。fail-closed：两个锚点都解析不到即失败。
func TestAttachmentSingleWriteEntry(t *testing.T) {
	root := backendRoot(t)

	// 1) DAO 原语只在一处。
	daoInsert := "s.attachments.Insert("
	daoHits := grepGoFiles(t, filepath.Join(root, "internal"), daoInsert)
	wantDAO := map[string]bool{
		filepath.Join(root, "internal", "persistence", "store", "attachments.go"): true,
	}
	for _, f := range daoHits {
		if !wantDAO[f] {
			t.Errorf("%s 直接调用了 `%s`——attachments 的写入原语只许在 store/attachments.go（唯一入口）", relTo(t, root, f), daoInsert)
		}
	}
	if len(daoHits) == 0 {
		t.Fatalf("在 internal/ 下找不到 `%s`：写入原语被改名/挪走了？（fail-closed）", daoInsert)
	}

	// 2) InsertAttachment 的调用方只许是 svcattachment。
	call := "InsertAttachment("
	callHits := grepGoFiles(t, filepath.Join(root, "internal"), call)
	// 定义处（func (s *Store) InsertAttachment）与接口声明（AttachmentStore 的 InsertAttachment）不算调用方。
	wantCallers := filepath.Join(root, "internal", "service", "svcattachment")
	var callers []string
	for _, f := range callHits {
		base := filepath.Base(f)
		if base == "attachments.go" && strings.Contains(f, "persistence/store") {
			continue // 定义处
		}
		if strings.Contains(f, "service/svcattachment") {
			callers = append(callers, f)
			continue
		}
		// 接口声明里的 `InsertAttachment(` 只出现在类型定义，不在别处。
		if isAttachmentStoreInterfaceDecl(t, f) {
			continue
		}
		t.Errorf("%s 调用了 `%s`——attachments 的唯一写入入口是 svcattachment.Upload，第二写入点会让「谁写了 attachments」失锚",
			relTo(t, root, f), call)
	}
	if len(callers) == 0 {
		t.Fatalf("找不到 `%s` 在 %s 下的调用方：上传入口被挪走/改名了？（fail-closed）", call, relTo(t, root, wantCallers))
	}
}

// isAttachmentStoreInterfaceDecl 判断该文件是否只是「接口声明 InsertAttachment」（
// svcattachment 的 AttachmentStore 接口 + assembly 的 nil-safe 空实现）。
func isAttachmentStoreInterfaceDecl(t *testing.T, path string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	src := stripLineComments(string(b))
	// 接口方法是 `InsertAttachment(ctx ...) error`（无接收者前缀、以 InsertAttachment( 起行）。
	// 空实现是 `func (unsupportedAttachmentStore) InsertAttachment(...)`。
	return strings.Contains(src, "InsertAttachment(ctx context.Context") ||
		strings.Contains(src, "func (unsupportedAttachmentStore) InsertAttachment")
}
