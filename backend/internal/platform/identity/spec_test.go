package identity

import "testing"

// TestFingerprintTracksCopy 指纹必须跟着**文案与版本**走，且与"当前进程/时间"无关。
//
// 两条方向都重要：改一个字要变（否则"改了身份"不会触发重交付），重复渲染不能变
// （否则每次启动都会重写文件、重装参数）。
func TestFingerprintTracksCopy(t *testing.T) {
	base := Spec{Version: "v1", Name: "ark-mate", SystemPrompt: "你是 ark-mate。", UserContext: "<p>身份</p>"}
	if base.Fingerprint() != base.Fingerprint() {
		t.Fatal("同一份 Spec 的指纹不稳定")
	}
	changed := base
	changed.SystemPrompt += "。"
	if changed.Fingerprint() == base.Fingerprint() {
		t.Fatal("改了系统提示正文，指纹却没变")
	}
	versioned := base
	versioned.Version = "v2"
	if versioned.Fingerprint() == base.Fingerprint() {
		t.Fatal("改了身份版本，指纹却没变")
	}
}

// TestSpecFaceFlags 两个面各自的判据（装配层据此决定"这次有没有东西可交付"）。
func TestSpecFaceFlags(t *testing.T) {
	if (Spec{}).Enabled() || (Spec{SystemPrompt: "  "}).UsesSystemPrompt() {
		t.Fatal("空白内容不算声明了身份")
	}
	if !(Spec{UserContext: "x"}).UsesUserContext() {
		t.Fatal("有首轮块时应报告使用首轮块面")
	}
	if !(Spec{SystemPrompt: "x"}).Enabled() {
		t.Fatal("有系统提示时应报告身份可用")
	}
}

// TestParseModeIsTheOnlyVocabulary 取值域只有一份：空 = auto，大小写/空白归一，
// 非法值报错（不静默回落——回落会让"我以为关掉了身份"与"还在交付"长得一模一样）。
func TestParseModeIsTheOnlyVocabulary(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Mode
	}{
		{"", ModeAuto},
		{"auto", ModeAuto},
		{" AUTO ", ModeAuto},
		{"System", ModeSystem},
		{"user", ModeUser},
		{"off", ModeOff},
	} {
		got, err := ParseMode(c.in)
		if err != nil || got != c.want {
			t.Fatalf("ParseMode(%q) = %q, %v（期望 %q）", c.in, got, err, c.want)
		}
	}
	if _, err := ParseMode("disabled"); err == nil {
		t.Fatal("非法取值必须报错")
	}
	if len(ValidModes) != len(Modes) {
		t.Fatal("ValidModes 与 Modes 不同源")
	}
}

// TestModeApplySystemNeverFallsBack `system` 模式刻意**不保留**首轮块：
// 它是用来验证主路径的，静默回退会让"系统提示没生效"这件事看不出来。
func TestModeApplySystemNeverFallsBack(t *testing.T) {
	full := Spec{SystemPrompt: "S", UserContext: "U"}
	if got := ModeSystem.Apply(full); got.UserContext != "" || got.SystemPrompt != "S" {
		t.Fatalf("system 模式应只留系统提示，得到 %+v", got)
	}
	if got := ModeUser.Apply(full); got.SystemPrompt != "" || got.UserContext != "U" {
		t.Fatalf("user 模式应只留首轮块，得到 %+v", got)
	}
	if got := ModeOff.Apply(full); got.Enabled() {
		t.Fatalf("off 模式应两个面都清空，得到 %+v", got)
	}
	if got := ModeAuto.Apply(full); got.SystemPrompt != "S" || got.UserContext != "U" {
		t.Fatalf("auto 模式应原样保留两个面，得到 %+v", got)
	}
}
