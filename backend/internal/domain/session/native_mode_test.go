package session

import "testing"

// TestToSessionRespOfCarriesNativeMode 读面必须回显会话钉住的原生权限模式（v29）：
// 界面据此显示真实档位，而不是硬编码"默认权限"（design §3.3 P0）。空值照样搬运（'' = 跟随默认）。
func TestToSessionRespOfCarriesNativeMode(t *testing.T) {
	pl := NewPlane()

	cases := []struct{ mode string }{
		{"read-only"},
		{"workspace-write"},
		{"danger-full-access"},
		{""},
	}
	for _, c := range cases {
		item := pl.ToSessionRespOf(&SessionRec{ID: "ses_x", NativeMode: c.mode})
		if item.NativeMode != c.mode {
			t.Fatalf("native_mode 回显丢失：want %q got %q", c.mode, item.NativeMode)
		}
	}
}
