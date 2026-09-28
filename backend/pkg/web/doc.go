// Package web 是前端构建产物的 go:embed 宿主（release 构建用），承载
// backend 单二进制内嵌前端静态资源的能力。
//
// # 产物来源
//
// dist/ 由仓库根 Makefile 的 `make web` 从 frontend/apps/web 构建并拷贝而来
// （`cp -r frontend/apps/web/dist backend/pkg/web/dist`），不入库。
//
// # 编译形态
//
// web.go 内嵌声明仅随 `-tags release`（即 `make all` 单二进制交付）编译；
// 默认构建不带该 tag，后端可独立起服、不依赖前端产物是否存在。
//
// # 前端的三种运行形态
//
//   - dev 联调：前端跑 vite dev server（proxy /v1 -> 127.0.0.1:10001），后端只管 API；
//   - 独立部署：直接托管 frontend/apps/web/dist 到任意静态宿主（nginx 等），
//     前端经可配置 API base（VITE_API_BASE）指向后端，后端按需开 CORS；
//   - 单二进制：make all 先拷贝 dist 再以 -tags release 编译，本包 embed 进产物。
package web
