package modelcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// cacheTTL 磁盘缓存有效期（三段式降级的中段）。
const cacheTTL = 24 * time.Hour

// DefaultCacheDir 默认模型缓存目录：$FORGE_MODEL_CACHE_DIR，否则 <~/.forge>/cache/models。
func DefaultCacheDir() string {
	if v := os.Getenv("FORGE_MODEL_CACHE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".forge", "cache", "models")
	}
	return filepath.Join(home, ".forge", "cache", "models")
}

// cachePath 磁盘缓存文件路径。
func cachePath(typ ProviderType, cacheDir string) string {
	return filepath.Join(cacheDir, string(typ)+".json")
}

// snapshotCacheFile 与快照同构的缓存文件（含 source；读取只取 models）。
type snapshotCacheFile struct {
	Source string  `json:"source"`
	Models []Model `json:"models"`
}

// writeCache 原子写缓存（Temp+rename，避免半写文件被并发读）。
func writeCache(typ ProviderType, cacheDir string, models []Model) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snapshotCacheFile{Source: string(SourceCache), Models: models}, "", "  ")
	if err != nil {
		return err
	}
	tmp := cachePath(typ, cacheDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath(typ, cacheDir))
}

// readCache 读未过期缓存；缺失/过期/非法 → (nil, false)，由调用方继续降级。
func readCache(typ ProviderType, cacheDir string) ([]Model, bool) {
	p := cachePath(typ, cacheDir)
	fi, err := os.Stat(p)
	if err != nil {
		return nil, false
	}
	if time.Since(fi.ModTime()) > cacheTTL {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var f snapshotCacheFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false
	}
	return f.Models, true
}

// snapshotModels 读 go:embed 内置快照 catalog/snapshots/<typ>.json。
func snapshotModels(typ ProviderType) ([]Model, error) {
	b, err := snapshotsFS.ReadFile("catalog/snapshots/" + string(typ) + ".json")
	if err != nil {
		return nil, err
	}
	var f snapshotCacheFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Models, nil
}
