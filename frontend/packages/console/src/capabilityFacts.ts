/**
 * `GET /capabilities` 声明里的**事实**（唯一读取点 + 进程内缓存，由 `bootstrap.tsx` 在挂载界面前调一次）。
 *
 * 为什么单独一个模块（2026-10-11，P3）：这份声明有消费面，此前与目录选择器各读各的——
 *   **目录选择器**要用 `native-dialog.available`（宿主能不能抬系统面板）。
 * 各打一次 `/capabilities` 不只是多一个请求：那意味着"本次会话的入口形态"有多个真相，
 * 而它们可以在失败路径上**分叉**（一个成功一个失败，界面就自相矛盾）。所以事实只有一份。
 *
 * 两条纪律（与 `directoryPicker.ts` 一致，别绕过）：
 *   1. **不嗅探客户端**：判据只有服务端声明（禁 UA / `window.electron` / `__MATE_DESKTOP__`）；
 *   2. **拿不准就当"没那么特殊"**：声明没到 / 请求失败 → 原生面板不可用（退回应用内浏览）。
 */
import { getCapabilities } from '@ark-mate/api'

/** 声明里被依赖的能力 id（与前端契约的 `CapabilityId` 一一对应）。 */
export const NATIVE_DIALOG_CAPABILITY = 'native-dialog'

/** 采样到的宿主/入口事实。 */
export interface CapabilityFacts {
  /** 宿主能不能抬起系统目录面板（`native-dialog.available`）。 */
  readonly nativeDialog: boolean
  /** 本次会话来自桌面壳（`entries.desktop`，由壳的 `X-Mate-Carrier` 头带出）。 */
  readonly desktopEntry: boolean
}

const UNKNOWN: CapabilityFacts = { nativeDialog: false, desktopEntry: false }

let facts: CapabilityFacts = UNKNOWN

/** 读当前事实（`loadCapabilityFacts()` 之前恒为 `UNKNOWN`）。 */
export function declaredFacts(): CapabilityFacts {
  return facts
}

/**
 * 读一次服务端能力声明并缓存。
 *
 * 失败**不抛**：拉不到声明就按 `UNKNOWN` 走——界面不该因为少一份声明而少一块。
 */
export async function loadCapabilityFacts(): Promise<void> {
  try {
    const caps = await getCapabilities()
    facts = {
      nativeDialog: caps?.capabilities?.find((c) => c.id === NATIVE_DIALOG_CAPABILITY)?.available === true,
      desktopEntry: caps?.entries?.desktop === true,
    }
  } catch {
    facts = UNKNOWN
  }
}
