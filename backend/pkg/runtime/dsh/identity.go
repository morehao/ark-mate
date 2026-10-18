// 本文件是 dsh 的**产品身份交付实现**：把 `pkg/identity.Spec` 写成平台自己的
// `mate-identity.patch.yml`，经 `--patch` 递入 dsh 的 `system-prompt` 条目。
//
// 为什么走 dsh 的官方配置扩展点而不是改源码：`@deepseek-ai/dsh-system-prompt` 的
// `includeHarnessIdentity` / `personaPrefix` 是 dsh 自己的配置面（ACP bundle 自己也用同一种
// `- id: system-prompt` patch 覆盖过 persona）。改源码/fork 会把"跟上游"变成长期维护成本，
// 而平台需要的东西（一句产品身份）恰好是这块配置能表达的。
//
// 三条边界（与 config.go 同款）：
//
//  1. 本包是纯数据子包：不 import `internal/**`（archtest 的 TestAdapterHasNoStoreLiterals 守），
//     也不 import 父包 `pkg/runtime`（父子 import 环）；
//  2. 启动面只能经 `PatchArgs` 改（`--patch` 与模型清单共用一个 flag）；
//  3. **启动期不跑 `--dump-config-schema`**：身份 patch 只有两个键、目标是已实测存在的条目，
//     每次启动多一次 dump 会把启动预算吃掉。正确性由静态校验 + golden + 版本 pin 保证，
//     真实 dsh 校验只在一次性 probe/CI 跑（见 .dsh/docs/notes/probes/）。
package dsh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pkgidentity "github.com/morehao/ark-mate/pkg/identity"
	"github.com/morehao/ark-mate/pkg/resource"
)

const (
	// IdentityPatchFilename 身份 overlay 的文件名（平台独占根下的一个文件）。
	//
	// 与模型清单的 `mate-models.patch.yml` 分开是刻意的：两个交付面的失败半径与生效面不同
	// （清单写坏会让底座拒启，身份写坏只影响模型怎么自称），合成一份文件会让"撤掉身份"
	// 必须动模型清单那份字节。
	IdentityPatchFilename = "mate-identity.patch.yml"

	// identityEntryID dsh 里承载系统提示的 entry id（dsh-base 的 `cordis.patch.yml` 有它，
	// ACP bundle 也已经用它覆盖过 `personaPrefix`）。
	identityEntryID = "system-prompt"

	// MaxIdentityPatchBytes 身份 overlay 的静态上限（8 KiB）。
	//
	// 它挡的不是磁盘，而是**配置膨胀**：这段字每轮都进系统提示，写长了是每轮重复的 token 成本。
	// 超限即拒绝交付（而不是截断）——截断会把一句话切成半句，模型的自述会变成一段残句。
	MaxIdentityPatchBytes = 8 << 10
)

// IdentityDeclaration 身份交付面的**声明**（纯数据：实测版本 + 已知边界）。
//
// 与 `Credential.TestedOnVersion` 同一条纪律：版本一变就必须重跑一次性 probe 并把这里改掉；
// `archtest` 的守卫会拿它和载荷 pin 逐字比对（不一致即红），所以"升级了载荷但没重测"不会
// 静默留在代码里。
type IdentityDeclaration struct {
	// PatchFilename 产物文件名（相对该 runtime 的平台独占根）。
	PatchFilename string
	// Tested 该交付面是否已端到端实测（探针跑过一次真实 dsh）。
	Tested bool
	// TestedOnVersion 实测时的载荷版本。
	TestedOnVersion string
	// Note 实测结论与已知边界（写进代码，避免下一个人重新踩）。
	Note string
}

// Identity 身份交付面的声明。取值随一次性 probe 的结论更新（见 Note）。
var Identity = IdentityDeclaration{
	PatchFilename:   IdentityPatchFilename,
	Tested:          true,
	TestedOnVersion: "0.2.1-rc.2",
	Note: "一次性 probe（2026-10-18，dsh 0.2.1-rc.2，临时 DSH_HOME + `--patch` + `--dump-config-schema`）：" +
		"① `- id: system-prompt` 是**已注册条目**（dsh-base 的 cordis.patch.yml 有它，ACP bundle 也用它覆盖 personaPrefix）；" +
		"② `includeHarnessIdentity: false` + `personaPrefix` 两个键被 schema 接受，dump 退出码 0、`x-cordis.complete=true`、诊断为空；" +
		"③ 未知 `{{...}}` 变量在**首轮组装**时才抛错（不是启动时）——所以静态校验必须在落盘前拦掉 `{{`/`}}`，否则表现为每次会话都失败；" +
		"④ 空/只有注释的 patch 会让底座拒启（与模型清单同一条坑），所以撤回必须是「删文件 + 摘参数」；" +
		"⑤ 启动期**不跑** dump（用户已拍板）：这个文件只有两个键、且目标条目在 pin 版本上已验证。",
}

