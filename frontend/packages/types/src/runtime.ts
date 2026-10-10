export interface CapabilityKV {
  key: string
  value: boolean
}

/**
 * 一个底座（`GET /runtimes`）。
 *
 * **v12 删掉了 `provider_projection`**：那是一张"每个底座认哪些运行时参数"的 schema，前端据此
 * 渲染逐底座的投影表单——逐底座投影本身就是被删掉的东西（ADR-7）。运行时参数不再暴露给用户，
 * 协议差异由各 adapter 自己知道（ADR-11）。
 */
export interface Runtime {
  name: string
  id: string
  version: string
  /** 静态能力基线（read-only）。 */
  capabilities: CapabilityKV[]
}
