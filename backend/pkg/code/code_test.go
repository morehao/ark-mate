package code_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/golib/gerror"
)

// TestFromErrorMapping 领域错误（acl 分类）→ 对外业务码映射矩阵。
//
// 这条矩阵原先在 internal/manager 里测（当时映射表是 Service.restErr 的私有实现）。
// 映射表收归 pkg/code 后随之搬来：它测的是"码怎么定"，与 manager 的装配无关，
// 放在这里也不再需要拉起整个 manager 测试脚手架。
func TestFromErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want int
	}{
		{"agent_not_found", acl.NewError(acl.ClassRequest, "agent_not_found", "x", nil), code.NotFound},
		{"conversation_not_found", acl.NewError(acl.ClassRequest, "conversation_not_found", "x", nil), code.NotFound},
		{"runtime_not_found", acl.NewError(acl.ClassRequest, "runtime_not_found", "x", nil), code.NotFound},
		{"mcp_not_found", acl.NewError(acl.ClassRequest, "mcp_not_found", "x", nil), code.NotFound},
		{"provider_not_found", acl.NewError(acl.ClassRequest, "provider_not_found", "x", nil), code.NotFound},
		{"request_other", acl.NewError(acl.ClassRequest, "agent_required", "x", nil), code.InvalidRequest},
		{"limit", acl.NewError(acl.ClassLimit, "turn_in_progress", "x", nil), code.Conflict},
		{"access", acl.NewError(acl.ClassAccess, "auth_required", "x", nil), code.Unauthorized},
		{"unsupported", acl.NewError(acl.ClassUnsupported, "resume_unsupported", "x", nil), code.NotSupported},
		{"service", acl.NewError(acl.ClassService, "orchestrator_unavailable", "x", nil), code.BadGateway},
		{"connection", acl.NewError(acl.ClassConnection, "connection_lost", "x", nil), code.BadGateway},
		{"unknown_class", acl.NewError(acl.ClassUnknown, "weird", "x", nil), code.UpstreamFailed},
		{"plain_error", errors.New("boom"), code.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ge gerror.Error
			if !errors.As(code.FromError(tc.in), &ge) || ge.Code != tc.want {
				t.Fatalf("映射不符: %v → %d (want %d)", tc.in, ge.Code, tc.want)
			}
		})
	}

	// details 为空 → msg 回落为 acl 稳定 code。
	var ge gerror.Error
	_ = errors.As(code.FromError(acl.NewError(acl.ClassLimit, "busy", "", nil)), &ge)
	if ge.Msg != "busy" {
		t.Fatalf("空 details 应回落 code: %+v", ge)
	}
}

// TestFromErrorNeverLosesCode 出口错误必须携带业务码。
//
// 这是 P5 的核心承诺：gincontext.buildErrorResponse 对不带 gerror.Error 的错误
// 填 `code: -1`，客户端拿不到可判定的码。FromError 的职责就是让这条路走不通——
// 无论喂进来什么（裸 error、包装过的、acl 分类的、已经带码的），出去的一律带码。
func TestFromErrorNeverLosesCode(t *testing.T) {
	inputs := []error{
		errors.New("裸错误"),
		acl.NewError(acl.ClassRequest, "agent_not_found", "x", nil),
		acl.NewError(acl.ClassUnknown, "weird", "", nil),
		code.Error(code.Conflict, "已带码"),
		code.Errorf(code.InvalidParam, "带参 %d", 1),
	}
	for _, in := range inputs {
		var ge gerror.Error
		if !errors.As(code.FromError(in), &ge) {
			t.Errorf("FromError(%v) 未携带业务码 → 客户端会看到 code:-1", in)
			continue
		}
		if ge.Code == -1 {
			t.Errorf("FromError(%v) 落成 -1，等于没映射", in)
		}
		if ge.Msg == "" {
			t.Errorf("FromError(%v) 的 msg 为空 → 前端渲染空错误气泡", in)
		}
	}

	// nil 原样返回，便于 `return code.FromError(err)` 直写。
	if got := code.FromError(nil); got != nil {
		t.Errorf("FromError(nil) 应为 nil，实际 %v", got)
	}
}

// TestFromErrorPassesCodedThrough 已带码的错误必须原样透传。
//
// 否则二次映射会把 40401 压成 50000（Code 是 int，只要 errors.As 命中就得原样返回）。
func TestFromErrorPassesCodedThrough(t *testing.T) {
	in := code.Error(code.NotFound, "未知会话")
	out := code.FromError(in)
	var ge gerror.Error
	if !errors.As(out, &ge) || ge.Code != code.NotFound || ge.Msg != "未知会话" {
		t.Fatalf("已带码错误被改写: %v → %+v", in, ge)
	}
	// 包装一层也要能认出来（errors.As 会穿透 %w）。
	wrapped := errors.New("outer: " + in.Error())
	if code.FromError(wrapped) == wrapped {
		t.Fatal("未包装错误不应原样返回")
	}
}

