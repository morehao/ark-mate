package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 `allowed_origins`（M3-5）的解析与校验判据。
//
// 与 internal/middleware/origin_test.go 的分工：这里钉**配置侧**（取值形状、归一结果、非法即报错），
// 那边钉**判定侧**（大小写/尾斜杠变体能不能命中这里归一出来的那张表）。两侧合起来
// 才是"配置值 ↔ 请求头比较键同一条规则"的完整证据。

// TestAllowedOriginsDefaults 未设置 / 显式空 = 只用内置 Origin 矩阵（不报错）。
func TestAllowedOriginsDefaults(t *testing.T) {
	hermetic(t)
	unset(t, "MATE_ALLOWED_ORIGINS")
	if cfg := Load(); len(cfg.AllowedOrigins) != 0 {
		t.Fatalf("默认应为空（只用内置矩阵）：%+v", cfg.AllowedOrigins)
	}
	for _, v := range []string{"", "   ", ",", " , "} {
		t.Setenv("MATE_ALLOWED_ORIGINS", v)
		if cfg := Load(); len(cfg.AllowedOrigins) != 0 {
			t.Fatalf("%q 应等于不登记任何额外来源：%+v", v, cfg.AllowedOrigins)
		}
	}
}

// TestAllowedOriginsEnv 逗号分隔 + 空白容忍 + 小写归一 + 去尾斜杠 + 排序去重。
func TestAllowedOriginsEnv(t *testing.T) {
	hermetic(t)
	t.Setenv("MATE_ALLOWED_ORIGINS", " HTTP://LocalHost:5173/ , http://127.0.0.1:5173 ,http://localhost:5173, ")
	cfg := Load()
	want := []string{"http://127.0.0.1:5173", "http://localhost:5173"}
	if len(cfg.AllowedOrigins) != len(want) {
		t.Fatalf("解析不符（期望去重后 %d 项）：%+v", len(want), cfg.AllowedOrigins)
	}
	for i := range want {
		if cfg.AllowedOrigins[i] != want[i] {
			t.Fatalf("第 %d 项不符（应小写归一 + 去尾斜杠 + 排序）：%+v", i, cfg.AllowedOrigins)
		}
	}
}

// TestAllowedOriginsArrayForm config.json 的数组形式（与 workspace_roots / projection_dirs 同一族多值键），
// 且**进程 env 覆盖数组**——两种书写形式不能各自为政。
func TestAllowedOriginsArrayForm(t *testing.T) {
	dir := hermetic(t)
	unset(t, "MATE_ALLOWED_ORIGINS")
	body := `{"allowed_origins": ["http://localhost:5173", "HTTPS://Mate.Example.COM/"]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	cfg, src, err := LoadResolved(dir)
	if err != nil {
		t.Fatalf("数组形式应合法: %v", err)
	}
	want := []string{"http://localhost:5173", "https://mate.example.com"}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != want[0] || cfg.AllowedOrigins[1] != want[1] {
		t.Fatalf("数组解析不符: %+v", cfg.AllowedOrigins)
	}
	if src["allowed_origins"] != SourceFile {
		t.Fatalf("来源应为 config.json: %v", src["allowed_origins"])
	}

	t.Setenv("MATE_ALLOWED_ORIGINS", "http://127.0.0.1:5173")
	cfg2, src2, err := LoadResolved(dir)
	if err != nil {
		t.Fatalf("LoadResolved: %v", err)
	}
	if len(cfg2.AllowedOrigins) != 1 || cfg2.AllowedOrigins[0] != "http://127.0.0.1:5173" {
		t.Fatalf("env 应覆盖 config.json 的数组值: %+v", cfg2.AllowedOrigins)
	}
	if src2["allowed_origins"] != SourceEnv {
		t.Fatalf("来源应为 env(process): %v", src2["allowed_origins"])
	}
}

// TestAllowedOriginsRejectsInvalid 非法取值启动即报错，且提示里给出合法形状/scheme（照既有枚举键的风格）。
func TestAllowedOriginsRejectsInvalid(t *testing.T) {
	hermetic(t)
	cases := []struct {
		val  string
		want string
	}{
		// 少了 scheme://：url.Parse 会把它当成 scheme=localhost 的怪东西，必须显式给写法。
		{"localhost:5173", "scheme://host[:port]"},
		{"127.0.0.1:5173", "scheme://host[:port]"},
		// scheme 不在白名单（只有会加载页面的上下文才带 Origin）。
		{"ftp://localhost:5173", "scheme"},
		{"file://localhost/tmp", "scheme"},
		// 带路径/查询/片段/userinfo：都不是 origin 的形状。
		{"http://localhost:5173/app", "纯 origin"},
		{"http://localhost:5173/?a=1", "纯 origin"},
		{"http://localhost:5173#frag", "纯 origin"},
		{"http://user:pw@localhost:5173", "纯 origin"},
		// 没有主机。
		{"http://", "纯 origin"},
		{"://localhost:5173", "纯 origin"},
		// 端口畸形。
		{"http://localhost:", "端口非法"},
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("MATE_ALLOWED_ORIGINS", tc.val)
			_, _, err := LoadResolved("")
			if err == nil {
				t.Fatalf("非法取值 %q 应报错", tc.val)
			}
			if !strings.Contains(err.Error(), "allowed_origins") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误提示应指名键与合法形状（含 %q），got %v", tc.want, err)
			}
		})
	}

	// 一个合法项夹一个非法项也要报错（不静默取前/后一段）。
	t.Setenv("MATE_ALLOWED_ORIGINS", "http://localhost:5173,ftp://x")
	if _, _, err := LoadResolved(""); err == nil {
		t.Fatal("列表中夹非法项应报错")
	}
}

// TestAllowedOriginsSchemeWhitelist 合法的 scheme 逐个过一遍（含桌面自定义 scheme）。
func TestAllowedOriginsSchemeWhitelist(t *testing.T) {
	hermetic(t)
	t.Setenv("MATE_ALLOWED_ORIGINS", "http://a.example,https://b.example:8443,mate-app://app")
	cfg := Load()
	// 顺序即 sort.Strings 的结果（normalizeAllowedOrigins 末尾刻意排序）：桌面自定义 scheme 的
	// 首字母从 f（旧名）变成 m（mate-app），在字典序里由最前挪到了 `http`/`https` 之后——
	// 所以下面这个数组不是"随手写的顺序"，改名时会跟着挪。
	want := []string{"http://a.example", "https://b.example:8443", "mate-app://app"}
	if len(cfg.AllowedOrigins) != len(want) {
		t.Fatalf("解析不符: %+v", cfg.AllowedOrigins)
	}
	for i := range want {
		if cfg.AllowedOrigins[i] != want[i] {
			t.Fatalf("第 %d 项不符: %+v", i, cfg.AllowedOrigins)
		}
	}
}

// TestAllowedOriginsUnknownKeyTypo 拼错的键名仍然启动即报错（不静默忽略），并列出支持键。
func TestAllowedOriginsUnknownKeyTypo(t *testing.T) {
	dir := hermetic(t)
	unset(t, "MATE_ALLOWED_ORIGINS")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"allowed_origin": "http://x"}`), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	_, _, err := LoadResolved(dir)
	if err == nil || !strings.Contains(err.Error(), "未知配置键") {
		t.Fatalf("未知键应报错，got %v", err)
	}
	if !strings.Contains(err.Error(), "allowed_origins") {
		t.Fatalf("错误提示应列出支持键（含 allowed_origins），got %v", err)
	}
}
