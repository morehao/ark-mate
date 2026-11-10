package svcapp

// ——— 应用市场：清单条目 + 本地装态 合并派生（M-A2）———
//
// 本方法只做**只读派生**：把一份市场清单（每条 = 一个可安装的应用包）与本地事实（apps 定义表 +
// 运行期包目录）合并，算出每条的 installed/enabled/updatable/installable/state。
//
// 为什么不放 appmarket：装态派生需要 store（定义表）+ runtime（本地包目录），那是用例层的能力；
// appmarket 只做纯清单（远端/缓存/内置）。分层：svcapp 收 dtoapp.MarketListing（跨层 DTO），
// 不 import infra/appmarket。派生结果不落库（与 C6「state 是派生的」同一纪律）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/core/apppkg"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
)

// MarketApps 把市场清单条目与本地装态合并，派生每条的四态 + state。
//
// 对每条 listing：
//   - installed    = runtime.HasPack(id) || apps 表有定义；
//   - enabled      = 定义的 enabled（无定义 = false）；
//   - state        = 已装时的生命周期态（走既有 stateOfApp 派生）；未装为空；
//   - updatable    = 已装本地包版本 < 清单 version（读 appsRoot/<id>/manifest.json 的 version 比）；
//   - installable  = source.type==dir && 来源根下目录存在 && 非「已装且版本相同」。
//
// 逐条失败隔离：单条错误（如读本地 manifest 损坏）不使整表失败——能力允许时降级该条的字段。
func (s *appSvc) MarketApps(ctx context.Context, listings []dtoapp.MarketListing) ([]dtoapp.MarketAppResp, error) {
	out := make([]dtoapp.MarketAppResp, 0, len(listings))
	for i := range listings {
		resp, err := s.deriveMarketApp(ctx, &listings[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *resp)
	}
	return out, nil
}

// deriveMarketApp 派生单条市场条目的装态。
func (s *appSvc) deriveMarketApp(ctx context.Context, l *dtoapp.MarketListing) (*dtoapp.MarketAppResp, error) {
	resp := &dtoapp.MarketAppResp{
		ID:       l.ID,
		Name:     l.Name,
		Icon:     l.Icon,
		Summary:  dtoapp.MarketSummary{ZH: l.Summary.ZH, EN: l.Summary.EN},
		Version:  l.Version,
		Homepage: l.Homepage,
		Tags:     cloneStrings(l.Tags),
		Source:   dtoapp.MarketSource{Type: l.Source.Type, Dir: l.Source.Dir},
	}

	// 本地已装事实：运行期包目录 or apps 定义表。
	hasPack := s.runtime != nil && s.runtime.HasPack(l.ID)
	var defEnabled bool
	var defApp *capability.App
	if s.store != nil {
		row, ok, err := s.store.GetAppDefinition(ctx, l.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			app, aerr := rowToApp(row)
			if aerr != nil {
				return nil, aerr
			}
			defApp = app
			defEnabled = app.IsEnabled()
		}
	}
	installed := hasPack || defApp != nil
	resp.Installed = installed
	resp.Enabled = defEnabled

	// state：已装且派生能力在时走既有 stateOfApp；未装留空。
	if installed && defApp != nil {
		st, _ := s.stateOfApp(ctx, defApp, defEnabled)
		resp.State = st
	} else if installed && hasPack {
		// 有包但无定义（极端边界）：按 off 呈现，仍算已装。
		resp.State = AppStateOff
	}

	// updatable：已装本地包版本 < 清单 version。
	if hasPack {
		if localVer, ok := s.localPackVersion(l.ID); ok {
			if cmp, err := compareVersions(localVer, l.Version); err == nil && cmp < 0 {
				resp.Updatable = true
			}
		}
	}

	// 解析来源根下目录（若可解析 = 落在某个来源根内且真实存在）。解析成功即把 **绝对路径** 回填到
	// resp.Source.Dir——前端把 source.dir 直接透传给 installApp(dir)（POST /apps/install {dir}），
	// 而 InstallApp 需要的是包根绝对目录（它内部对 filepath.Dir(srcDir) 做 Distribute）。
	// 若不可解析（目录缺失/越界/url 类型），保留清单原文的 dir（前端据此仍知道指向哪个相对路径）。
	resolvedDir, resolvedOK := s.resolveSourceDir(l.Source.Dir)
	if resolvedOK {
		resp.Source.Dir = resolvedDir
	}

	// installable：dir 类型 && 目录可解析（存在且在来源根内）&& 非「已装且版本相同」。
	resp.Installable = s.marketInstallable(l, installed, hasPack, resolvedOK)
	return resp, nil
}

// marketInstallable 判定一条清单条目是否可一键安装：
//   - source.type 必须是 dir（url 属 C8 远程分发，本期不可装）；
//   - 目录必须落在某个来源根内且真实存在（resolvedOK；防清单指向 /etc 这类系统路径）；
//   - 「已装且本地包版本 == 清单版本」→ 无需再装 → 不可装（重复装同版本无意义）。
func (s *appSvc) marketInstallable(l *dtoapp.MarketListing, installed, hasPack, resolvedOK bool) bool {
	if l.Source.Type != "dir" || l.Source.Dir == "" {
		return false
	}
	if !resolvedOK {
		return false
	}
	// 已装且版本相同 → 不可装。
	if installed && hasPack {
		if localVer, ok := s.localPackVersion(l.ID); ok {
			if cmp, err := compareVersions(localVer, l.Version); err == nil && cmp == 0 {
				return false
			}
		}
	}
	return true
}

// resolveSourceDir 把一个相对来源根路径解析成真实存在的绝对目录（落在某个来源根内才返回）。
// 复用 apppkg.WithinDir 做越界校验（防 `..` 上跳 / 指向系统路径）。
func (s *appSvc) resolveSourceDir(rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	for _, root := range s.sourceRoots {
		if root == "" {
			continue
		}
		candidate := filepath.Join(root, filepath.FromSlash(rel))
		if !apppkg.WithinDir(root, candidate) {
			continue
		}
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// localPackVersion 读本地已装包 `appsRoot/<id>/manifest.json` 的 version。
// 读不到 / 解析失败返回 ("", false)（updatable/installable 判定降级为「未知」）。
func (s *appSvc) localPackVersion(id string) (string, bool) {
	path := filepath.Join(s.appsRoot(), id, apppkg.ManifestFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	if m.Version == "" {
		return "", false
	}
	return m.Version, true
}

// compareVersions 比较两个语义化版本串（major.minor.patch，忽略预发布/构建后缀），
// 复用 apppkg 的版本排序口径（同一套解析，避免两处口径漂移）。
// 返回 a<b → -1、a==b → 0、a>b → +1；无法解析 → error（调用方降级为「未知」）。
func compareVersions(a, b string) (int, error) {
	return apppkg.CompareVersions(a, b)
}
