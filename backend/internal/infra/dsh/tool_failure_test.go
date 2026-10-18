package dsh

import "testing"

// TestToolFailureClassifiesSandboxSignatures 签名表的判据（S4 / v27）。
//
// 三个签名**都来自实测**（2026-10-16 探针）：被沙箱拒绝的那一帧 `status` 是 `completed`，
// 所以"为什么没成"只能从这段正文里读。这里钉三件事：
//
//	① 两种失败归到**两个不同的值**（处置相反：一个能引导升权，一个不能）；
//	② 顺序：runner 坏掉的文本里也可能出现 `sandbox` 字样，先认它；
//	③ **不认识的文本回空串**——不猜。写一个猜的原因比不写更糟：用户会照着它修一个不存在的问题。
func TestToolFailureClassifiesSandboxSignatures(t *testing.T) {
	c := ToolFailure{}

	// ① 被沙箱拒绝（实测原文）。
	reason, code := c.ClassifyToolFailure("[sandbox: file access denied under workspace-write mode]\n若需写入请申请更高权限")
	if reason != FailureSandboxDenied || code != CodeSandboxWriteDenied {
		t.Fatalf("沙箱拒绝应归成 (%q,%q)，got (%q,%q)", FailureSandboxDenied, CodeSandboxWriteDenied, reason, code)
	}

	// ② 沙箱后端坏了（实测原文）——**不能**归成"可以升权"那一档。
	for _, text := range []string{
		`Error: sandbox mode "workspace-write" is not available: no sandbox backend is usable on this host`,
		"Runner failure: spawn /usr/bin/bwrap ENOENT",
	} {
		reason, code := c.ClassifyToolFailure(text)
		if reason != FailureSandboxUnavailable {
			t.Fatalf("runner 失败应归成 %q，got %q（文本：%s）", FailureSandboxUnavailable, reason, text)
		}
		if code != "" {
			t.Fatalf("runner 失败没有更细的码（got %q）——别编一个", code)
		}
	}

	// ③ 不认识 → 空（不猜）。空文本同样。
	for _, text := range []string{"", "exit status 1", "file access denied"} {
		if reason, code := c.ClassifyToolFailure(text); reason != "" || code != "" {
			t.Fatalf("不认识的文本应回空（%q → %q/%q）：写一个猜的原因会让用户去修一个不存在的问题",
				text, reason, code)
		}
	}
}

// TestToolFailureOrderMatters 顺序判据：同一条文本里既有 `sandbox` 又有 runner 字样时，
// 必须归到**不能升权**那一档（先认坏掉的后端）。
func TestToolFailureOrderMatters(t *testing.T) {
	c := ToolFailure{}
	text := `sandbox mode "workspace-write" … no sandbox backend is usable on this host`
	if reason, _ := c.ClassifyToolFailure(text); reason != FailureSandboxUnavailable {
		t.Fatalf("同时命中两类签名时应归到 %q（不能引导升权），got %q", FailureSandboxUnavailable, reason)
	}
}
