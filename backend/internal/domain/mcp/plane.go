package mcp

import (
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// defaultProbeTimeout 单次探测预算（覆盖 spawn + initialize + tools/list 翻页）。
const defaultProbeTimeout = 15 * time.Second

// ProbeStatus 一个 server 的最近一次探测状态（At 零值 = 从未探测）。
type ProbeStatus struct {
	At  time.Time
	Err string // 空 = 成功
}

// Plane 连接器平面的**有状态主体**：持有能力注册表（MCP 定义都在这里）与探测状态。
//
// 为什么注册表是注入的而不是自己造：**它是 skill 与 mcp 两个平面共用的那一份**（注册表里
// 同时装着技能与连接器定义），谁都不该 new 一个自己的——两份注册表 = 两个事实源。
// 注入在装配期完成、之后只读，所以不加锁（与 `skill.Plane` 同形）。
//
// 探测状态（`probes` / `budget`）自持一把锁：它只保护这两个字段，与注册表、与凭据面都无关
// ——所以这里**不取** `Service.mu`，也不存在"读会话 + 写探测状态必须原子"的要求。
type Plane struct {
	caps    *capability.Registry
	defs    Definitions
	secrets *Secrets

	mu     sync.Mutex
	probes map[string]ProbeStatus
	budget time.Duration
}

// NewPlane 造一个连接器平面。三个依赖都必须由装配期注入**同一个实例**：
// `caps` 是 skill 与 mcp 共用的注册表（两份 = 两个事实源）、`defs` 是定义机制
// （写面经它读写载体）、`secrets` 是密钥解析面（两份 = 同一份引用解析出两个结果）。
func NewPlane(caps *capability.Registry, defs Definitions, secrets *Secrets) *Plane {
	return &Plane{caps: caps, defs: defs, secrets: secrets, probes: make(map[string]ProbeStatus)}
}

// SetProbeTimeout 覆盖单次探测预算（<=0 → 默认 15s；测试注入短预算用）。
func (p *Plane) SetProbeTimeout(d time.Duration) {
	if d <= 0 {
		d = defaultProbeTimeout
	}
	p.mu.Lock()
	p.budget = d
	p.mu.Unlock()
}

// ProbeBudget 当前的单次探测预算（未设或非正 → 默认值）。
func (p *Plane) ProbeBudget() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.budget <= 0 {
		return defaultProbeTimeout
	}
	return p.budget
}

// recordProbe 记一次探测结果（成功传 nil）。
func (p *Plane) recordProbe(id string, err error) {
	st := ProbeStatus{At: time.Now()}
	if err != nil {
		st.Err = err.Error()
	}
	p.mu.Lock()
	p.probes[id] = st
	p.mu.Unlock()
}

// ProbeSnapshot 取探测状态的副本（读面用；不回传内部 map）。
func (p *Plane) ProbeSnapshot() map[string]ProbeStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]ProbeStatus, len(p.probes))
	for k, v := range p.probes {
		out[k] = v
	}
	return out
}

// ToolCounts 统计各 server 当前归属的 Tool 条目数。
func (p *Plane) ToolCounts() map[string]int {
	out := map[string]int{}
	for _, t := range p.caps.ListTools() {
		if t.Source == capability.ToolSourceMCP && t.McpServer != "" {
			out[t.McpServer]++
		}
	}
	return out
}
