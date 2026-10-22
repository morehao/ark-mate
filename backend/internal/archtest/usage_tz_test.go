package archtest

// 本文件钉住 **用量统计的日历日属于客户端时区**（原 O8 的时区那半，2026-10-17 修；O8 已移入 C28）。
//
// 为什么需要一条静态守卫：这条链跨了四层（页面算偏移 → api 拼 query → 控制器搬运 →
// 用例层解析 → 存储层分桶），而**任何一层丢掉 tz_offset 都不会报错**：后端按 UTC 分桶，
// 界面照常渲染，只是 UTC+8 用户每天 00:00–08:00 的轮次被算进前一天。单测只能钉住
// Go 那两层的当下行为，钉不住"前端没传"这种沉默；而这条边恰恰是上一版缺的那一半。
//
// 判据五段，都 fail-closed（文件/片段解析不到即失败）。

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestUsageDayBucketsCarryClientZone 见文件头。
func TestUsageDayBucketsCarryClientZone(t *testing.T) {
	root := backendRoot(t)
	frontend := filepath.Join(root, "..", "frontend")

	// ① 存储层：分桶用 q.Location（UTC 固定写死 = 旧行为）。
	storeSrc := stripGoComments(readFileOrFatal(t, filepath.Join(root, "internal", "persistence", "store", "usage.go")))
	if !strings.Contains(storeSrc, "r.startedAt.In(loc)") ||
		!strings.Contains(storeSrc, `Format("2006-01-02")`) {
		t.Fatalf("store/usage.go 的 day 分桶没有按 Location 投影：`r.startedAt.In(loc)` + `Format(\"2006-01-02\")` 必须同时出现")
	}
	if strings.Contains(storeSrc, "r.startedAt.UTC().Format(\"2006-01-02\")") {
		t.Fatalf("store/usage.go 仍按 UTC 分桶：UTC+8 用户每天 00:00–08:00 的轮次会被算进前一天")
	}

	// ② 用例层：必须解析 tz_offset 并把它交给存储层（只解析不传 = 同一个缺口）。
	domainSrc := stripGoComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "usage.go")))
	// 压掉缩进与对齐空格再比：`TZOffset: tz` 这类字面量会被 gofmt 按列对齐，直接 contains 很脆。
	domainFlat := strings.Join(strings.Fields(domainSrc), " ")
	for _, want := range []string{"parseUsageZone(q.TZOffset)", "Location: loc", "TZOffset: tz,"} {
		if !strings.Contains(domainFlat, want) {
			t.Fatalf("domain/session/usage.go 缺少 %q：tz_offset 必须被解析、用于日期解析、并回显到响应", want)
		}
	}

	// ③ 控制器：读 `tz_offset` 查询键（gin 对不认识的键一个字都不说）。
	ctrSrc := stripGoComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrusage", "usage.go")))
	if !strings.Contains(ctrSrc, `c.Query("tz_offset")`) {
		t.Fatalf("ctrusage 没有读 `tz_offset`：前端传了也会被丢掉，后端静默按 UTC 分桶")
	}

	// ④ 前端：类型声明 + api 拼参 + 页面真的算偏移。
	typesSrc := stripTSComments(readFileOrFatal(t, filepath.Join(frontend, "packages", "types", "src", "usage.ts")))
	if !strings.Contains(typesSrc, "tz_offset?: number") {
		t.Fatalf("types/usage.ts 没有声明 `tz_offset?: number`：拼参与页面传值都没有类型依据")
	}
	apiSrc := stripTSComments(readFileOrFatal(t, filepath.Join(frontend, "packages", "api", "src", "resources", "mate.ts")))
	if !strings.Contains(apiSrc, "p.set('tz_offset'") {
		t.Fatalf("api 的 usageSummary 没有拼 `tz_offset`：页面传了也只停在调用参数里")
	}
	pageSrc := stripTSComments(readFileOrFatal(t, filepath.Join(frontend, "packages", "console", "src", "pages", "UsagePage.tsx")))
	if !strings.Contains(pageSrc, "tz_offset: -new Date().getTimezoneOffset()") {
		t.Fatalf("UsagePage 没有把本机时区交给后端（`-new Date().getTimezoneOffset()`）：日期参数与 day 桶会退回 UTC")
	}
}
