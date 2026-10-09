package code

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/acl"
)

// TestFromACLCarriesStderrTail 底座起不来时，信封文案要带 stderr 尾巴（诊断可用性）。
func TestFromACLCarriesStderrTail(t *testing.T) {
	ae := acl.NewError(acl.ClassConnection, "process_exited", "agent 进程异常退出", nil)
	ae.StderrTail = []string{"dsh: .env sets DSH_VERSION", "  exit 3  "}
	got, ok := fromACL(ae)
	if !ok {
		t.Fatal("fromACL 应命中 acl.Error")
	}
	if got.Code != BadGateway {
		t.Fatalf("ClassConnection 应映射为 %d，got %d", BadGateway, got.Code)
	}
	want := "agent 进程异常退出；底座 stderr 尾部: dsh: .env sets DSH_VERSION | exit 3"
	if got.Msg != want {
		t.Fatalf("文案不符:\n got %q\nwant %q", got.Msg, want)
	}
	// 没有尾巴时不得凭空多出一段（既有文案保持逐字不变）。
	plain := acl.NewError(acl.ClassRequest, "invalid_params", "参数不对", nil)
	if p, _ := fromACL(plain); p.Msg != "参数不对" {
		t.Fatalf("无尾巴时文案应原样: %q", p.Msg)
	}
}

// TestStderrMsgSuffix 信封尾巴的量纲：5 行上限、逐行按 rune 截断、空行跳过、空输入无输出。
func TestStderrMsgSuffix(t *testing.T) {
	if got := stderrMsgSuffix(nil); got != "" {
		t.Fatalf("无尾巴应返回空串: %q", got)
	}
	if got := stderrMsgSuffix([]string{"   ", ""}); got != "" {
		t.Fatalf("全是空行应返回空串: %q", got)
	}
	got := stderrMsgSuffix([]string{"a", "b", "c", "d", "e", "f", "g"})
	if want := "底座 stderr 尾部: c | d | e | f | g"; got != want {
		t.Fatalf("应只保留最后 5 行:\n got %q\nwant %q", got, want)
	}
	// 超长单行按字符截断（不是按字节，否则多字节会被切坏）。
	long := strings.Repeat("诊", 400)
	one := stderrMsgSuffix([]string{long})
	if !strings.HasSuffix(one, "…") {
		t.Fatalf("超长行应带省略号: %q", one)
	}
	if n := len([]rune(strings.TrimPrefix(one, "底座 stderr 尾部: "))); n != stderrMsgRunes+1 {
		t.Fatalf("超长行应截到 %d 字符（含省略号），got %d", stderrMsgRunes+1, n)
	}
	if !strings.HasPrefix(one, "底座 stderr 尾部: 诊") {
		t.Fatalf("截断不该破坏前缀: %q", one)
	}
}
