package assembly

// ——— M-A7：示例包「来源根解析 → 市场派生 → 一键装通」端到端 ———
//
// 验证：把 MATE_APP_SOURCE_ROOTS 指到仓库 examples/ 目录（其下有 apps/weather-query 等示例包），
// 走真实链路：appmarket 内置清单 → 适配器翻译 DTO → svcapp.MarketApps 派生 installable + 绝对 source.dir
// → svcapp.InstallApp 真装到临时 appsRoot → 再派生 installed=true。
//
// 关键点：
//   - source.dir 相对来源根解析成绝对路径（前端直接喂 installApp(dir) 能装通）；
//   - withinDir 防越界（`../etc` 之类不可装）；
//   - 版本门：示例包声明 min_core_version=1.0.0，测试临时把 version.Version 置成 1.0.0 以过门槛
//     （dev 构建的 "dev" 会 fail-closed 拒绝，这是正确的版本门行为）。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/infra/appmarket"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcapp"
	"github.com/morehao/ark-mate/internal/version"
)

// fakeMarketRuntime 是市场 e2e 用的最小 AppRuntimePort：只维护「本地有包」标记。
type fakeMarketRuntime struct {
	packs map[string]bool
}

func newFakeMarketRuntime() *fakeMarketRuntime {
	return &fakeMarketRuntime{packs: map[string]bool{}}
}

func (f *fakeMarketRuntime) EnableApp(context.Context, svcapp.AppRuntimeRef) error  { return nil }
func (f *fakeMarketRuntime) DisableApp(context.Context, svcapp.AppRuntimeRef) error { return nil }
func (f *fakeMarketRuntime) RuntimeState(context.Context, svcapp.AppRuntimeRef) svcapp.RuntimeState {
	return svcapp.RuntimeState{State: svcapp.AppStateOff}
}
func (f *fakeMarketRuntime) HasPack(id string) bool { return f.packs[id] }
func (f *fakeMarketRuntime) UninstallPack(_ context.Context, id string) (bool, error) {
	delete(f.packs, id)
	return true, nil
}

// TestMarketInstallFromExamplesEndToEnd 端到端：来源根 → 市场派生 → 装通 examples/apps/weather-query。
func TestMarketInstallFromExamplesEndToEnd(t *testing.T) {
	// 示例包在仓库根 examples/apps/ 下（本测试文件在 backend/internal/bootstrap/assembly，上溯 4 级到仓库根）。
	repoExamples, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	// 来源根 = examples/（其下有 apps/weather-query，市场清单 dir=apps/weather-query 用）。
	t.Setenv("MATE_APP_SOURCE_ROOTS", repoExamples)

	// 版本门：临时把核心版本置 1.0.0（示例包 min_core_version=1.0.0），测试后还原。
	origVer := version.Version
	version.Version = "1.0.0"
	t.Cleanup(func() { version.Version = origVer })

	// 真实 store（临时库）+ 假运行期端口 + 临时 appsRoot。
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	appsRoot := t.TempDir()
	rt := newFakeMarketRuntime()
	svc := svcapp.NewApp(st, svcapp.WithRuntimePort(rt), svcapp.WithAppsRoot(appsRoot), svcapp.WithSourceRoots(resolveAppSourceRoots()))

	// 建真实 market（off 只用内置清单）并翻译 DTO。
	t.Setenv("MATE_APP_MARKET", "off")
	src := appMarketSourceAdapter(appmarket.New(appmarket.Options{SourceRoots: resolveAppSourceRoots()}))
	listings, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("市场清单读取失败：%v", err)
	}
	var weather *dtoapp.MarketListing
	for i := range listings {
		if listings[i].ID == "weather-query" {
			weather = &listings[i]
		}
	}
	if weather == nil {
		t.Fatalf("内置清单应含 weather-query，得到 %d 条", len(listings))
	}

	// 装前派生：weather-query 应 installable=true，source.dir 为绝对路径且指向真实示例包目录。
	before, err := svc.MarketApps(context.Background(), listings)
	if err != nil {
		t.Fatalf("MarketApps(before): %v", err)
	}
	var beforeWeather *dtoapp.MarketAppResp
	for i := range before {
		if before[i].ID == "weather-query" {
			beforeWeather = &before[i]
		}
	}
	if beforeWeather == nil || !beforeWeather.Installable {
		t.Fatalf("装前 weather-query 应 installable=true，得到 %+v", beforeWeather)
	}
	wantDir := filepath.Join(repoExamples, "apps", "weather-query")
	if beforeWeather.Source.Dir != wantDir {
		t.Fatalf("source.dir 应为绝对示例包目录 %q，得到 %q", wantDir, beforeWeather.Source.Dir)
	}
	if beforeWeather.Installed {
		t.Fatal("装前应 installed=false")
	}

	// 一键装通：直接用派生出来的 source.dir 装（前端就是透传它）。
	if _, err := svc.InstallApp(context.Background(), dtoapp.AppInstallReq{Dir: beforeWeather.Source.Dir, Enable: false}); err != nil {
		t.Fatalf("安装 weather-query 失败：%v", err)
	}
	// 假运行期端口需反映「本地有包」（真实现由装配根维护；测试手动置位以模拟装到本地）。
	rt.packs["weather-query"] = true

	// 装后派生：installed=true，且版本相同 → installable=false（无需再装）。
	after, err := svc.MarketApps(context.Background(), listings)
	if err != nil {
		t.Fatalf("MarketApps(after): %v", err)
	}
	var afterWeather *dtoapp.MarketAppResp
	for i := range after {
		if after[i].ID == "weather-query" {
			afterWeather = &after[i]
		}
	}
	if afterWeather == nil || !afterWeather.Installed {
		t.Fatalf("装后 weather-query 应 installed=true，得到 %+v", afterWeather)
	}
	if afterWeather.Installable {
		t.Fatal("装后（版本相同）应 installable=false")
	}
}

// TestSourceDirEscapeNotInstallable 断言来源根越界（防 /etc）的条目 installable=false。
func TestSourceDirEscapeNotInstallable(t *testing.T) {
	t.Setenv("MATE_APP_SOURCE_ROOTS", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := svcapp.NewApp(st, svcapp.WithSourceRoots(resolveAppSourceRoots()))

	got, err := svc.MarketApps(context.Background(), []dtoapp.MarketListing{{
		ID: "evil", Name: "evil", Version: "1.0.0",
		Summary: dtoapp.MarketSummary{ZH: "z"},
		Source:  dtoapp.MarketSource{Type: "dir", Dir: "../../etc"},
	}})
	if err != nil {
		t.Fatalf("MarketApps: %v", err)
	}
	if got[0].Installable {
		t.Fatal("越出来源根的 dir 应 installable=false（防指向 /etc）")
	}
}
