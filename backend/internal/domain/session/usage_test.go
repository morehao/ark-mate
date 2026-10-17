package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/gerror"
)

// TestParseUsageZone 客户端时区偏移：空 = UTC；±14h 之外与非整数显式拒绝。
//
// 拒绝而不是"回落 UTC"是刻意的：回落会让一个打错参数的调用方拿到一份看起来正常的
// 全量统计，而偏差正是这条要修的东西。
func TestParseUsageZone(t *testing.T) {
	if off, loc, err := parseUsageZone(""); err != nil || off != 0 || loc != time.UTC {
		t.Fatalf("空 tz_offset 应为 (0, UTC)，得到 (%d, %v, %v)", off, loc, err)
	}
	if off, loc, err := parseUsageZone("-480"); err != nil || off != -480 || loc == nil {
		t.Fatalf("-480 应通过，得到 (%d, %v, %v)", off, loc, err)
	}
	for _, bad := range []string{"abc", "0.5", "900", "-900", " "} {
		_, _, err := parseUsageZone(bad)
		if err == nil {
			t.Fatalf("tz_offset=%q 应被拒绝", bad)
		}
		var ce gerror.Error
		if !errors.As(code.FromError(err), &ce) || ce.Code != code.InvalidRequest ||
			!strings.Contains(ce.Msg, "invalid_usage_tz_offset") {
			t.Fatalf("tz_offset=%q 应为 40001 invalid_usage_tz_offset，得到 %+v", bad, code.FromError(err))
		}
	}
}

// TestParseUsageTimeUsesClientZone 日期形式按客户端时区解释，RFC3339 按自己的偏移。
//
// 这是 O8 的那条「跨时区 ≤1 天偏差」的机器判据：UTC+8 的「9 月 1 日」= 2026-08-31T16:00Z。
func TestParseUsageTimeUsesClientZone(t *testing.T) {
	east8 := time.FixedZone("client", 8*3600)
	got, err := parseUsageTime("2026-09-01", east8)
	if err != nil {
		t.Fatalf("parseUsageTime: %v", err)
	}
	if want := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("UTC+8 的 2026-09-01 应为 %s，得到 %s", want, got)
	}

	// 空串 = 不限；RFC3339 自带偏移（不受 loc 影响）。
	if got, err := parseUsageTime("", east8); err != nil || !got.IsZero() {
		t.Fatalf("空串应为零值: %v %v", got, err)
	}
	rfc, err := parseUsageTime("2026-09-01T00:00:00Z", east8)
	if err != nil {
		t.Fatalf("RFC3339: %v", err)
	}
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !rfc.Equal(want) {
		t.Fatalf("RFC3339 应按自身偏移解析，得到 %s", rfc)
	}
}
