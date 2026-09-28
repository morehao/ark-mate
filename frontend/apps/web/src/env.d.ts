/// <reference types="vite/client" />

/**
 * 构建期注入的前端构建 id（M2-4；值由 vite.config.ts 的 `define` 给出）。
 *
 * 只读它，不要自己算一遍：版本闸门比的就是"编译进 bundle 的这个值"与"核心实际读到的
 * `build-id.txt`"，两边各自计算就失去意义了。
 */
declare const __FORGE_WEB_BUILD_ID__: string
