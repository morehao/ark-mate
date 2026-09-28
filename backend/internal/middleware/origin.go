package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// Host 白名单 + Origin 矩阵（M3-5；设计 §7.2「Origin 矩阵」行、§9.1「回环监听面」）。
//
// 存在的理由：核心默认只绑回环，但**回环不等于安全**——恶意网页可以用
// `fetch("http://127.0.0.1:10001/…")` 打本机服务（DNS rebinding 还能把 Host 换成攻击者的域名，
// 让同源策略失效）。回环面因此需要两道与凭据无关的判定（凭据之外的纵深）：
//
//  1. **Host 白名单**：Host 的主机部分必须是 127.0.0.1 / ::1 / localhost，且端口必须等于
//     本进程**实际监听端口**——DNS rebinding 的请求会带着攻击者的 Host，直接 421；
//  2. **Origin 矩阵**：浏览器对跨源请求（以及所有非 GET 的同源请求）都会带 Origin，
//     壳内反代/curl/页面导航不带。矩阵只放行三类 + 显式登记表，其余 403。
//
// 两条判定**只在一个函数里实现**（Decide），REST 中间件与 WS 握手共用同一个策略对象——
// 它们要拦的是同一件事，各写一份迟早分叉（WS 侧漏了 Host 判定就是"换个协议绕过白名单"）。

// DesktopAppOrigin 桌面壳渲染进程的 Origin（设计 §7.2 的 Origin 矩阵行）。
//
// 为什么它是**唯一**硬编码放行的自定义 scheme：桌面态的事件流由渲染进程**直连回环**
// （`protocol.handle` 不承载 WS upgrade），页面 origin 是 `forge-app://app`——它不是 http(s)，
// 无法用"自身监听端口的同源"这条规则表达。
const DesktopAppOrigin = "forge-app://app"

// loopbackHostNames Host 白名单的主机集合（大小写不敏感；端口另判）。
//
// 只有这三个：它们正是"本机"的通用写法（`[::1]` 在 net.SplitHostPort 后是 `::1`）。
// 显式绑 `127.0.0.2` 之类其它回环地址时白名单**不会启用**（见 NewOriginPolicy）——
// 白名单表达不了那个主机，硬启用只会把每个请求都打成 421，那是"服务不可用"而不是"更安全"。
var loopbackHostNames = []string{"127.0.0.1", "::1", "localhost"}

// maxEchoLen 拒绝文案里回显 Host/Origin 的截断长度。
//
// 这两个值**完全由调用方控制**：回显它们是为了让排障一眼看到"你到底从哪来的"（跨源开发时
// 尤其重要：文案会直接告诉他去 FORGE_ALLOWED_ORIGINS 登记什么），但无上限回显等于把响应体
// 交给对方决定大小。
const maxEchoLen = 120

// Decision 一次来源判定的结果。
type Decision struct {
	// Status 0 = 放行；否则是要回给调用方的 HTTP 状态码。
	Status int
	// Msg 拒绝原因（放行时为空）。不含敏感信息：只说"不符合哪条规则、该怎么改"。
	Msg string
}

// Allowed 报告是否放行。
func (d Decision) Allowed() bool { return d.Status == 0 }

// Err 把判定结果转成对外错误（放行时 nil）。
//
// 码取 InvalidRequest（40001）：这确实是**请求侧**的问题（换一个来源/主机就能过），
// 而不是服务内部故障。为什么不新加一个码：对外错误码是三处同步的契约
// （pkg/code ↔ README ↔ 前端 BizCode，由守卫钉住），本次范围不含前端；为什么不落 50000：
// 它是"未归类的内部错误"的兜底码，用它描述一次**刻意拒绝**会让客户端与运维以为是平台崩了。
func (d Decision) Err() error {
	if d.Allowed() {
		return nil
	}
	return code.Error(code.InvalidRequest, d.Msg)
}

// OriginPolicy 回环监听面的 Host 白名单 + Origin 矩阵。
//
// **端口从哪里来**：由 `net.Listener.Addr()` 给出的**实际**监听端口（桌面态是内核分配的随机
// 端口，`--addr 127.0.0.1:0`）。装机顺序因此是「先绑定 → 用真实地址构造策略 → 再装路由」，
// 而不是猜端口/跳过端口校验：端口是这套判定里**唯一定义"同源"的东西**（桌面态每次启动都变），
// 猜错就等于把"只有本页能调"放宽成"任意本地端口都能调"。
//
// 策略构造后**不可变**：没有 SetPort 之类的后置写入，也就不存在"忘了设端口 → 全部 421"
// 或"设晚了 → 前几个请求按旧端口放行"的窗口。
type OriginPolicy struct {
	enabled bool
	port    int
	// extra 额外允许表（FORGE_ALLOWED_ORIGINS，归一后的比较键）。
	extra map[string]bool
}

