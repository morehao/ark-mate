// Package credential 承载控制面的「凭据 → scope」只读表（M1-1：桌面态双凭据）。
//
// 为什么是 internal 而不是 pkg：scope 语义（rest 与 stream 两个面**互斥**、stream 面只读）
// 是本控制面的应用策略，不是可复用基础设施——pkg/ 的准入标准是"抽出去别的应用也能用"
// （见仓库根 AGENTS.md 的分界）。
//
// 威胁模型（设计 §7.2 / §9.1）：单机单操作员、只绑回环。凭据体系只解决两件事：
//  1. 把「改数据」（REST）与「只订阅事件」（WS）分成两个面，事件流那条通道即使令牌被页面持有，
//     也拿不到任何写能力；
//  2. 让外部 origin 的恶意页面即使打到回环，也拿不到任何一个面。
//
// 令牌传递（同设计 §7.2）：只经 **stdin** 交给核心（桌面态）、或由部署者设 FORGE_TOKEN（独立态）；
// 不进 argv / env / 文件 / URL。
package credential

import (
	"crypto/subtle"
	"fmt"
	"strings"
	"time"
)

// StreamAttachDeadline 事件流**首帧 attach** 的截止时限（M1-1）。
//
// 与前端契约 `@ark/desktop-contract` 的 `STREAM_ATTACH_DEADLINE_MS = 5000` 同值：
// 壳按这个数做启动预算，服务端按这个数关门——两边写死同一个数字，改动必须同时改两处。
const StreamAttachDeadline = 5 * time.Second

// Scope 一条凭据被允许触达的面（位掩码：独立态的单一令牌同时具备两个面）。
type Scope uint8

const (
	// ScopeREST REST 面：容器/独立部署的直连，以及桌面态壳的反代。改数据的能力都在这里。
	ScopeREST Scope = 1 << iota
	// ScopeStream 事件流面（只读）：`/v1/forge/ws/events` 的首帧 attach 凭据。
	ScopeStream
)

// String 人类可读的 scope 名（日志与错误信息用）。
func (s Scope) String() string {
	switch s {
	case ScopeREST:
		return "rest"
	case ScopeStream:
		return "stream"
	case 0:
		return "none"
	}
	parts := make([]string, 0, 2)
	if s&ScopeREST != 0 {
		parts = append(parts, "rest")
	}
	if s&ScopeStream != 0 {
		parts = append(parts, "stream")
	}
	return strings.Join(parts, "|")
}

// Pair 一条凭据及其 scope。
type Pair struct {
	Token  string
	Scopes Scope
}

// Set 只读凭据表：进程生命周期内不变，空表 = 不鉴权（历史默认行为，仅绑回环时使用）。
type Set struct {
	entries []entry
}

type entry struct {
	token  []byte
	scopes Scope
}

// New 构造凭据表。
//
// 规则（每条都对应一个真实误配）：
//   - 空 token = 该面未配置，**跳过**（不算错误：桌面态两个都必填，独立态只有一个）；
//   - scopes 为 0 的条目非法——拿不到任何面的凭据没有意义，只说明调用方写错了；
//   - 同一个 token 不允许同时以**不同** scope 出现：那正是 V15 要防的
//     "拿 streamToken 也能改数据"，一旦发生必须启动即失败，而不是静默放宽。
func New(pairs ...Pair) (*Set, error) {
	set := &Set{}
	for _, p := range pairs {
		if p.Token == "" {
			continue
		}
		if p.Scopes == 0 {
			return nil, fmt.Errorf("credential: 凭据缺少 scope（token 长度 %d）", len(p.Token))
		}
		for _, e := range set.entries {
			if subtle.ConstantTimeCompare([]byte(p.Token), e.token) == 1 && e.scopes != p.Scopes {
				return nil, fmt.Errorf("credential: 同一个令牌被赋予了两个不同的面（%s 与 %s）——两个面必须互斥", e.scopes, p.Scopes)
			}
		}
		set.entries = append(set.entries, entry{token: []byte(p.Token), scopes: p.Scopes})
	}
	return set, nil
}

// MustNew 是 New 的装配便利形式（仅当调用方已保证取值合法时使用，如由本进程生成的令牌）。
func MustNew(pairs ...Pair) *Set {
	set, err := New(pairs...)
	if err != nil {
		panic(err)
	}
	return set
}

// Required 报告是否配置了任何凭据（false = 不鉴权）。nil 接收者安全。
func (s *Set) Required() bool { return s != nil && len(s.entries) > 0 }

// Authorize 报告 token 是否具备 want 面。
//
// **空表一律拒绝**（不鉴权的判定属于调用方：先看 Required()）——让"忘记配置"变成拒绝而不是放行。
// 比较是**恒定时间**的：遍历全部条目、不提前返回，与旧 token 中间件同口径。
// （长度不同时 ConstantTimeCompare 会立即返回 0，因此"长度是否匹配"仍可被观测——
// 令牌长度由生成方固定，可接受。）
func (s *Set) Authorize(token string, want Scope) bool {
	if s == nil || want == 0 {
		return false
	}
	got := []byte(token)
	ok := 0
	for _, e := range s.entries {
		if subtle.ConstantTimeCompare(got, e.token) == 1 && e.scopes&want != 0 {
			ok = 1
		}
	}
	return ok == 1
}

// Count 凭据条目数（启动日志用，不含任何令牌值）。
func (s *Set) Count() int {
	if s == nil {
		return 0
	}
	return len(s.entries)
}
