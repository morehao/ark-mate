package provider

import (
	"github.com/morehao/ark-mate/internal/core/protocol"
	"sync"
	"time"
)

// ——— 运行时模型目录的**缓存与拒绝值反馈**（P4a 正文第六十刀：从 `internal/manager` 搬入）———
//
// 这张表是「底座此刻广告了哪些模型配置项」的进程内缓存，与供应商档案同属模型域的事实：
// 写点有三处（目录取样 / 运行时推送 / 会话模型应用），读点更多。放在域里让它们共用**一份**
// 缓存——两份的表现是"面板刚刷新过、另一个入口还是旧目录"，而两边都编译得过。

// rejectedValueTTL 被运行时拒绝的值（如凭证缺失）在目录里标注为不可用的时长。
const rejectedValueTTL = 10 * time.Minute

// ConfigCatalogEntry 一次目录取样的结果（内存缓存，不落盘：任何一次真实会话都会刷新它）。
type ConfigCatalogEntry struct {
	Runtime   string
	Options   []protocol.ConfigOption
	FetchedAt time.Time
}

// rejectedValue 运行时拒绝过的一个取值（用于目录里的「不可用」标注）。
type rejectedValue struct {
	reason string
	at     time.Time
}

// configCatalog 目录缓存与拒绝值反馈（独立锁，避免与档案表的锁交叉）。
//
// 键是 **runtime id**（v19 之前是 agent id）：单底座下它只有一个取值，但保留这一维让
// "这条目录是谁说的"在缓存里显式可见——换成裸单例会让将来多底座时又多一处要改的隐式前提。
type configCatalog struct {
	mu       sync.Mutex
	entries  map[string]*ConfigCatalogEntry
	rejected map[string]map[string]rejectedValue
}

func newConfigCatalog() *configCatalog {
	return &configCatalog{
		entries:  map[string]*ConfigCatalogEntry{},
		rejected: map[string]map[string]rejectedValue{},
	}
}

// put 记录一次目录取样（options 为空也记录：表示「运行时确实没给」）。
func (c *configCatalog) put(runtimeID string, options []protocol.ConfigOption) {
	c.mu.Lock()
	c.entries[runtimeID] = &ConfigCatalogEntry{Runtime: runtimeID, Options: cloneOptions(options), FetchedAt: time.Now().UTC()}
	c.mu.Unlock()
}

// get 取未过期缓存。
func (c *configCatalog) get(runtimeID string, ttl time.Duration) (*ConfigCatalogEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[runtimeID]
	if !ok || len(e.Options) == 0 {
		return nil, false
	}
	if ttl > 0 && time.Since(e.FetchedAt) > ttl {
		return nil, false
	}
	return &ConfigCatalogEntry{Runtime: e.Runtime, Options: cloneOptions(e.Options), FetchedAt: e.FetchedAt}, true
}

// markRejected 记录一次「运行时拒绝了该值」（按 runtime 维度）。
// 只在运行时明确拒绝（invalidParams 类）时调用；网络/超时等暂态失败不在此列。
func (c *configCatalog) markRejected(runtimeID, value, reason string) {
	if runtimeID == "" || value == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.rejected[runtimeID]
	if m == nil {
		m = map[string]rejectedValue{}
		c.rejected[runtimeID] = m
	}
	m[value] = rejectedValue{reason: reason, at: time.Now()}
}

// rejection 取某值最近的拒绝记录（过期 → 无）。
func (c *configCatalog) rejection(runtimeID, value string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.rejected[runtimeID]
	if m == nil {
		return "", false
	}
	r, ok := m[value]
	if !ok {
		return "", false
	}
	if time.Since(r.at) > rejectedValueTTL {
		delete(m, value)
		return "", false
	}
	return r.reason, true
}

// clearRejection 运行时接受了该值时清除拒绝标记（自愈）。
func (c *configCatalog) clearRejection(runtimeID, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m := c.rejected[runtimeID]; m != nil {
		delete(m, value)
	}
}

// cloneOptions 深拷贝选项（含每个选项的值切片），避免缓存与调用方共享底层数组。
func cloneOptions(in []protocol.ConfigOption) []protocol.ConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]protocol.ConfigOption, len(in))
	for i, o := range in {
		out[i] = o
		if len(o.Values) > 0 {
			out[i].Values = append([]protocol.ConfigOptionValue(nil), o.Values...)
		}
	}
	return out
}

// ——— Plane 上的事实出口（编排侧只认这四个方法）———

// PutConfigOptions 记录一次目录取样（运行时推送 / 会话模型应用 / 目录取样共用）。
func (pl *Plane) PutConfigOptions(runtimeID string, options []protocol.ConfigOption) {
	pl.catalog.put(runtimeID, options)
}

// ConfigOptions 取未过期缓存（ttl<=0 = 不判过期）。
func (pl *Plane) ConfigOptions(runtimeID string, ttl time.Duration) (*ConfigCatalogEntry, bool) {
	return pl.catalog.get(runtimeID, ttl)
}

// MarkModelRejected 记录「运行时拒绝了这个取值」（目录里标灰）。
func (pl *Plane) MarkModelRejected(runtimeID, value, reason string) {
	pl.catalog.markRejected(runtimeID, value, reason)
}

// ModelRejection 取某取值最近的拒绝记录（过期 → 无）。
func (pl *Plane) ModelRejection(runtimeID, value string) (string, bool) {
	return pl.catalog.rejection(runtimeID, value)
}

// ClearModelRejection 运行时接受了该值 → 清掉标灰（自愈）。
func (pl *Plane) ClearModelRejection(runtimeID, value string) {
	pl.catalog.clearRejection(runtimeID, value)
}

// ResetConfigCatalog 清空目录缓存与拒绝记录（测试隔离用；重载定义时不调用——那由取样刷新）。
func (pl *Plane) ResetConfigCatalog() {
	pl.catalog = newConfigCatalog()
}
