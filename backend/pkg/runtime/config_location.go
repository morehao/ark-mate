package runtime

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ——— 模型清单配置的**落点**声明（只有位置，不表达格式） ———
//
// 与 v11 的 `ConfigProjection` 的关键差别：这里**只回答「写到哪里」**，不再回答「写成什么格式、
// 走哪条通道、生效时刻、有没有实测」。
//
// 为什么要把后四样删掉（ADR-2/ADR-11）：格式与通道是**该底座的私有知识**——某个底座用哪个复数键、
// 另一个底座用的是 `- id: ...` 与 `--patch` 参数，这些只有写配置的人（adapter）知道。
// 把它们留在平台内核里，等于每接一个底座就要改内核（T2 不成立），而且内核里会长期维护两套
// 外部 schema。谁写字节谁承担格式，这条边界由 archtest 的 `TestInternalHasNoRuntimeConfigLiterals`
// 钉住；落点之所以还留在 `Spec`，是因为**路径的占位符只能靠该 runtime 自己的隔离 env 解出来**
// （adapter 拿不到注册表，约束⑦），所以解析必须发生在父包。
//
// 与 `Projection`（skill 物化）刻意分开，两者不能混用：
//
//	Projection     每个能力一个**目录**，Layout 强制含 `<id>`，平台独占该目录。
//	ModelConfig    每个 runtime 一个**文件**（单例；文件名由该 adapter 在 `Spec.ModelConfig` 里给），
//	               `<id>` 不参与。混用会让 `ValidateProjectionLayout` 直接拒绝，也会让
//	               「谁独占这个落点」的判据变浑。

// ModelConfigLocation 某 runtime 的清单配置落点。
type ModelConfigLocation struct {
	// Root 目录，支持 `${VAR}` 占位（取值只看该 runtime 自己的 `Launch.Env`，
	// **刻意不回落到进程环境**：回落会让平台写到用户自己的 `~/.dsh` / `~/.config/opencode`）。
	Root string
	// Filename 文件名（不含目录；含目录分隔符或为 `.`/`..` 即报错）。
	// **空 = 该 runtime 不承载平台模型清单**（T5 不可达）。
	Filename string
	// CredentialDoc 原生凭据文档的**相对路径**（空 = 该底座不走原生凭据文档）；相对 Root。
	//
	// 为什么放在这里：它与清单配置同住一个平台独占根，占位符也只有一个（该 runtime 的 home）；
	// 分成两份声明只会让"谁负责解析 `${DSH_HOME}`"这件事出现两种答案。
	// 占位符形态仍保留在 `spec.Credential.NativeDoc` 里一份给诊断面用。
	CredentialDoc string
}

// ConfigLocationPath 已解析（占位符展开为绝对路径）的落点。
type ConfigLocationPath struct {
	// RootAbs Root 展开后的绝对路径。
	RootAbs string
	// Path 清单配置文件绝对路径（RootAbs + Filename）。
	Path string
	// CredentialDocPath 原生凭据文档绝对路径（未声明 = 空）。
	CredentialDocPath string
}

// Declared 该 runtime 是否声明了清单配置落点（= 平台模型清单对它是可达的）。
//
// 判据是"三个字段里**任意一个**非空"而不是"Filename 非空"：只写了 Root 却忘了 Filename 是一种
// **半截声明**，必须在 `ResolveConfigLocation` 里报错（fail-loud），而不是被静默当成"该底座不
// 承载清单"——后者会让用户看到"保存成功、模型永远不生效"而没有任何线索。
func (m ModelConfigLocation) Declared() bool {
	return strings.TrimSpace(m.Root) != "" || strings.TrimSpace(m.Filename) != "" || strings.TrimSpace(m.CredentialDoc) != ""
}