// NewOriginPolicy 用**实际监听地址**（`host:port`）与额外允许表构造策略。
//
// 启用条件（设计 §9.4）：监听主机必须是 Host 白名单集合里的回环主机。非回环监听
// （`--addr 0.0.0.0`、`FORGE_ADDR=:10001` 等）**不启用**——设计把 Host/Origin 白名单与
// "令牌强制/TLS 终止/多操作员审计"并列为对外产品化缺的四件事之一，本期范围外；`--addr`
// 的 flag 说明也写明对外暴露自担风险。装配层据此打 WARN，不在这里静默/假装启用。
// 注意 Host 白名单与启用条件是**同一个集合**：两者不一致就会出现"启用了但每个请求都被拒"。
func NewOriginPolicy(listenAddr string, allowedOrigins []string) *OriginPolicy {
	p := &OriginPolicy{extra: make(map[string]bool, len(allowedOrigins))}
	for _, o := range allowedOrigins {
		if c := canonicalOrigin(o); c != "" {
			p.extra[c] = true
		}
	}
	host, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		// 到不了这里：地址已经过 net.Listen（非法地址在绑定期就报错了）。返回未启用的策略
		// 而不是 panic：策略只负责判定，装配期的地址合法性由装机层负责。
		return p
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return p
	}
	p.port = port
	p.enabled = loopbackHostAllowed(host)
	return p
}

// Enabled 报告策略是否生效（非回环监听 → false，所有判定一律放行）。
func (p *OriginPolicy) Enabled() bool { return p != nil && p.enabled }

// Port 返回实际监听端口（未启用时是解析出的值，仅用于日志）。
func (p *OriginPolicy) Port() int {
	if p == nil {
		return 0
	}
	return p.port
}

// Decide 判定一次请求（Host 与 Origin 一起看）。Status 0 = 放行。
//
// 判定顺序刻意是 Host 在前：Host 不对时请求**根本没走到本进程该服务的主机**上，
// 后面那条 Origin 判定对它没有意义（也避免用 Origin 的文案误导排障方向）。
func (p *OriginPolicy) Decide(r *http.Request) Decision {
	if !p.Enabled() {
		return Decision{}
	}
	if !p.hostAllowed(r.Host) {
		return Decision{
			Status: http.StatusMisdirectedRequest,
			Msg: fmt.Sprintf("请求的 Host %s 不是本进程的监听主机（只接受 127.0.0.1:%d / [::1]:%d / localhost:%d）",
				truncateForEcho(r.Host), p.port, p.port, p.port),
		}
	}
	// 无 Origin：壳内反代（壳经 protocol.handle 转发的 REST 请求）、curl、页面导航与静态子资源。
	// 这类请求不携带浏览器自动附加的来源信息，它们的可信度由**凭据**兜底
	// （桌面态 restToken 只在壳的主进程里，从不进渲染进程）。
	origin := r.Header.Get("Origin")
	if origin == "" {
		return Decision{}
	}
	if p.originAllowed(origin) {
		return Decision{}
	}
	// `Origin: null`（沙箱 iframe / 某些重定向）走到这里，与任何未知来源同样处理：它是
	// **明确声明了"来源不可知"**，绝不能因为"不是合法 URL"就当成无 Origin 放行。
	return Decision{
		Status: http.StatusForbidden,
		Msg: fmt.Sprintf("请求来源 %s 不在允许的 Origin 矩阵内；跨源开发/自建反代请用 FORGE_ALLOWED_ORIGINS 显式登记",
			truncateForEcho(origin)),
	}
}

// hostAllowed 报告请求 Host（`host[:port]`）是否属于本进程。
//
// 无端口一律拒绝：核心的监听端口是"同源"的组成部分（桌面态还是随机端口），
// 省略端口意味着请求指向的是 80/443 上的某个东西，不是本进程。
func (p *OriginPolicy) hostAllowed(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n != p.port {
		return false
	}
	return loopbackHostAllowed(host)
}

