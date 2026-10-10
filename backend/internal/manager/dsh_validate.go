package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— T5 的 dsh 校验门（G5/G6）：把候选文件**作为 `--patch` 递进去跑一次 dump** ———
//
// 为什么必须有这一级，而且判据不能只看退出码：
//
//	dsh 的 patch 层有三条**静默失败**，全部不改退出码（vendor/include/src/index.ts）：
//	  ① 目标 id 不存在 → `warn('patch: entry "x" not found')` 然后 **continue 跳过整条**；
//	  ② `name` 与目标不符 → 同样跳过；
//	  ③ patch 里除 `id` 外的键被**原样拷到目标 entry 上** ⇒ 把 `config` 拼成 `configs` 会被当成
//	     entry 的一个新属性挂上去，`config` 一动不动，**完全无声**。
//	所以判据是 **exitCode == 0 且 `x-cordis.diagnostics` 为空 且 `x-cordis.complete == true`**。
//	只看 exitCode 会把"补丁没打到任何一行"当成成功（`id: acpx` 那条负例正是这个形态）。
//
// 判据落在 `--dump-config-schema` 上（**已对 0.2.0-rc.2 实测核对**）：只有它输出 JSON 并带
// `x-cordis.{complete,diagnostics}`；`--dump-config` 输出的是 YAML、诊断只写 stderr。
// 它还揭示了一条容易写错的语义：**`complete=false` 才会把退出码置 1，diagnostics 非空不会**——
// 所以"退出码为 0"绝不足以判通过（实测：`id: acpx` 时 exit=0 且只有一条 warning 诊断）。
//
// 还有一条机制依据让这一级不是"看起来合理"：dsh 的 `include` 实现逐字说它的 patch 语义
// 「shared by mounting (applyPatches) and offline config tooling (dsh --dump-config) **so a dump can
// never drift from what boots**」（vendor/include/src/index.ts）。也就是说 dump 走的**就是**启动那条路。
//
// 三条纪律：
//   - **必须在 staging `DSH_HOME` 上跑**：`--dump-config` 是 boot-free 的，但 `prepareProfile` 会建
//     profile 目录；在活跃的 `${MATE_HOME}/runtime/dsh` 上跑等于去动正在服务的底座 home。
//   - **在候选上跑、不在成品上跑**：`--patch` 的语义就是"额外覆盖一层"，所以"生成 → 当 --patch 校验
//     → 通过才落到真正的位置"是天然顺序，不需要先写到位再回滚。
//   - **失败不落盘**：校验门在 `MaterializeRuntimeConfig` 的 ② 里调用，失败即中止（一个字节都不写）。

// dshDumpResult 一次 `--dump-config` 的结论。
type dshDumpResult struct {
	ExitCode int
	// Complete `x-cordis.complete`（false = 有 entry 没被解析成完整形态）。
	Complete bool
	// Diagnostics `x-cordis.diagnostics` 的逐条文本（**空**才是通过）。
	Diagnostics []string
	// Raw 原始输出（截断后进错误文案，便于用户自己看）。
	Raw string
}

// dshValidator 校验门的可注入面（测试用假命令；生产走真 dsh）。
type dshValidator struct {
	// command/args 覆盖（nil = 用 spec 的启动形态）。
	command string
	args    []string
	// stagingRoot 覆盖 staging 目录的父目录（测试用 t.TempDir）。
	stagingRoot string
	timeout     time.Duration
}

