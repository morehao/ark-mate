// Package model 定义持久层实体（gorm 映射）与库侧取值约定。
//
// 实体字段与既有 SQLite 表**逐列对齐**（列类型、文本格式都不变）：建表仍由
// `internal/store` 的自管迁移内核负责，本包不参与 AutoMigrate。
package model

import "time"

// LayoutTS 是时间列的落库布局。
//
// 选 RFC3339Nano 的理由：与 REST 返回的 ISO 形态同源，且在 SQLite 里是
// **可直接字符串比较**的定长前缀格式（游标 `seq`/区间过滤都按字符串比较）。
const LayoutTS = time.RFC3339Nano

// FormatTime 把时间格式化为落库文本；零值 → 空串（历史行里 "未发生" 的表示）。
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(LayoutTS)
}

// FormatTimeOrNow 同 FormatTime，但零值取当前时间（写入路径的默认值语义）。
func FormatTimeOrNow(t time.Time) string {
	if t.IsZero() {
		return NowTS()
	}
	return t.UTC().Format(LayoutTS)
}

// NowTS 返回当前时刻的落库文本。
func NowTS() string { return time.Now().UTC().Format(LayoutTS) }

// NowTSTime 与 NowTS 同一时刻的 time.Time 形态（走 gorm 列序列化的写路径用它）。
func NowTSTime() time.Time { return time.Now().UTC() }

// ParseTime 解析落库文本；空串或不可解析 → 零值（读路径容错，不因单行异常中断查询）。
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(LayoutTS, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
