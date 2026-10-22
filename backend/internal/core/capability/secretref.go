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
//	store:<namespace>/<name>  平台托管（v20 起事实源是库里的 `mcp_secrets`；
//	                          `<MATE_HOME>/credentials.json` 是它的 0600 可重建镜像）；
//	                          定义文件里只有引用，密钥本体不在定义文件里
//	env:<ENV_NAME>            进程环境变量（容器/CI 注入；盘上什么都没有）
//
// 这一层只做**词法**校验（纯字符串，不碰磁盘/不碰密钥材料），解析成明文发生在注入前一刻，
// 由机制层（`internal/drivers/mechanism`）与用例层查库完成——能力数据面不认识
// `internal/persistence/store`，"密钥从哪来"这件事
// 留在应用层，能力数据面只负责"什么算合法的引用"。

// 引用前缀。
const (
	// SecretRefStorePrefix 平台托管引用的前缀（v20 起）。
	SecretRefStorePrefix = "store:"
	// SecretRefEnvPrefix 进程环境变量引用的前缀。
	SecretRefEnvPrefix = "env:"
	// SecretRefLegacyVaultPrefix v20 **之前**的前缀，指向已被删除的那套机制（密文文件 + 主密钥）。
	//
	// 它只被下面的 `TranslateLegacySecretRef` 使用，且**这是全仓唯一一处翻译点**：
	// 库里的 `mcp_servers.body` 由 v20 迁移改写前缀，但**档案文件**（`providers.json`、
	// 能力目录里的 `mcp/*.json`）不在库里、迁移改不到，只能在导入它们时显式翻译并告警。
	SecretRefLegacyVaultPrefix = "vault:"
)

// secretRefNameRE 引用的 ns/name 词法：`^[A-Za-z0-9._-]{1,64}$`，且 `.`/`..` 非法。
// 两处一致性由 manager 侧的 TestSecretRefLexiconMatchesStore 断言——同一个引用不能出现
// "数据面说合法、存储层说非法"。
var secretRefNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// envNameRE 环境变量名（POSIX 风格：字母/下划线开头）。
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SecretRefKind 报告引用类型："store" / "env" / ""（既不是前者也不是后者）。
func SecretRefKind(ref string) string {
	switch {
	case strings.HasPrefix(strings.TrimSpace(ref), SecretRefStorePrefix):
		return "store"
	case strings.HasPrefix(strings.TrimSpace(ref), SecretRefEnvPrefix):
		return "env"
	default:
		return ""
	}
}

// TranslateLegacySecretRef 把 v20 之前的旧前缀显式翻成 `store:`，返回 (新引用, 是否翻过)。
//
// **它是全仓唯一一处翻译点**（导入路径的两个调用方都必须经它，见 `manager` 侧的导入器）：
// 旧前缀指的是一个已被删除的机制，留着它等于让定义文件指向不存在的东西；而"翻译"必须
// 是显式的——静默当合法会让下一个人以为那套机制还在。
//
// 只在**导入档案文件**时用：库里的引用已由迁移改写，运行时读到旧前缀就是坏数据（应当报错）。
func TranslateLegacySecretRef(ref string) (string, bool) {
	r := strings.TrimSpace(ref)
	if !strings.HasPrefix(r, SecretRefLegacyVaultPrefix) {
		return ref, false
	}
	return SecretRefStorePrefix + strings.TrimPrefix(r, SecretRefLegacyVaultPrefix), true
}

// LegacySecretRefWarning 旧前缀被翻译时该说的那句话（**文案与字面量收在同一处**：
// 调用方只负责说"是在哪个文件/哪条定义上翻的"，不重复写前缀）。
//
// 它刻意说清三件事，而不是只说"已翻译"：旧前缀指的是**一个已被删除的机制**；档案文件不在
// 库里、迁移改不到它们；翻译只发生在这条导入路径上。
func LegacySecretRefWarning(ref string) string {
	return fmt.Sprintf("密钥引用 %q 用的是 v20 之前的前缀 %q：那套机制（密文文件 + 主密钥）已在 v20 删除。"+
		"档案文件不在库里、迁移改不到它，所以这里**就地翻成** %q（磁盘上的那一份这次不改——"+
		"下一次导出/重写会带上新前缀）",
		ref, SecretRefLegacyVaultPrefix, SecretRefStorePrefix)
}

// ValidateSecretRef 校验一条引用的词法（不检查密钥是否真的存在——那是注入前的事）。
func ValidateSecretRef(ref string) error {
	r := strings.TrimSpace(ref)
	if r == "" {
		return fmt.Errorf("密钥引用为空（值只能是 %s<namespace>/<name> 或 %s<ENV_NAME>）",
			SecretRefStorePrefix, SecretRefEnvPrefix)
	}
	switch SecretRefKind(r) {
	case "store":
		body := strings.TrimPrefix(r, SecretRefStorePrefix)
		ns, name, ok := strings.Cut(body, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("store 引用 %q 需为 %s<namespace>/<name>", r, SecretRefStorePrefix)
		}
		for _, part := range []string{ns, name} {
			if !secretRefNameRE.MatchString(part) || part == "." || part == ".." {
				return fmt.Errorf("store 引用 %q 的 %q 非法（允许字母数字 . _ -，1–64 位，且不得为 . / ..）", r, part)
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
			r, SecretRefStorePrefix, SecretRefEnvPrefix)
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
