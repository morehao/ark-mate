package logging

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/golib/glog"

	// 注册 zap driver：glog 的工厂表是空的（driver 的匿名引入发生在 glog 包初始化之后），
	// 不引入则 InitLogger 找不到 LoggerType，日志全部落 nop。测试要真出日志必须带上它。
	_ "github.com/morehao/golib/glog/driver/zap"
)

// captureAt 把进程级 glog 出口临时切到一个 debug 级文件（t.TempDir 下），跑 fn，
// 关闭刷盘后回收所有 JSON 日志行。At 走的是包级 glog 函数，只能通过全局 logger 观测。
func captureAt(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	dir := t.TempDir()
	cfg := &glog.LogConfig{
		Service:    "at-test",
		Module:     "test",
		Level:      glog.DebugLevel,
		LoggerType: glog.LoggerTypeZap,
		Writers:    []glog.WriterConfig{{Type: glog.WriterFile, Level: glog.DebugLevel, Dir: dir}},
	}
	if err := glog.InitLogger(cfg); err != nil {
		t.Fatalf("初始化 glog 失败: %v", err)
	}
	fn()
	if err := glog.GetDefaultLogger().Close(); err != nil {
		t.Fatalf("关闭 glog 失败: %v", err)
	}

	var lines []map[string]any
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".log") {
			return nil
		}
		f, openErr := os.Open(p)
		if openErr != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				lines = append(lines, m)
			}
		}
		return nil
	})
	return lines
}

// TestAtDispatch 钉住六个档位的落点：前四个直通对应级别；Panic/Fatal 及非法值统一按 Error
// 记录并带原档位（`log_level`）——改造前它们会被静默降级成 Info。
func TestAtDispatch(t *testing.T) {
	cases := []struct {
		level     glog.Level
		wantLevel string // zap 输出里的 level 字段
		wantTag   string // 期望的 log_level 字段；空串表示不该出现
	}{
		{glog.DebugLevel, "debug", ""},
		{glog.InfoLevel, "info", ""},
		{glog.WarnLevel, "warn", ""},
		{glog.ErrorLevel, "error", ""},
		{glog.PanicLevel, "error", "panic"},
		{glog.FatalLevel, "error", "fatal"},
		{glog.Level("bogus"), "error", "bogus"},
	}

	lines := captureAt(t, func() {
		for _, c := range cases {
			At(context.Background(), c.level, "msg:"+string(c.level), "k", "v")
		}
	})

	got := make(map[string]map[string]any, len(lines))
	for _, m := range lines {
		if msg, _ := m["msg"].(string); msg != "" {
			got[msg] = m
		}
	}

	for _, c := range cases {
		msg := "msg:" + string(c.level)
		m, ok := got[msg]
		if !ok {
			t.Fatalf("档位 %q 没有产出日志行（共 %d 行）", c.level, len(lines))
		}
		if lv, _ := m["level"].(string); lv != c.wantLevel {
			t.Errorf("档位 %q：日志 level = %q，期望 %q", c.level, lv, c.wantLevel)
		}
		if c.wantTag == "" {
			if _, exists := m["log_level"]; exists {
				t.Errorf("档位 %q：不应带 log_level，实际 %v", c.level, m["log_level"])
			}
		} else if tag, _ := m["log_level"].(string); tag != c.wantTag {
			t.Errorf("档位 %q：log_level = %q，期望 %q", c.level, tag, c.wantTag)
		}
		if v, _ := m["k"].(string); v != "v" {
			t.Errorf("档位 %q：原始 kv 丢失，k=%v", c.level, m["k"])
		}
	}
}

// TestAtDoesNotMutateCallerSlice 钉住 default 分支的 kv 重拼：`At` 不能就地 append 写进
// 调用方切片。传一个 len<cap 的切片，容量区若被写脏即为回归。
func TestAtDoesNotMutateCallerSlice(t *testing.T) {
	kvs := make([]any, 2, 4)
	kvs[0], kvs[1] = "k", "v"

	captureAt(t, func() {
		At(context.Background(), glog.FatalLevel, "alias", kvs...)
	})

	full := kvs[:cap(kvs)]
	if full[2] != nil || full[3] != nil {
		t.Fatalf("At 就地 append 写进了调用方切片的容量区：%v", full[2:4])
	}
	if len(kvs) != 2 || kvs[0] != "k" || kvs[1] != "v" {
		t.Fatalf("At 改动了调用方切片：%v", kvs)
	}
}
