package credential

// ProviderMirrorRefPrefix 托管镜像里 provider 密钥的键前缀（完整键 = 它 + provider id）。
//
// **这是一个持久化格式，不是一个内部常量**：它写进 `<MATE_HOME>/credentials.json` 的键，
// 也回写进 `providers.json` 的 `key_ref`。改动它 = 让**已有的**引用全部指向不存在的键，
// 而失效是静默的（取值落空 → 表现为"密钥没配"）。守卫 `TestProviderMirrorRefFormatIsPinned`
// 把它钉在这里，就是为了让这种改动**必须**是一次有意识的迁移。
const ProviderMirrorRefPrefix = "providers/"

// ProviderRef 某 provider 在托管镜像里的键（`providers/<id>`）。
func ProviderRef(id string) string { return ProviderMirrorRefPrefix + id }
