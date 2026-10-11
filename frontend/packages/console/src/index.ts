/**
 * `@ark-mate/console` 的对外入口（P5 / Stage 1，2026-10-11）。
 *
 * 这个包是**产品界面本身**：应用外壳、领域视图、状态与传输。`apps/web` 只剩入口 + 组装
 * （挂载点与构建配置），于是"界面"第一次成为一个**可被守卫、可被第二个消费者复用**的东西——
 * 而不是 `apps/web` 的私有源码。为什么这件事值得做，见 `.dsh/docs/plans/2026-10-11-dev-delivery-parity-plan.md` §4-D7：
 * `internal/archtest/frontend_boundary_test.go` 禁止 `packages/*` 反向引 `apps/*`，所以只要界面还在
 * `apps/web/src` 里，任何非 `apps/web` 的消费者（第二个窗口形态、测试夹具）就**够不到**它。
 *
 * **导出面刻意只有一个**：入口。领域模块不从这里透出——它们之间用包内相对路径协作（搬迁保留原
 * 目录结构，所以这一步是纯移动、行为零变化）。要新增导出面，先想清楚"谁会消费"，别把内部结构
 * 变成对外契约。
 */
export { bootstrap } from './bootstrap'
