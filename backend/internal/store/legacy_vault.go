package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ——— v20 遗留面：vault 密文的一次性搬运 ———
//
// v10–v19 的 provider / MCP 密钥落在 `<vaultHome>/vault/<ns>/<name>.enc`：
// AES-256-GCM，AAD 绑定 `{kind, ref}`（`kind + "\x00" + ref`，**ref 是带 `vault:` 前缀的完整引用**，
// 不是去前缀的密文路径），文件内容 = base64(nonce‖密文) + 换行；
// 主密钥是 32 字节，来自 `MATE_VAULT_KEY`（base64/hex）或 `<vaultHome>/vault.key`（0600）。
// v20 把权威搬进库（`providers.key` 列 + `mcp_secrets` 表）并删掉落盘加密层，于是那批密文
// 是用户**唯一一份**密钥本体——解不开就等于丢了。搬运因此必须是：
//
//   - **幂等**：库里已经有值的条目不再覆盖，所以装配期每次启动都能跑（主密钥晚一点修好也能补上）；
//   - **不静默丢**：主密钥不可用 / 单条解不开 / 库里的行不存在，一律记进 `Problems` 交给 doctor
//     报出，其余条目照常搬（一条失败不拖垮整批）；
//   - **不删任何东西**：只读密文，`vault/` 与 `vault.key` 原样留着。删除是**不可逆**动作，
//     由用户确认 providers 的 `has_key` 都对得上之后自己做；`mate doctor` 只报"遗留，可删"。
//
// 格式细节**刻意在这里留一份**（而不是 import `internal/vault`）：那个包要被删掉，而这份代码
// 必须比它活得久——它服务的是"从旧版本升上来"的用户。两处一致性由 `legacy_vault_test.go`
// 的夹具钉住（测试自己用 AES-GCM 造密文，不依赖任何将删的包）。
const (
	legacyVaultDir      = "vault"
	legacyVaultKeyFile  = "vault.key"
	legacyVaultEnvKey   = "MATE_VAULT_KEY"
	legacyVaultKeyLen   = 32
	legacyVaultFileExt  = ".enc"
	legacyProviderNS    = "providers"
	legacyKindProvider  = "provider_key"
	legacyKindMcpSecret = "mcp_secret"
	legacyStorePrefix   = "store:"
	// legacyVaultRefPrefix 旧引用前缀。**AAD 用的是带前缀的完整引用**（`vault:providers/yygu`）：
	// 文件路径去掉它，AAD 不去——这一处曾按去前缀的路径算，结果真机上的密文一条都解不开
	// （2026-10-14 用用户真实 `vault/providers/yygu.enc` 实测抓出来的）。
	legacyVaultRefPrefix = "vault:"
)

// legacyRefSegmentRe 引用段的合法字符（与旧 `vault.ValidateRef` 同一套：`^[A-Za-z0-9._-]{1,64}$`）。
var legacyRefSegmentRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// LegacyVaultProblem 是一条没搬成的密文（**只记引用与原因，绝不记明文**）。
//
// `Ref` 为空串 = 整批都没搬（典型：主密钥不可用），原因写在 `Reason` 里。
type LegacyVaultProblem struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// LegacyVaultMove 是一次搬运的结果（逐条计数 + 未搬原因）。
type LegacyVaultMove struct {
	// Ciphertexts 是 `<vaultHome>/vault/` 下发现的密文总数（0 = 这台机器没有旧 vault）。
	Ciphertexts int
	// ProviderKeys / McpSecrets 是这次真正写进库的条数。
	ProviderKeys int
	McpSecrets   int
	// AlreadyMoved 是"库里已经有值"因而幂等跳过的条数（含已经搬过、已经手填过的）。
	AlreadyMoved int
	// Problems 是没搬成的条目（doctor 的 problems[] 直接消费）。
	Problems []LegacyVaultProblem
}

