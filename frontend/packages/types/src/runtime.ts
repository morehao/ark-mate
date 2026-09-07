export interface CapabilityKV {
  key: string
  value: boolean
}

export interface Runtime {
  name: string
  id: string
  version: string
  capabilities: CapabilityKV[] // 静态能力基线（read-only）
}
