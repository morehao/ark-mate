package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ——— §13.4 S2：物化产物的目录级 SHA-256 摘要（审计 / 复现 / 篡改可见） ———
//
// 为什么需要：物化是「把平台事实源投影成 runtime 原生文件」，但投影完之后**没有任何记录**
// 说明"这个目录本该长什么样"。于是三件事都做不到：
//  1. 审计：agent 实际读到的 SKILL.md 与注册表里的定义是否一致，无从证明；
//  2. 复现：换个时间/换台机器重校，凭什么说产物相同；
//  3. 篡改可见：有人（或某个脚本）改了/塞了文件，平台下一次重校前毫无察觉。
//
// 摘要算法逐条对照 compozy `internal/skills/provenance.go:54`（`ComputeDirectoryHash`）与
// `:189`（`writeHashEntry`）：
//   - 收集**相对路径**并排序（遍历顺序与文件系统无关）；
//   - 普通文件：`file:<slash 相对路径>\nmode:<权限八进制>\n` + 内容 + `\0`（权限与内容都入哈希，
//     所以 `chmod` 也算变更）；
//   - 软链：**不 follow**，把 `symlink:<相对路径>\nmode:<权限>\ntarget:<字面目标>\n` 入哈希；
//     且目标**逃出目录即拒**（`ErrSymlinkEscape`）——一个指向 `~/.ssh` 的软链会被材质化进 agent 目录；
//   - 其它类型（设备/管道…）：直接报错，不入哈希；
//   - 遍历**排除平台记账文件**（`.forge-managed` 归属标记、本文件写的 sidecar）：它们随投影次数变化，
//     入哈希会让"内容没变"被误判成"变了"。
//
// 与 compozy 的差异（有意）：那边的 provenance 服务于 marketplace 安装（registry/slug/version），
// ark-forge 没有 marketplace，来源就是本机定义文件，所以 sidecar 记的是 **skill id + runtime + format
// + 摘要 + 写入时间**——本地可审计需要的那几项，不假装有版本号。

// ProvenanceFileName 物化目录里的摘要 sidecar（与 `.forge-managed` 归属标记并列）。
const ProvenanceFileName = ".forge-provenance.json"

// provenanceVersion 当前 sidecar 结构版本（将来加字段时据此兼容读）。
const provenanceVersion = 1

// ErrSymlinkEscape 物化目录载荷里的软链指向目录之外。
var ErrSymlinkEscape = errors.New("capability: 物化目录里的软链指向目录之外")

// HashMismatchError 摘要不一致（期望 vs 实测）。
type HashMismatchError struct {
	Expected string
	Actual   string
}

func (e *HashMismatchError) Error() string {
	if e == nil {
		return "capability: 物化摘要不一致"
	}
	return fmt.Sprintf("capability: 物化摘要不一致：期望 %s，实测 %s", e.Expected, e.Actual)
}

// Provenance 一次物化的可复现记录（写在产物目录里的 sidecar）。
type Provenance struct {
	Version int    `json:"version"`
	SkillID string `json:"skill_id"`
	// Runtime 本次投影的目标 runtime（空 = 单 root 兼容路径）。
	Runtime string `json:"runtime,omitempty"`
	// Format 本次采用的物化 format（已实证的那些之一）。
	Format string `json:"format"`
	// Hash 目录载荷摘要（`.forge-managed` 与 sidecar 自身除外）。
	Hash string `json:"hash"`
	// Bytes 载荷字节数（人读用；不参与摘要）。
	Bytes int `json:"bytes"`
	// WrittenAt 写入时间（UTC RFC3339；审计用）。
	WrittenAt time.Time `json:"written_at"`
}

// payloadFile 一个待写入/已读取的载荷文件（摘要的统一输入）。
type payloadFile struct {
	Path    string // slash 分隔的相对路径
	Mode    fs.FileMode
	Content []byte
}

// bookkeepingFiles 平台记账文件：不参与摘要（它们随投影次数变化，不是"内容"）。
func isBookkeepingFile(name string) bool {
	return name == forgeMarker || name == ProvenanceFileName
}