// Empty 报告这次搬运什么都没发生（没有旧 vault、也没有半条要搬）。
func (m LegacyVaultMove) Empty() bool {
	return m.Ciphertexts == 0 && m.ProviderKeys == 0 && m.McpSecrets == 0 &&
		m.AlreadyMoved == 0 && len(m.Problems) == 0
}

// ImportLegacyVault 把旧 vault 的密文解出来写进库（v20 迁移的第二步，幂等、可重跑）。
//
// vaultHome 是**旧 vault 的根**，与 v13–v19 的取法逐字相同：`filepath.Dir(ProviderDir)`
// （默认 `~/.ark-mate/providers` → `~/.ark-mate`）。注意它**不一定是** `MATE_HOME`：
// 用户把 `MATE_PROVIDERS` 指到别处时，历史实现就是按"provider 目录的父目录"找 keyfile 的，
// 搬运必须跟着它，否则会去找一个当年根本没写过的路径。
//
// 返回的 error 只表示"参数不合法 / 目录读不动"这类结构性失败；**单条失败一律进 Problems**，
// 因为搬不动一条密钥不该让整台机器起不来。
func (s *Store) ImportLegacyVault(ctx context.Context, vaultHome string) (LegacyVaultMove, error) {
	var out LegacyVaultMove
	if strings.TrimSpace(vaultHome) == "" {
		return out, errors.New("store: ImportLegacyVault 需要旧 vault 根目录（空串）")
	}
	root := filepath.Join(vaultHome, legacyVaultDir)
	refs, err := listLegacyCiphertexts(root)
	if err != nil {
		return out, fmt.Errorf("store: 扫描 %s: %w", root, err)
	}
	out.Ciphertexts = len(refs)
	if len(refs) == 0 {
		return out, nil
	}

	aead, err := openLegacyVault(vaultHome)
	if err != nil {
		// 整批没搬：这是"待处理"，不是错误——用户修好主密钥后重启即可重跑。
		out.Problems = append(out.Problems, LegacyVaultProblem{
			Reason: err.Error() + "；本条与其余 " + fmt.Sprint(len(refs)-1) +
				" 条都没搬（库里的密钥一条都没丢，修好后重启即会重跑，搬运是幂等的）",
		})
		return out, nil
	}

	for _, ref := range refs {
		ns, name, _ := strings.Cut(ref, "/")
		kind := legacyKindMcpSecret
		if ns == legacyProviderNS {
			kind = legacyKindProvider
		}
		raw, err := readLegacyCiphertext(root, ref)
		if err != nil {
			out.Problems = append(out.Problems, LegacyVaultProblem{Ref: ref, Reason: "读密文失败：" + err.Error()})
			continue
		}
		pt, err := decryptLegacyVault(aead, ref, kind, raw)
		if err != nil {
			out.Problems = append(out.Problems, LegacyVaultProblem{Ref: ref, Reason: err.Error()})
			continue
		}
		if ns == legacyProviderNS {
			cur, ok, err := s.ProviderKey(ctx, name)
			switch {
			case err != nil:
				out.Problems = append(out.Problems, LegacyVaultProblem{Ref: ref, Reason: "读 providers 行失败：" + err.Error()})
				continue
			case !ok:
				out.Problems = append(out.Problems, LegacyVaultProblem{
					Ref:    ref,
					Reason: "providers 表里没有这一行（先把 providers.json 导进来，或重建该档案；之后再重跑本搬运）",
				})
				continue
			case cur != "":
				out.AlreadyMoved++
				continue
			}
			if _, err := s.SetProviderKey(ctx, name, string(pt)); err != nil {
				out.Problems = append(out.Problems, LegacyVaultProblem{Ref: ref, Reason: "写 providers.key 失败：" + err.Error()})
				continue
			}
			out.ProviderKeys++
			continue
		}

		newRef := legacyStorePrefix + ref
		cur, ok, err := s.McpSecret(ctx, newRef)
		switch {
		case err != nil:
			out.Problems = append(out.Problems, LegacyVaultProblem{Ref: ref, Reason: "读 mcp_secrets 行失败：" + err.Error()})
			continue
		case ok && cur != "":
			out.AlreadyMoved++
			continue
		}
		if err := s.SetMcpSecret(ctx, newRef, string(pt)); err != nil {
			out.Problems = append(out.Problems, LegacyVaultProblem{Ref: ref, Reason: "写 mcp_secrets 失败：" + err.Error()})
			continue
		}
		out.McpSecrets++
	}
	return out, nil
}