// ResolveConfigLocation 解析某 runtime 的清单配置落点（唯一解析链）。
//
// `ok=false` = 该 runtime 没声明（不是错误）。任何**声明了但解析不了**的情况都返回 error 而不是
// 退回一个"看起来能用"的值——写错落点等于把配置写到别的底座或用户目录里。
//
// 占位符取值顺序：**只有**该 runtime 自己的 `Launch.Env`（隔离表注入的那份，装机期已合并）。
// 为什么刻意不看进程环境：`<变量>` 是平台经隔离表**注入子进程**的值（如 dsh 的 `DSH_HOME`），
// 进程环境里通常没有它们；只看进程环境会解析失败，或更糟——命中用户自己在 shell 里设的同名变量
// （那正是隔离纪律要避免的：平台会去写用户真实目录）。详见 `expandConfigRoot`。
func ResolveConfigLocation(spec Spec) (ConfigLocationPath, bool, error) {
	m := spec.ModelConfig
	if !m.Declared() {
		return ConfigLocationPath{}, false, nil
	}
	name := strings.TrimSpace(m.Filename)
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return ConfigLocationPath{}, false, fmt.Errorf("runtime %s 的清单配置文件名 %q 非法（须是不含目录的普通文件名）",
			spec.ID, m.Filename)
	}
	expanded, err := expandConfigRoot(spec, m.Root)
	if err != nil {
		return ConfigLocationPath{}, false, fmt.Errorf("runtime %s 的清单配置根 %q 无法解析：%w", spec.ID, m.Root, err)
	}
	out := ConfigLocationPath{RootAbs: expanded, Path: filepath.Join(expanded, name)}
	if doc := strings.TrimSpace(m.CredentialDoc); doc != "" {
		// 凭据文档是**相对 Root** 的一个文件（可以带一层目录）。校验它与文件名同级的两种坏形态：
		// 绝对路径与 `..` 逃逸——两者都会让平台写到平台独占根之外。
		if filepath.IsAbs(doc) {
			return ConfigLocationPath{}, false, fmt.Errorf("runtime %s 的原生凭据文档 %q 非法（须是相对路径，相对 Root）",
				spec.ID, m.CredentialDoc)
		}
		rel := filepath.Clean(doc)
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ConfigLocationPath{}, false, fmt.Errorf("runtime %s 的原生凭据文档 %q 非法（不得逃出 Root）",
				spec.ID, m.CredentialDoc)
		}
		out.CredentialDocPath = filepath.Join(expanded, rel)
	}
	return out, true, nil
}

// expandConfigRoot 展开配置根里的 `${VAR}`——**只看该 runtime 自己的 `Launch.Env`**。
//
// 为什么**刻意不看进程环境**（这与 `Resolve` 的投影根解析不同，是有意的偏离）：
// 这些落点变量都是平台经隔离表注入**子进程**的值（如 dsh 的 `DSH_HOME`）。若这里回落到进程环境，
// 那么在"隔离还没应用"的任何路径上（测试、误装配、未来的新入口），平台就会解析到**用户自己**的
// `~/.dsh` / `~/.config/opencode` ——然后往里写配置文件。那是 AGENTS.md 明令禁止的行为，
// 而且**静默**：只有路径看起来"正常"。
//
// 所以这里 **fail-closed**：解析不到就报错（提示先过 `ApplyIsolationEnv`），绝不猜、绝不回落。
func expandConfigRoot(spec Spec, root string) (string, error) {
	launch := map[string]string{}
	for _, kv := range spec.Launch.Env {
		if name, val, ok := strings.Cut(kv, "="); ok {
			launch[strings.TrimSpace(name)] = val
		}
	}
	lookup := func(name string) (string, bool) {
		v, ok := launch[name]
		if !ok || strings.TrimSpace(v) == "" {
			return "", false
		}
		return v, true
	}
	out, err := ExpandProjectionRoot(root, lookup)
	if err != nil {
		return "", fmt.Errorf("%w（提示：该 runtime 的隔离 env 里应当有对应变量——平台只在**自己的**隔离根上物化，"+
			"绝不回落到进程环境或用户自己的目录；请确认装配期已调用 Registry.ApplyIsolationEnv）", err)
	}
	return out, nil
}
