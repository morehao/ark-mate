package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/glog"
)

// 开发期资产重建通知（P6-A，2026-10-11）。
//
// **它解决什么**：P1 取消了"从源码现编译"的 dev server（`apps/web/vite.config.ts` 的
// rejectStandaloneServe），改 UI 走 `make dev`（`vite build --watch` 重写 dist → 核心服务
// **同一份产物**）。代价是**失去 Fast Refresh**：改完要等重建（≈0.4–0.5s）**再手动刷新**。
// 本文件是那条回路的上半段：宿主自己盯着**它正在服务的那份目录**，变了就经 SSE 告诉页面。
//
// 三条纪律（每条都对应一个具体的失效形态，别改：
//
//  1. **默认关闭**（零值即关）。这条路由**只有装配层显式打开才注册**——交付形态（桌面壳、
//     容器、自建二进制）下 `/dev/assets/events` 根本不存在，会走 MountWeb 的 SPA 回落。
//     与 DSH 的偏离写在这里：DSH 的 `/plugins/events` 通道是**常挂**的（它还要带运行期图更新
//     `graph` 帧，见 packages/client/hmr/src/index.ts 的包注释），本仓没有运行期图更新要带，
//     没有理由让交付态长跑一个 500ms 的轮询。
//  2. **判据是内容摘要，不是 mtime**。mtime 在本仓的资产层已经被判过不可靠（部署方式决定：
//     解包/COPY/rsync 各不同，见 web.go 的 AssetInfo 注释）。这里更具体：`vite build --watch`
//     会在**内容没变**时也写产物（空转重写），拿 mtime 当判据会让页面白刷一次。
//     所以指纹（路径+大小+mtime）只做**廉价触发**，真正决定"要不要推帧"的是内容合成摘要。
//  3. **它是一条"通知"通道，不是第二条资产路径**。页面还是只执行 `--web-dir` 那一份产物，
//     本文件不改任何资产来源、也不给前端编译期开关（那会变成新的 import.meta.env 分支，
//     正是 P1 消灭的东西）。页面侧代码在同一份产物里，**通道在不在**由"核心有没有注册这条
//     路由"决定（见 packages/console/src/transport/assetWatch.ts 的 HEAD 探测）。
//
// 另有一处与 DSH 的能力差别要写明：DSH 收到 rebuilt 后做的是模块级重度装载
// （`entries.reload(id, rev)`，页面状态保住），那需要它的模块系统；本仓今天没有，
// 所以页面侧只能整页 `location.reload()`。"不用手按 F5"回来了，"页面状态不丢"没有。

// AssetWatchPath 开发期资产重建通知的端点（SSE；GET 流式、HEAD 探测）。
const AssetWatchPath = "/dev/assets/events"

// AssetWatchInterval 默认轮询间隔（与 DSH 的 pollIntervalMs 默认值一致：500ms；
// 它是"构建侧 watcher 的轮询默认值"）。间隔越短越跟手、越费 CPU；实测一次全树指纹
// （1.4MB 产物的 stat 走查）远低于 1ms，所以 500ms 在这个规模上几乎是白给。
const AssetWatchInterval = 500 * time.Millisecond

// watchFrame SSE 的帧体。页面**不需要**理解它（收到 rebuilt 就刷新），带上是为了
// 让人手 `curl -N` 时能看出"这次推的是哪一份产物"，以及为将来"页面自己比对"留出可能。
type watchFrame struct {
	// Digest 内容合成摘要（`sha256:<hex>`，见 CompositeDigest）。
	Digest string `json:"digest"`
	// BuildID 该产物里的构建 id（build-id.txt；读不到即 unknown）。
	BuildID string `json:"buildId"`
}

// AssetWatcher 盯着一份**已经解析好**的资产目录，变化时向订阅者广播一帧。
//
// 生命周期由调用方给的 ctx 兜住（装配层传进程生命周期那个），Stop 是幂等的提前收尾。
type AssetWatcher struct {
	assets   AssetInfo
	interval time.Duration

	mu        sync.Mutex
	subs      map[chan watchFrame]struct{}
	published string // 已对外发布的合成摘要（起点 = 解析期那份，见 NewAssetWatcher）

	// run 循环独占以下两个字段（不加锁：只有它读写）。
	lastFP  string
	pending bool

	quit     chan struct{}
	stopOnce sync.Once
}

