package objid

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestNewIsStrictlyIncreasing 同毫秒内必须严格递增（进程内单调）。
//
// 手法：把进程内的毫秒锚推进到远期，于是每次调用都落进"同一毫秒 → 递增随机段"那条分支，
// 1000 次铸造全在同一毫秒内完成 —— 这正是不注入时钟做不到的确定性。
func TestNewIsStrictlyIncreasing(t *testing.T) {
	mu.Lock()
	seeded = true
	lastMS = uint64(time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	lastEntropy = [10]byte{}
	mu.Unlock()

	prev := ""
	for i := 0; i < 1000; i++ {
		got := New(Session)
		if prev != "" && got <= prev {
			t.Fatalf("第 %d 次铸造没有严格递增：prev=%q got=%q", i, prev, got)
		}
		prev = got
	}
}

// TestNewLexicographicOrderEqualsMintOrder 字典序必须等于铸造序。
//
// 这是 `messages` / `inputs` 靠 `ORDER BY id ASC` 兜底排序的**唯一前提**
// （SQLite TEXT 默认 BINARY 比较 = ASCII 序，而 Crockford 字母表的 ASCII 序 = 字母表序）。
func TestNewLexicographicOrderEqualsMintOrder(t *testing.T) {
	const n = 200
	minted := make([]string, n)
	for i := range minted {
		minted[i] = New(Message)
	}
	sorted := append([]string(nil), minted...)
	sort.Strings(sorted)
	for i := range minted {
		if minted[i] != sorted[i] {
			t.Fatalf("字典序 ≠ 铸造序：第 %d 位 minted=%q sorted=%q", i, minted[i], sorted[i])
		}
	}
}

// TestNewBodyShape 铸出来的 id 形状：登记前缀 + 26 位合法 Crockford。
func TestNewBodyShape(t *testing.T) {
	for _, prefix := range []string{Session, Message, Run, Input, Workspace, Model, PermissionMemory} {
		got := New(prefix)
		if !strings.HasPrefix(got, prefix) {
			t.Fatalf("前缀丢了: %q", got)
		}
		body := strings.TrimPrefix(got, prefix)
		if len(body) != ulidBodyLen {
			t.Fatalf("%q 的主体长度 %d ≠ %d", got, len(body), ulidBodyLen)
		}
		for i := 0; i < len(body); i++ {
			if strings.IndexByte(alphabet, body[i]) < 0 {
				t.Fatalf("%q 主体含字母表外字符 %q（Crockford 去掉了 I/L/O/U）", got, body[i])
			}
		}
		if !IsPlatformID(got) {
			t.Fatalf("自己铸的 id 没通过 IsPlatformID: %q", got)
		}
		if p, ok := Prefix(got); !ok || p != prefix {
			t.Fatalf("Prefix(%q) = (%q,%v)，期望 (%q,true)", got, p, ok, prefix)
		}
	}
}

// TestNewEntropyOverflowAdvancesMillisecond 随机段 80 位全部进位溢出时必须前进一毫秒，
// 而不是回绕成"更小的 id"（那会击穿字典序 = 铸造序）。
func TestNewEntropyOverflowAdvancesMillisecond(t *testing.T) {
	mu.Lock()
	seeded = true
	lastMS = uint64(time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	for i := range lastEntropy {
		lastEntropy[i] = 0xFF
	}
	msBefore := lastMS
	mu.Unlock()

	got := New(Run)

	mu.Lock()
	msAfter := lastMS
	mu.Unlock()
	if msAfter != msBefore+1 {
		t.Fatalf("熵溢出后毫秒没有前进：before=%d after=%d", msBefore, msAfter)
	}
	if !IsPlatformID(got) {
		t.Fatalf("溢出后铸出的 id 形状不对: %q", got)
	}
}

func TestWrapRuntime(t *testing.T) {
	ok, err := WrapRuntime("ses_01J9Z6Q0M7X2V8K4B3N5TRW1PD")
	if err != nil {
		t.Fatalf("正常原文不该失败: %v", err)
	}
	if ok != "rt_ses_01J9Z6Q0M7X2V8K4B3N5TRW1PD" {
		t.Fatalf("壳值不对: %q", ok)
	}
	// dsh 的裸 uuid 同样加壳（底座形状不影响这条判据）。
	if got, err := WrapRuntime("9f3c2b8e-4c1a-4d3b-9f2e-1a2b3c4d5e6f"); err != nil ||
		got != "rt_9f3c2b8e-4c1a-4d3b-9f2e-1a2b3c4d5e6f" {
		t.Fatalf("裸 uuid 加壳不对: (%q,%v)", got, err)
	}

	// 三条错误路径。
	cases := []struct {
		name string
		raw  string
	}{
		{"空原文", ""},
		{"已带壳（防双包）", "rt_ses_01J9Z6Q0M7X2V8K4B3N5TRW1PD"},
		{"含空白字符", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1P D"},
	}
	for _, tc := range cases {
		if _, err := WrapRuntime(tc.raw); !errors.Is(err, ErrRuntimeID) {
			t.Fatalf("%s：期望 ErrRuntimeID，得到 %v", tc.name, err)
		}
	}
}

func TestUnwrapRuntime(t *testing.T) {
	raw := "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PD"
	shelled, err := WrapRuntime(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapRuntime(shelled)
	if err != nil {
		t.Fatalf("正常壳值不该失败: %v", err)
	}
	if got != raw {
		t.Fatalf("解包结果不对: %q", got)
	}

	// 三条错误路径。
	cases := []struct {
		name string
		in   string
	}{
		{"无壳（拿到的是平台 id 或旧形状）", raw},
		{"壳内为空", "rt_"},
		{"壳内有壳（双包）", "rt_rt_ses_01J9Z6Q0M7X2V8K4B3N5TRW1PD"},
	}
	for _, tc := range cases {
		if _, err := UnwrapRuntime(tc.in); !errors.Is(err, ErrRuntimeID) {
			t.Fatalf("%s：期望 ErrRuntimeID，得到 %v", tc.name, err)
		}
	}
}

func TestPrefixAndIsPlatformID(t *testing.T) {
	positive := []string{
		New(Session), New(Message), New(Run), New(Input), New(Workspace), New(Model), New(PermissionMemory),
		New(Event),
	}
	for _, id := range positive {
		if !IsPlatformID(id) {
			t.Fatalf("正样本被判负: %q", id)
		}
	}

	negative := []struct {
		name string
		id   string
	}{
		{"裸 uuid（旧形状）", "9f3c2b8e-4c1a-4d3b-9f2e-1a2b3c4d5e6f"},
		{"底座壳不是平台 id", "rt_ses_01J9Z6Q0M7X2V8K4B3N5TRW1PD"},
		{"不认识的前缀", "xyz_01J9Z6Q0M7X2V8K4B3N5TRW1PD"},
		{"主体过短", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1P"},
		{"主体过长", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PDD"},
		{"主体含保留字母 I", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PI"},
		{"主体含保留字母 L", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PL"},
		{"主体含保留字母 O", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PO"},
		{"主体含保留字母 U", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PU"},
		{"主体含小写", "ses_01j9z6q0m7x2v8k4b3n5trw1pd"},
		{"空串", ""},
	}
	for _, tc := range negative {
		if IsPlatformID(tc.id) {
			t.Fatalf("%s 被判成正样本: %q", tc.name, tc.id)
		}
	}

	if _, ok := Prefix("9f3c2b8e-4c1a-4d3b-9f2e-1a2b3c4d5e6f"); ok {
		t.Fatal("裸 uuid 不该解出登记前缀")
	}
}
