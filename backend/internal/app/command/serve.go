package command

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/morehao/ark-forge/internal/app"
	"github.com/morehao/ark-forge/internal/credential"
)

// serve 启动控制面：REST（列表 + conversations create/prompt/cancel）+ WS /ws/events。
// 会话动作由 Orchestrator 跑真实 ACP runtime；事件经 eventbus 出流。
//
// 本函数只做三件事：解析子命令参数、把配置交给 internal/app 装配、按退出码收尾。
// 依赖装配与启停生命周期全在 app 包（见其包注释的分层说明）。
func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "监听地址（默认读 FORGE_ADDR/`127.0.0.1:10001`；对外暴露请显式指定并自担无鉴权风险）")
	turnTimeout := fs.Duration("turn-timeout", 0, "单轮 turn 看门狗超时（0=关闭；默认读 FORGE_TURN_TIMEOUT）")
	authStdin := fs.Bool("auth-stdin", false,
		"从 stdin 首行读取桌面态双凭据（JSON {\"restToken\":\"…\",\"streamToken\":\"…\"}，各 32 字节随机 base64url）；"+
			"令牌因此不进 argv/env/文件/URL。解析失败或两者之一为空 → 退出码 2")
	readyStdout := fs.Bool("ready-stdout", false,
		"监听成功后往 stdout 打一行 `FORGE_READY {\"addr\":\"127.0.0.1:<port>\",\"port\":<port>}`（不含令牌）；"+
			"桌面壳据此立刻拿到地址，不传则壳退回 health 轮询")
	webDir := fs.String("web-dir", "",
		"前端资产目录（绝对路径，须含 index.html；同 FORGE_WEB_DIR）。不传则用构建期内置资产；"+
			"两者都没有 → 拒绝启动（退出码 3）。显式给了目录就**只**用它，不回落内嵌")
	exitWithParent := fs.Bool("exit-with-parent", false,
		"父进程消失即自动退出（POSIX：父进程死后被 reparent，`Getppid` 随之变化）；"+
			"桌面壳用 `kill -9` 强杀时靠它避免留下孤儿核心与 runtime 子进程。Windows 无 reparent 语义，那里要等 Job Object（M4-3）")
	_ = fs.Parse(args)

	cfg, sources := mustConfig()
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *turnTimeout > 0 {
		cfg.TurnTimeout = *turnTimeout
	}
	// CLI flag 优先级最高（与 --addr 同一条规则：默认 < config.json < env < FORGE_* < flag）。
	if *webDir != "" {
		cfg.WebDir = *webDir
	}

	opts := []app.Option{}
	if *readyStdout {
		opts = append(opts, app.WithReadyStdout(true))
	}
	if *authStdin {
		creds, err := readCredentialStdin(os.Stdin)
		if err != nil {
			exitWith(&app.ExitError{Code: 2, Err: err})
		}
		opts = append(opts, app.WithCredentials(creds))
	}

	// 优雅退出：SIGINT/SIGTERM → 停 HTTP → 关编排器（回收 runtime 子进程）。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 父进程自保（M1-6）：桌面壳被强杀时不会发 SIGTERM，核心必须自己发现"爹没了"并退出，
	// 否则它和它手里的 runtime 子进程会一起变孤儿。取消 ctx 走的是与 SIGTERM 同一条优雅停机路径。
	if *exitWithParent {
		// 壳一死，核心的 stdout/stderr 就没读者了——不忽略 SIGPIPE 的话，停机过程中的
		// 第一条日志就会把进程当场打死，优雅停机形同虚设（见 ignoreSIGPIPE 的注释）。
		ignoreSIGPIPE()
		go watchParentExit(ctx, os.Getppid, parentPollInterval, func() {
			slog.Warn("serve: 父进程已消失（壳被强杀？），开始优雅停机")
			cancel()
		})
	}

	a := app.New(cfg, sources, opts...)
	if err := a.Init(ctx); err != nil {
		exitWith(err)
	}
	defer a.Close()

	if err := a.Run(ctx); err != nil {
		exitWith(err)
	}
}

// readCredentialStdin 读 stdin **首行** JSON 双凭据（M1-1 的桌面握手）。
//
// 为什么是 stdin：argv 会被 `ps` 看到、env 会被 runtime 子进程继承、文件会落盘——
// stdin 是三者里唯一"传完即走"的通道（壳 spawn 核心后立即关掉写端）。读完首行即返回，
// **不再读 stdin**：那之后 stdin 不归核心使用。
//
// 两个令牌都必须非空（桌面态两个面都要），且互不相同（同串会让两个面失去区分，
// 由 credential.New 拒绝）。
func readCredentialStdin(r io.Reader) (*credential.Set, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("--auth-stdin: 读 stdin 失败: %w", err)
		}
		return nil, errors.New("--auth-stdin: stdin 没有首行（期望一行 JSON：{\"restToken\":\"…\",\"streamToken\":\"…\"}）")
	}
	line := strings.TrimSpace(sc.Text())
	if line == "" {
		return nil, errors.New("--auth-stdin: stdin 首行为空（期望一行 JSON 双凭据）")
	}
	var hs struct {
		RestToken   string `json:"restToken"`
		StreamToken string `json:"streamToken"`
	}
	if err := json.Unmarshal([]byte(line), &hs); err != nil {
		return nil, fmt.Errorf("--auth-stdin: stdin 首行不是合法 JSON（%v）", err)
	}
	if hs.RestToken == "" || hs.StreamToken == "" {
		return nil, errors.New("--auth-stdin: restToken 与 streamToken 都必须非空")
	}
	creds, err := credential.New(
		credential.Pair{Token: hs.RestToken, Scopes: credential.ScopeREST},
		credential.Pair{Token: hs.StreamToken, Scopes: credential.ScopeStream},
	)
	if err != nil {
		return nil, fmt.Errorf("--auth-stdin: %w", err)
	}
	return creds, nil
}

// exitWith 按装配错误自带的退出码收尾（配置类 2、运行期 1），未标注的按 1 处理。
func exitWith(err error) {
	var ee *app.ExitError
	code := 1
	if errors.As(err, &ee) {
		code = ee.Code
	}
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(code)
}
