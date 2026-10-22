package acp

import (
	acpsdk "github.com/coder/acp-go-sdk"
)

// commandsFromSDK 归一化 ACP 命令列表。
// 无名条目**跳过**：名字是唯一可执行标识，空名字既无法补全也无法发送（不伪造占位名）。
// 顺序**原样保留**——列表顺序由底座给出，是它的展示意图，平台不重排。
func commandsFromSDK(in []acpsdk.AvailableCommand) []CommandInfo {
	out := make([]CommandInfo, 0, len(in))
	for _, c := range in {
		if c.Name == "" {
			continue
		}
		info := CommandInfo{Name: c.Name, Description: c.Description}
		if c.Input != nil && c.Input.Unstructured != nil {
			info.Hint = c.Input.Unstructured.Hint
		}
		out = append(out, info)
	}
	return out
}