// NewAssetWatcher 构造（尚未开始轮询；由 Start 拉起）。
//
// `published` 的起点是**解析期**算出的那份摘要（ResolveAssets → withDigests）。这一点很关键：
// 否则启动后的第一拍会把"本来就是当前这份"当成一次变化推出去，页面白刷。
func NewAssetWatcher(assets AssetInfo, interval time.Duration) *AssetWatcher {
	if interval <= 0 {
		interval = AssetWatchInterval
	}
	return &AssetWatcher{
		assets:    assets,
		interval:  interval,
		subs:      map[chan watchFrame]struct{}{},
		published: assets.Digest,
		quit:      make(chan struct{}),
	}
}

// Start 拉起轮询（ctx 结束或 Stop 即退出）。**要求 assets.Has()**：调用方在没资产时不该构造它。
func (w *AssetWatcher) Start(ctx context.Context) {
	// 起点指纹：启动那一刻的目录状态。取不到（正被写）也照常起——下一拍会补上。
	w.lastFP, _ = fingerprint(w.assets.FS)
	go w.run(ctx)
}

// Stop 提前收尾（幂等）。
func (w *AssetWatcher) Stop() {
	w.stopOnce.Do(func() { close(w.quit) })
}

// Mount 注册这条端点（**只在开关打开时被调用**，见 router.New 的注册点）。
//
// GET = SSE 流；HEAD = 探测（页面侧据此判断"开发形态的通道在不在"，而**不是**用编译期常量）。
// 两个方法都要注册：gin 不会把 HEAD 落到 GET 上（没注册就是 404，而 404 会让探测退化成
// "通道不存在"以外的另一种失败形态，说不清）。
func (w *AssetWatcher) Mount(r *gin.Engine) {
	r.GET(AssetWatchPath, w.serve)
	r.HEAD(AssetWatchPath, w.probe)
}

// probe HEAD 探测：只回头，立刻结束。Content-Type 就是页面侧要认的那个判据。
func (w *AssetWatcher) probe(c *gin.Context) {
	setEventStreamHeaders(c)
	c.Status(http.StatusOK)
}

// serve SSE 流：首帧 hello（带当前摘要，便于人手核对）→ 每有重建推一帧 rebuilt。
//
// 心跳是 SSE 注释行（`: ping`），不是帧：注释不被 EventSource 交给业务代码，
// 只用来穿过中间层/内核的空闲超时（回环直连下几乎用不到，但代理存在时就重要了）。
func (w *AssetWatcher) serve(c *gin.Context) {
	ch, unsubscribe := w.subscribe()
	defer unsubscribe()

	setEventStreamHeaders(c)
	c.Status(http.StatusOK)

	w.mu.Lock()
	current := watchFrame{Digest: w.published, BuildID: w.assets.BuildID}
	w.mu.Unlock()
	writeSSE(c, "hello", current)

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-w.quit:
			return
		case frame := <-ch:
			writeSSE(c, "rebuilt", frame)
		case <-ping.C:
			_, _ = c.Writer.WriteString(": ping\n\n")
			c.Writer.Flush()
		}
	}
}

// setEventStreamHeaders SSE 的响应头。`no-store` 是必须的：这条流的语义就是"永远不要缓存"，
// 而它在交付形态下并不存在（走 SPA 回落），所以这里不需要与 cacheControlFor 的三档对齐。
func setEventStreamHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("Connection", "keep-alive")
	// 反代（自建/容器形态）默认会缓冲响应体，缓冲住 SSE 就等于通道失效。
	c.Header("X-Accel-Buffering", "no")
}

// writeSSE 写一帧并立刻 flush（不 flush 的话帧会攒在 bufio 里，页面收不到）。
func writeSSE(c *gin.Context, event string, payload watchFrame) {
	b, err := json.Marshal(payload)
	if err != nil {
		// watchFrame 全是字符串，marshal 不可能失败；真失败了也只能跳过这一帧。
		glog.Warnw(c.Request.Context(), "dev 资产通知：帧序列化失败（跳过）", "err", err)
		return
	}
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b)
	c.Writer.Flush()
}

