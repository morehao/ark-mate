// Package vault 是本地密钥的**加密落盘层**：AES-256-GCM，AAD 绑定密文身份（`{kind, ref}`），
// 主密钥走 keyfile（`<forge_home>/vault.key`）或 `FORGE_VAULT_KEY` 覆盖。
//
// 为什么需要它：provider 档案原先明文落 `providers.json`，任何能读文件的人（备份、误提交、
// 同步盘）都能拿到底座 API key。现在文件里只留**引用**（`vault:providers/<id>`），
// 密文单独落 `<forge_home>/vault/<namespace>/<name>.enc`。
//
// AAD 绑定身份的意义：把 A 的密文挪到 B 的引用上（或换 kind）**解密必然失败**，
// 而不是悄悄解出别人的密钥。密文文件被文件名/引用错配时不可能"错位成功"。
//
// keyfile 的硬化（对齐 compozy internal/vault/crypto.go）：
//   - 创建用 **O_EXCL 临时文件 + link 原子安装**（并发首次启动只有一个赢家，不互相覆盖）；
//   - 读取时**拒符号链接**（防被指向别处的密钥）、**拒 group/other 权限位**（应 0600）；
//   - 权限位/符号链接不符 → **拒绝使用**（宁可显式失败，不静默降级为明文）。
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// RefPrefix 引用语法前缀：`vault:<namespace>/<name>`。
	RefPrefix = "vault:"
	// KeyFileName 主密钥文件名（位于 forge home）。
	KeyFileName = "vault.key"
	// DirName 密文目录名（位于 forge home）。
	DirName = "vault"
	// EnvKey 覆盖主密钥的进程环境变量：32 字节的 base64 或 hex。
	EnvKey = "FORGE_VAULT_KEY"
	// KindMcpSecret MCP server 密钥（secret_env / secret_headers）的 kind（参与 AAD）。
	// 与 provider 密钥分开：同一份密文在两个 kind 之间不可互换（AAD 里带 kind）。
	KindMcpSecret = "mcp_secret"

	// KindProviderKey provider 档案密钥的 kind（参与 AAD）。
	KindProviderKey = "provider_key"
	// fileSuffix 密文文件后缀。
	fileSuffix = ".enc"
	// keyLen 主密钥长度（AES-256）。
	keyLen = 32
)

// ErrNotFound 密文不存在（Put 过？Delete 过？）。
var ErrNotFound = errors.New("vault: 密文不存在")

// refSegmentRe 引用段合法字符（provider id 会进文件名 → 必须严格，防路径穿越）。
var refSegmentRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Store 一个已装载主密钥的 vault 句柄（并发安全：aead 与路径都是只读）。
type Store struct {
	dir     string // 密文目录
	keyPath string // 主密钥路径（FORGE_VAULT_KEY 覆盖时为空 = 无 keyfile）
	aead    cipher.AEAD
}

// Open 装载/创建主密钥并返回 vault 句柄。home 为 forge home
// （默认 `~/.forge`；密文落 `<home>/vault/`、密钥落 `<home>/vault.key`）。
//
// `FORGE_VAULT_KEY` 非空时优先用它，且**不读不建 keyfile**（容器/CI 友好）。
func Open(home string) (*Store, error) {
	if home == "" {
		return nil, errors.New("vault: home 目录为空")
	}
	if v := strings.TrimSpace(os.Getenv(EnvKey)); v != "" {
		key, err := ParseKey(v)
		if err != nil {
			return nil, fmt.Errorf("vault: %s 非法: %w", EnvKey, err)
		}
		return newStore(filepath.Join(home, DirName), "", key)
	}
	keyPath := filepath.Join(home, KeyFileName)
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	return newStore(filepath.Join(home, DirName), keyPath, key)
}

func newStore(dir, keyPath string, key []byte) (*Store, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: 主密钥无效: %w", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("vault: GCM 初始化失败: %w", err)
	}
	return &Store{dir: dir, keyPath: keyPath, aead: aead}, nil
}

// KeyPath 主密钥路径（FORGE_VAULT_KEY 覆盖时为空）。
func (s *Store) KeyPath() string { return s.keyPath }

// Dir 密文目录。
func (s *Store) Dir() string { return s.dir }

// ParseKey 解析主密钥：接受 32 字节的 base64（标准或 URL 变体，可带换行）或 64 位 hex。
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("空密钥")
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == keyLen {
			return b, nil
		}
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == keyLen {
		return b, nil
	}
	return nil, fmt.Errorf("需为 %d 字节的 base64 或 hex（得到 %d 字符）", keyLen, len(s))
}

