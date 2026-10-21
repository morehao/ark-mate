package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Origin 白名单（M3-5，设计 §7.2 的 Origin 矩阵行 / §9.1）。
//
// 默认矩阵（见 internal/adapters/inbound/http/middleware/origin.go）**只**放行：无 Origin 的壳内反代/curl/同源导航、
// `mate-app://app`（桌面直连）与核心自身端口上的 http 同源。本键是**额外的显式登记口**：
// 跨源开发（`make dev` 的 vite :5173 → 代理到核心）与自建反代（页面 origin 与核心监听 origin 不同）
// 都落在默认矩阵之外，必须在这里指名登记——**不是默认放开**，也不是"配了就放行任意 Origin"。
//
// 合法的取值形状只有一个：纯 origin `scheme://host[:port]`，不带路径/查询/片段（可带一个尾斜杠，
// 比较前会去掉）。比较统一小写。非法取值启动即报错（不静默忽略）——静默忽略的后果是
// "我登记了却还是 403"，而用户会以为是登记口本身没生效。

// OriginSchemes 允许出现在 allowed_origins 里的 scheme（错误提示用）。
//
// 为什么是白名单而不是"任意 RFC 3986 scheme"：只有会**加载页面**的上下文才会带 Origin——
// 浏览器是 http/https，桌面壳自定义 scheme 是 mate-app；放行 ftp/file 之类只会把
// 一条永远命中不了的登记项写进配置，或者更糟：让人以为"登记了某个协议就给那个协议开了口子"。
var OriginSchemes = []string{"http", "https", "mate-app"}

// parseAllowedOrigins 解析 `scheme://host[:port][,...]`（MATE_ALLOWED_ORIGINS 的逗号分隔写法）。
func parseAllowedOrigins(v string) ([]string, error) {
	return normalizeAllowedOrigins(strings.Split(v, ","))
}

// normalizeAllowedOrigins 规范化额外允许来源（config.json 可写 JSON 数组，两种书写形式汇到这条路径）。
//
// 规则（每条都对应一种「看起来生效了其实没有」的形态，故一律报错不静默）：
//   - 跳过空项（`a,,b` 与末尾逗号是常见手滑，视同未写）；
//   - 必须带 `scheme://`：`localhost:5173` 会被 url.Parse 当成 scheme=localhost 的怪东西，
//     直接拒绝并给出正确写法；
//   - scheme 必须在 OriginSchemes 内；
//   - 不得带路径/查询/片段/userinfo（`http://x/y`、`http://x?a=1` 都不是 origin 的形状，
//     静默按 origin 处理等于把"我指的是这个页面"变成"我指的是这个站"）；
//   - 去重（同一来源写两遍只会让错误信息变长）。
//
// 归一：小写 + 去尾部 `/`（Origin 头本身是大小写不敏感的，且浏览器不发尾斜杠）。
func normalizeAllowedOrigins(items []string) ([]string, error) {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		// 去掉尾部斜杠后再解析：`http://localhost:5173/` 是同一件事的另一种写法，
		// 为它报错只会让人去猜"到底哪个形状才算纯"。
		trimmed := strings.TrimRight(item, "/")
		if !strings.Contains(trimmed, "://") {
			return nil, fmt.Errorf("需为纯 origin `scheme://host[:port]`（如 http://localhost:5173），得到 %q", item)
		}
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("需为纯 origin `scheme://host[:port]`，得到 %q", item)
		}
		scheme := strings.ToLower(u.Scheme)
		if !validOriginScheme(scheme) {
			return nil, fmt.Errorf("scheme %q 不支持（合法：%s），得到 %q",
				u.Scheme, strings.Join(OriginSchemes, "|"), item)
		}
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Errorf("只能是纯 origin `scheme://host[:port]`（不带路径/查询/片段），得到 %q", item)
		}
		if u.Port() == "" && strings.Contains(u.Host, ":") && !strings.Contains(u.Host, "]") {
			// url.Parse 对 `http://host:abc` 会报错，这里兜的是其它畸形端口写法；host:port 里
			// 有冒号却解析不出端口，说明它既不是 IPv6 也不是合法端口。
			return nil, fmt.Errorf("端口非法（需为 `scheme://host:port` 或省略端口），得到 %q", item)
		}
		canonical := canonicalOrigin(trimmed)
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	// 全空（如显式设为空格）= 不登记任何额外来源：与"未设置"同义，不报错（便于用 env 临时清空）。
	//
	// 排序是刻意的：白名单的语义是**集合**，而 `mate config` 的输出会被用来 diff / 排障 /
	// 进工单，输入顺序不该泄漏到输出里（同一个集合两次打印必须逐字相同）。
	sort.Strings(out)
	return out, nil
}

// canonicalOrigin 把 origin 文本归一成**比较键**：trim + 小写 + 去尾部 `/`。
//
// internal/adapters/inbound/http/middleware 的 OriginPolicy 对请求头做同一步归一再查表（配置值由本函数归一后交出去），
// 两侧的一致性由 pkg/config 与 internal/adapters/inbound/http/middleware 各自的用例共同钉住（前者断言配置值归一结果，
// 后者断言大小写/尾斜杠变体都能命中该值）。
func canonicalOrigin(s string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/"))
}

// validOriginScheme 报告 scheme 是否在白名单内。
func validOriginScheme(scheme string) bool {
	for _, s := range OriginSchemes {
		if scheme == s {
			return true
		}
	}
	return false
}
