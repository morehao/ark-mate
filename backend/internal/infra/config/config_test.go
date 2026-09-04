package config

import (
	"os"
	"path/filepath"
	"testing"
)

// tsetenv 设置环境变量并在测试结束后恢复（为空值视为“未设置”）。
func tsetenv(t *testing.T, key, val string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// unset 清除环境变量并在测试结束后恢复。
func unset(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		}
	})
}

// TestLoadDefaults 无环境变量时默认 :8099 + ~/.forge/transcripts。
func TestLoadDefaults(t *testing.T) {
	unset(t, "FORGE_ADDR")
	unset(t, "FORGE_TRANSCRIPTS")
	c := Load()
	if c.Addr != ":8099" {
		t.Fatalf("默认 Addr 不符: %q", c.Addr)
	}
	if c.TranscriptDir == "" {
		t.Fatal("TranscriptDir 默认不应为空")
	}
	if filepath.Base(c.TranscriptDir) != "transcripts" {
		t.Fatalf("默认 TranscriptDir 不符: %q", c.TranscriptDir)
	}
}

// TestLoadEnvOverrides 环境变量覆盖默认值。
func TestLoadEnvOverrides(t *testing.T) {
	tsetenv(t, "FORGE_ADDR", ":9999")
	tsetenv(t, "FORGE_TRANSCRIPTS", "/tmp/forge-tx")
	c := Load()
	if c.Addr != ":9999" {
		t.Fatalf("Addr 覆盖不符: %q", c.Addr)
	}
	if c.TranscriptDir != "/tmp/forge-tx" {
		t.Fatalf("TranscriptDir 覆盖不符: %q", c.TranscriptDir)
	}
}

// TestDefault 默认值结构。
func TestDefault(t *testing.T) {
	if d := Default(); d.Addr != ":8099" || d.TranscriptDir != "" {
		t.Fatalf("Default 不符: %+v", d)
	}
}
