// Package objtime 承载「平台对外时间口径」的唯一实现：REST/WS **返回**的时刻一律 Unix 毫秒。
//
// 为什么单独成包（对齐 objid / objusage 的口径）：
//   - 时间口径是**对外契约**（前端 `new Date(ms)`、脚本、诊断包都按它读），与 objid 同属
//     "跨层共享值对象"；
//   - 投影点跨三层（domain view、service 装配、driver 诊断），放在任何一层都会让另外两层
//     反向依赖；
//   - 它是 **leaf**：只依赖标准库，L1 层，任何一层都能直接 import。
//
// 三条边界（与 docs/agents/time.md 同源）：
//   - **单位毫秒**：与 JS `Date.now()` / `new Date(ms)` 同量纲，读侧零换算；
//   - **零值 → 0**：Go 的 `time.Time{}` 直接 `UnixMilli()` 是一个巨大负数（年份 1），
//     必须走这里，让"未发生"稳定地落成 `0` + `omitempty`；
//   - **只用于出口**：落库仍是 `model.LayoutTS`（RFC3339Nano 文本），transcript/导出等
//     文件载体也保持 RFC3339——那些是"可回灌的载体"，不是接口响应。
package objtime

import (
	"encoding/json"
	"strconv"
	"time"
)

// Milli 把 time.Time 转成对外时间戳（Unix 毫秒）；零值 → 0。
func Milli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Millis 处理指针形态：nil / 零值 → 0。
//
// 用于 `*time.Time` 的运行态字段（如 automation 的 `last_fired_at`）：它天然区分
// "没有下一次"（nil）与"某一刻"，转出来与空串 + omitempty 的旧语义逐字一致。
func Millis(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return Milli(*t)
}

// Parse 宽容解析「可能是旧格式」的载荷时间值，返回 Unix 毫秒；无法识别 → 0。
//
// 读侧兼容的唯一理由：`events.payload_json` 是**不可变的历史事实**——本次口径切换之前
// 落库的 `run.status.at` / `timeout_at` 仍是 RFC3339 字符串，事件不能回写。于是读回放
// （`RunStatusSnapshot`）必须同时认字符串与数字；写侧只产出毫秒。
//
// 支持：time.Time、int64/int/int32/uint64、float64（encoding/json 默认数字）、
// json.Number、RFC3339Nano/RFC3339 字符串、十进制毫秒字符串。
func Parse(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case time.Time:
		return Milli(x)
	case *time.Time:
		return Millis(x)
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		return 0
	case string:
		if x == "" {
			return 0
		}
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return Milli(t)
		}
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return Milli(t)
		}
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}