// ValidateRef 校验引用并返回相对密文路径；非法引用（前缀/段数/字符/`..`）一律报错。
func ValidateRef(ref string) (string, error) {
	if !strings.HasPrefix(ref, RefPrefix) {
		return "", fmt.Errorf("vault: 引用 %q 必须以 %q 开头", ref, RefPrefix)
	}
	body := strings.TrimPrefix(ref, RefPrefix)
	parts := strings.Split(body, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("vault: 引用 %q 需为 %s<namespace>/<name>", ref, RefPrefix)
	}
	for _, p := range parts {
		if p == "." || p == ".." || !refSegmentRe.MatchString(p) {
			return "", fmt.Errorf("vault: 引用 %q 的段 %q 非法（仅 a-zA-Z0-9._-，1-64 字符）", ref, p)
		}
	}
	return parts[0] + string(filepath.Separator) + parts[1] + fileSuffix, nil
}

// aad 密文身份（kind + ref）：换 kind 或换引用都解不开。
func aad(ref, kind string) []byte { return []byte(kind + "\x00" + ref) }

// Encrypt 加密并返回 base64(nonce||ciphertext)（内存态，不落盘）。
func (s *Store) Encrypt(ref, kind string, plaintext []byte) (string, error) {
	if _, err := ValidateRef(ref); err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("vault: 生成 nonce 失败: %w", err)
	}
	ct := s.aead.Seal(nil, nonce, plaintext, aad(ref, kind))
	return base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Decrypt 解密 Encrypt/Put 的产物；引用/kind 不匹配或密文被篡改都返回错误。
func (s *Store) Decrypt(ref, kind, blob string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(blob))
	if err != nil {
		return nil, fmt.Errorf("vault: 密文 base64 非法: %w", err)
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("vault: 密文过短")
	}
	pt, err := s.aead.Open(nil, raw[:ns], raw[ns:], aad(ref, kind))
	if err != nil {
		return nil, fmt.Errorf("vault: 解密失败（主密钥不匹配 / 引用或 kind 不符 / 密文被改）: %w", err)
	}
	return pt, nil
}

// Put 加密写入 `<dir>/<namespace>/<name>.enc`（原子：临时文件 + rename，0600）。
func (s *Store) Put(ref, kind string, plaintext []byte) error {
	blob, err := s.Encrypt(ref, kind, plaintext)
	if err != nil {
		return err
	}
	path, err := s.pathFor(ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("vault: 建密文目录失败: %w", err)
	}
	if err := writeFileAtomic(path, []byte(blob+"\n"), 0o600); err != nil {
		return fmt.Errorf("vault: 写密文失败: %w", err)
	}
	return nil
}

// Get 读取并解密密文；不存在返回 ErrNotFound。
func (s *Store) Get(ref, kind string) ([]byte, error) {
	path, err := s.pathFor(ref)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		return nil, fmt.Errorf("vault: 读密文失败: %w", err)
	}
	return s.Decrypt(ref, kind, string(b))
}

// Delete 删除密文（引用不存在时不算错误——清除语义要幂等）。
func (s *Store) Delete(ref string) error {
	path, err := s.pathFor(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("vault: 删密文失败: %w", err)
	}
	return nil
}

// Has 报告密文是否存在（不校验能否解密）。
func (s *Store) Has(ref string) bool {
	path, err := s.pathFor(ref)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// pathFor 引用 → 绝对密文路径。
func (s *Store) pathFor(ref string) (string, error) {
	rel, err := ValidateRef(ref)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.dir, rel), nil
}

// writeFileAtomic 同目录临时文件 + rename（0600）。
func writeFileAtomic(path string, b []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// loadOrCreateKey 读主密钥；不存在则原子创建（O_EXCL + link）。
func loadOrCreateKey(path string) ([]byte, error) {
	b, err := readKeyFile(path)
	if err == nil {
		return ParseKey(string(b))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("vault: 生成主密钥失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("vault: 建密钥目录失败: %w", err)
	}
	// O_EXCL 临时文件（CreateTemp 默认 0600）→ link 到目标：并发首次启动只有一个赢家，
	// 且目标路径**永不出现半写内容**（link 是原子操作，rename 会覆盖别人的密钥）。
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vault-key-*")
	if err != nil {
		return nil, fmt.Errorf("vault: 建临时密钥文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("vault: 写临时密钥失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// 别的进程先创建 → 用它的（绝不覆盖已存在的主密钥：覆盖 = 已有密文全废）。
			b, rerr := readKeyFile(path)
			if rerr != nil {
				return nil, rerr
			}
			return ParseKey(string(b))
		}
		return nil, fmt.Errorf("vault: 安装主密钥失败: %w", err)
	}
	return key, nil
}

// readKeyFile 读主密钥并做硬化校验（拒符号链接 / 拒 group-other 权限位）。
func readKeyFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("vault: 主密钥 %s 是符号链接，拒绝使用（防被指向别处的密钥）", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("vault: 主密钥 %s 不是普通文件（mode=%s）", path, fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("vault: 主密钥 %s 权限 %04o 过宽（group/other 可读，应为 0600）", path, perm)
	}
	return os.ReadFile(path)
}
