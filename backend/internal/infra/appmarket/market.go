package appmarket

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ——— 清单读取：内存缓存 → 远端 → 磁盘缓存 → 内置 embed（三级兜底）———
//
// 形态照搬 magpie 的 market（`plugin/market.go`，把「远端拉取失败 → 磁盘缓存 → 内置兜底」做得很稳）。
// ark-mate 的偏离：市场是「一份可换源的清单」，不做 npm 搜索（分发单位是带 manifest 的应用包，
// 不是 npm 包），少一层外部依赖，契合「单机个人助手」定位。
//
// 读取顺序（Market.List）：
//  1. 内存缓存命中且未过期（TTL 6h）→ 直接返回；
//  2. 未命中 → 按来源（MATE_APP_MARKET，默认内置）拉取：
//     - off      → 直接用内置 embed（不联网）；
//     - 空       → 默认视为 off（本期默认只用内置，远端 registry 地址由后续配置注入）；
//     - URL      → HTTP GET（6s 超时）→ 成功则解析并写磁盘缓存 → 失败进 3；
//  3. 磁盘缓存（若存在且可解析）→ 返回；
//  4. 内置 embed market.json（离线兜底，随包分发；恒可用）。
//
// 任一层解析失败都向下退一层，绝不白屏、不报错（诊断信息通过 Source 暴露给前端提示）。

// marketFileEnv 是覆盖远端市场清单地址的环境变量名：URL（启用远端）或 `off`（只用内置）。
const marketFileEnv = "MATE_APP_MARKET"

// cacheTTL 是内存缓存有效期（6h，与 magpie 一致；防远端抖动 + 减少请求）。
const cacheTTL = 6 * time.Hour

// fetchTimeout 是单次远端拉取超时（6s；离线时快速退化到缓存/内置）。
const fetchTimeout = 6 * time.Second

// builtinFS 内置清单（go:embed，随二进制分发，离线兜底恒可用）。
//
//go:embed market.json
var builtinFS embed.FS

// builtinMarketFile 是内置清单在 embed 里的文件名。
const builtinMarketFile = "market.json"

// cacheFileName 是磁盘缓存文件名（落在构造时的 cacheDir 下）。
const cacheFileName = "market.cache.json"

// SourceKind 表示一次清单读取实际来自哪一层（供前端顶部「来源提示」用）。
type SourceKind string

const (
	// SourceBuiltin 内置 embed 清单（离线兜底 / off）。
	SourceBuiltin SourceKind = "builtin"
	// SourceRemote 远端 registry 拉取成功。
	SourceRemote SourceKind = "remote"
	// SourceCache 远端失败、退回磁盘缓存。
	SourceCache SourceKind = "cache"
)

// Options 是 Market 的构造参数。
type Options struct {
	// SourceRoots 允许的应用来源根（绝对路径集合）。用于校验清单 dir 条目的 source.dir 不越界
	// （防指向 /etc）。空 = 不做来源根越界校验（M-A1 单测常用空）。
	SourceRoots []string
	// CacheDir 磁盘缓存目录（落 market.cache.json）。空 = 不落磁盘缓存（只用内存+内置）。
	CacheDir string
	// HTTPClient 可注入的 HTTP 客户端（测试用；空则用带 fetchTimeout 的默认客户端）。
	HTTPClient *http.Client
	// Now 可注入的时钟（测试用；空则用 time.Now）。
	Now func() time.Time
}

// Market 是清单读取器（内存缓存 + 三级兜底）。并发安全。
type Market struct {
	opts   Options
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	cached    []Listing
	source    SourceKind
	fetchedAt time.Time
	hasCache  bool
}

// New 构造 Market。
func New(opts Options) *Market {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Market{opts: opts, client: client, now: now}
}