// IdentityOptions 造一个身份交付实现所需的**已解析落点**。
//
// 路径由父包解析（`ResolveConfigLocation(spec).RootAbs`）后传入：adapter 拿不到注册表，
// 也不该自己拼 HOME——那条路会写到用户自己的 `~/.dsh`。
type IdentityOptions struct {
	// RootAbs 该 runtime 的平台独占根（身份 patch 落在它下面）。
	RootAbs string
}

// IdentityAdapter 本包的身份交付实现（结构上满足 `pkg/runtime.IdentityAdapter`）。
type IdentityAdapter interface {
	DeliverIdentity(ctx context.Context, spec pkgidentity.Spec) (resource.Outcome, error)
}

// NewIdentity 构造身份交付实现。patches 必须与模型 syncer 是**同一个实例**。
func NewIdentity(opts IdentityOptions, patches *PatchArgs) IdentityAdapter {
	if patches == nil {
		patches = NewPatchArgs(nil)
	}
	root := strings.TrimSpace(opts.RootAbs)
	var patchPath string
	if root != "" {
		patchPath = filepath.Join(root, IdentityPatchFilename)
	}
	return &identityAdapter{patchPath: patchPath, patches: patches}
}

type identityAdapter struct {
	patchPath string
	patches   *PatchArgs
}

// DeliverIdentity 交付 / 撤回产品身份。
//
// 三条行为（与 `IdentityAdapter` 契约一一对应）：
//   - `SystemPrompt` 为空（`user` / `off` 模式）→ **撤回**：删文件 + 从 `--patch` 摘掉；
//   - 校验不过（含 `{{`/`}}`、NUL、超长、空串）→ 返回错误，一个字节都不写；
//   - 内容与上一份逐字节相同 → `unchanged`（幂等；不重写、不重装）。
func (a *identityAdapter) DeliverIdentity(ctx context.Context, spec pkgidentity.Spec) (resource.Outcome, error) {
	if a.patchPath == "" {
		return resource.Outcome{
			Status:   resource.StatusUnsupported,
			Where:    "未声明身份落点",
			Effect:   resource.EffectNextProcess,
			Problems: []string{"该 runtime 没有声明平台独占根：身份不会被交付（首轮块仍可用）"},
		}, nil
	}
	if !spec.UsesSystemPrompt() {
		return a.revoke()
	}
	data, err := buildIdentityPatch(spec)
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("生成 dsh 身份 overlay 失败（未写任何字节）：%w", err)
	}
	changed, err := writeIfChanged(a.patchPath, data)
	if err != nil {
		return resource.Outcome{}, err
	}
	abs, err := filepath.Abs(a.patchPath)
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("解析 dsh 身份 overlay 绝对路径失败（%s）：%w", a.patchPath, err)
	}
	// 参数每次都要装：启动参数住在注册表内存里，重启后必须靠一次交付重建（幂等由 PatchArgs 判）。
	argChanged, err := a.patches.SetIdentity(abs)
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("装载 dsh 身份启动参数失败（%s）：%w", PatchFlag, err)
	}
	status := resource.StatusApplied
	problems := []string{
		"仅做静态校验（未跑底座级配置校验）：身份 patch 只有两个键，且目标条目在载荷 pin 版本上已验证",
	}
	if !changed && !argChanged {
		status = resource.StatusUnchanged
		problems = append(problems, "内容与上一份相同（幂等重放）：没有重写任何字节")
	}
	return resource.Outcome{
		Status:   status,
		Where:    a.patchPath + "（平台自有 overlay，经 " + PatchFlag + " 以绝对路径递入）",
		Effect:   resource.EffectNextProcess,
		Problems: problems,
	}, nil
}

// revoke 撤回身份：**删文件 + 摘参数**。
//
// 为什么不能"什么都不做"：上一份 patch 还在、`--patch` 还装着，底座照旧读旧系统提示——用户把
// 模式切到 `user`/`off` 之后，模型仍会说 ark-mate，而界面上没有任何一行解释这件事。
// 为什么不能"写一份空 patch"：某底座逐字说空/只有注释的 patch 会 **fails boot**。
func (a *identityAdapter) revoke() (resource.Outcome, error) {
	existed := false
	if _, err := os.Stat(a.patchPath); err == nil {
		existed = true
	} else if !os.IsNotExist(err) {
		return resource.Outcome{}, fmt.Errorf("检查 dsh 身份 overlay 失败（%s）：%w", a.patchPath, err)
	}
	argChanged, err := a.patches.SetIdentity("")
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("摘除 dsh 身份启动参数失败（%s）：%w", PatchFlag, err)
	}
	if existed {
		if err := os.Remove(a.patchPath); err != nil && !os.IsNotExist(err) {
			return resource.Outcome{}, fmt.Errorf("删除 dsh 身份 overlay 失败（%s）：%w", a.patchPath, err)
		}
	}
	status := resource.StatusUnchanged
	problems := []string{"身份未启用：未写入身份 overlay"}
	if existed || argChanged {
		status = resource.StatusApplied
		problems = []string{"身份已撤回：overlay 文件不存在、启动参数已摘除"}
	}
	return resource.Outcome{
		Status:   status,
		Where:    a.patchPath + "（已撤回：文件不存在、启动参数已摘除）",
		Effect:   resource.EffectNextProcess,
		Problems: problems,
	}, nil
}

