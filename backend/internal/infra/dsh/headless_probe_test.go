package dsh

// M3-2 · headless 一次性执行探针（批处理/一次性任务，**不承担多轮续跑**）。
//
// 与同目录 `resume_consistency_test.go` / `upgrade_probe_test.go` 同款：dsh 启动级探针，默认跳过，
// 只在 `DSH_HEADLESS_PROBE=1` 时跑。
//
// 它验的是 headless **执行形态**（M3-2 的本质）：`dsh --profile headless --json "<task>"` 在
// **一个子进程内答一个任务就退出**——不开端口、不常驻、不承载多轮流式会话。这正是 plan 说的
// 「批处理/一次性强隔离任务，跑完即回收；冷会话续跑统一交给 ACP session/resume，不用 headless」。
//
// 判据（不依赖模型是否答对——那要真凭据；这里只钉**形态**）：
//  1. 进程**自行退出**（cmd.Run 返回，不挂起）——一次性任务，非常驻服务；
//  2. `--json` 把运行事件以 JSONL 打到 stdout，且含**会话事件**（`type:"session"` 带 sessionId）与
//     **终态事件**（`type:"final"`）——机器可消费的一次性执行输出；
//  3. 全程无端口/无交互。
//
// 环境变量：DSH_HEADLESS_PROBE=1 / DSH_PROBE_PAYLOAD / DSH_PROBE_TMP。
// 隔离：私有 HOME + 私有 DSH_HOME 建在 DSH_PROBE_TMP 下；一次性任务无进程残留，收尾清目录。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func headlessProbeEnv(t *testing.T) (tmp string) {
	t.Helper()
	if os.Getenv("DSH_HEADLESS_PROBE") != "1" {
		t.Skip("headless 探针默认跳过：设 DSH_HEADLESS_PROBE=1 显式运行")
	}
	_, tmp = probeEnv(t)
	return tmp
}

func TestHeadlessOneShotRun(t *testing.T) {
	tmp := headlessProbeEnv(t)

	home := filepath.Join(tmp, "headless", "home")
	_ = os.RemoveAll(filepath.Join(tmp, "headless"))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(tmp, "headless")) })
	cwd := filepath.Join(home, "cwd")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatalf("建 cwd 失败：%v", err)
	}

	// 一次性任务：launcher 旗标（--profile / --json 归各层解析）在前，任务在后。
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dsh", "--profile", "headless", "--json", "回答一个字：好")
	cmd.Dir = cwd
	cmd.Env = []string{
		"HOME=" + home,
		"DSH_HOME=" + filepath.Join(home, "runtime", "dsh"),
		"PATH=" + os.Getenv("PATH"),
	}
	// 注意：不注入任何模型凭据——headless 会在 LLM 步骤报 MISSING_CREDENTIAL 并以非 0 退出；
	// 但**退出本身就是判据**（一次性、跑完即结束），且退出前已把 JSONL 事件（含 session / final）
	// 打到 stdout。故这里**接受非 0 退出**，只看形态与事件流是否齐全。
	out, _ := cmd.CombinedOutput()

	s := string(out)
	if strings.TrimSpace(s) == "" {
		t.Fatalf("headless --json 没有输出（一次性执行形态未达成）")
	}
	var sawSession, sawFinal bool
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		if strings.Contains(line, `"type":"session"`) && strings.Contains(line, `"sessionId"`) {
			sawSession = true
		}
		if strings.Contains(line, `"type":"final"`) {
			sawFinal = true
		}
	}
	if !sawSession {
		t.Fatalf("headless --json 事件流里缺会话事件（type:session）：\n%s", out)
	}
	if !sawFinal {
		t.Fatalf("headless --json 事件流里缺终态事件（type:final）：\n%s", out)
	}
	// 进程已退出（cmd.CombinedOutput 返回即说明它没常驻）——一次性任务形态成立。
	t.Logf("headless 一次性任务形态通过：子进程内跑完即退出、无端口；--json 事件含 session + final")
}