// listLegacyCiphertexts 列出 `<root>/<ns>/<name>.enc`，返回 `ns/name`（升序，便于日志与测试比对）。
//
// 形态不认识的（多级目录、段名含怪字符）**直接跳过**：那不是本实现写出来的东西，
// 不问也不改（与仓库对用户目录的一贯态度一致）。
func listLegacyCiphertexts(root string) ([]string, error) {
	var refs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), legacyVaultFileExt) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(strings.TrimSuffix(rel, legacyVaultFileExt))
		ns, name, ok := strings.Cut(rel, "/")
		if !ok || !legacyRefSegmentRe.MatchString(ns) || !legacyRefSegmentRe.MatchString(name) {
			return nil
		}
		refs = append(refs, ns+"/"+name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(refs)
	return refs, nil
}

// openLegacyVault 解析主密钥并返回 AES-GCM（`MATE_VAULT_KEY` 优先；否则读 keyfile 并做同一套硬化检查）。
func openLegacyVault(vaultHome string) (cipher.AEAD, error) {
	raw := strings.TrimSpace(os.Getenv(legacyVaultEnvKey))
	if raw == "" {
		keyPath := filepath.Join(vaultHome, legacyVaultKeyFile)
		fi, err := os.Lstat(keyPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("没有主密钥：%s 不存在，%s 也没设", keyPath, legacyVaultEnvKey)
			}
			return nil, fmt.Errorf("读主密钥 %s：%w", keyPath, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("主密钥 %s 是符号链接，拒绝使用（防被指向别处的密钥）", keyPath)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("主密钥 %s 不是普通文件（mode=%s）", keyPath, fi.Mode())
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			return nil, fmt.Errorf("主密钥 %s 权限 %04o 过宽（group/other 可读，应为 0600）", keyPath, perm)
		}
		b, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("读主密钥 %s：%w", keyPath, err)
		}
		raw = strings.TrimSpace(string(b))
	}
	key, err := parseLegacyVaultKey(raw)
	if err != nil {
		return nil, fmt.Errorf("主密钥非法：%w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES 初始化：%w", err)
	}
	return cipher.NewGCM(block)
}

// parseLegacyVaultKey 接受 32 字节的 base64（四种变体）或 hex（旧 `vault.ParseKey` 逐字同形）。
func parseLegacyVaultKey(s string) ([]byte, error) {
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == legacyVaultKeyLen {
			return b, nil
		}
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == legacyVaultKeyLen {
		return b, nil
	}
	return nil, fmt.Errorf("需为 %d 字节的 base64 或 hex（得到 %d 字符）", legacyVaultKeyLen, len(s))
}

// readLegacyCiphertext 读一个密文文件并 base64 解码成 `nonce‖密文`。
func readLegacyCiphertext(root, ref string) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(ref)+legacyVaultFileExt)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, errors.New("内容不是 base64（不是本实现写出来的密文？）")
	}
	return raw, nil
}

// decryptLegacyVault 解一条密文；**错误信息里只有引用与 kind，绝不含明文或密钥材料**。
func decryptLegacyVault(aead cipher.AEAD, ref, kind string, raw []byte) ([]byte, error) {
	ns := aead.NonceSize()
	if len(raw) <= ns {
		return nil, errors.New("密文长度不足（短于 nonce）")
	}
	aad := []byte(kind + "\x00" + legacyVaultRefPrefix + ref)
	pt, err := aead.Open(nil, raw[:ns], raw[ns:], aad)
	if err != nil {
		return nil, errors.New("解不开：主密钥不对，或密文被挪到了别的引用/kind 上（AAD 绑定身份）")
	}
	return pt, nil
}