// buildIdentityPatch 生成身份 overlay 的字节（确定性的手写 YAML，不引第三方 YAML 库）。
//
// 形态来自 dsh 自己的 patch 语法：
//
//   - id: <entry>      —— 目标是已存在的条目（不存在时 dsh 只 WARN 跳过，退出码仍是 0）
//     config:          —— **整行替换**该条目的 config，不与该行原值合并
//
// 所以这里**只写两个键**：`includeHarnessIdentity: false`（去掉固定开场白）与 `personaPrefix`
// （产品身份段）。刻意不写 `includeRuntimeContext`（默认 true，cwd 等必需上下文要保留）、
// 也不写 `toolOrder` / `complete`——那些是底座自己的行为，平台没有理由替它决定。
func buildIdentityPatch(spec pkgidentity.Spec) ([]byte, error) {
	if err := validateIdentity(spec); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("# ark-mate 生成的产品身份 overlay（平台自有文件；经 dsh --patch 递入）\n")
	b.WriteString("# 只覆盖 " + identityEntryID + "；模型清单是另一份平台 overlay，两者互不覆盖。\n")
	b.WriteString("# 归属标记：本文件只有平台会写；撤回时**删文件 + 摘参数**（空 patch 会让底座拒启）。\n")
	b.WriteString("- id: " + identityEntryID + "\n")
	b.WriteString("  config:\n")
	b.WriteString("    includeHarnessIdentity: false\n")
	b.WriteString("    personaPrefix: |-\n")
	for _, line := range strings.Split(spec.SystemPrompt, "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("      " + line + "\n")
	}
	data := b.Bytes()
	if len(data) > MaxIdentityPatchBytes {
		return nil, fmt.Errorf("身份 patch 超过 %d 字节上限（%d）：这段字每轮都进系统提示，写长了是重复的 token 成本",
			MaxIdentityPatchBytes, len(data))
	}
	if err := validateIdentityPatch(data); err != nil {
		return nil, err
	}
	return data, nil
}

// validateIdentity 落盘前的静态校验（**没有这一层就会让底座拒启或每轮失败**）。
//
// 判据集合：
//   - 正文非空、长度在预算内；
//   - 不含 `{{` / `}}`：底座的 persona 会走模板插值，未知变量在**首轮组装**时抛错；
//   - 不含 NUL：YAML 块标量里的 NUL 会让解析器在读到它时失败；
//   - 不含 `!!js`：YAML 显式标签能构造出「看起来是字符串、实际是可执行片段」的值，静态文案
//     没有理由出现它。
func validateIdentity(spec pkgidentity.Spec) error {
	text := spec.SystemPrompt
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("身份正文为空")
	}
	// 两组花括号都要拦：`{{x}}` 是插值、`{% x %}` 是标签。底座的 persona 走模板渲染，
	// 未知变量/标签在**首轮组装**时才抛错——那时用户已经点了发送，只会看到一条莫名的失败。
	if pkgidentity.HasTemplateBraces(text) || strings.Contains(text, "{%") || strings.Contains(text, "%}") {
		return errors.New("身份正文含模板标记（`{{`/`}}` 或 `{%`/`%}`）：底座的 persona 是模板，未解析项会在首轮组装时抛错")
	}
	if strings.ContainsRune(text, 0) {
		return fmt.Errorf("身份正文含 NUL 字节")
	}
	if strings.Contains(text, "!!js") {
		return fmt.Errorf("身份正文含 YAML 显式标签 `!!js`")
	}
	return nil
}

// validateIdentityPatch 生成后的**形状自校验**：把字节读回来判断"是不是预期那份"。
//
// 不引 YAML 解析器（零新依赖 + 键序必须可钉），所以判据落在这份产物仅有的几个结构事实上：
// 顶层是数组、目标条目 id 对、两个键逐字出现。三条都 fail-closed——任何一个缺失都说明生成器
// 被改坏了，而那种坏法在 dsh 侧的表现是"静默跳过整条 patch"（退出码仍是 0）。
func validateIdentityPatch(data []byte) error {
	text := string(data)
	for _, must := range []string{
		"- id: " + identityEntryID + "\n",
		"    includeHarnessIdentity: false\n",
		"    personaPrefix: |-\n",
	} {
		if !strings.Contains(text, must) {
			return fmt.Errorf("生成的 patch 缺少必要形态 %q", strings.TrimSpace(must))
		}
	}
	return nil
}

// writeIfChanged 原子写一份文件；内容与上一份相同则不重写（返回 changed）。
func writeIfChanged(path string, data []byte) (bool, error) {
	prev, err := os.ReadFile(path)
	switch {
	case err == nil:
		if bytes.Equal(prev, data) {
			return false, nil
		}
	case os.IsNotExist(err):
	case err != nil:
		return false, fmt.Errorf("读上一份 dsh 身份 overlay 失败（%s）：%w", path, err)
	}
	if err := writeFileAtomic(path, data, patchPerm); err != nil {
		return false, fmt.Errorf("写 dsh 身份 overlay 失败（%s）：%w", path, err)
	}
	return true, nil
}
