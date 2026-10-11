/**
 * 构建期注入的前端构建 id（M2-4；值由本包的 `vite.config.ts` 的 `define` 给出）。
 *
 * **声明留在注入方**（P5 迁包时定下来的边界）：界面代码已经搬进 `@ark-mate/console`，而"往 bundle
 * 里塞一个常量"是**这个应用**的构建行为，不是界面包的性质——所以由入口读一次、作为参数交给界面包
 * （`bootstrap({ webBuildId })`），包那边不再有 `typeof __MATE_WEB_BUILD_ID__` 这种"读 app 注入的全局"。
 *
 * 刻意不引 `vite/client`：引了等于给 `import.meta.env` 开类型门，而"只在这一种构建里生效"那个维度
 * 正是 P1 取消掉的（守卫 `TestDevLoopServesBuiltArtifact` 明确禁止 UI 出现 `import.meta.env`）。
 * 不引它，谁再写 `import.meta.env` 会**当场编译失败**，不必等守卫来拦。
 */
declare const __MATE_WEB_BUILD_ID__: string
