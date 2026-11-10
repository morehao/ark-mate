package svcapp_test

// ——— M-A2：svcapp.MarketApps 只读装态派生 ——
//
// 验证市场清单条目 + 本地事实（apps 定义表 + 运行期包目录 + 来源根）合并派生的四态：
//   - 空表 + 空包目录 → 全部 installed/enabled/updatable/installable=false；
//   - 装一个后 installed=true；enabled 跟随定义；
//   - 本地包版本 < 清单 version → updatable=true；相同 → installable=false；
//   - 来源根下目录存在 → installable=true；目录不存在/越界 → false。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/service/svcapp"
)

func listing(id, version, dir string) dtoapp.MarketListing {
	return dtoapp.MarketListing{
		ID:      id,
		Name:    id,
		Version: version,
		Summary: dtoapp.MarketSummary{ZH: "摘要"},
		Source:  dtoapp.MarketSource{Type: "dir", Dir: dir},
	}
}

func TestMarketAppsEmptyTableAllFalse(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()

	got, err := svc.MarketApps(ctx, []dtoapp.MarketListing{
		listing("weather-query", "1.0.0", "apps/weather-query"),
	})
	if err != nil {
		t.Fatalf("MarketApps: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应回 1 条，得到 %d", len(got))
	}
	r := got[0]
	if r.Installed || r.Enabled || r.Updatable || r.Installable || r.State != "" {
		t.Fatalf("空表 + 空包目录应全 false，得到 %+v", r)
	}
}

func TestMarketAppsInstalledAndEnabledFollowDefinition(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()

	// 建一条定义（enabled=true）。
	if _, err := svc.WriteApp(ctx, "", req(map[string]string{
		"id":      `"weather-query"`,
		"name":    `"天气查询"`,
		"enabled": `true`,
	}), true); err != nil {
		t.Fatalf("建定义失败：%v", err)
	}

	got, err := svc.MarketApps(ctx, []dtoapp.MarketListing{listing("weather-query", "1.0.0", "apps/weather-query")})
	if err != nil {
		t.Fatalf("MarketApps: %v", err)
	}
	r := got[0]
	if !r.Installed {
		t.Fatal("有定义应 installed=true")
	}
	if !r.Enabled {
		t.Fatal("定义 enabled=true 应反映 enabled=true")
	}
}

func TestMarketAppsUpdatableWhenLocalOlder(t *testing.T) {
	_, st := newSvc(t)
	appsRoot := t.TempDir()
	rt := newFakeRuntime()
	rt.packs["weather-query"] = true
	svc := svcapp.NewApp(st, svcapp.WithRuntimePort(rt), svcapp.WithAppsRoot(appsRoot))
	ctx := context.Background()

	// 本地包版本 1.0.0。
	writeInstalledManifest(t, appsRoot, "weather-query", "1.0.0")

	// 清单推荐 1.1.0 > 本地 → updatable。
	got, err := svc.MarketApps(ctx, []dtoapp.MarketListing{listing("weather-query", "1.1.0", "apps/weather-query")})
	if err != nil {
		t.Fatalf("MarketApps: %v", err)
	}
	r := got[0]
	if !r.Installed {
		t.Fatal("HasPack=true 应 installed=true")
	}
	if !r.Updatable {
		t.Fatal("本地 1.0.0 < 清单 1.1.0 应 updatable=true")
	}
}

func TestMarketAppsNotUpdatableWhenSameOrNewer(t *testing.T) {
	_, st := newSvc(t)
	appsRoot := t.TempDir()
	rt := newFakeRuntime()
	rt.packs["weather-query"] = true
	svc := svcapp.NewApp(st, svcapp.WithRuntimePort(rt), svcapp.WithAppsRoot(appsRoot))
	ctx := context.Background()

	writeInstalledManifest(t, appsRoot, "weather-query", "1.0.0")
	got, _ := svc.MarketApps(ctx, []dtoapp.MarketListing{listing("weather-query", "1.0.0", "apps/weather-query")})
	if got[0].Updatable {
		t.Fatal("版本相同应 updatable=false")
	}
}

