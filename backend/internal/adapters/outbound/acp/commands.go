package acp

import (
	acpsdk "github.com/coder/acp-go-sdk"
)

// CommandInfo 一条 ACP available command 的归一化投影
// （session/update: available_commands_update）。
//
// **它只是"发现面"**：ACP 规范里命令没有独立的执行方法，执行 = 把命令行原样当
// `session/prompt` 的文本发出去（<https://agentclientprotocol.com/protocol/v1/slash-commands>）。
// 因此本类型不含任何调用语义，也不代表平台"拥有"这些命令——它们是**底座自报**的。
type CommandInfo struct {
	// Name 命令名（不含前导 '/'；ACP 的 AvailableCommand.Name，如 `review`）。
	Name string `json:"name"`
	// Description 人类可读说明（agent 可留空）。
	Description string `json:"description,omitempty"`
	// Hint 命令需要参数时底座给出的占位提示（ACP UnstructuredCommandInput.Hint）；
	// 无参命令为空。仅作输入提示，不参与校验。
	Hint string `json:"hint,omitempty"`
}

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