// List 返回市场清单（内存缓存命中则直接返回；否则按来源走三级兜底）。
//
// 返回值 ([]Listing, SourceKind, error)：error 在**内置 embed 也解析失败**（属构建事故，正常不会发生）
// 时才非 nil；正常路径恒成功（最差退回内置）。
func (m *Market) List(ctx context.Context) ([]Listing, SourceKind, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.hasCache && m.now().Sub(m.fetchedAt) < cacheTTL {
		return cloneListings(m.cached), m.source, nil
	}

	listings, src := m.load(ctx)
	m.cached = listings
	m.source = src
	m.fetchedAt = m.now()
	m.hasCache = true
	return cloneListings(listings), src, nil
}

// Refresh 清空内存缓存，使下次 List 强制重新走来源拉取（前端「刷新」按钮用）。不清磁盘缓存。
func (m *Market) Refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hasCache = false
	m.cached = nil
	m.fetchedAt = time.Time{}
	m.source = ""
}

// load 执行实际的来源拉取与三级兜底（调用方持锁）。
func (m *Market) load(ctx context.Context) ([]Listing, SourceKind) {
	src := strings.TrimSpace(os.Getenv(marketFileEnv))
	switch strings.ToLower(src) {
	case "", "off":
		// 默认/关闭：只用内置（不联网）。
		listings, _ := m.loadBuiltinLocked()
		return listings, SourceBuiltin
	}

	// src 视为远端 URL：拉取 → 成功则解析 + 写缓存；失败退磁盘缓存 → 内置。
	if listings, ok := m.fetchRemote(ctx, src); ok {
		m.writeDiskCache(listings)
		return listings, SourceRemote
	}
	if listings, ok := m.loadDiskCache(); ok {
		return listings, SourceCache
	}
	listings, _ := m.loadBuiltinLocked()
	return listings, SourceBuiltin
}

// fetchRemote 从远端 URL 拉取清单并解析。成功返回 (listings, true)；任何失败返回 (nil, false)。
func (m *Market) fetchRemote(ctx context.Context, url string) ([]Listing, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 上限 4MB，防灌爆
	if err != nil {
		return nil, false
	}
	f, err := Parse(body, m.opts.SourceRoots)
	if err != nil {
		return nil, false
	}
	return f.Apps, true
}

// loadDiskCache 读磁盘缓存（若配置了 CacheDir 且文件存在且可解析）。失败返回 (nil,false)。
func (m *Market) loadDiskCache() ([]Listing, bool) {
	if strings.TrimSpace(m.opts.CacheDir) == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(m.opts.CacheDir, cacheFileName))
	if err != nil {
		return nil, false
	}
	f, err := Parse(data, m.opts.SourceRoots)
	if err != nil {
		return nil, false
	}
	return f.Apps, true
}

// writeDiskCache 尽力写磁盘缓存（失败静默；缓存只是加速与降级，不是正确性依赖）。
func (m *Market) writeDiskCache(listings []Listing) {
	dir := strings.TrimSpace(m.opts.CacheDir)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(MarketFile{Version: 1, Apps: listings})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, cacheFileName), data, 0o644)
}

// loadBuiltinLocked 解析内置 embed 清单。失败返回 (nil, error)（属构建事故）。
func (m *Market) loadBuiltinLocked() ([]Listing, error) {
	data, err := builtinFS.ReadFile(builtinMarketFile)
	if err != nil {
		return nil, fmt.Errorf("appmarket: 读取内置清单失败（构建事故）: %w", err)
	}
	f, err := Parse(data, m.opts.SourceRoots)
	if err != nil {
		return nil, fmt.Errorf("appmarket: 解析内置清单失败（构建事故）: %w", err)
	}
	return f.Apps, nil
}

// cloneListings 浅拷贝切片（防止调用方改动缓存内部切片）。Listing 内含切片字段（Tags），
// 调用方如需改动 Tags 应自行深拷贝——本包保证返回的切片本身与缓存隔离。
func cloneListings(in []Listing) []Listing {
	if in == nil {
		return nil
	}
	out := make([]Listing, len(in))
	copy(out, in)
	return out
}
