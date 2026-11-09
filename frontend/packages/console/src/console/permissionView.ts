// ——— 权限 chip 的展示映射（纯函数，便于 Node 侧单测）———
//
// 口径（design §3.3 / §3.4）：权限是唯一的安全轴；当前接入方式（D11）会话内不可切，
// chip 一律渲染成**锁定态 + 解释**，不做假切换。档位文案对齐 DSH 三档预设 + 跟随默认。
//
// 三档 + 默认的取值来自 `sessions.native_mode`（后端只读回显）：
//   ''                    → 跟随默认（会话未钉住，走工作空间默认值）
//   read-only             → 仅可查看
//   workspace-write       → 工作区内修改
//   danger-full-access    → 完全权限（危险档，带风险标记）

type NativePermMode = '' | 'read-only' | 'workspace-write' | 'danger-full-access'

/** 原生权限档 → chip 短标签。未知值原样回落（不吞、不猜）。 */
export function permLabel(mode: string | null | undefined): string {
  switch (mode) {
    case '':
    case undefined:
    case null:
      return '跟随默认'
    case 'read-only':
      return '仅可查看'
    case 'workspace-write':
      return '工作区内修改'
    case 'danger-full-access':
      return '完全权限'
    default:
      return String(mode)
  }
}

/** 三档 + 默认的完整说明（解释弹层用，顺序即展示顺序）。 */
interface PermRow {
  mode: NativePermMode
  name: string
  desc: string
  /** 危险档：解释里带风险语气。 */
  risk?: boolean
}

export const PERM_ROWS: PermRow[] = [
  { mode: '', name: '跟随默认', desc: '沿用工作空间默认值，新建会话时可选具体档位。' },
  { mode: 'read-only', name: '仅可查看', desc: '只能读、不能改；需要写入时会向你申请升级。' },
  { mode: 'workspace-write', name: '工作区内修改', desc: '会话工作区内的文件可改，越界与敏感操作再问你。' },
  {
    mode: 'danger-full-access',
    name: '完全权限',
    desc: '不限制写入，也不再询问敏感操作（风险自担）。',
    risk: true,
  },
]

/** 是否危险档（弹层里给当前档加风险标记）。 */
export function isDanger(mode: string | null | undefined): boolean {
  return mode === 'danger-full-access'
}
