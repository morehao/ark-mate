// ─────────────────────────────────────────────────────────────────────────────
// flags.ts —— 预览层的运行时开关（PREVIEW_V2）
//
// ⚠ 不用 `import.meta.env`：本仓守卫 TestDevLoopServesBuiltArtifact 明确禁止界面源码出现
//   编译期开发分支（会让开发态与交付态分叉）。这里用一个**运行时**本地开关，默认开启；
//   关闭时 PreviewProvider 回退到旧的 window.open 行为（灰度回退用）。
// ─────────────────────────────────────────────────────────────────────────────
const PREVIEW_V2_KEY = 'ark-mate.preview-v2'

/** 预览 V2（应用内统一预览壳）是否启用。默认 true；localStorage 显式写 'off' 时关闭。 */
export function isPreviewV2Enabled(): boolean {
  if (typeof localStorage === 'undefined') return true
  try {
    return localStorage.getItem(PREVIEW_V2_KEY) !== 'off'
  } catch {
    return true
  }
}

/** 设置预览 V2 开关（调试/灰度用；'off' 关闭，其它值或清除恢复默认开启）。 */
export function setPreviewV2Enabled(enabled: boolean): void {
  if (typeof localStorage === 'undefined') return
  try {
    if (enabled) localStorage.removeItem(PREVIEW_V2_KEY)
    else localStorage.setItem(PREVIEW_V2_KEY, 'off')
  } catch {
    // 忽略：存不了就按默认（开启）走
  }
}