// originAllowed 报告 Origin 头的值是否命中矩阵。
//
// 三类放行：
//  1. 桌面直连的 `forge-app://app`；
//  2. `http://127.0.0.1:<实际端口>` / `http://[::1]:<实际端口>` / `http://localhost:<实际端口>`
//     ——浏览器入口页面自身的同源（核心自己服务的那份 dist）；
//  3. 额外允许表（FORGE_ALLOWED_ORIGINS，配置层已归一：小写 + 无尾斜杠）。
//
// 其余一律拒，包括 `Origin: null`（沙箱 iframe / 某些重定向的"来源不可知"声明）：
// 它既不是空串，也进不了允许表——配置层要求登记项必须是 `scheme://host[:port]`，
// 所以 "null" 连被手写进白名单的资格都没有（不存在"配了 null 就放行"这条路）。
//
// https 不在同源规则里：核心在回环上只提供明文 HTTP，`https://127.0.0.1:<port>` 不是任何
// 真实页面的来源（真走到 TLS 形态时，来源该由**反代**的域名显式登记进允许表）。
func (p *OriginPolicy) originAllowed(origin string) bool {
	if origin == DesktopAppOrigin {
		return true
	}
	if u, err := url.Parse(origin); err == nil {
		if strings.EqualFold(u.Scheme, "http") && loopbackHostAllowed(u.Hostname()) && p.portMatches(u.Port()) {
			return true
		}
	}
	return p.extra[canonicalOrigin(origin)]
}

// portMatches 报告 URL 里的端口是否是本进程的实际监听端口（缺端口 = 不匹配）。
func (p *OriginPolicy) portMatches(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n == p.port
}

// CheckFunc 返回 WS 握手用的判定回调：返回非 nil error = 拒绝（错误已带业务码，status 是要回的
// HTTP 状态码）。策略未启用时返回 nil（控制器据此完全跳过判定）。
//
// 为什么不是 `websocket.AcceptOptions.CheckOrigin`：本项目用的 coder/websocket v1.8.15
// **没有**这个字段（AcceptOptions 只有 InsecureSkipVerify 与 OriginPatterns）。库自带的
// `authenticateOrigin` 也不能当这道闸门：它把"Origin 的 host == 请求的 Host"视为同一来源，
// 于是 `Host: evil.com` + `Origin: http://evil.com` 会被它放行——而我们的 Host 白名单必须先把
// 这种请求拦在 421。所以握手判定由**控制器在 Accept 之前**调用本回调完成（与 REST 中间件共用
// 同一个 Decide），库那边继续 InsecureSkipVerify（它的判定已被更严的这道覆盖）。
func (p *OriginPolicy) CheckFunc() func(*http.Request) (int, error) {
	if !p.Enabled() {
		return nil
	}
	return func(r *http.Request) (int, error) {
		d := p.Decide(r)
		if d.Allowed() {
			return 0, nil
		}
		return d.Status, d.Err()
	}
}

// Origin 挂载 Host 白名单 + Origin 矩阵（挂在 `/v1/forge` 组的 REST 与 /health 上，见 internal/router）。
//
// 范围说明：只覆盖 API 组里的这些路由。静态资产与 SPA 回落（`/`、`/assets/*`、深链）不在组里——
// 页面导航与子资源加载本来就不带 Origin（除 CORS 与非 GET 同源请求），真正的攻击面是 API。
// 事件流（`/ws/events`）由控制器在升级前调 CheckFunc（同一策略），本中间件不覆盖它。
// nil 或未启用的策略 → 放行 no-op（与 Credential 对空凭据表的处置一致：装配处不必判空）。
func Origin(p *OriginPolicy) gin.HandlerFunc {
	if !p.Enabled() {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		d := p.Decide(c.Request)
		if d.Allowed() {
			c.Next()
			return
		}
		// 421/403 用状态码本身表达语义（与 401 的处置一致）：客户端/壳不经解析信封即可识别
		// "我走错主机了/来源不被接受"，而信封仍由 golib 单点构造（Abort 中断后续 handler）。
		c.Abort()
		gincontext.FailWithStatus(c, d.Status, d.Err())
	}
}

// loopbackHostAllowed 报告主机名是否属于白名单（大小写不敏感；输入是**不带端口**的主机部分）。
func loopbackHostAllowed(host string) bool {
	for _, want := range loopbackHostNames {
		if strings.EqualFold(host, want) {
			return true
		}
	}
	return false
}

// canonicalOrigin 请求头侧的归一：trim + 小写 + 去尾部 `/`。
//
// 必须与 pkg/config 对 `allowed_origins` 的归一同一规则（配置值以该形态存进 extra）。
// Origin 是**大小写不敏感**的（等价于 scheme/host 的大小写规则）；尾斜杠在浏览器里不出现，
// 但手工/脚本发起的请求可能带，去掉它不会放宽判定（主机与端口仍须逐字命中）。
func canonicalOrigin(s string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/"))
}

// truncateForEcho 截断回显值（见 maxEchoLen），按 rune 截以避免切出半个 UTF-8 字符。
func truncateForEcho(s string) string {
	if len(s) <= maxEchoLen {
		return s
	}
	r := []rune(s)
	if len(r) <= maxEchoLen {
		return s
	}
	return string(r[:maxEchoLen]) + "…"
}
