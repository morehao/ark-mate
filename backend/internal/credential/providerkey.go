package credential

import "strings"

// ——— 供应商拨号密钥的词汇（v3.2 刀 8）———
//
// 这三样都是**值**，不是解析实现：一条密钥的解析结果、它在托管文档里的键、托管文档自己的
// 文件名。它们要出现在领域快照（`provider.Def.KeySource`）、诊断文案与存档文件里，所以归值层；
// 留在机制里会让领域为了一个字符串反向依赖基础设施。

// ProviderKey 一条 provider 拨号密钥的**解析结果**。
type ProviderKey struct {
	// Value 生效的明文（绝不落档案文件）。
	Value string
	// Source 这把密钥是从哪一层来的（空 = 没有密钥）。
	Source Source
	// EnvKey 生效值来自哪个环境变量名（仅 Source == env 时有值；诊断面展示用）。
	EnvKey string
	// Problems 解析过程中的问题（分层报"某层读不了"、库与镜像不一致等）；非空不等于失败。
	Problems []string
}

// ProviderMirrorRefPrefix 托管镜像里 provider 密钥的键前缀（完整键 = 它 + provider id）。
//
// **这是一个持久化格式，不是一个内部常量**：它写进 `<MATE_HOME>/credentials.json` 的键，
// 也回写进 `providers.json` 的 `key_ref`。改动它 = 让**已有的**引用全部指向不存在的键，
// 而失效是静默的（取值落空 → 表现为"密钥没配"）。守卫 `TestProviderMirrorRefFormatIsPinned`
// 把它钉在这里，就是为了让这种改动**必须**是一次有意识的迁移。
const ProviderMirrorRefPrefix = "providers/"

// ProviderRef 某 provider 在托管文档里的键（`providers/<id>`）。
func ProviderRef(id string) string { return ProviderMirrorRefPrefix + id }

// CredentialsFileName 托管镜像的文件名（`<MATE_HOME>/<它>`）。
const CredentialsFileName = "credentials.json"

// OneKeyInParens 把一个可能为空的名字渲染成括号后缀（空 → 空串，不留一对空括号）。
func OneKeyInParens(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	return "（" + name + "）"
}
