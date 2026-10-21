// Package dtocommandpolicy 命令门禁（A2）的读/写 DTO。
package dtocommandpolicy

// Rule 一条前缀规则（与 `interaction.PrefixRule` 同形；JSON 键与规则文件逐字一致）。
//
// 为什么不直接复用域类型：传输面与配置文件是**两个**契约（一个走 REST，一个走磁盘），
// 现在同形是事实，但把域类型直接当 DTO 会让"REST 想加一个只属于界面的字段"变成改域。
type Rule struct {
	Name            string     `json:"name,omitempty"`
	Pattern         [][]string `json:"pattern"`
	Decision        string     `json:"decision"`
	MatchSamples    [][]string `json:"match_samples,omitempty"`
	NotMatchSamples [][]string `json:"not_match_samples,omitempty"`
	HostExecutable  []string   `json:"host_executable,omitempty"`
}

// PolicyResp 当前规则（`GET /command-policy`）。
type PolicyResp struct {
	// Enabled false = 规则文件未配置（`MATE_COMMAND_POLICY` 为空）；此时写面回 40001。
	Enabled bool   `json:"enabled"`
	Rules   []Rule `json:"rules"`
}

// PolicyReq 全量替换规则（`PUT /command-policy`）。
type PolicyReq struct {
	Rules []Rule `json:"rules"`
}
