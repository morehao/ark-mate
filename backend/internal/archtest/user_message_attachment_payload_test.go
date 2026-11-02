package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestUserMessageAttachmentPayloadIsMetadataOnly（G6）`session.user_message.attachments[]` 的载荷
// 元素类型（`session.AttachmentMeta`）**只含元数据字段**，绝无字节/base64/本地路径。
//
// 挡的是「把图片字节或本机绝对路径灌进持久事件」：transcript 会被撑爆，且本机路径泄漏进历史。
// 判据落在 `AttachmentMeta` 的字段集与 json 键上（那是载荷形状的唯一来源）。
func TestUserMessageAttachmentPayloadIsMetadataOnly(t *testing.T) {
	path := filepath.Join(backendRoot(t), "internal", "domain", "session", "attachment_resolver.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", path, err)
	}
	src := stripLineComments(string(raw))
	body, ok := attachmentMetaStructBody(src, "AttachmentMeta")
	if !ok {
		t.Fatalf("解析不到 type AttachmentMeta struct：载荷类型被改名/挪走？（fail-closed）")
	}
	// 取 json 键。
	keys := attachmentMetaJSONKeys(body)
	sort.Strings(keys)
	want := []string{"id", "kind", "mime_type", "name", "size_bytes"}
	sort.Strings(want)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("session.user_message.attachments[] 的键集合变了（G6）：\n got  %v\n want %v（只元数据）",
			keys, want)
	}
	// 反向：字段里不得出现字节/内容/路径类字段。
	for _, bad := range []string{"Data", "ReadPath", "Content", "Base64", "Path", "StorageURI"} {
		if regexp.MustCompile(`(?m)^\s*` + bad + `\s+`).MatchString(body) {
			t.Errorf("AttachmentMeta 出现字段 %q——它是**元数据**载荷，不得携带字节/路径（G6）", bad)
		}
	}
}

// attachmentMetaStructBody 取 `type <name> struct { ... }` 的花括号内文本。
func attachmentMetaStructBody(src, name string) (string, bool) {
	re := regexp.MustCompile(`(?s)type ` + regexp.QuoteMeta(name) + ` struct \{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// attachmentMetaJSONKeys 从结构体成员体里取 `json:"...` 的第一个键（去 omitempty）。
func attachmentMetaJSONKeys(body string) []string {
	re := regexp.MustCompile("json:\"([a-zA-Z0-9_]+)")
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}
