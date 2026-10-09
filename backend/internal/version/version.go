// Package version 是核心版本与 API 契约版本的唯一落点。
//
// 为什么单独一个叶子包：`/v1/mate/meta`（M1-7）与 `release.json`（M4-1）都要读它，
// 而它不该依赖任何应用层——`-ldflags` 注入也就只需要改这一个变量。
package version

// Version 核心版本号。开发构建为 `dev`；发布构建由 Makefile 注入：
//
//	go build -ldflags "-X github.com/morehao/ark-mate/internal/version.Version=<tag>"
//
// （M4-1 的 `release.json` 与 `mate-core --version` 都取这里，因此它是**唯一**版本源。）
var Version = "dev"

// APIContract /v1/mate 对外契约的版本标识。
//
// 与 Version 分开：核心可以出很多个版本而 REST/WS 契约不变；版本闸门（设计 §6.7）比的是
// **前端构建 id ↔ 核心回报值**，而这个常量回答"契约本身是哪一代"——它变了才需要前端同步升级。
const APIContract = "v1"
