// Package web 是前端构建产物的宿主：**两种来源，一套行为**（设计 §6.1）。
//
// 来源（只有一个机制能命中，不搜索、不回落）：
//   - **构建期内置**：`-tags release` 时 `//go:embed dist`（见 web_embed_release.go）；
//   - **显式目录**：`--web-dir` / `MATE_WEB_DIR` 指向一份已构建的 `apps/web/dist`
//     （桌面形态由壳指过来，独立部署也可指向 nginx 那份）。
//
// 两者的**差别只在来源**：缓存语义、SPA 回退、`/v1/*` 的信封 404 全由 internal/router 的
// 资产层统一实现（M2-2 的一致性断言就是钉这一条）。本包只回答"有没有、在哪"。
//
// 谁在什么形态下用什么：
//
//	独立二进制（make all）  → 内嵌（无需 --web-dir）
//	容器镜像（Dockerfile）  → 内嵌（构建期 -tags release）
//	桌面核（make core/desktop）→ --web-dir（壳传包内那一份，核不内嵌）
//	开发联调（make dev）    → --web-dir frontend/apps/web/dist（默认 tag 没有内嵌）
//	什么都不给             → 拒绝启动（退出码 3），见 internal/router.ResolveAssets
package web
