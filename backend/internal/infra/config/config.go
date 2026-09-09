// Package config 提供进程级配置加载。
// M0 起步：CLI flag（cmd/forge 解析）+ 环境变量兜底；配置文件随 M2 的 `.forge/config`
// 目录化引入（见 docs/design-and-roadmap.md §6 M2）。
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config 是进程级配置。
type Config struct {
	// Addr HTTP 监听地址。
	Addr string
	// TranscriptDir 会话 wire transcript 落盘目录（M1 #5；空 = 禁用）。
	TranscriptDir string
	// DBPath SQLite 库文件路径（P1 落库：events/sessions/…；FORGE_DB 覆盖，默认 ~/.forge/forge.db）。
	DBPath string
	// StoreMode 持久层开关："sqlite" | "memory" | ""(auto：DB 文件已存在 → sqlite，否则 memory)。
	// FORGE_STORE 覆盖。
	StoreMode string
	// HandoffMaxChars bounded handoff memo 字符预算（§5.6/§18 D-3；FORGE_HANDOFF_MAX_CHARS
	// 覆盖，默认 6000；0 = 关闭 memo 兜底）。跨 runtime / 无原生 resume 时把历史压成摘要注入
	// 首条 prompt。
	HandoffMaxChars int
	// TurnTimeout 单轮 prompt 看门狗超时（0 = 关闭）。runtime 吞错/挂起（如 opencode 模型限流
	// 只记自己日志不上报）时，超时自动 cancel 并发终态事件，避免 busy 永久占用。
	TurnTimeout time.Duration
	// PermissionTimeout 挂起权限请求自动拒绝超时（默认 30min，对齐 AionUi F-PERM-01）。
	PermissionTimeout time.Duration
	// ElicitationTimeout 挂起结构化提问自动取消超时（默认 30min，对齐 AionUi）。
	ElicitationTimeout time.Duration
	// AgentDir Agent definitions 目录（P2：<id>.json 文件定义自定义 agent；FORGE_AGENTS 覆盖，
	// 默认 ~/.forge/agents；空 = 不启用 definitions 落盘，仅内存注册）。
	AgentDir string
	// CapabilityDir 能力数据面目录（P1：skills|mcp|tools/*.json → Skill/MCP/Tool 目录；
	// FORGE_CAPABILITIES 覆盖，默认 ~/.forge/capabilities；空 = 不启用文件数据面）。
	CapabilityDir string
	// SkillMaterializeDir skill 物化目标根目录（§5.4/§16.5；FORGE_SKILLS_MATERIALIZE_DIR 覆盖，
	// 默认空 = 关闭）。非空时 serve 启动装载能力后把 opencode SKILL.md 源 skill 物化到该目录
	// （<dir>/<skillID>/SKILL.md，opencode v1 自动扫描约定）。目标多为真实 opencode 用户目录，
	// 须显式开启（默认不写用户机器配置）。
	SkillMaterializeDir string
	// IdleTimeout 空闲回收阈值：StartAgent 暖进程闲置超过该时长自动停进程（P2；
	// FORGE_IDLE_TIMEOUT 覆盖，默认 5m；0 = 关闭回收）。
	IdleTimeout time.Duration
	// ReapInterval 空闲回收扫描间隔（P2；FORGE_REAP_INTERVAL 覆盖，默认 5m）。
	ReapInterval time.Duration
}

// 默认超时（与 manager.DefaultPermissionTimeout/DefaultElicitationTimeout 对齐）。
const (
	DefaultPermissionTimeout  = 30 * time.Minute
	DefaultElicitationTimeout = 30 * time.Minute
	DefaultIdleTimeout        = 5 * time.Minute
	DefaultReapInterval       = 5 * time.Minute
)

// Default 返回默认配置。
func Default() Config {
	return Config{
		Addr:               ":8099",
		PermissionTimeout:  DefaultPermissionTimeout,
		ElicitationTimeout: DefaultElicitationTimeout,
		IdleTimeout:        DefaultIdleTimeout,
		ReapInterval:       DefaultReapInterval,
	}
}

// Load 在默认值之上应用环境变量兜底（flag 优先级最高，由 cmd 层覆盖）。
func Load() Config {
	c := Default()
	if v := os.Getenv("FORGE_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("FORGE_TRANSCRIPTS"); v != "" {
		c.TranscriptDir = v
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		c.TranscriptDir = filepath.Join(home, ".forge", "transcripts")
	} else {
		c.TranscriptDir = ".forge/transcripts"
	}
	if v := os.Getenv("FORGE_DB"); v != "" {
		c.DBPath = v
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		c.DBPath = filepath.Join(home, ".forge", "forge.db")
	} else {
		c.DBPath = ".forge/forge.db"
	}
	// FORGE_STORE：sqlite|memory|空（auto）。非法值视为空（auto）。
	if v := os.Getenv("FORGE_STORE"); v == "sqlite" || v == "memory" {
		c.StoreMode = v
	}
	if v := os.Getenv("FORGE_AGENTS"); v != "" {
		c.AgentDir = v
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		c.AgentDir = filepath.Join(home, ".forge", "agents")
	} else {
		c.AgentDir = ".forge/agents"
	}
	if v := os.Getenv("FORGE_CAPABILITIES"); v != "" {
		c.CapabilityDir = v
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		c.CapabilityDir = filepath.Join(home, ".forge", "capabilities")
	} else {
		c.CapabilityDir = ".forge/capabilities"
	}
	// FORGE_SKILLS_MATERIALIZE_DIR：skill 物化根目录（空 = 关闭物化，不写用户目录）。
	if v := os.Getenv("FORGE_SKILLS_MATERIALIZE_DIR"); v != "" {
		c.SkillMaterializeDir = v
	}
	// FORGE_IDLE_TIMEOUT / FORGE_REAP_INTERVAL：Go duration 文本（如 30s/2m/1h）；
	// FORGE_IDLE_TIMEOUT 非法或 0 = 关闭回收；间隔非法回落默认 5m。
	if v := os.Getenv("FORGE_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.IdleTimeout = d
		}
	}
	if v := os.Getenv("FORGE_REAP_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.ReapInterval = d
		}
	}
	// FORGE_TURN_TIMEOUT：Go duration 文本（如 10m/1h）；非法值视为 0（关闭看门狗）。
	if v := os.Getenv("FORGE_TURN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.TurnTimeout = d
		}
	}
	// FORGE_HANDOFF_MAX_CHARS：bounded handoff memo 字符预算；非法值回落默认 6000。
	c.HandoffMaxChars = 6000
	if v := os.Getenv("FORGE_HANDOFF_MAX_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.HandoffMaxChars = n
		}
	}
	// FORGE_PERMISSION_TIMEOUT / FORGE_ELICITATION_TIMEOUT：Go duration 文本；非法值回落默认 30min。
	if v := os.Getenv("FORGE_PERMISSION_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.PermissionTimeout = d
		}
	}
	if v := os.Getenv("FORGE_ELICITATION_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.ElicitationTimeout = d
		}
	}
	return c
}
