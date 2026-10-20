package provider

import (
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/infra/acp"
)

// ModelApplyReason 从运行时错误里提取一句人类可读的「为什么这个值不可用」
// （用于目录里把该值标灰；最终展示仍以运行时原始信息为准）。
func ModelApplyReason(err error) string {
	if ae, ok := acp.As(err); ok && ae != nil && ae.Details != "" {
		return ae.Details
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "运行时拒绝了该取值"
	}
	return fmt.Sprintf("运行时拒绝了该取值：%s", msg)
}
