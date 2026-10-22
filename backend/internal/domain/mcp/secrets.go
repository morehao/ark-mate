package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/redact"
)

// Secrets 密钥解析面：把定义里的**引用**变成**最终注入的明文**。
//
// 它是全仓**唯一**把 MCP 密钥引用变成明文的地方（`ResolveRef` 一处开关），所以脱敏登记
// （`redact.RegisterDynamicSecret`）也挂在这里：凡是被解析过的值，离开进程前必被替换。
//
// 依赖只有一个：`mechanism`（"`store:` 引用指向哪一行"的知识在库里/托管文档里，
// 那不属于本平面）。**没有库句柄、没有注册表**——所以它搬进域里不需要任何新端口。
type Secrets struct {
	creds SecretResolver
}

// NewSecrets 用凭据机制造一个密钥解析面（装配期注入同一个实例：两份会让同一份引用解析出不同结果）。
func NewSecrets(creds SecretResolver) *Secrets { return &Secrets{creds: creds} }

// Resolve 把 server 的明文绑定与密钥引用合成**最终注入值**：
//
//	Env（明文）+ SecretEnv（store:/env: 引用解析后）→ 环境变量
//	Headers（明文）+ SecretHeaders（解析后）        → header
//
// 任一引用解析失败 → 返回全部失败原因（调用方据此跳过注入 + 告警）。失败原因**只含键名与引用**，
// 绝不含密钥值本身——日志不该成为泄漏面。
func (p *Secrets) Resolve(m capability.McpServer) (env, headers map[string]string, problems []string) {
	env = make(map[string]string, len(m.Env)+len(m.SecretEnv))
	headers = make(map[string]string, len(m.Headers)+len(m.SecretHeaders))
	for k, v := range m.Env {
		env[k] = v
	}
	for k, v := range m.Headers {
		headers[k] = v
	}
	apply := func(field string, bindings map[string]string, dst map[string]string) {
		for _, k := range capability.KeysOf(bindings) {
			ref := bindings[k]
			val, err := p.ResolveRef(ref)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s[%q] 引用 %q 无法解析：%v", field, k, ref, err))
				continue
			}
			dst[k] = val
		}
	}
	apply("secret_env", m.SecretEnv, env)
	apply("secret_headers", m.SecretHeaders, headers)
	return env, headers, problems
}

// UnresolvedKeys 列出"绑了引用但当前解析不出来"的密钥键（只读体检，不取回值）。
//
// `store:` 引用用 `McpSecret` 判存在（**读回一个值只为判空**，但那条路上本来就要读它：
// 列表端点不该造第二套"存在性"判据，否则"列表说能解析、注入时说不行"会分叉）。
func (p *Secrets) UnresolvedKeys(m capability.McpServer) []string {
	var out []string
	for _, k := range capability.KeysOf(m.SecretEnv) {
		if !p.RefResolvable(m.SecretEnv[k]) {
			out = append(out, k)
		}
	}
	for _, k := range capability.KeysOf(m.SecretHeaders) {
		if !p.RefResolvable(m.SecretHeaders[k]) {
			out = append(out, k)
		}
	}
	return out
}

// RefResolvable 报告一条引用当前是否"看起来能解析"（词法已在校验期保证，这里只看存在性）。
func (p *Secrets) RefResolvable(ref string) bool {
	switch capability.SecretRefKind(ref) {
	case "store":
		_, err := p.creds.ResolveStoreSecret(context.Background(), strings.TrimSpace(ref))
		return err == nil
	case "env":
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), capability.SecretRefEnvPrefix))
		return os.Getenv(name) != ""
	default:
		return false
	}
}

// ResolveRef 解析一条密钥引用：
//
//	store:<ns>/<name>  查库里的 `mcp_secrets`（v20 起密钥的权威；没有库时查托管文档）
//	env:<NAME>         进程环境变量（容器/CI 注入；盘上不留痕）
//
// 解析不出来**不是"静默跳过"**，而是明确失败：让 MCP server 带着空密钥跑起来，问题会以
// "tools/list 返回 401"的面目出现在很远的地方。
//
// 旧前缀（v20 之前那套密文机制）在这里**不翻译**：库里的引用已由迁移改写，运行时读到它
// 就是坏数据，翻译只会掩盖它。档案文件里的旧前缀由导入路径显式翻译（见 `definition.TranslateLegacySecretRef`）。
func (p *Secrets) ResolveRef(ref string) (string, error) {
	if err := capability.ValidateSecretRef(ref); err != nil {
		return "", err
	}
	var (
		val string
		err error
	)
	switch capability.SecretRefKind(ref) {
	case "store":
		val, err = p.creds.ResolveStoreSecret(context.Background(), strings.TrimSpace(ref))
	case "env":
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), capability.SecretRefEnvPrefix))
		if v := os.Getenv(name); v != "" {
			val = v
		} else {
			err = fmt.Errorf("环境变量 %s 未设置或为空", name)
		}
	default:
		return "", fmt.Errorf("不支持的引用 %q（本期只有 %s<namespace>/<name> 与 %s<ENV_NAME>）",
			ref, capability.SecretRefStorePrefix, capability.SecretRefEnvPrefix)
	}
	if err != nil {
		return "", err
	}
	// 解析出来的明文登记进脱敏闸门的动态表（`internal/core/redact` 的第 1 档：按值替换、零误报）。
	//
	// 为什么 MCP 密钥也要登记（v19 及更早只有 provider key 登记）：这些值会被塞进 stdio
	// 子进程的环境变量/请求头，而子进程把凭据打进 stderr 是最常见的形态（"401，token=…"）。
	// 那串会经 `run.error` 的 `stderr_tail` 与事件载荷外露——不登记就只能靠形状正则，
	// 而真实密钥往往没有任何可辨识形状。这里是全仓唯一把引用变成明文的地方，登记放在这里
	// 意味着"凡是被解析过的密钥值，离开进程前必被替换"。
	redact.RegisterDynamicSecret(val)
	return val, nil
}