func TestMarketAppsInstallableWhenSourceDirExists(t *testing.T) {
	_, st := newSvc(t)
	srcRoot := t.TempDir()
	// 造来源根下 apps/weather-query 目录。
	if err := os.MkdirAll(filepath.Join(srcRoot, "apps", "weather-query"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := svcapp.NewApp(st, svcapp.WithSourceRoots([]string{srcRoot}))
	ctx := context.Background()

	got, err := svc.MarketApps(ctx, []dtoapp.MarketListing{listing("weather-query", "1.0.0", "apps/weather-query")})
	if err != nil {
		t.Fatalf("MarketApps: %v", err)
	}
	if !got[0].Installable {
		t.Fatal("来源根下目录存在且未装 → installable=true")
	}
	// source.dir 应回填为**绝对路径**（前端把它直接透传给 installApp(dir)）。
	wantDir := filepath.Join(srcRoot, "apps", "weather-query")
	if got[0].Source.Dir != wantDir {
		t.Fatalf("installable 条目的 source.dir 应为解析后的绝对路径 %q，得到 %q", wantDir, got[0].Source.Dir)
	}
	if !filepath.IsAbs(got[0].Source.Dir) {
		t.Fatalf("source.dir 必须是绝对路径，得到 %q", got[0].Source.Dir)
	}
}

func TestMarketAppsNotInstallableWhenSourceDirMissing(t *testing.T) {
	_, st := newSvc(t)
	srcRoot := t.TempDir() // 空，无 apps/weather-query
	svc := svcapp.NewApp(st, svcapp.WithSourceRoots([]string{srcRoot}))
	ctx := context.Background()

	got, _ := svc.MarketApps(ctx, []dtoapp.MarketListing{listing("weather-query", "1.0.0", "apps/weather-query")})
	if got[0].Installable {
		t.Fatal("来源根下目录不存在 → installable=false")
	}
}

func TestMarketAppsNotInstallableWhenInstalledSameVersion(t *testing.T) {
	_, st := newSvc(t)
	appsRoot := t.TempDir()
	srcRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRoot, "apps", "weather-query"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := newFakeRuntime()
	rt.packs["weather-query"] = true
	svc := svcapp.NewApp(st, svcapp.WithRuntimePort(rt), svcapp.WithAppsRoot(appsRoot), svcapp.WithSourceRoots([]string{srcRoot}))
	ctx := context.Background()

	// 已装本地版本与清单相同 → 无需再装 → installable=false。
	writeInstalledManifest(t, appsRoot, "weather-query", "1.0.0")
	got, _ := svc.MarketApps(ctx, []dtoapp.MarketListing{listing("weather-query", "1.0.0", "apps/weather-query")})
	if got[0].Installable {
		t.Fatal("已装且版本相同 → installable=false")
	}
}

func TestMarketAppsSourceDirEscapeRejected(t *testing.T) {
	_, st := newSvc(t)
	srcRoot := t.TempDir()
	svc := svcapp.NewApp(st, svcapp.WithSourceRoots([]string{srcRoot}))
	ctx := context.Background()

	// `../etc` 越出来源根 → installable=false（防指向系统路径）。
	got, _ := svc.MarketApps(ctx, []dtoapp.MarketListing{listing("evil", "1.0.0", "../etc")})
	if got[0].Installable {
		t.Fatal("越出来源根的 dir 应 installable=false")
	}
}

// writeInstalledManifest 在 appsRoot/<id>/manifest.json 写一份最小清单（只用于 localPackVersion 读 version）。
func writeInstalledManifest(t *testing.T, appsRoot, id, version string) {
	t.Helper()
	dir := filepath.Join(appsRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + id + `","version":"` + version + `","entrypoint":"entrypoint.sh","integrity":{"sha256":"` + "0000000000000000000000000000000000000000000000000000000000000000" + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
