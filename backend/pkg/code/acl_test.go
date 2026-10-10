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

// TestFromACLProviderModelCodesFrozen 钉住「设置 · 模型」（v11）新增语义的**分类 → 对外码**映射。
//
// 这张表的每一行都是"用户下一步该做什么"的判据，所以它必须被逐条写死而不是靠 Class 默认值兜底：
//
//	provider_exists / provider_in_use / model_exists → 40901（时机/状态冲突，换 id 或解开引用）
//	model_is_default                                 → 40501（当前形态不支持这个动作，先指定另一个默认）
//	model_not_found / provider_not_found             → 40401（平台不会"顺便新建"）
//	config_write_failed                              → 50001（服务侧失败；**不是** 50201"上游连接失败"，
//	                                                   否则排障方向会被带到底座进程/网络上）
func TestFromACLProviderModelCodesFrozen(t *testing.T) {
	cases := []struct {
		name  string
		class acl.Class
		want  int
	}{
		{"provider_exists", acl.ClassLimit, Conflict},
		{"provider_in_use", acl.ClassLimit, Conflict},
		{"model_exists", acl.ClassLimit, Conflict},
		{"model_is_default", acl.ClassUnsupported, NotSupported},
		{"model_not_found", acl.ClassRequest, NotFound},
		{"provider_not_found", acl.ClassRequest, NotFound},
		{"invalid_provider_id", acl.ClassRequest, InvalidRequest},
		{"definition_changed", acl.ClassLimit, Conflict},
		{"config_write_failed", acl.ClassService, UpstreamFailed},
	}
	for _, tc := range cases {
		got, ok := fromACL(acl.NewError(tc.class, tc.name, "细节", nil))
		if !ok {
			t.Fatalf("%s: fromACL 应命中", tc.name)
		}
		if got.Code != tc.want {
			t.Errorf("%s（Class=%s）应映射为 %d，got %d", tc.name, tc.class, tc.want, got.Code)
		}
	}
	// classCodeOverrides 只该改**一个**码，别把它当成"绕过 Class 语义"的通用后门。
	if len(classCodeOverrides) != 1 {
		t.Fatalf("例外表新增了条目（%v）：每一条都要有"+"这里写的那个量级"+"的理由", classCodeOverrides)
	}
	// 未登记的名字仍按 Class 兜底（config_write_failed 之外的服务侧错误仍是 50201）。
	if got, _ := fromACL(acl.NewError(acl.ClassService, "backend_boom", "x", nil)); got.Code != BadGateway {
		t.Fatalf("ClassService 的默认映射应仍是 %d，got %d", BadGateway, got.Code)
	}
}
