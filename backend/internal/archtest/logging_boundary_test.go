package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是「日志出口唯一」的静态守卫。
//
// 2026-10-10 起本仓的日志出口是 golib/glog（zap driver）：装配根注册 driver
// （`cmd/mate` 的匿名引入）+ `internal/bootstrap/cliops/logging.go` 定配置，业务代码一律
// 调 `glog.Xxxw(ctx, ...)`。唯一允许出现 `log/slog` 的地方是 `internal/platform/logging`——
// 它把"只认 *slog.Logger"的第三方库（当前是 ACP SDK 的 SetLogger）桥进 glog。
//
// 为什么要有机器判据：迁移当天能靠人盯着，之后不行。任何一个 `slog.Info` 都能编译通过、
// 也"看起来在工作"，但它的记录会绕过 glog 直接落到 std log 的 stderr——级别、格式、
// 轮转、目标全都不一样，症状是"这条日志在文件出口里找不到"，而且只有出事那天才会被注意到。
//
// 判据是**剥注释后仍出现 `"log/slog"` 导入路径**（注释里提到 slog 是正常的，比如
// internal/platform/logging 与几处说明性注释）。

// slogAllowedDirs 允许 import "log/slog" 的包（相对 backend 根）。
var slogAllowedDirs = []string{
	// 第三方 slog 边界桥：只认 *slog.Logger 的库（ACP SDK）从这里接入 glog。
	filepath.Join("internal", "platform", "logging"),
}

// TestOnlyLoggingPackageImportsSlog 除 internal/platform/logging 外，全仓不得 import "log/slog"。
func TestOnlyLoggingPackageImportsSlog(t *testing.T) {
	root := backendRoot(t)
	var offenders []string
	scanned := 0
	for _, path := range goFilesUnder(t, root) {
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		// 跳过本目录自身：这条守卫的源码里就写着 `"log/slog"` 这个字面量（与
		// knowledge_boundary 的禁词表同一处境）。
		if strings.HasPrefix(rel, filepath.Join("internal", "archtest")+string(filepath.Separator)) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s：%v", path, err)
		}
		// 用 TS 注释剥离器：它只处理 // 与 /* */，对 Go 源码同样适用（本仓既有做法）。
		if !strings.Contains(stripTSComments(string(raw)), `"log/slog"`) {
			continue
		}
		scanned++
		if allowedLogSlog(rel) {
			continue
		}
		offenders = append(offenders, rel)
	}
	if scanned == 0 {
		t.Fatalf("全仓一个 log/slog 都没扫到：桥包还在吗？这条守卫失去了检查对象（fail-closed）")
	}
	if len(offenders) > 0 {
		t.Fatalf("以下文件 import 了 \"log/slog\"（只允许 internal/platform/logging）：\n\t%s\n\n"+
			"业务日志一律走 glog：`glog.Infow(ctx, msg, kvs...)` / `Warnw` / `Errorw` / `Debugw`；\n"+
			"确需喂给只认 *slog.Logger 的第三方库时，用 internal/platform/logging.SDKLogger()。",
			strings.Join(offenders, "\n\t"))
	}
}

func allowedLogSlog(rel string) bool {
	for _, dir := range slogAllowedDirs {
		if strings.HasPrefix(rel, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
