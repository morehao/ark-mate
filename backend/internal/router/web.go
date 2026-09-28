package router

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/pkg/web"
)

// 资产层（M2-1，设计 §6.1 / §6.2）：**两种来源，一套行为**。
//
// 这个文件替代了历史上的 web_release.go / web_dev.go 分叉：那时"有没有资产"由**构建 tag**决定，
// 于是默认构建只能到一个"能调 API 但打不开界面"的服务，桌面核又被迫带 release tag（与
// "桌面核不内嵌资产"冲突）。现在来源是**运行时解出来的**（显式目录 > 内嵌 > 拒绝启动），
// 构建形态只决定"内嵌那一份在不在"。

// AssetSource 资产来源（同时是 `/meta` 的 webSource 取值，设计 §6.7）。
type AssetSource string

const (
	// AssetSourceBuiltin 构建期内置（`-tags release` 的 //go:embed）。
	AssetSourceBuiltin AssetSource = "builtin"
	// AssetSourceDir 显式目录（`--web-dir` / `FORGE_WEB_DIR`）。
	AssetSourceDir AssetSource = "dir"
)

// AssetInfo 已解析的前端资产。零值（FS == nil）= 没有资产（只有测试会走到）。
type AssetInfo struct {
	Source AssetSource
	// Path 目录来源的**绝对路径**；builtin 为空。
	Path string
	// FS 资产根（内含 index.html 与 assets/）。
	FS fs.FS
	// BuildID 前端构建 id（M2-4 填；从 build-id.txt 读）。
	BuildID string
	// Digest 逐文件 sha256 的确定性合成（M2-3 填）。
	Digest string
}

// Has 报告是否真的拿到了资产。
func (a AssetInfo) Has() bool { return a.FS != nil }

// ResolveAssets 按设计 §6.1 解析资产来源。**无效即报错**（调用方按退出码 3 收尾）：
//
//   - 给了 webDir：必须绝对、存在、是目录、且含 index.html；合法就用它，**不回落内嵌**；
//   - 未给 webDir：用内嵌；内嵌也没有（默认构建）→ 报错，并给出"两条出路"的提示。
//
// "不回落"是刻意的（设计 §6.1 规则）：显式目录配错时必须**拒绝启动**，而不是悄悄用内嵌的旧资产——
// 后者表现为"用户看到旧界面，还以为是新版本没生效"，是最难排查的一类问题。
func ResolveAssets(webDir string) (AssetInfo, error) {
	dir := strings.TrimSpace(webDir)
	if dir == "" {
		sub, ok := web.Dist()
		if !ok {
			return AssetInfo{}, fmt.Errorf(
				"没有可用的前端资产：本构建未内嵌（未带 -tags release），也没有给 --web-dir\n" +
					"  独立二进制：用 `-tags release` 构建（资产进二进制）\n" +
					"  桌面形态：由壳传 --web-dir 指向包内那一份 dist")
		}
		return AssetInfo{Source: AssetSourceBuiltin, FS: sub}, nil
	}

	if !filepath.IsAbs(dir) {
		return AssetInfo{}, fmt.Errorf("--web-dir 必须是绝对路径，得到 %q", dir)
	}
	abs := filepath.Clean(dir)
	fi, err := os.Stat(abs)
	if err != nil {
		return AssetInfo{}, fmt.Errorf("--web-dir 指向的目录不可用：%s（%v）", abs, err)
	}
	if !fi.IsDir() {
		return AssetInfo{}, fmt.Errorf("--web-dir 不是目录：%s", abs)
	}
	index := filepath.Join(abs, "index.html")
	if _, err := os.Stat(index); err != nil {
		return AssetInfo{}, fmt.Errorf("--web-dir 目录里没有 index.html：%s（%v）", index, err)
	}
	return AssetInfo{Source: AssetSourceDir, Path: abs, FS: os.DirFS(abs)}, nil
}

// MountWeb 把已解析的资产挂到引擎上（设计 §6.2）。
//
// 路由分工：
//   - `GET /` → index.html；
//   - 命中存在的静态文件 → 原样返回（**带未来缓存头**，M2-2 统一）；
//   - 其余非 API 路径 → 回落 index.html（SPA 深链）；
//   - `/v1/*` 未注册路径 → 标准信封 404（**不回落**：把 API 404 变成 HTML 会让前端把
//     HTML 当 JSON 解析，报出与真实原因无关的错误）。
//
// 无资产时只注册信封 404 的 NoRoute（测试与"仅供 API"的进程会走到这里）。
func MountWeb(r *gin.Engine, assets AssetInfo) {
	if !assets.Has() {
		r.NoRoute(writeNotFound)
		return
	}
	index, err := fs.ReadFile(assets.FS, "index.html")
	if err != nil {
		// ResolveAssets 已经校验过 index.html 存在；这里再兜一次是因为 FS 也可能是内嵌的。
		panic(fmt.Sprintf("资产里读不到 index.html（来源 %s）：%v", assets.Source, err))
	}
	indexResp := func(c *gin.Context) {
		// 浏览器入口的文档也下发 CSP（与桌面壳同一个头，M1-5）：前端产物不得依赖 eval，
		// 也不允许任何外域连接——这条与"R3 零出站"是同一条纪律的两面。
		setDocumentCSP(c)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", cacheControlFor("/index.html"))
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write(index)
	}
	hfs := http.FS(assets.FS)

	r.GET("/", indexResp)
	r.NoRoute(func(c *gin.Context) {
		if isAPIPath(c.Request.URL.Path) {
			writeNotFound(c)
			return
		}
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if p == "index.html" {
			indexResp(c)
			return
		}
		if p != "" && !strings.HasSuffix(p, "/") {
			if fi, err := fs.Stat(assets.FS, p); err == nil && !fi.IsDir() {
				setDocumentCSP(c)
				c.Header("Cache-Control", cacheControlFor("/"+p))
				c.FileFromFS(p, hfs)
				return
			}
		}
		// SPA 深链：前端路由（/settings、/conversations/<id>…）由页面自己解析。
		indexResp(c)
	})
}

// cacheControlFor 缓存语义（设计 §6.2；M2-2 统一两侧）：
//
//   - `index.html`（含 `/`）→ `no-store`：它引用带内容 hash 的 assets，必须每次拿新的，
//     否则发版后页面还在引用旧 chunk；
//   - `/assets/*` → `immutable` 一年：文件名含内容 hash，内容变了文件名就变；
//   - 其余（favicon / manifest / 其它）→ `no-cache`（可缓存但每次revalidate）。
func cacheControlFor(path string) string {
	switch {
	case path == "/" || path == "/index.html":
		return "no-store"
	case strings.HasPrefix(path, "/assets/"):
		return "public, immutable, max-age=31536000"
	default:
		return "no-cache"
	}
}
