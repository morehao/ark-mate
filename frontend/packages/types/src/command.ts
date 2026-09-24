/**
 * 底座自报的可用斜杠命令（ACP `session/update: available_commands_update`，
 * 平台经 `session.commands` 域事件全量转发；见 ADR-17）。
 *
 * **这是发现面，不是平台声明的能力**：命令由底座自己注册，平台既不拥有也不投影它们。
 * ACP 规范里命令只有通告、没有独立的执行方法——执行就是把命令行当普通提示词发出去
 * （平台本来就支持），所以这里没有"调用"语义，只有展示与补全。
 *
 * 逐底座如实：opencode 会通告（并会把 skill 也以命令形式通告），dsh 的 ACP 面不含命令
 * 故**永不产生该事件**——「没收到」与「收到了空列表」是两件不同的事，见 CommandCatalog 语义。
 */
export interface CommandInfo {
  /** 命令名（不含前导 `/`），如 `review`。 */
  name: string
  /** 人类可读说明（底座可留空）。 */
  description?: string
  /** 命令需要参数时底座给出的占位提示；仅作输入提示，不参与校验。 */
  hint?: string
}
