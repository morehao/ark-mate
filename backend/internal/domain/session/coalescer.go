package session

import (
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// ③ 流式增量合并（消写放大）：把连续的同 msg_id 文本增量合并成一条事件后再落库/推流。
//
// 背景：每个 ACP message/thought delta 原本各产生一条事件 → 长回复 = 几百行 events +
// 几百行 transcript + 几百个 WS 帧。阈值对齐 compozy 的 prompt chunk coalescer
// （internal/session/prompt_chunk_coalescer.go:11-14：25ms / 4KB / 单批 64 条）。
//
// 只合并 `session.message` / `session.thought` 两类**文本增量**；其余类型（工具卡、权限卡、
// 提问卡、用户消息、终态）一律**先 flush 本会话缓冲再发布**，因此：
//
//   - 「文本先于终态」的 seq 顺序不被越过（run.ended/run.error/run.cancelled 前必 flush）；
//   - 工具卡/权限卡不会被文本增量越过（它们之间不合并，且发布前 flush）；
//   - 同键内 msg_id 变化时先 flush 旧缓冲，**跨 msg_id 的原始顺序不被重排**。
//
// payload 形状不变（仍带 session_id/agent_id/msg_id，text 为拼接结果）→ 前端按 msg_id 追加
// 同一条消息的渲染逻辑无需改动，历史回放读的是同一份（更粗）的事件。
const (
	// ChunkFlushInterval 定时窗口：一个 (会话, 类型) 缓冲最多攒这么久。
	ChunkFlushInterval = 25 * time.Millisecond
	// ChunkFlushBytes 字节阈值：达到即同步 flush（大块不必等窗口）。
	ChunkFlushBytes = 4 * 1024
	// ChunkFlushCount 条数阈值：达到即同步 flush（与 compozy 单批 64 条一致）。
	ChunkFlushCount = 64
)

// chunkKeyT 缓冲键：按 (会话, 事件类型) 分桶——不同会话/不同事件的增量绝不相邻合并。
type chunkKeyT struct {
	sessionID string
	typ       string
}

// chunkBufT 是一段待发布的文本增量（**除 text 外完全同形**的连续 delta 拼接）。
type chunkBufT struct {
	// base 首块载荷的浅拷贝（保留 session_id/agent_id/msg_id 等形状；text 在 flush 时覆盖）。
	base  map[string]any
	text  strings.Builder
	count int
	first time.Time
	// ord 创建序（自增）：flush 多个缓冲时按它排序，保证落库/推流顺序**确定**且与首块到达序一致
	// （Go map 遍历序随机，若不排序则同会话的 message/thought 谁先落盘变成随机）。
	ord int64
}

// payload 生成 flush 时的载荷（形状与单条增量一致，text 为拼接结果）。
func (b *chunkBufT) payload() map[string]any {
	out := make(map[string]any, len(b.base)+1)
	for k, v := range b.base {
		out[k] = v
	}
	out["text"] = b.text.String()
	return out
}

// Coalescer 文本增量合并器。零值不可用，须经 NewCoalescer 构造。
type Coalescer struct {
	mu      sync.Mutex
	bufs    map[chunkKeyT]*chunkBufT
	nextOrd int64
	flushFn func(typ, sessionID string, payload any)
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// NewCoalescer 构造合并器并启动定时窗口 goroutine（flushFn 必填，通常为 Service.publishRaw）。
func NewCoalescer(flushFn func(typ, sessionID string, payload any)) *Coalescer {
	c := &Coalescer{
		bufs:    make(map[chunkKeyT]*chunkBufT),
		flushFn: flushFn,
		done:    make(chan struct{}),
	}
	c.wg.Add(1)
	go c.loop()
	return c
}

// Offers 报告某事件类型是否走合并（只有文本增量两类）。
func (c *Coalescer) Offers(typ string) bool {
	return typ == BusMessage || typ == BusThought
}

// add 收下一段增量：同键同 msg_id 直接拼接；msg_id 变化先 flush 旧缓冲；达阈值同步 flush。
func (c *Coalescer) Add(typ, sessionID string, payload map[string]any) {
	if c == nil {
		return
	}
	text, _ := payload["text"].(string)
	key := chunkKeyT{sessionID: sessionID, typ: typ}

	c.mu.Lock()
	cur := c.bufs[key]
	if cur != nil && !sameShape(cur.base, payload) {
		// 除 text 外形状不同的增量不能合并（msg_id 变了会把 B 的尾巴拼到 A 后面；
		// 其它字段变了会丢信息）：先落旧缓冲，再开新缓冲——跨消息的原始顺序不被重排。
		prev := c.takeLocked(func(k chunkKeyT) bool { return k == key })
		c.mu.Unlock()
		for _, item := range prev {
			c.emit(item.key.typ, item.key.sessionID, item.buf)
		}
		c.mu.Lock()
		cur = nil
	}
	if cur == nil {
		base := make(map[string]any, len(payload))
		for k, v := range payload {
			base[k] = v
		}
		cur = &chunkBufT{base: base, first: time.Now(), ord: c.nextOrd}
		c.nextOrd++
		c.bufs[key] = cur
	}
	cur.text.WriteString(text)
	cur.count++

	var hit []taken
	if cur.text.Len() >= ChunkFlushBytes || cur.count >= ChunkFlushCount {
		hit = c.takeLocked(func(k chunkKeyT) bool { return k == key })
	}
	c.mu.Unlock()
	for _, item := range hit {
		c.emit(item.key.typ, item.key.sessionID, item.buf)
	}
}

// flush 落掉某会话的全部缓冲增量（非合并类型发布前调用，保证 seq 顺序）。
func (c *Coalescer) Flush(sessionID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	pending := c.takeLocked(func(k chunkKeyT) bool { return k.sessionID == sessionID })
	c.mu.Unlock()
	for _, item := range pending {
		c.emit(item.key.typ, item.key.sessionID, item.buf)
	}
}

// FlushAll 落掉全部缓冲增量（停机/关闭 transcript 前调用，避免丢尾部文本）。
func (c *Coalescer) FlushAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	pending := c.takeLocked(func(chunkKeyT) bool { return true })
	c.mu.Unlock()
	for _, item := range pending {
		c.emit(item.key.typ, item.key.sessionID, item.buf)
	}
}

// Close 停止定时窗口并落掉全部缓冲（幂等）。
func (c *Coalescer) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() { close(c.done) })
	c.wg.Wait()
	c.FlushAll()
}

