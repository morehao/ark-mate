package testutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/golib/glog"
)

// CaptureLogs 把 glog 的**全局**出口重定向到本测试的临时目录，返回"读当前日志文本"的函数。
//
// 为什么需要它：迁到 glog 之后日志是包级 API（不再由调用方注入 *slog.Logger），而有些用例
// 必须断言**日志内容**——"降级必须留痕"“密钥绝不能进日志”这类。它读的是真实出口
// （glog 的文件 writer），不是替身，所以同时证明了"日志真的写出了东西"。
//
// 两条纪律：
//  1. 它改的是全局 logger，同一测试包内**不能并行**（用到的包本来也没有 t.Parallel）；
//  2. glog 的文件 writer 是缓冲的（256KB / 5s），读之前必须先 Close 刷出；而 Close 之后
//     底层 writer 已置空、再写会 panic，所以读完立刻重装一个同目录的。
func CaptureLogs(t *testing.T) func() string {
	t.Helper()
	dir := t.TempDir()
	install := func() {
		t.Helper()
		if err := glog.InitLogger(&glog.LogConfig{
			Service:    "mate-test",
			Level:      glog.DebugLevel,
			LoggerType: glog.LoggerTypeZap,
			Writers:    []glog.WriterConfig{{Type: glog.WriterFile, Level: glog.DebugLevel, Dir: dir}},
		}); err != nil {
			t.Fatalf("初始化测试日志出口: %v", err)
		}
	}
	install()
	t.Cleanup(func() {
		// 收尾必须**重装一个可用出口**，不能只 Close：Close 之后底层 writer 被置空，
		// 同包后续用例（哪怕不抓日志）再写一行就会 panic（实测：internal/adapters/inbound/mcpgateway 的
		// TestRunExitsOnStdinEOF 就是这么炸的）。这里装一个 Error 档的 console 出口：
		// 既不会因为缓冲写进已删除的临时目录，也几乎不会往 stdout 说话。
		_ = glog.Close()
		_ = glog.InitLogger(&glog.LogConfig{
			Service:    "mate-test",
			Level:      glog.ErrorLevel,
			LoggerType: glog.LoggerTypeZap,
			Writers:    []glog.WriterConfig{{Type: glog.WriterConsole, Level: glog.ErrorLevel}},
		})
	})
	return func() string {
		_ = glog.Close()
		out := readLogTree(t, dir)
		install()
		return out
	}
}

// readLogTree 读出 glog 文件 writer 在 dir 下写出的全部日志
// （布局是 `<dir>/<YYYYMMDD>/<service>_full.log`）。
func readLogTree(t *testing.T, dir string) string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历日志目录 %s: %v", dir, err)
	}
	sort.Strings(files)
	var sb strings.Builder
	for _, f := range files {
		b, rErr := os.ReadFile(f)
		if rErr != nil {
			t.Fatalf("读日志文件 %s: %v", f, rErr)
		}
		sb.Write(b)
	}
	return sb.String()
}
