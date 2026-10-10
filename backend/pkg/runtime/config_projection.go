package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ——— 模型配置文件的落点声明（T5 `config_write`）———
//
// 与既有的 `Projection`（T4，skill 物化）**刻意分开**，两者不能混用：
//
//	Projection（T4） 每个能力一个**目录**，Layout 强制含 `<id>`，平台独占该目录。
//	ConfigProjection（T5） 每个 runtime 一个**文件**（单例：opencode.json / mate-models.patch.yml），
//	  `<id>` 不参与。混用会让 `ValidateProjectionLayout` 直接拒绝，也会让"谁独占这个落点"的判据变浑。
//
// 为什么通道要 per-runtime 声明（而不是"写文件"一种）：三个取值对应三种**机制上不同**的交付方式，
// 而本仓在两个底座上各自选了最不冲突的那一种（ADR-12/ADR-14/ADR-15）：
//
//	config_dir_read —— 底座自己从约定路径读（平台写的文件就是底座读的文件）。
//	cli_patch_arg   —— 作为**启动参数**显式递进去（dsh 的 `--patch <绝对路径>`）：平台写的是
//	                   自己的 overlay，坏了只要去掉参数，底座就回到出厂配置。
//	env_content     —— 作为**内联内容**经环境变量递进去（opencode 的 `OPENCODE_CONFIG_CONTENT`，
//	                   优先级 ⑥，高于项目级发现 ④⑤）：不需要文件就不存在归属冲突，且可撤销。
//
// 后两种让"平台写/注入**自己的**那一份"，于是归属冲突与"这文件是谁的"问题随之消失。

// 配置格式取值（`ConfigProjection.Format`）。每个值对应**唯一一个生成器**（switch 穷举，未知即报错）。
const (
	// ConfigFormatOpencodeJSON opencode 的 `opencode.json`（providers.<route>.{env,settings,models} + 顶层 model）。
	ConfigFormatOpencodeJSON = "opencode_json"
	// ConfigFormatDshPatchYAML dsh 的 patch 文档（`[{id, config}]` 顶层数组；条目目标 `id: acp`）。
	ConfigFormatDshPatchYAML = "dsh_patch_yaml"
)

// ValidConfigFormats 合法格式集合（配置解析与守卫共用；未知格式报错不静默）。
var ValidConfigFormats = map[string]bool{
	ConfigFormatOpencodeJSON: true,
	ConfigFormatDshPatchYAML: true,
}

// 交付通道取值（`ConfigProjection.Channel`）。
const (
	// ConfigChannelDirRead 底座从约定路径读（只写文件的形态）。
	ConfigChannelDirRead = "config_dir_read"
	// ConfigChannelCLIPatchArg 作为启动参数递入（dsh 的 --patch）。
	ConfigChannelCLIPatchArg = "cli_patch_arg"
	// ConfigChannelEnvContent 作为内联内容经环境变量递入（opencode 的 OPENCODE_CONFIG_CONTENT）。
	ConfigChannelEnvContent = "env_content"
)

// ValidConfigChannels 合法通道集合。
var ValidConfigChannels = map[string]bool{
	ConfigChannelDirRead:     true,
	ConfigChannelCLIPatchArg: true,
	ConfigChannelEnvContent:  true,
}

// 生效时刻取值（`ConfigProjection.Effect`）。
const (
	// ConfigEffectNextProcess 新进程启动时读配置（**默认值，最保守**）。
	ConfigEffectNextProcess = "next_process"
	// ConfigEffectNextRequest 底座在下一次请求时重新解析配置（不重启）。
	//
	// 保留这个取值是因为 dsh 的 pi-ai 路由自称「changes take effect on the next request
	// without a restart」——实测若成立，只把这一行写细，不推翻通道模型。
	ConfigEffectNextRequest = "next_request"
)

// ValidConfigEffects 合法生效时刻集合。
var ValidConfigEffects = map[string]bool{
	ConfigEffectNextProcess: true,
	ConfigEffectNextRequest: true,
}

// ConfigProjection 声明某 runtime 的**模型配置文件**（T5 的落点）。
//
// 空 Format = 该 runtime 不承载平台模型清单（T5 不可达 → 条目写面按"未声明"拒绝，
// 前端按 not_applicable 呈现）。
type ConfigProjection struct {
	// Format 生成格式（"opencode_json" | "dsh_patch_yaml"）。
	Format string
	// Root 文件所在目录；支持 `${VAR}` 占位，取值顺序见 ResolveConfigProjection
	// （先看该 runtime 自己的 Launch.Env，再看进程环境）。
	Root string
	// Filename 文件名（不含目录）。
	Filename string
	// Channel 平台**用什么方式**把这份内容交给底座（见文件头的三个取值）。
	Channel string
	// FileChannel 非空 = **仍然写文件**，且这是它承担的角色。
	//
	// 为什么需要它：opencode 的主通道是内联注入（env_content），但文件仍然要写——
	// 它是 `GET /models` 对账的**读回锚点**，也是没有内联通道时的兜底。
	// 值为 config_dir_read 表示"文件按约定路径被底座读"；空 = 该 runtime 不写文件。
	FileChannel string
	// Tested 是否经真实底座**端到端**实测过（未实测 → 只读不写，同 Projection 的纪律）。
	Tested bool
	// TestedOnVersion 实测时的底座版本。**patch 是版本绑定的**：entry id 与字段 schema 随底座
	// 升级漂移（本仓已吃过 `protocol is not configurable`、`imageDetail` 废弃两例），版本一变
	// Tested 失效 → 回落到「只读清单、不写文件」，不允许拿旧结论继续写。
	TestedOnVersion string
	// Effect 生效时刻（空 = next_process，最保守）。
	Effect string
	// Note 实测结论与已知坑（写进诊断面，避免下一个人重新踩）。
	Note string
}

