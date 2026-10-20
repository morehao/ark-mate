package automation

import (
	"github.com/morehao/ark-mate/internal/core/code"
	"time"
)

// PreviewCountDefault / PreviewCountMax `POST /automations/preview` 的次数约束。
const (
	PreviewCountDefault = 5
	PreviewCountMax     = 20
)

// Preview 返回 `from` 之后的 count 次到期时刻（默认 5，上限 20）。
//
// 存在的理由是**把"这个表达式到底什么时候跑"变成用户能看见的事实**：时区、DST、weekly
// 的星期集合三样东西叠在一起，没人能从 JSON 里读出来。返回里带 `warnings`，跨 DST 空档时
// 非空（它会在界面上被显示，而不是躺在日志里）。
func Preview(s Schedule, from time.Time, count int) ([]Occurrence, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if count <= 0 {
		count = PreviewCountDefault
	}
	if count > PreviewCountMax {
		return nil, code.NewError(code.ClassRequest, "invalid_param", "count 上限 20（预览是给人看的，不是导出工具）", nil)
	}
	if from.IsZero() {
		from = time.Now()
	}
	out := make([]Occurrence, 0, count)
	cursor := from
	for len(out) < count {
		next, ok, err := s.NextAfter(cursor)
		if err != nil {
			return nil, err
		}
		if !ok {
			break // once 只有一次；后面的位置不存在不是错误
		}
		out = append(out, next)
		cursor = next.At
	}
	return out, nil
}
