package vault

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoundTripAndAADBinding 加解密往返 + AAD 绑定身份：换引用/换 kind/被篡改都解不开。
func TestRoundTripAndAADBinding(t *testing.T) {
	home := t.TempDir()
	st, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const ref = "vault:providers/default"
	blob, err := st.Encrypt(ref, KindProviderKey, []byte("sk-secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	pt, err := st.Decrypt(ref, KindProviderKey, blob)
	if err != nil || string(pt) != "sk-secret" {
		t.Fatalf("往返失败: %q err=%v", pt, err)
	}
	// 换引用（把 A 的密文挪到 B）→ 必失败。
	if _, err := st.Decrypt("vault:providers/other", KindProviderKey, blob); err == nil {
		t.Fatal("换引用应解密失败（AAD 绑定身份）")
	}
	// 换 kind → 必失败。
	if _, err := st.Decrypt(ref, "mcp_header", blob); err == nil {
		t.Fatal("换 kind 应解密失败")
	}
	// 篡改密文 → 必失败。
	raw, _ := base64.StdEncoding.DecodeString(blob)
	raw[len(raw)-1] ^= 0xff
	if _, err := st.Decrypt(ref, KindProviderKey, base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("篡改密文应解密失败")
	}
	// 换主密钥（另一个 vault）→ 必失败。
	st2, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open(2): %v", err)
	}
	if _, err := st2.Decrypt(ref, KindProviderKey, blob); err == nil {
		t.Fatal("换主密钥应解密失败")
	}
}

// TestPutGetDelete 落盘：密文文件在 <home>/vault/<ns>/<name>.enc、权限 0600、
// 清除幂等、不存在报 ErrNotFound。
func TestPutGetDelete(t *testing.T) {
	home := t.TempDir()
	st, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const ref = "vault:providers/ds"
	if err := st.Put(ref, KindProviderKey, []byte("sk-ds")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := st.Put(ref, KindProviderKey, []byte("sk-ds")); err != nil {
		t.Fatalf("Put 幂等: %v", err)
	}
	path := filepath.Join(home, DirName, "providers", "ds"+fileSuffix)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("密文未落盘: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("密文权限应为 0600，got %04o", perm)
	}
	// 密文文件里不得出现明文。
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "sk-ds") {
		t.Fatal("密文文件不应含明文")
	}
	pt, err := st.Get(ref, KindProviderKey)
	if err != nil || string(pt) != "sk-ds" {
		t.Fatalf("Get: %q err=%v", pt, err)
	}
	if !st.Has(ref) {
		t.Fatal("Has 应为 true")
	}
	if err := st.Delete(ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := st.Delete(ref); err != nil {
		t.Fatalf("Delete 应幂等: %v", err)
	}
	if st.Has(ref) {
		t.Fatal("Delete 后 Has 应为 false")
	}
	if _, err := st.Get(ref, KindProviderKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应返回 ErrNotFound: %v", err)
	}
}

// TestKeyFileHardening 主密钥硬化：默认 0600；符号链接与过宽权限都拒绝使用。
func TestKeyFileHardening(t *testing.T) {
	home := t.TempDir()
	st, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	keyPath := st.KeyPath()
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("主密钥未创建: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("主密钥权限应为 0600，got %04o", perm)
	}
	// 过宽权限 → 拒绝。
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(home); err == nil {
		t.Fatal("group/other 可读的主密钥应被拒绝")
	} else if !strings.Contains(err.Error(), "过宽") {
		t.Fatalf("错误应说明权限过宽: %v", err)
	}
	// 符号链接 → 拒绝。
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	real := keyPath + ".real"
	if err := os.Rename(keyPath, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, keyPath); err != nil {
		t.Skipf("平台不支持符号链接: %v", err)
	}
	if _, err := Open(home); err == nil {
		t.Fatal("符号链接主密钥应被拒绝")
	} else if !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("错误应说明符号链接: %v", err)
	}
}

// TestEnvKeyOverride MATE_VAULT_KEY 覆盖：不读不建 keyfile，且与同密钥的 keyfile vault 互通。
func TestEnvKeyOverride(t *testing.T) {
	home := t.TempDir()
	raw := make([]byte, keyLen)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	t.Setenv(EnvKey, enc)
	st, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if st.KeyPath() != "" {
		t.Fatalf("env 覆盖时不应有 keyfile 路径: %s", st.KeyPath())
	}
	if _, err := os.Stat(filepath.Join(home, KeyFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("env 覆盖时不应创建 keyfile")
	}
	// hex 形式同样可解析。
	if _, err := ParseKey(strings.ToUpper(hexOf(raw))); err != nil {
		t.Fatalf("hex 主密钥应可解析: %v", err)
	}
	if _, err := ParseKey("short"); err == nil {
		t.Fatal("非法长度应报错")
	}
	// 与使用同一密钥的 keyfile vault 互通（容器里注入 env、本地用 keyfile 是同一份密钥）。
	blob, err := st.Encrypt("vault:providers/x", KindProviderKey, []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKey, "")
	if err := os.WriteFile(filepath.Join(home, KeyFileName), []byte(enc+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(home)
	if err != nil {
		t.Fatalf("Open(keyfile): %v", err)
	}
	if pt, err := st2.Decrypt("vault:providers/x", KindProviderKey, blob); err != nil || string(pt) != "v" {
		t.Fatalf("同一密钥应互通: %q err=%v", pt, err)
	}
}

// TestValidateRefRejectsTraversal 引用校验：路径穿越/段数/字符非法一律拒绝。
func TestValidateRefRejectsTraversal(t *testing.T) {
	bad := []string{
		"",
		"providers/x",              // 缺前缀
		"vault:providers",          // 段数不足
		"vault:providers/a/b",      // 段数过多
		"vault:../etc/passwd",      // 穿越
		"vault:providers/..",       // 穿越
		"vault:providers/.",        // 当前目录
		"vault:providers/a b",      // 空格
		"vault:providers/a;rm -rf", // 特殊字符
		"vault:providers/" + strings.Repeat("x", 65), // 过长
	}
	for _, ref := range bad {
		if _, err := ValidateRef(ref); err == nil {
			t.Fatalf("非法引用应被拒绝: %q", ref)
		}
	}
	if rel, err := ValidateRef("vault:providers/default"); err != nil || rel != "providers/default"+fileSuffix {
		t.Fatalf("合法引用应通过: %q err=%v", rel, err)
	}
}

// TestOpenRequiresHome 无 home 直接报错（调用方据此决定是否退化为「不落盘」）。
func TestOpenRequiresHome(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("空 home 应报错")
	}
}

func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}