// ConfigProjectionPath 已解析（Root 展开为绝对路径）的配置落点。
type ConfigProjectionPath struct {
	ConfigProjection
	// RootAbs Root 展开后的绝对路径。
	RootAbs string
	// Path 配置文件绝对路径（RootAbs + Filename）。
	Path string
}

// Declared 该 runtime 是否声明了模型配置落点（= T5 是否可达）。
func (c ConfigProjection) Declared() bool { return strings.TrimSpace(c.Format) != "" }

// EffectiveEffect 生效时刻（未声明或非法取值 → 最保守的 next_process）。
func (c ConfigProjection) EffectiveEffect() string {
	if ValidConfigEffects[c.Effect] {
		return c.Effect
	}
	return ConfigEffectNextProcess
}

// Writable 现在能不能真的往这个落点写（未实测 → 只读）。
//
// 这条判据是 v11 唯一一处"用实测结论换写入权"的地方：dsh 侧的失败半径是**底座拒启**
// （写坏 patch = dsh 起不来），所以没实测过就绝不写。
func (c ConfigProjection) Writable() bool { return c.Declared() && c.Tested }

// UnverifiedReason 未实测时给用户看的一句话（Note 为空时兜底，保证界面不出现空原因）。
func (c ConfigProjection) UnverifiedReason() string {
	if c.Note != "" {
		return c.Note
	}
	return "该 runtime 的模型配置落点声明未附实测记录"
}

// ResolveConfigProjection 解析某 runtime 的模型配置落点（唯一解析链）。
//
// `ok=false` = 该 runtime 没声明（T5 不可达，不是错误）。任何**声明了但解析不了**的情况都返回
// error 而不是退回一个"看起来能用"的值——写错落点等于把配置写到别的底座或用户目录里。
//
// 占位符取值顺序：① 该 runtime 自己的 `Launch.Env`（隔离表注入的那份，装机期已合并）→
// ② 进程环境。为什么先看 Launch.Env：`OPENCODE_CONFIG_DIR` / `DSH_HOME` 是平台**注入子进程**的
// 隔离值，进程环境里通常**没有**它们；只看进程环境会解析失败，或更糟——命中用户自己在 shell 里
// 设的同名变量（那正是隔离纪律要避免的：平台会去写用户真实目录）。
func ResolveConfigProjection(spec Spec) (ConfigProjectionPath, bool, error) {
	c := spec.ConfigProjection
	if !c.Declared() {
		return ConfigProjectionPath{}, false, nil
	}
	if !ValidConfigFormats[c.Format] {
		return ConfigProjectionPath{}, false, fmt.Errorf("runtime %s 的模型配置声明了未知格式 %q（合法：%s|%s）",
			spec.ID, c.Format, ConfigFormatOpencodeJSON, ConfigFormatDshPatchYAML)
	}
	if !ValidConfigChannels[c.Channel] {
		return ConfigProjectionPath{}, false, fmt.Errorf("runtime %s 的模型配置声明了未知通道 %q（合法：%s|%s|%s）",
			spec.ID, c.Channel, ConfigChannelDirRead, ConfigChannelCLIPatchArg, ConfigChannelEnvContent)
	}
	if c.FileChannel != "" && !ValidConfigChannels[c.FileChannel] {
		return ConfigProjectionPath{}, false, fmt.Errorf("runtime %s 的模型配置文件通道声明了未知取值 %q", spec.ID, c.FileChannel)
	}
	name := strings.TrimSpace(c.Filename)
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return ConfigProjectionPath{}, false, fmt.Errorf("runtime %s 的模型配置文件名 %q 非法（须是不含目录的普通文件名）",
			spec.ID, c.Filename)
	}
	expanded, err := expandConfigRoot(spec, c.Root)
	if err != nil {
		return ConfigProjectionPath{}, false, fmt.Errorf("runtime %s 的模型配置根 %q 无法解析：%w", spec.ID, c.Root, err)
	}
	out := ConfigProjectionPath{ConfigProjection: c, RootAbs: expanded, Path: filepath.Join(expanded, name)}
	return out, true, nil
}

// expandConfigRoot 展开配置根里的 `${VAR}`（先看 spec.Launch.Env，再看进程环境）。
//
// 与 `ExpandProjectionRoot` 的差别只在**取值来源**（多一层 Launch.Env），校验口径逐字相同：
// 占位符未设置就报错，不猜值；展开后必须是绝对路径。
func expandConfigRoot(spec Spec, root string) (string, error) {
	launch := map[string]string{}
	for _, kv := range spec.Launch.Env {
		if name, val, ok := strings.Cut(kv, "="); ok {
			launch[strings.TrimSpace(name)] = val
		}
	}
	lookup := func(name string) (string, bool) {
		if v, ok := launch[name]; ok && strings.TrimSpace(v) != "" {
			return v, true
		}
		return os.LookupEnv(name)
	}
	out, err := ExpandProjectionRoot(root, lookup)
	if err != nil {
		return "", fmt.Errorf("%w（提示：该 runtime 的隔离 env 里应当有对应变量，见 pkg/runtime/isolation.go）", err)
	}
	return out, nil
}