// subscribe 加一个订阅者。返回的取消函数**不关闭** channel：
// 广播是"加锁取快照 → 解锁后非阻塞发"，关闭会与在途发送竞争（panic: send on closed channel），
// 而缓冲为 1 的丢弃语义已经够用——这条通道只关心"最新一次重建"。
func (w *AssetWatcher) subscribe() (chan watchFrame, func()) {
	ch := make(chan watchFrame, 1)
	w.mu.Lock()
	w.subs[ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		delete(w.subs, ch)
		w.mu.Unlock()
	}
}

// broadcast 非阻塞广播：订阅者没在读（页面卡住/刚 reload）就丢掉这一帧，
// 下一次变化会再推。**不排队**——排队意味着页面 reload 回来会补收一串过期帧。
func (w *AssetWatcher) broadcast(frame watchFrame) {
	w.mu.Lock()
	subs := make([]chan watchFrame, 0, len(w.subs))
	for ch := range w.subs {
		subs = append(subs, ch)
	}
	w.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- frame:
		default:
		}
	}
}

// run 轮询循环：两段判据（指纹 → 内容摘要），中间隔着"必须安静一拍"。
//
// 为什么要"安静一拍"（debounce）：一次前端重建会连续写好几个文件
// （index.html、assets/*.js、build-id.txt），中间任何一拍都可能落在写到一半的状态上。
// 指纹变了就**只记待定、不发布**；等到某一拍指纹与上一拍完全相同（写入停了），
// 才去算内容摘要并发布。于是"一次重建 = 最多一帧"，也不会把半成品当成新版本。
func (w *AssetWatcher) run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.quit:
			return
		case <-ticker.C:
		}

		fp, err := fingerprint(w.assets.FS)
		if err != nil {
			// 目录正在写（文件消失/权限瞬时变化）→ 下一拍再看，不发布。
			continue
		}
		if fp != w.lastFP {
			w.lastFP = fp
			w.pending = true
			continue
		}
		if !w.pending {
			continue
		}
		w.pending = false

		// 复用解析期那条路重算：walk + 逐文件 sha256 + 合成摘要。
		// 走查失败/空树（"Digest 为空"）一律**不发布**：那种值会让页面按一个不存在的
		// 版本去刷新，而正确的处置是等下一拍。
		fresh, _ := withDigests(AssetInfo{Path: w.assets.Path, FS: w.assets.FS})
		if fresh.Digest == "" || fresh.Digest == w.published {
			continue
		}

		w.mu.Lock()
		w.published = fresh.Digest
		w.mu.Unlock()
		frame := watchFrame{Digest: fresh.Digest, BuildID: fresh.BuildID}
		w.broadcast(frame)
		glog.Infow(ctx, "dev 资产已重建，通知页面刷新（P6-A）",
			"dir", w.assets.Path, "digest", fresh.Digest, "buildId", fresh.BuildID)
	}
}

// fingerprint 目录的**廉价**指纹：路径 + 大小 + mtime（纳秒）。
//
// 它的职责只是"看起来变了吗"，用来决定**要不要花力气算内容摘要**（1.4MB 的逐文件 sha256
// 在每拍都做也不算贵，但没必要）。判据的最终归属仍是内容摘要——指纹相同而内容不同
// （同一纳秒内改回同样大小，现实中不会发生）最多让我们晚一拍发现。
//
// 错就错在"指纹当判据"：`vite build --watch` 空转重写会让指纹变、内容不变，
// 拿指纹发布就会让页面白刷一次（A-5 钉的就是这条）。
func fingerprint(fsys fs.FS) (string, error) {
	if fsys == nil {
		return "", fmt.Errorf("资产 FS 为空")
	}
	h := sha256.New()
	// fs.WalkDir 按字典序遍历，所以不需要额外排序（CompositeDigest 那条路才需要）。
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", p, fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
