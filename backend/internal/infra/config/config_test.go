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

// TestLoadDefaults 无环境变量时默认 127.0.0.1:10001（仅回环，无鉴权面不收外网）+ ~/.forge/transcripts。
func TestLoadDefaults(t *testing.T) {
	unset(t, "FORGE_ADDR")
	unset(t, "FORGE_TRANSCRIPTS")
	c := Load()
	if c.Addr != "127.0.0.1:10001" {
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
	if d := Default(); d.Addr != "127.0.0.1:10001" || d.TranscriptDir != "" {
		t.Fatalf("Default 不符: %+v", d)
	}
}

// TestCapabilityDirIndependentOfSkillMaterialize 回归：FORGE_CAPABILITIES 必须生效，
// 且不受 FORGE_SKILLS_MATERIALIZE_DIR 是否设置影响（曾因 else 分支误覆盖 CapabilityDir
// 导致能力目录被吞、永远落到 ~/.forge/capabilities）。
func TestCapabilityDirIndependentOfSkillMaterialize(t *testing.T) {
	unset(t, "FORGE_SKILLS_MATERIALIZE_DIR")
	tsetenv(t, "FORGE_CAPABILITIES", "/tmp/forge-caps")
	c := Load()
	if c.CapabilityDir != "/tmp/forge-caps" {
		t.Fatalf("FORGE_CAPABILITIES 未生效: %q", c.CapabilityDir)
	}
	if c.SkillMaterializeDir != "" {
		t.Fatalf("未设置物化目录时应为空（默认不写用户目录），got %q", c.SkillMaterializeDir)
	}

	tsetenv(t, "FORGE_SKILLS_MATERIALIZE_DIR", "/tmp/forge-skills")
	c = Load()
	if c.CapabilityDir != "/tmp/forge-caps" || c.SkillMaterializeDir != "/tmp/forge-skills" {
		t.Fatalf("两项应互不影响: caps=%q skills=%q", c.CapabilityDir, c.SkillMaterializeDir)
	}
}
