package credential

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// 凭据的**取值与对账**面。搬到机制层后仍需要库句柄（`st`），所以 `Store` 多持一个
// 装配期注入的引用——与 `definition.Mechanism` 同形（都是"装配期注入、运行期只读"）。

// MatchEnvKey 找出**哪一个**候选环境变量给出了这个值（用与分层同一把取值函数，避免两处口径分叉）。
func (m *Store) MatchEnvKey(keys []string, value string) string {
	if value == "" {
		return ""
	}
	env := os.Getenv
	if m.Layered() != nil && m.Layered().Env != nil {
		env = m.Layered().Env
	}
	for _, k := range keys {
		if v := env(k); strings.TrimSpace(v) != "" && v == value {
			return k
		}
	}
	return ""
}

// SyncCredentialMirror 把库里的凭据**并进**托管镜像，返回改动的条目数（幂等）。
//
// **非破坏性**：只覆盖/补齐库里有值的那几条，**不删**镜像里已有的其它键。理由有两条：
//
//   - "镜像有而库里没有"是 `mate doctor` 要**报出来**的一种事实（用户手写过、或某次镜像
//     删除失败过），装配期顺手删掉它，那条判据就永远看不到东西了；
//   - 镜像里有用户手写的键，抹掉它就是替用户删数据（与"搬运不删旧密文"同一条纪律）。
//
// 库里没有的键**绝不回灌**（设计 §3）：镜像不是事实源，回灌只会把一份陈旧文件变成权威。
func (m *Store) SyncCredentialMirror(ctx context.Context, reason string) (int, error) {
	if m.st == nil {
		return 0, nil // memory 模式：镜像就是事实源，绝不用库（没有库）去覆盖它。
	}
	doc := m.Doc()
	if doc == nil {
		return 0, nil
	}
	return SyncMirror(ctx, m.st, doc, reason)
}

// ResolveStoreSecret 解析一条 `store:<ns>/<name>` 引用（MCP 密钥面）。
//
// 与 provider 密钥不同，它**没有环境变量锚点**（引用是定义体里写死的），所以没有四层回落：
// 有库时查库（权威），没有库时查镜像文档（memory 模式文件即事实源）。库里没有而镜像里有
// 时**不采用**镜像那一份（理由同 provider：清了密钥不能被陈旧镜像撤销），只把这件事说清楚。
func (m *Store) ResolveStoreSecret(ctx context.Context, ref string) (string, error) {
	if m.st != nil {
		v, ok, err := m.st.McpSecret(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("读库里的密钥失败：%w", err)
		}
		if ok && strings.TrimSpace(v) != "" {
			return v, nil
		}
		if _, mirrored := MirrorValue(m.Doc(), ref); mirrored {
			return "", fmt.Errorf("托管文档里有 %s 而库里没有（镜像陈旧，或写入/删除曾失败）：**不采用**"+
				"镜像里那一份；在面板里重新填写该 MCP 的密钥即可对齐", ref)
		}
		// 出路**只写用户今天真能做到的那一条**：库里 `mcp_secrets` 的值这一批**还没有写入口**
		// （REST/CLI/面板都没有），所以"把值写进库"对用户不是一条出路——写它等于让人去找一个
		// 不存在的入口。能改的是定义里的**引用**，而 `env:` 那一层由进程环境/`.env` 提供。
		return "", fmt.Errorf("库里没有这条密钥（引用 %s）：在能力面板里把该 MCP 的引用改成 "+
			"env:<ENV_NAME>（值由进程环境或 .env 提供）", ref)
	}
	v, ok := MirrorValue(m.Doc(), ref)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("托管文档里没有这条密钥（引用 %s）：memory 模式下它就在 "+
			"`<MATE_HOME>/%s` 里，或把定义里的引用改成 env:<ENV_NAME>", ref, CredentialsFileName)
	}
	return v, nil
}