// TestRegistrySelfConsistent 注册表自洽：每个码都有默认文案，且码值唯一。
func TestRegistrySelfConsistent(t *testing.T) {
	seen := map[int]string{}
	for _, c := range code.Codes() {
		if prev, dup := seen[c]; dup {
			t.Errorf("码 %d 重复登记（%s / ...）", c, prev)
		}
		seen[c] = "declared"
		if code.Message(c) == "" {
			t.Errorf("码 %d 没有默认文案", c)
		}
	}
	if code.Message(code.OK) == "" {
		t.Error("成功码也要有文案")
	}
	// 未登记的码返回空串（调用方据此判断"这不是我的码"）。
	if code.Message(999999) != "" {
		t.Error("未登记码应返回空文案")
	}
}

// TestReadmeCodesMatchRegistry README「业务错误码」表必须与 pkg/code 一致。
//
// 与 TestReadmeRoutesMatchRouter 同一个思路：对外契约写在 README 里，代码是事实源，
// 两边靠测试钉住。加码忘记写文档、或者文档里留着已删的码，都会在这里红。
func TestReadmeCodesMatchRegistry(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("读 README: %v", err)
	}
	section := codeTableSection(t, string(raw))

	row := regexp.MustCompile("^\\|\\s*`(\\d+)`\\s*\\|")
	doc := map[int]bool{}
	for _, line := range strings.Split(section, "\n") {
		m := row.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("解析文档里的码 %q: %v", m[1], err)
		}
		doc[n] = true
	}
	if len(doc) == 0 {
		t.Fatal("README 未解析到任何错误码（表格格式变了？）")
	}

	reg := map[int]bool{}
	for _, c := range code.Codes() {
		reg[c] = true
		if !doc[c] {
			t.Errorf("码 %d（%s）已在 pkg/code 声明但 README 未记录", c, code.Message(c))
		}
	}
	for c := range doc {
		if !reg[c] {
			t.Errorf("README 记录了口令码 %d，但 pkg/code 里没有", c)
		}
	}
}

// TestFrontendBizCodesMatchRegistry 前端 BizCode 常量表必须与 pkg/code 双向一致。
//
// 第三个同步点：码同时写在 pkg/code（事实源）、README（文档）、前端 response.ts（消费方）。
// 前两者由 TestReadmeCodesMatchRegistry 钉住，前端此前只靠人工——而人工同步正是漂移的
// 温床（前端原先只有 Success/Unauthorized 两个，其余 7 个码只能靠魔数比较）。
// 用 Go 读 TypeScript 源码是刻意的：不引入前端测试框架，却让"改码忘记同步前端"当场变红。
func TestFrontendBizCodesMatchRegistry(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "frontend", "packages", "types", "src", "response.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读前端 response.ts: %v", err)
	}
	src := string(raw)

	// 只取 `export const BizCode = { ... } as const` 这一块，避免把文件里别的数字一起抓进来。
	start := strings.Index(src, "export const BizCode = {")
	if start < 0 {
		t.Fatal("前端 response.ts 里找不到 `export const BizCode = {`（重命名了？守卫需要同步）")
	}
	end := strings.Index(src[start:], "} as const")
	if end < 0 {
		t.Fatal("前端 BizCode 块缺少结尾 `} as const`")
	}
	block := src[start : start+end]

	// 匹配 `Name: 12345`（允许行内注释）。名字只用于报错信息，比对以数值为准。
	entry := regexp.MustCompile(`(?m)^\s*([A-Za-z][A-Za-z0-9_]*)\s*:\s*(\d+)\s*,`)
	fe := map[int]string{}
	for _, m := range entry.FindAllStringSubmatch(block, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("解析前端码 %q: %v", m[2], err)
		}
		if prev, dup := fe[n]; dup {
			t.Errorf("前端 BizCode 里 %d 出现两次（%s 与 %s）", n, prev, m[1])
		}
		fe[n] = m[1]
	}
	if len(fe) == 0 {
		t.Fatal("前端 BizCode 未解析到任何常量（格式变了？）")
	}

	reg := map[int]bool{}
	for _, c := range code.Codes() {
		reg[c] = true
		if _, ok := fe[c]; !ok {
			t.Errorf("码 %d（%s）在 pkg/code 已声明但前端 BizCode 缺失——前端只能拿魔数比较", c, code.Message(c))
		}
	}
	for n, name := range fe {
		if !reg[n] {
			t.Errorf("前端 BizCode.%s = %d，但 pkg/code 里没有这个码", name, n)
		}
	}
}

// codeTableSection 截取 README「### 业务错误码」到下一个二级/三级标题之间的正文。
func codeTableSection(t *testing.T, readme string) string {
	t.Helper()
	lines := strings.Split(readme, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "### 业务错误码" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("README 缺少「### 业务错误码」小节")
	}
	for j := start + 1; j < len(lines); j++ {
		if strings.HasPrefix(lines[j], "## ") || strings.HasPrefix(lines[j], "### ") {
			return strings.Join(lines[start:j], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// repoRoot 从 backend/pkg/code 上溯到仓库根（含 README.md 的那一层）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("未能定位仓库根（未找到 README.md）")
	return ""
}
