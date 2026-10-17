/**
 * 命令门禁（A2）的对外契约。
 *
 * 与后端 `internal/dto/dtocommandpolicy` 逐字段一致；`pattern` 是**前缀 token 模式**，
 * 每个位置可写一组备选（`[["git","hg"],["status"]]`）。
 */
export type CommandDecision = 'allow' | 'prompt' | 'forbidden'

export interface CommandPolicyRule {
  name?: string
  pattern: string[][]
  decision: CommandDecision
  match_samples?: string[][]
  not_match_samples?: string[][]
  host_executable?: string[]
}

export interface CommandPolicyResp {
  /** false = 规则文件未配置（`MATE_COMMAND_POLICY` 为空）；此时写面会拒绝。 */
  enabled: boolean
  rules: CommandPolicyRule[]
}
