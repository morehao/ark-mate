package apppkg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestFileName 是每个应用包目录里清单的固定文件名。
const ManifestFileName = "manifest.json"

// AppsDirEnv 是覆盖本地应用目录的环境变量名。未设置时用 DefaultAppsDir 的默认路径。
const AppsDirEnv = "MATE_APPS_DIR"

// DefaultAppsDir 返回本地应用包的根目录：优先 `MATE_APPS_DIR`，否则 `~/.dsh/mate/apps`。
// 每个应用一个子目录 `<dir>/<id>/`，内含 `manifest.json` + entrypoint + 资源。
func DefaultAppsDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(AppsDirEnv)); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("apppkg: 解析用户主目录失败: %w", err)
	}
	return filepath.Join(home, ".dsh", "mate", "apps"), nil
}

// Package 是一个**通过校验、可被装载**的应用包（解析后的清单 + 它在磁盘上的位置）。
type Package struct {
	// Manifest 是已解析并校验通过的清单。
	Manifest *Manifest
	// Dir 是这个包的根目录（绝对或调用方给定的路径）。
	Dir string
	// EntrypointPath 是 entrypoint 解析到磁盘上的绝对/相对完整路径。
	EntrypointPath string
}

// FailedPackage 是一个**扫描到但不可装载**的包：带目录与该包失败的原因（明示给用户/日志）。
type FailedPackage struct {
	// Dir 是失败包的根目录。
	Dir string
	// Reason 是可读的失败原因（解析错/校验错/哈希不符/入口缺失等）。
	Reason string
}

// ScanResult 是一次目录扫描的结果：成功包列表 + 失败包列表。
//
// 默认目录为空/不存在 = 空集合、不报错（对齐「无定义照跑」：没有装任何包时平台照常启动）。
type ScanResult struct {
	Loaded []*Package
	Failed []FailedPackage
}

// Scan 扫描 dir 下的每个子目录，逐个解析 manifest、做完整性（sha256）校验，汇总成功与失败。
//
// 行为约定：
//   - dir 不存在 → 返回空 ScanResult 且 err=nil（默认无包）。
//   - dir 存在但某项不是目录（散落的文件）→ 跳过（不算失败；应用包必须以子目录形式存在）。
//   - 每个子目录视为一个包；该包任何一步失败都进 Failed（带原因），不影响其它包（失败隔离）。
//   - 单包失败不返回 error（error 只用于"扫描根目录本身不可读"这类整体失败）。
//
// 返回的 Loaded 按包 id 升序（稳定顺序，便于日志/测试）。
func Scan(dir string) (*ScanResult, error) {
	res := &ScanResult{Loaded: []*Package{}, Failed: []FailedPackage{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// 目录不存在 = 没有装任何包，正常。
			return res, nil
		}
		return nil, fmt.Errorf("apppkg: 读取应用目录 %q 失败: %w", dir, err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pkgDir := filepath.Join(dir, e.Name())
		pkg, reason := loadOne(pkgDir)
		if reason != "" {
			res.Failed = append(res.Failed, FailedPackage{Dir: pkgDir, Reason: reason})
			continue
		}
		res.Loaded = append(res.Loaded, pkg)
	}

	sort.Slice(res.Loaded, func(i, j int) bool { return res.Loaded[i].Manifest.ID < res.Loaded[j].Manifest.ID })
	sort.Slice(res.Failed, func(i, j int) bool { return res.Failed[i].Dir < res.Failed[j].Dir })
	return res, nil
}

// loadOne 装载单个包目录。成功返回 (*Package, "")；失败返回 (nil, 原因)。
//
// 步骤：读 manifest.json → 解析+校验清单 → 校验 entrypoint 文件存在且 sha256 与清单一致。
func loadOne(pkgDir string) (*Package, string) {
	manifestPath := filepath.Join(pkgDir, ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Sprintf("读取 %s 失败: %v", ManifestFileName, err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, err.Error()
	}

	// 入口文件路径（清单已校验为相对、无上跳；这里做路径拼接 + 二次越界确认，防符号链接/拼接绕过）。
	epPath := filepath.Join(pkgDir, filepath.FromSlash(m.Entrypoint))
	if !withinDir(pkgDir, epPath) {
		return nil, fmt.Sprintf("entrypoint %q 解析后越出包根 %q（拒绝装载）", m.Entrypoint, pkgDir)
	}
	epContent, err := os.ReadFile(epPath)
	if err != nil {
		return nil, fmt.Sprintf("entrypoint %q 不可读: %v", m.Entrypoint, err)
	}

	// 完整性：entrypoint 内容的 sha256 必须与清单声明一致（防篡改，安全基线第一步）。
	sum := sha256.Sum256(epContent)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, m.Integrity.SHA256) {
		return nil, fmt.Sprintf("完整性校验失败：entrypoint sha256 不符（清单声明 %s，实际 %s）——包被篡改或未正确打包，拒绝装载", m.Integrity.SHA256, got)
	}

	return &Package{Manifest: m, Dir: pkgDir, EntrypointPath: epPath}, ""
}

// WithinDir 报告 target 是否在 dir 之内（按 filepath.Clean + Rel 判定，防 `..` 上跳）。
//
// 导出供 `internal/infra/appmarket` 复用来源根防越界校验（市场清单的 `source.dir` 必须落在允许
// 来源根内，防止清单指向 `/etc` 这类系统路径被当包装载）；语义与包内 `withinDir` 完全一致，内部
// 直接转发，不复制逻辑，避免两套判定口径漂移。
func WithinDir(dir, target string) bool { return withinDir(dir, target) }

// withinDir 报告 target 是否在 dir 之内（按 filepath.Clean + Rel 判定，防 `..` 上跳）。
func withinDir(dir, target string) bool {
	dirClean := filepath.Clean(dir)
	targetClean := filepath.Clean(target)
	if targetClean == dirClean {
		return true
	}
	rel, err := filepath.Rel(dirClean, targetClean)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// LoadFromDir 是 Scan(DefaultAppsDir()) 的便捷封装：解析默认目录、扫描、返回结果。
func LoadFromDir() (*ScanResult, error) {
	dir, err := DefaultAppsDir()
	if err != nil {
		return nil, err
	}
	return Scan(dir)
}
