package assembly

// ——— 应用市场装配（M-A3/M-A7）———
//
// 把 infra 的 `appmarket.Market`（清单拉取/缓存/内置兜底）适配成控制器认的窄端口 `ctrapp.MarketSource`：
// 控制器不认识 appmarket 的结构，只收 `[]dtoapp.MarketListing`（本文件做 Listing → DTO 翻译）。
//
// 来源根（M-A7）：市场清单 dir 条目的解析根，由 `MATE_APP_SOURCE_ROOTS`（冒号分隔的绝对路径）+
// 默认发行根 `apps/` 目录给出，注入 svcapp.WithSourceRoots（installable 判定）与 appmarket（越界校验）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/internal/controller/ctrapp"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/infra/appmarket"
)

// appMarketSourceAdapter 把 *appmarket.Market 包成控制器窄端口；nil 市场 → nil 端口（控制器回空数组）。
func appMarketSourceAdapter(m *appmarket.Market) ctrapp.MarketSource {
	if m == nil {
		return nil
	}
	return appMarketSource{m: m}
}

// appMarketSource 把 *appmarket.Market 适配成 ctrapp.MarketSource（翻译清单结构 → DTO，结构不跨层）。
type appMarketSource struct {
	m *appmarket.Market
}

// List 读市场清单并翻译成 DTO 形态（appmarket.Listing → dtoapp.MarketListing）。
func (s appMarketSource) List(ctx context.Context) ([]dtoapp.MarketListing, error) {
	listings, _, err := s.m.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]dtoapp.MarketListing, 0, len(listings))
	for _, l := range listings {
		out = append(out, dtoapp.MarketListing{
			ID:             l.ID,
			Name:           l.Name,
			Icon:           l.Icon,
			Summary:        dtoapp.MarketSummary{ZH: l.Summary.ZH, EN: l.Summary.EN},
			Version:        l.Version,
			Homepage:       l.Homepage,
			Source:         dtoapp.MarketSource{Type: l.Source.Type, Dir: l.Source.Dir},
			MinCoreVersion: l.MinCoreVersion,
			Tags:           append([]string(nil), l.Tags...),
		})
	}
	return out, nil
}

// Refresh 清空清单缓存（转发 appmarket.Refresh）。
func (s appMarketSource) Refresh() { s.m.Refresh() }

// appMarket 构造应用市场清单读取器（M-A3）：来源根做清单越界校验，磁盘缓存落 <HomeDir>/market。
// 远端地址由 MATE_APP_MARKET 控制（空/off = 只用内置清单），见 appmarket 包。
func (a *assembly) appMarket(sourceRoots []string) *appmarket.Market {
	var cacheDir string
	if a.cfg.HomeDir != "" {
		cacheDir = filepath.Join(a.cfg.HomeDir, "market")
	}
	return appmarket.New(appmarket.Options{SourceRoots: sourceRoots, CacheDir: cacheDir})
}

// appSourceRootsEnv 是应用来源根环境变量（冒号分隔的绝对路径）。
const appSourceRootsEnv = "MATE_APP_SOURCE_ROOTS"

// resolveAppSourceRoots 解析应用来源根：`MATE_APP_SOURCE_ROOTS`（冒号分隔，取绝对存在的目录）+
// 默认发行根 `apps/` 目录（若存在）。返回顺序：env 里的在前，默认发行根在后。
//
// 默认发行根：优先取可执行文件同级的 `apps/`（随发行包分发的示例应用包），其次当前工作目录的 `apps/`。
// 只收绝对路径且真实存在的目录（防把相对路径当根导致越界校验空转）。
func resolveAppSourceRoots() []string {
	var roots []string
	seen := map[string]bool{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return
		}
		if seen[abs] {
			return
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return
		}
		seen[abs] = true
		roots = append(roots, abs)
	}

	// 1) env：冒号分隔的绝对路径。
	if v := strings.TrimSpace(os.Getenv(appSourceRootsEnv)); v != "" {
		for _, p := range strings.Split(v, string(os.PathListSeparator)) {
			add(p)
		}
	}

	// 2) 默认发行根 apps/：可执行文件同级优先，再退当前工作目录。
	if exe, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(exe), "apps"))
	}
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "apps"))
	}
	return roots
}
