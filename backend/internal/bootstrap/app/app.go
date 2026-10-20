// Package app 是控制面应用的**应用壳**（v3.1 §3.4）。
//
// 职责边界（v3.1 起）：
//   - cmd/mate：进程级分发（把参数交给 cli，把返回码交还 shell）；
//   - internal/bootstrap/cli：命令树（参数解析、输出、退出码）；
//   - internal/bootstrap/assembly：**唯一装配根**（对象图 + 启停序列）；
//   - 本包：server 启停、ready 行、信号与优雅关闭。**不构造 service/store/controller，不注册路由**。
//
// 为什么壳还在：`assembly.Build` 只把对象装好（含监听器与 http.Handler），而"把 Handler 挂到
// `http.Server`、在监听成功后回写就绪行、按 ctx 优雅停机"这三件事只在进程交付面成立——
// 命令树里的 `doctor`/`migrate` 也要装配产物，但它们不需要 HTTP。
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/credential"
	"github.com/morehao/ark-mate/internal/dto/dtosystem"
	"github.com/morehao/golib/glog"
)

// App 控制面应用壳：持有装配产物与 HTTP 生命周期。
type App struct {
	cfg     config.Config
	sources config.Sources

	// creds 凭据表（M1-1）：桌面态由命令层经 --auth-stdin 注入；独立态由 MATE_TOKEN 折叠而来。
	// **不进 config.Config**——配置面会被 `mate config --json` 打印出来，令牌不该出现在那里。
	creds *credential.Set

	// payloads 桌面态已解析的随包运行时载荷清单（M3-4）：**唯一真源是壳**，核心只把它透出到
	// `/meta` 的诊断面。非桌面态为 nil → `/meta` 照发空数组。
	payloads []dtosystem.PayloadInfo

	// readyStdout 是否在监听成功后往 stdout 打一行 `MATE_READY {...}`（M1-2，桌面壳据此拿地址）。
	readyStdout bool

	// res 本次装配的产物（`Init` 之后非 nil）。
	res *assembly.BuildResult
	srv *httpServer

	closeOnce sync.Once
}

// Option 装配可选项（M1-1/M1-2）。
type Option func(*App)

// WithCredentials 注入凭据表（桌面态双凭据；独立态通常不传，走 MATE_TOKEN 折叠）。
func WithCredentials(creds *credential.Set) Option {
	return func(a *App) { a.creds = creds }
}

// WithReadyStdout 开启就绪行回写（桌面壳 spawn 核心时传 `--ready-stdout`）。
func WithReadyStdout(on bool) Option {
	return func(a *App) { a.readyStdout = on }
}

// WithPayloads 注入桌面壳**已解析**的随包载荷清单（M3-4；壳经 `--payloads` 交来）。
//
// 只收结构化值：JSON 解析与校验在命令层（照 WithCredentials 的分层），
// 本包因此既不需要认识 wire 形状，也不会因为"某一层少校验一条"而透出假来源。
func WithPayloads(payloads []dtosystem.PayloadInfo) Option {
	return func(a *App) { a.payloads = payloads }
}

// New 构造 App（只记录配置与可选项，不做任何 IO）。
func New(cfg config.Config, sources config.Sources, opts ...Option) *App {
	a := &App{cfg: cfg, sources: sources}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Handler 返回底层 HTTP handler（供测试挂载/断言路由）。
func (a *App) Handler() http.Handler {
	if a.srv == nil {
		return nil
	}
	return a.srv.Handler()
}

// Result 返回本次装配的产物（`Init` 之前为 nil）。
func (a *App) Result() *assembly.BuildResult { return a.res }

// Meta 返回当前诊断面快照（供测试与诊断命令断言，等价于 `GET /meta` 的 data）。
func (a *App) Meta() dtosystem.MetaInfo {
	if a.res == nil {
		return dtosystem.MetaInfo{}
	}
	return a.res.Probe.Meta()
}

// Init 装配全部运行时依赖（委托 `assembly.Build`），并把 HTTP handler 挂到监听器上。
//
// 装配失败返回的 `*assembly.ExitError` 带退出码，由命令层决定进程结局；HTTP 服务本身
// 仍由本包启停（`Run`/`Close`），装配根不持有 `http.Server`。
func (a *App) Init(ctx context.Context) error {
	res, err := assembly.Build(ctx, a.cfg, a.sources,
		assembly.WithCredentials(a.creds), assembly.WithPayloads(a.payloads))
	if err != nil {
		return err
	}
	a.res = res
	a.srv = newHTTPServerOn(res.Handler.Listener, res.Handler.Handler)
	return nil
}

// Run 启动 HTTP 服务并阻塞至 ctx 结束（信号或调用方取消），随后优雅停机。
func (a *App) Run(ctx context.Context) error {
	if a.srv == nil {
		return errors.New("app: Run 前必须先 Init")
	}
	// 优雅退出：ctx 结束 → 停 HTTP；返回后由 Close 关全部装配产物（回收 runtime 子进程）。
	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
		_ = a.srv.Stop(stopCtx)
	}()

	// MCP 自动探测（B4；MATE_MCP_AUTO_PROBE=startup，默认 off）：HTTP 起来后后台逐个探测，
	// 不阻塞服务启动；结果与显式 POST /mcps/:id/probe 一致（写 Tool 目录 + 探测状态）。
	if a.cfg.McpAutoProbe == config.McpAutoProbeStartup {
		go func() {
			glog.Infow(ctx, "serve: MCP 自动探测已启用（startup 模式，后台逐个探测已注册 server）")
			_ = a.res.Probe.AutoProbeMCP(ctx)
		}()
	}

	// 就绪行（M1-2）：监听器在装配期就绑好了，这里打的是**实际**地址（`:0` 时是内核分配的端口）。
	// 只走 stdout 一行、不含任何令牌；壳按行首 `MATE_READY ` 匹配，拿不到就退回 health 轮询。
	if a.readyStdout {
		if line, err := ReadyLine(a.srv.Addr()); err != nil {
			glog.Warnw(ctx, "serve: 组装就绪行失败，壳将退回 health 轮询", "err", err)
		} else {
			fmt.Println(line)
		}
	}

	if err := a.srv.Start(); err != nil {
		return assembly.FatalErr("serve: %v", err)
	}
	return nil
}

// Close 释放装配的资源：先停 HTTP（关监听器），再按启动逆序关闭全部步骤。
func (a *App) Close() {
	a.closeOnce.Do(func() {
		if a.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), stopGrace)
			defer cancel()
			_ = a.srv.Stop(ctx)
		}
		if a.res != nil {
			a.res.Close()
		}
	})
}

// 确保 time 包在只读诊断路径里被用到（`stopGrace` 的类型来自 server.go）。
var _ = time.Second
