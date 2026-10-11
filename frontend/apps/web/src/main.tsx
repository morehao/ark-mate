import { bootstrap } from '@ark-mate/console'
import { UNKNOWN_BUILD_ID } from '@ark-mate/desktop-contract'

// 构建期注入的构建 id（本包 vite.config.ts 的 `define`）。取不到就按 unknown——与核心同一口径。
const webBuildId = typeof __MATE_WEB_BUILD_ID__ === 'string' ? __MATE_WEB_BUILD_ID__ : UNKNOWN_BUILD_ID

// 入口只做一件事：把构建 id 交给 bootstrap（先过版本闸门，再挂载）。
void bootstrap({ webBuildId })
