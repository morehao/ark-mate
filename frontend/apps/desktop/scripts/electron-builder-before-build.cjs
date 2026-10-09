/**
 * electron-builder 的 `beforeBuild` 钩子（M4-1）：返回 `false` = "node_modules 由我们自己管"。
 *
 * ## 为什么是 false（而不是只写 `npmRebuild: false`）
 *
 * `apps/desktop` **没有任何生产依赖**（`dependencies` 为空，由 `internal/archtest` 的前端边界
 * 守卫钉住）：运行时要用的东西——前端资产、`mate-core`、随包载荷、私有 bin——全部由本仓脚本
 * 装配好，经 `extraResources` 进包（见 `electron-builder.yml`）。所以"帮忙装依赖/重建原生件"
 * 这件事对我们永远是空操作。返回 `false` 让 electron-builder 做两件事：
 *
 * 1. **跳过依赖安装与重建**（等价于 `npmRebuild: false`，但那条会提前 return，反而让第 2 条失效）；
 * 2. **跳过 node module collector**：electron-builder 26 在打包前会起包管理器列一遍依赖树
 *    （pnpm → `pnpm list --prod --json --depth Infinity`），把生产依赖收进 asar。我们一个都不需要，
 *    而这条在受限环境里会**整包失败**：pnpm 11 的 store index 是 SQLite（`<store>/index.db`），
 *    打包进程若打不开它（只读挂载、缓存目录不在可写区、CI 里没预装包管理器缓存等），
 *    electron-builder 直接以 `Node module collector process exited with code 1` 中止——
 *    症状与"我们的包少文件"毫无关系，纯属被无关的包管理器状态拖死（实测踩到）。
 *
 * 代价说清楚：**将来 apps/desktop 若真的加了生产依赖**，这个钩子会让它们**不进包**。
 * 那时有两条路：把依赖改成随包资产（像载荷那样显式装配，本项目的既定形态），或删掉这个钩子
 * 并保证打包环境里包管理器可用。`dependencies` 为空有 archtest 守卫，所以"悄悄加依赖"会先红。
 */

module.exports = async function beforeBuild() {
  return false
}