// validateDshPatchCandidate 把候选字节递进去跑一次 dump，返回结论。
func (s *Service) validateDshPatchCandidate(ctx context.Context, spec runtime.Spec, candidate []byte, v dshValidator) (*dshDumpResult, error) {
	cmd := v.command
	if cmd == "" {
		cmd = spec.Launch.Command
	}
	timeout := v.timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	root := v.stagingRoot
	if root == "" {
		// staging 必须**与活跃 home 分开**：放在某个 runtime 目录的旁边而不是它里面。
		root = filepath.Join(os.TempDir(), "mate-dsh-validate")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(root, "stage-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	cand := filepath.Join(staging, "candidate.patch.yml")
	if err := os.WriteFile(cand, candidate, 0o600); err != nil {
		return nil, err
	}

	args := append([]string{}, v.args...)
	if v.args == nil {
		// 用启动形态的**非 patch 部分** + `--patch <候选> --dump-config`。
		// 用 **`--dump-config-schema`**：只有它输出 JSON 并带 `x-cordis.{complete,diagnostics}`；
		// `--dump-config` 输出的是 YAML（诊断只写 stderr），判据不好机器化。
		// 代价：schema dump 会 import 各插件模块（可能初始化 profile）——所以 staging home 是必需的。
		args = append(stripPatchArg(spec.Launch.Args), patchFlag, cand, "--dump-config-schema")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(cctx, cmd, args...)
	c.Dir = staging
	// 隔离 env：把 DSH_HOME 指到 staging（**绝不在活跃 home 上跑**），HOME 指到 staging。
	env := make([]string, 0, len(spec.Launch.Env)+4)
	for _, kv := range spec.Launch.Env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(strings.TrimSpace(name), "DSH_HOME") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "DSH_HOME="+filepath.Join(staging, "dsh"), "HOME="+staging)
	c.Env = mergeChildEnv(os.Environ(), env)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr

	runErr := c.Run()
	res := &dshDumpResult{}
	if ee, ok := runErr.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
	} else if runErr != nil {
		return nil, fmt.Errorf("manager: 跑 dsh 校验门失败（%s）: %w", cmd, runErr)
	}
	raw := stdout.String()
	if len(raw) > 8000 {
		raw = raw[:8000] + "…（截断）"
	}
	res.Raw = raw
	if se := strings.TrimSpace(stderr.String()); se != "" {
		res.Diagnostics = append(res.Diagnostics, "stderr: "+firstLine(se))
	}
	// 解析 `x-cordis`（dump 的输出是 JSON；解析不了就是"无法校验"→ 记诊断而不是当成通过）。
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		res.Diagnostics = append(res.Diagnostics, "dump 输出不是 JSON：无法校验")
		return res, nil
	}
	xc, _ := doc["x-cordis"].(map[string]any)
	if xc == nil {
		res.Diagnostics = append(res.Diagnostics, "dump 输出里没有 x-cordis：无法校验")
		return res, nil
	}
	if b, ok := xc["complete"].(bool); ok {
		res.Complete = b
	}
	switch d := xc["diagnostics"].(type) {
	case []any:
		for _, item := range d {
			res.Diagnostics = append(res.Diagnostics, diagnosticText(item))
		}
	case nil:
	default:
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("%v", d))
	}
	return res, nil
}

// dshDumpPassed 校验门的判据（三条同时成立）。
func dshDumpPassed(res *dshDumpResult) bool {
	return res != nil && res.ExitCode == 0 && res.Complete && len(res.Diagnostics) == 0
}

// diagnosticText 把一条诊断渲染成人话（形状不稳，尽量兼容）。
func diagnosticText(item any) string {
	switch v := item.(type) {
	case string:
		return v
	case map[string]any:
		level := bodyString(v, "level")
		msg := bodyString(v, "message")
		if msg == "" {
			msg = bodyString(v, "text")
		}
		if path := bodyString(v, "path"); path != "" {
			msg = path + ": " + msg
		}
		if level != "" {
			return level + " " + msg
		}
		return msg
	}
	return fmt.Sprintf("%v", item)
}

// mergeChildEnv 合并子进程环境：**显式给的 env 赢、且每个键只出现一次**。
//
// 与 `pkg/acl` 的子进程 env 合并同一语义（POSIX getenv 取第一条、Node 取最后一条——同名两条会让
// 同一份环境在 Go 子进程与 JS 子进程里语义相反）。这里只需要"显式值赢"，所以重复实现 20 行，
// 而不是把 acl 的内部工具导出成公共 API。
func mergeChildEnv(base, override []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(override))
	for _, kv := range override {
		name, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(strings.TrimSpace(name))
		if up == "" || seen[up] {
			continue
		}
		seen[up] = true
		out = append(out, kv)
	}
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(strings.TrimSpace(name))
		if up == "" || seen[up] {
			continue
		}
		seen[up] = true
		out = append(out, kv)
	}
	return out
}

// firstLine 取一段文本的首行（诊断文案里只放一行，避免把 stderr 全量灌进响应）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// validateDshCandidateIfWritable 在物化时按需跑校验门（仅 dsh 的 cli_patch_arg 通道）。
//
// 幂等与代价：校验门要拉起一次 dsh（秒级）。它只在该 runtime **可写**（已实测）时才会被走到，
// 所以"首版只读"这条路径上它一次都不跑——那也正是"未实测 → 只读"的意义：**不写就不用校验**。
func (s *Service) validateDshCandidateIfWritable(ctx context.Context, spec runtime.Spec, proj runtime.ConfigProjectionPath, data []byte) error {
	if proj.Channel != runtime.ConfigChannelCLIPatchArg {
		return nil
	}
	res, err := s.validateDshPatchCandidate(ctx, spec, data, dshValidator{})
	if err != nil {
		return err
	}
	if dshDumpPassed(res) {
		return nil
	}
	parts := []string{fmt.Sprintf("退出码 %d", res.ExitCode)}
	if !res.Complete {
		parts = append(parts, "x-cordis.complete=false")
	}
	if len(res.Diagnostics) > 0 {
		parts = append(parts, strings.Join(res.Diagnostics, "；"))
	}
	return fmt.Errorf("manager: dsh 校验门拒绝这份候选（%s）", strings.Join(parts, "，"))
}