// taken 是一次取出的待发布缓冲（连同其键，供解锁后发布）。
type taken struct {
	key chunkKeyT
	buf *chunkBufT
}

// takeLocked 取出满足条件的缓冲并清表（调用方持锁；发布必须在解锁后进行）。
func (c *Coalescer) takeLocked(match func(chunkKeyT) bool) []taken {
	var out []taken
	for k, b := range c.bufs {
		if match(k) {
			out = append(out, taken{key: k, buf: b})
			delete(c.bufs, k)
		}
	}
	// 按创建序发布（map 遍历序随机 → 不排序会让同会话不同流的落盘顺序不确定）。
	sort.Slice(out, func(i, j int) bool { return out[i].buf.ord < out[j].buf.ord })
	return out
}

// emit 发布一段合并结果。
//
// 注意：**不跳过空文本**——`publish` 的契约是「发布这条事件」，静默丢弃比多写一行更糟
// （除 text 外还带字段的载荷会被整条吃掉）。空 delta 合并后仍只多一行，不是问题。
func (c *Coalescer) emit(typ, sessionID string, b *chunkBufT) {
	if b == nil {
		return
	}
	c.flushFn(typ, sessionID, b.payload())
}

// sameShape 判断两块载荷是否除 text 外完全一致（同一条消息的连续增量）。
// 值比较用 reflect.DeepEqual：载荷是 JSON 形状的小 map，代价可忽略。
func sameShape(base, next map[string]any) bool {
	if len(base) != len(next) {
		return false
	}
	for k, v := range base {
		if k == "text" {
			continue
		}
		nv, ok := next[k]
		if !ok || !reflect.DeepEqual(v, nv) {
			return false
		}
	}
	return true
}

// loop 定时窗口：到期的缓冲按 (会话, 类型) 落库。
func (c *Coalescer) loop() {
	defer c.wg.Done()
	t := time.NewTicker(ChunkFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case now := <-t.C:
			c.flushExpired(now)
		}
	}
}

// flushExpired 落掉已攒够一个窗口的缓冲。
func (c *Coalescer) flushExpired(now time.Time) {
	c.mu.Lock()
	pending := c.takeLocked(func(k chunkKeyT) bool {
		b := c.bufs[k]
		return b != nil && now.Sub(b.first) >= ChunkFlushInterval
	})
	c.mu.Unlock()
	for _, item := range pending {
		c.emit(item.key.typ, item.key.sessionID, item.buf)
	}
}
