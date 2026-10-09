package capability

import (
	"fmt"
	"regexp"
	"strings"
)

// ——— §13.3 M1/M6：密钥引用（明文 vs 密钥在类型上分开） ———
//
// 为什么需要：ark-mate 原先只有一个 `McpServer.Headers`，于是"非密钥的 header（如
// `X-Client: mate`）"和"密钥（如 `Authorization: Bearer sk-…`）"**在同一个字段里**，
// 谁也没法在类型层面说清哪个是秘密——结果就是密钥明文躺在 `<CapabilityDir>/mcp/*.json` 里，
// 而读模型只好整个 `headers` 都不返回（要么泄密要么没用）。
//
// 分开之后规矩才立得住：`Env`/`Headers` 是**明文**（可读回、可展示、可 diff），
// `SecretEnv`/`SecretHeaders` 的**值只能是引用**，永远不接受明文。引用两种：
//
//	vault:<namespace>/<name>  平台托管密文（AES-256-GCM，见 §5）；定义文件里只有引用，
//	                          密钥本体在 `<mate_home>/vault/<ns>/<name>.enc`
//	env:<ENV_NAME>            进程环境变量（容器/CI 注入；盘上什么都没有）
//
// 这一层只做**词法**校验（纯字符串，不碰磁盘/不碰密钥材料），解析成明文发生在注入前一刻，
// 由 internal/manager 调 vault 完成——`pkg/` 不认识 `internal/vault`，"谁能解密"这件事
// 留在应用层，能力数据面只负责"什么算合法的引用"。

// 引用前缀。
const (
	SecretRefVaultPrefix = "vault:"
	SecretRefEnvPrefix   = "env:"
)

// secretRefNameRE 引用的 ns/name 词法：与 `internal/vault.ValidateRef` 同一套规则
// （`^[A-Za-z0-9._-]{1,64}$`，且 `.`/`..` 非法）。两处一致性由 manager 侧的
// TestSecretRefLexiconMatchesVault 断言——同一个引用不能出现"数据面说合法、vault 说非法"。
var secretRefNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// envNameRE 环境变量名（POSIX 风格：字母/下划线开头）。
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SecretRefKind 报告引用类型："vault" / "env" / ""（既不是前者也不是后者）。
func SecretRefKind(ref string) string {
	switch {
	case strings.HasPrefix(strings.TrimSpace(ref), SecretRefVaultPrefix):
		return "vault"
	case strings.HasPrefix(strings.TrimSpace(ref), SecretRefEnvPrefix):
		return "env"
	default:
		return ""
	}
}

// ValidateSecretRef 校验一条引用的词法（不检查密钥是否真的存在——那是注入前的事）。
func ValidateSecretRef(ref string) error {
	r := strings.TrimSpace(ref)
	if r == "" {
		return fmt.Errorf("密钥引用为空（值只能是 %s<namespace>/<name> 或 %s<ENV_NAME>）",
			SecretRefVaultPrefix, SecretRefEnvPrefix)
	}
	switch SecretRefKind(r) {
	case "vault":
		body := strings.TrimPrefix(r, SecretRefVaultPrefix)
		ns, name, ok := strings.Cut(body, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("vault 引用 %q 需为 %s<namespace>/<name>", r, SecretRefVaultPrefix)
		}
		for _, part := range []string{ns, name} {
			if !secretRefNameRE.MatchString(part) || part == "." || part == ".." {
				return fmt.Errorf("vault 引用 %q 的 %q 非法（允许字母数字 . _ -，1–64 位，且不得为 . / ..）", r, part)
			}
		}
		return nil
	case "env":
		name := strings.TrimSpace(strings.TrimPrefix(r, SecretRefEnvPrefix))
		if !envNameRE.MatchString(name) {
			return fmt.Errorf("env 引用 %q 的环境变量名非法（须字母/下划线开头，只含字母数字下划线）", r)
		}
		return nil
	default:
		return fmt.Errorf("密钥引用 %q 非法：值只能是 %s<namespace>/<name> 或 %s<ENV_NAME>，"+
			"**不接受明文密钥**（明文写在定义文件里等着落盘就是这条规则要防的事）",
			r, SecretRefVaultPrefix, SecretRefEnvPrefix)
	}
}

// ValidateSecretBindings 校验一组「键 → 引用」绑定（键非空、值必须是合法引用）。
// field 只用于错误文案（如 "secret_env"）。
func ValidateSecretBindings(field string, bindings map[string]string) error {
	keys := make([]string, 0, len(bindings))
	for k := range bindings {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("capability mcp: %s 含空键名", field)
		}
		if err := ValidateSecretRef(bindings[k]); err != nil {
			return fmt.Errorf("capability mcp: %s[%q] 非法：%w", field, k, err)
		}
	}
	return nil
}

// ValidatePlainBindings 校验一组明文字典（键/值都不得为空——空值几乎总是配置写漏了）。
func ValidatePlainBindings(field string, bindings map[string]string) error {
	keys := make([]string, 0, len(bindings))
	for k := range bindings {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("capability mcp: %s 含空键名", field)
		}
		if strings.TrimSpace(bindings[k]) == "" {
			return fmt.Errorf("capability mcp: %s[%q] 的值为空（要么给值，要么删掉这一项）", field, k)
		}
	}
	return nil
}

// sortStrings 本地排序（避免为一个工具函数引入依赖；调用方都只关心确定性错误顺序）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// KeysOf 返回字典的键（排序；读模型只回 key 时用）。
func KeysOf(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
