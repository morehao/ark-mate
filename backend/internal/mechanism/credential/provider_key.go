package credential

import (
	"github.com/morehao/ark-mate/internal/infra/credentials"
)

// ProviderKey 一条 provider 拨号密钥的**解析结果**。
//
// 放在凭据机制里而不是 provider 域：它是"四层回落 + 库对账"这件事的产物，字段全部在描述
// **凭据本身**（值 / 来自哪一层 / 来自哪个环境变量 / 有哪些问题），与 provider 档案的形状无关。
// provider 域经 `domain/provider → mechanism/credential` 消费它（向下 ✓）。
type ProviderKey struct {
	// Value 生效的明文（绝不落档案文件）。
	Value string
	// Source 这把密钥是从哪一层来的（空 = 没有密钥）。
	Source credentials.Source
	// EnvKey 生效值来自哪个环境变量名（仅 Source == env 时有值；诊断面展示用）。
	EnvKey string
	// Problems 解析过程中的问题（分层报"某层读不了"、库与镜像不一致等）；非空不等于失败。
	Problems []string
}
