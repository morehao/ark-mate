/**
 * 壳的本地偏好（M1-6）：目前只有两项，都存在 `userData/desktop-prefs.json`。
 *
 * 为什么不引 electron-store 之类：本仓对前端/壳的依赖一向"能不引就不引"（C12/C14），
 * 而这里要存的就是两个布尔值。写入用**同目录 staging + rename**（原子替换），
 * 避免"写一半断电"留下损坏的 JSON 让下次启动读不出来。
 */
import { app } from 'electron'
import { mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

export type Prefs = {
  /** 是否已经给过"关窗只是隐藏到托盘"的提示（T10：只提示一次，用户勾了"不再提示"）。 */
  trayNoticeShown?: boolean
  /** 开机自启：**默认不写登录项**，只有用户在托盘菜单里显式开启才为 true（设计 §8）。 */
  openAtLogin?: boolean
}

function prefsFile(): string {
  return join(app.getPath('userData'), 'desktop-prefs.json')
}

/** 读偏好；文件缺失/损坏一律当空偏好（壳不该因为一个偏好文件起不来）。 */
export function readPrefs(): Prefs {
  try {
    const raw = readFileSync(prefsFile(), 'utf8')
    const parsed = JSON.parse(raw) as Prefs
    return typeof parsed === 'object' && parsed !== null ? parsed : {}
  } catch {
    return {}
  }
}

/** 合并写入并返回新值。 */
export function writePrefs(patch: Prefs): Prefs {
  const next = { ...readPrefs(), ...patch }
  const file = prefsFile()
  try {
    mkdirSync(dirname(file), { recursive: true })
    const staging = `${file}.staging-${process.pid}`
    writeFileSync(staging, JSON.stringify(next, null, 2) + '\n', 'utf8')
    renameSync(staging, file)
  } catch (e) {
    console.warn(`[shell] 写偏好失败（不影响本次运行）：${String(e)}`)
  }
  return next
}
