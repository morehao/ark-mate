package objtime

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMilliZeroIsZero(t *testing.T) {
	if got := Milli(time.Time{}); got != 0 {
		t.Fatalf("零值时间必须落成 0（不是年份 1 的负数毫秒），got %d", got)
	}
	if got := Millis(nil); got != 0 {
		t.Fatalf("nil 指针必须落成 0，got %d", got)
	}
}

func TestMilliUnixMillis(t *testing.T) {
	at := time.Date(2026, 11, 5, 10, 42, 21, 123_000_000, time.UTC)
	if got := Milli(at); got != at.UnixMilli() {
		t.Fatalf("Milli = %d，期望 %d", got, at.UnixMilli())
	}
	if got := Millis(&at); got != at.UnixMilli() {
		t.Fatalf("Millis = %d，期望 %d", got, at.UnixMilli())
	}
}

// TestParseToleratesLegacyISO 读侧兼容：口径切换前的事件载荷仍是 RFC3339。
func TestParseToleratesLegacyISO(t *testing.T) {
	at := time.Date(2026, 11, 5, 10, 42, 21, 0, time.UTC)
	cases := []struct {
		name string
		in   any
		want int64
	}{
		{"RFC3339Nano", "2026-11-05T10:42:21.123Z", at.Add(123 * time.Millisecond).UnixMilli()},
		{"RFC3339", "2026-11-05T10:42:21Z", at.UnixMilli()},
		{"int64", int64(1234), 1234},
		{"float64", float64(1234), 1234},
		{"json.Number", json.Number("1234"), 1234},
		{"十进制字符串", "1234", 1234},
		{"nil", nil, 0},
		{"空串", "", 0},
		{"非法串", "not-a-time", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.in); got != tc.want {
				t.Fatalf("Parse(%v) = %d，期望 %d", tc.in, got, tc.want)
			}
		})
	}
}