// hashPayload 计算一组载荷文件的摘要（算法见文件头注释）。
func hashPayload(files []payloadFile) string {
	ordered := append([]payloadFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	h := sha256.New()
	for _, f := range ordered {
		writeHashString(h, fmt.Sprintf("file:%s\nmode:%#o\n", f.Path, f.Mode.Perm()))
		h.Write(f.Content)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PayloadHash 单文件物化产物的期望摘要（物化只写 SKILL.md 一个载荷文件，
// 记账文件与 sidecar 不入哈希）——重校用它判断"磁盘上的是不是该有的那个"。
func PayloadHash(content string) string {
	return hashPayload([]payloadFile{{Path: "SKILL.md", Mode: 0o644, Content: []byte(content)}})
}

// PayloadBytes 载荷字节数（sidecar 审计字段）。
func payloadBytes(content string) int { return len(content) }

// writeHashString 把字符串写进 hasher（hash.Hash 的 Write 不返回错误，忽略即可）。
func writeHashString(h hash.Hash, s string) { _, _ = h.Write([]byte(s)) }

// ComputeDirHash 计算目录载荷摘要（排除记账文件；软链不 follow、逃逸即拒）。
func ComputeDirHash(dir string) (string, error) {
	absRoot, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("capability: 解析物化目录 %q 失败: %w", dir, err)
	}
	entries := make([]string, 0, 8)
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absRoot || d.IsDir() {
			return nil
		}
		if isBookkeepingFile(filepath.Base(path)) {
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("capability: 遍历物化目录 %q 失败: %w", absRoot, err)
	}
	sort.Strings(entries)

	h := sha256.New()
	for _, rel := range entries {
		if err := writeHashEntry(h, absRoot, rel); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeHashEntry 把一个目录项写进 hasher（普通文件读内容、软链读字面目标、其它类型报错）。
func writeHashEntry(h hash.Hash, root, rel string) error {
	normalized := filepath.ToSlash(rel)
	abs := filepath.Join(root, rel)
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("capability: 读取待哈希路径 %q 失败: %w", abs, err)
	}
	switch {
	case info.Mode().IsRegular():
		f, err := os.Open(abs)
		if err != nil {
			return fmt.Errorf("capability: 打开待哈希文件 %q 失败: %w", abs, err)
		}
		defer f.Close()
		writeHashString(h, fmt.Sprintf("file:%s\nmode:%#o\n", normalized, info.Mode().Perm()))
		buf := make([]byte, 32*1024)
		for {
			n, rErr := f.Read(buf)
			if n > 0 {
				h.Write(buf[:n])
			}
			if rErr == nil {
				continue
			}
			if errors.Is(rErr, io.EOF) {
				break
			}
			return fmt.Errorf("capability: 读取待哈希文件 %q 失败: %w", abs, rErr)
		}
		h.Write([]byte{0})
		return nil
	case info.Mode()&os.ModeSymlink != 0:
		if err := ensureSymlinkWithinRoot(root, abs); err != nil {
			return err
		}
		target, err := os.Readlink(abs)
		if err != nil {
			return fmt.Errorf("capability: 读取软链 %q 失败: %w", abs, err)
		}
		writeHashString(h, fmt.Sprintf("symlink:%s\nmode:%#o\ntarget:%s\n",
			normalized, info.Mode().Perm(), filepath.ToSlash(target)))
		return nil
	default:
		return fmt.Errorf("capability: 物化目录含不支持的文件类型 %q（mode=%v）", abs, info.Mode())
	}
}

// ensureSymlinkWithinRoot 校验软链的字面目标解析后仍在 root 内（逃逸即拒）。
//
// 用**字面**目标解析（不做 EvalSymlinks 全解析）：链中任意一跳逃出 root 都不能接受；
// 目标不存在也算逃逸（要么是坏链，要么指向未来才出现的目录之外的东西——都不该进 agent 目录）。
func ensureSymlinkWithinRoot(root, linkPath string) error {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return fmt.Errorf("capability: 读取软链 %q 失败: %w", linkPath, err)
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(linkPath), resolved)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("capability: 软链 %q → %q 逃出物化目录 %q: %w",
			linkPath, target, absRoot, ErrSymlinkEscape)
	}
	return nil
}

// WriteProvenance 写 sidecar（0600：里面是路径与摘要，不含密钥，但没必要给所有人读）。
func WriteProvenance(dir string, p Provenance) error {
	if p.Version == 0 {
		p.Version = provenanceVersion
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("capability: 序列化摘要失败: %w", err)
	}
	b = append(b, '\n')
	path := filepath.Join(dir, ProvenanceFileName)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("capability: 写摘要 %s 失败: %w", path, err)
	}
	return nil
}

// ReadProvenance 读 sidecar（文件不存在/坏 JSON 都报错——「没有摘要」本身就是要暴露的状态）。
func ReadProvenance(dir string) (Provenance, error) {
	var p Provenance
	path := filepath.Join(dir, ProvenanceFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("capability: 解析摘要 %s 失败: %w", path, err)
	}
	if p.Version > provenanceVersion {
		return p, fmt.Errorf("capability: 摘要 %s 版本 %d 高于本进程支持的 %d（升级后再读）",
			path, p.Version, provenanceVersion)
	}
	return p, nil
}

// VerifyDir 重算目录摘要并与 sidecar 比对（不一致返回 *HashMismatchError）。
func VerifyDir(dir string) (Provenance, error) {
	p, err := ReadProvenance(dir)
	if err != nil {
		return p, err
	}
	actual, err := ComputeDirHash(dir)
	if err != nil {
		return p, err
	}
	if actual != p.Hash {
		return p, &HashMismatchError{Expected: p.Hash, Actual: actual}
	}
	return p, nil
}
