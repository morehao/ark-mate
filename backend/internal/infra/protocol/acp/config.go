package acp

import (
	"strconv"

	acpsdk "github.com/coder/acp-go-sdk"
)

// configOptionsFromSDK 把 ACP 选项联合体（select/boolean）归一化为内部投影。
// 未知 type 整体跳过：不猜语义、不伪造选项（宁可少列）。
func configOptionsFromSDK(in []acpsdk.SessionConfigOption) []ConfigOption {
	out := make([]ConfigOption, 0, len(in))
	for _, o := range in {
		switch {
		case o.Select != nil:
			out = append(out, selectFromSDK(o.Select))
		case o.Boolean != nil:
			out = append(out, ConfigOption{
				ID:           string(o.Boolean.Id),
				Name:         o.Boolean.Name,
				Description:  firstNonEmptyPtr(o.Boolean.Description),
				Category:     categoryOf(o.Boolean.Category),
				Type:         OptionTypeBoolean,
				CurrentValue: strconv.FormatBool(o.Boolean.CurrentValue),
			})
		}
	}
	return out
}

// selectFromSDK 归一化一个单选（下拉）选项：分组与扁平两种 wire 形态都展平进 Values。
func selectFromSDK(s *acpsdk.SessionConfigOptionSelect) ConfigOption {
	opt := ConfigOption{
		ID:           string(s.Id),
		Name:         s.Name,
		Description:  firstNonEmptyPtr(s.Description),
		Category:     categoryOf(s.Category),
		Type:         OptionTypeSelect,
		CurrentValue: string(s.CurrentValue),
	}
	if g := s.Options.Grouped; g != nil {
		opt.Grouped = true
		for _, grp := range *g {
			for _, v := range grp.Options {
				opt.Values = append(opt.Values, ConfigOptionValue{
					Value:       string(v.Value),
					Name:        v.Name,
					Description: firstNonEmptyPtr(v.Description),
					Group:       string(grp.Group),
					GroupName:   grp.Name,
				})
			}
		}
		return opt
	}
	if u := s.Options.Ungrouped; u != nil {
		for _, v := range *u {
			opt.Values = append(opt.Values, ConfigOptionValue{
				Value:       string(v.Value),
				Name:        v.Name,
				Description: firstNonEmptyPtr(v.Description),
			})
		}
	}
	return opt
}

// categoryOf 解引用 category（未提供 → 空串，表示「无分类」，消费方按空处理而非猜测）。
func categoryOf(c *acpsdk.SessionConfigOptionCategory) string {
	if c == nil {
		return ""
	}
	return string(*c)
}
