package skill

import (
	"github.com/morehao/ark-mate/internal/capability"
)

// Warnings 把一条技能定义上的告警投影成对外形态。
//
// 放在技能平面里而不是能力聚合面：告警是**技能定义自己的**质量信号（路由声明写错、投放目标
// 认不出来这类），与"哪些技能/连接器在册"的联合视图无关；聚合面只是把它转出去。
//
// 纯函数（收值、返回新切片），所以它随技能平面走，不需要 `*Service` 的任何状态。
func Warnings(sk capability.Skill) []WarningView {
	out := make([]WarningView, 0, len(sk.Warnings))
	for _, w := range sk.Warnings {
		out = append(out, WarningView{
			Severity: w.Severity.String(), Pattern: w.Pattern, Message: w.Message,
		})
	}
	return out
}
