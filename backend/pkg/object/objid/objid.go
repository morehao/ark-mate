// Package objid 承载「平台铸造 id」与「底座 id 包装」的唯一实现。
//
// 为什么单独成包（对齐 pkg/object/objusage 的口径）：
//   - id 形状是**对外契约**（`GET /sessions` 的 `id`、诊断包里的 id、脚本里书签的 id），
//     与 objusage 同属"跨层共享值对象"；
//   - 铸造点跨三层（manager 建会话/轮次/输入、store 建工作空间、dao 读回写入），
//     放在其中任何一层都会让另外两层反向依赖；
//   - `pkg/` 内部不得 import `internal/`（internal/archtest 守卫），而将来 `pkg/runtime/*`
//     若需要铸号也能直接用。
//
// 两条互不重叠的家族（这是本包最容易读错的一处）：
//
//	① **铸造前缀**（`ses_`/`msg_`/…）：平台自己发的号，主体是 26 位 Crockford Base32 ULID。
//	   家族规则是「3 位小写字母 + 下划线」，登记在下面的常量表里。
//	② **透传壳**（`rt_` = runtime）：底座发过来的原生会话 id，平台**只加壳、不解析、不重写**
//	   （§4.5.2 的不变式 I2）。它属于另一个命名空间，所以刻意**不**凑成 3 字母。
//
// 刻意**没有** `FromLegacy` 这类"旧 id 派生"：Q10 已按"旧数据不要"定稿，旧 transcript
// 在装载期被跳过 + WARN（见 internal/manager/transcript_load.go），不做任何映射。
package objid

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 铸造前缀登记表：**只有平台自己铸号的实体**才登记在这里。
//
// 刻意不登记的（登记即意味着"平台铸造"，是判据不是遗漏）：
//   - 定义类（skill / mcp_server / agent / automation / provider）：id 是**用户自定义文件名**
//     （`validDefinitionID` 要求 `filepath.Base(id)==id`），且 `providers.id` 参与 vault 的
//     AAD（`vault:providers/<id>`）——改它会让她已加密的密钥全部解不开；
//   - `events.seq` / `definition_revisions.seq`：它们是**单调游标**（后者直接当能力代际 rev），
//     换 TEXT 会毁掉 `MAX(seq)` 与索引序，收益为零。**v17 起 `events` 另有登记的 `id`**——
//     登记的是**行身份**，两列各司其职（身份 `evt_…` / 位次 `seq`），不是把 seq 换成 id。
const (
	// Session 会话（`sessions.id`）。
	Session = "ses_"
	// Message 消息物化行（`messages.id`）。
	Message = "msg_"
	// Run 轮次（`runs.id`）。
	Run = "run_"
	// Input 排队输入（`inputs.id`）。
	Input = "inp_"
	// Workspace 工作空间登记（`workspaces.id`）。
	Workspace = "wsp_"
	// Model 平台模型条目（`models.id`）。
	Model = "mdl_"
	// PermissionMemory 会话级权限记忆（`permission_memories.id`）。
	PermissionMemory = "pem_"
	// Event 事件事实行（`events.id`，v17）。
	//
	// 它回答的是"**是哪一条事件**"（诊断包引用某一帧、`mate migrate` 按 id 判重与分叉检测、
	// memory 模式重启后同一条事件的身份不变）；位次仍由 `events.seq` 承担，`id` 永不当游标
	// （守卫 G1 钉住这条）。
	Event = "evt_"
)

// RuntimeShell 透传壳前缀（= runtime）。
//
// 它解决的是"平台 id 与底座 id 可能同形"：有的底座原生就发 `ses_…`，与平台铸造的 `ses_…`
// 长得一模一样；有的发裸 uuid。加壳之后**两个形状都由平台单方面定义**，底座换实现、
// 换前缀、发裸 uuid 都不影响这条判据（设计 §4.5，选项 D）。
//
// v17 起名字里的 `provider` 已收口成 `runtime`（会话列 `runtime_session_id`）：`provider`
// 在本仓是**模型供应商**的词，而这里装的是**底座会话**。
const RuntimeShell = "rt_"

// Crockford Base32 字母表：刻意去掉 I/L/O/U（避免与 1/0 混淆），且 **ASCII 序 = 字母表序**，
// 因此 `ORDER BY id ASC`（SQLite TEXT 默认 BINARY 比较）就是铸造序 —— 这是 `messages`
// 与 `inputs` 靠 `id ASC` 兜底排序的前提（设计 §5.1 P3/P4）。
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ulidBodyLen ULID 主体长度（10 位时间戳 + 16 位随机 = 26 字符）。
const ulidBodyLen = 26

var (
	mu sync.Mutex
	// lastMS 上次铸造用的毫秒时间戳（进程内单调的锚）。
	lastMS uint64
	// lastEntropy 上次铸造用的 80 位随机段；同毫秒内递增它保证严格递增。
	lastEntropy [10]byte
	// seeded 是否已经初始化过（零值 lastEntropy 与"未初始化"不可区分，故单独记）。
	seeded bool
)

// New 铸造一个平台 id：`<prefix><26 位 ULID>`。
//
// 进程内**单调**：同一毫秒内递增随机段，保证同进程铸造的 id 严格递增
// （跨进程同毫秒可能乱序，本仓不依赖跨进程顺序）。
//
// prefix 必须是上面的登记常量之一；传别的值也能铸（本函数不做白名单校验，
// 白名单由 archtest 守卫按"登记表 ↔ 调用点"判），但请不要这么做。
func New(prefix string) string {
	mu.Lock()
	defer mu.Unlock()

	ms := uint64(time.Now().UnixMilli())
	switch {
	case !seeded || ms > lastMS:
		lastMS = ms
		randomize()
		seeded = true
	case ms < lastMS:
		// 时钟回拨：沿用上次的毫秒并递增随机段——宁可"时间戳保守"，
		// 也不让 id 倒序（倒序会击穿 ORDER BY id ASC 的等价性）。
		ms = lastMS
		increment()
	default:
		// 同一毫秒：递增随机段。
		ms = lastMS
		increment()
	}

	var b [ulidBodyLen]byte
	// 时间戳：48 位 → 10 字符（首位只用到 3 个有效位）。
	b[0] = alphabet[(ms>>45)&0x1F]
	b[1] = alphabet[(ms>>40)&0x1F]
	b[2] = alphabet[(ms>>35)&0x1F]
	b[3] = alphabet[(ms>>30)&0x1F]
	b[4] = alphabet[(ms>>25)&0x1F]
	b[5] = alphabet[(ms>>20)&0x1F]
	b[6] = alphabet[(ms>>15)&0x1F]
	b[7] = alphabet[(ms>>10)&0x1F]
	b[8] = alphabet[(ms>>5)&0x1F]
	b[9] = alphabet[ms&0x1F]
	// 随机段：80 位 → 16 字符（两个 40 位半段各 8 字符）。
	encode40(b[10:18], lastEntropy[0:5])
	encode40(b[18:26], lastEntropy[5:10])

	return prefix + string(b[:])
}

// encode40 把 5 字节（40 位）编成 8 个 Crockford 字符。
func encode40(dst []byte, src []byte) {
	v := uint64(src[0])<<32 | uint64(src[1])<<24 | uint64(src[2])<<16 | uint64(src[3])<<8 | uint64(src[4])
	for i := 0; i < 8; i++ {
		dst[i] = alphabet[(v>>uint(35-5*i))&0x1F]
	}
}

// randomize 重新取一段随机熵。
func randomize() {
	if _, err := rand.Read(lastEntropy[:]); err != nil {
		// crypto/rand 在支持的系统上不会失败；真失败了也不能静默铸出重复 id。
		panic(fmt.Sprintf("objid: 取随机熵失败: %v", err))
	}
}

// increment 递增 80 位随机段（大端进位）。全部进位溢出时前进一毫秒并重新取随机。
func increment() {
	for i := len(lastEntropy) - 1; i >= 0; i-- {
		lastEntropy[i]++
		if lastEntropy[i] != 0 {
			return
		}
	}
	// 80 位全部归零：概率可忽略，但真发生时必须保证单调。
	lastMS++
	randomize()
}

// Prefix 从 id 解出登记前缀；第二个返回值区分"不认识的前缀"。
//
// 只做**前缀**匹配，不校验主体形状——形状判据是 IsPlatformID。
func Prefix(id string) (string, bool) {
	for _, p := range []string{Session, Message, Run, Input, Workspace, Model, PermissionMemory, Event} {
		if strings.HasPrefix(id, p) {
			return p, true
		}
	}
	return "", false
}

// IsPlatformID 判断 s 是否是**平台铸造**的合法 id（`<登记前缀><26 位 Crockford>`）。
//
// 它是形状判据的唯一实现，供 transcript 装载面（旧形状直接拒收）与守卫复用。
// 刻意**不**把 `rt_…` 算作平台 id：那是底座原文的壳，语义相反。
func IsPlatformID(s string) bool {
	p, ok := Prefix(s)
	if !ok {
		return false
	}
	return validBody(strings.TrimPrefix(s, p))
}

// validBody 判断 s 是否是 26 位合法 Crockford 主体。
func validBody(s string) bool {
	if len(s) != ulidBodyLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// ErrRuntimeID 是包装/解包失败时的哨兵错误（errors.Is 可用）。
var ErrRuntimeID = errors.New("objid: 非法底座会话 id")

// WrapRuntime 给底座原文加壳：`raw` → `rt_<raw>`。
//
// **严格**，因为"忘了包装"的后果不响：DB 里会落一列与平台 id 同形的裸值，两代形状混在
// 一起而没有任何运行时信号。三条拒绝路径：
//   - raw 为空 → 上游没拿到会话 id，包装一个 `rt_` 只会把错误推迟到 load/resume；
//   - raw 已带 `rt_` → 双包（`rt_rt_…`），解包时解不干净；
//   - raw 含空白字符 → 协议标识符不可能是这样，多半是上游把别的字段传错了。
func WrapRuntime(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%w: 原文为空", ErrRuntimeID)
	}
	if strings.HasPrefix(raw, RuntimeShell) {
		return "", fmt.Errorf("%w: 原文已带 %q 壳（防双包）: %q", ErrRuntimeID, RuntimeShell, raw)
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", fmt.Errorf("%w: 原文含空白字符: %q", ErrRuntimeID, raw)
	}
	return RuntimeShell + raw, nil
}

// UnwrapRuntime 解出底座原文：`rt_<raw>` → `raw`。
//
// **严格**，因为"忘了解包"的后果不响：底座收到 `rt_ses_…` 找不到会话 → 打一条 WARN 后
// 走 handoff memo 兜底（ADR-4 明确允许的降级），用户侧只看到"上下文丢了"。让错的那次在
// 边界上**当场失败**，是这条函数存在的全部理由。三条拒绝路径：
//   - 无 `rt_` 前缀 → 说明调用方拿的是平台 id 或旧形状值；
//   - 解出后为空；
//   - 解出后仍带 `rt_` → 双包。
func UnwrapRuntime(s string) (string, error) {
	if !strings.HasPrefix(s, RuntimeShell) {
		return "", fmt.Errorf("%w: 缺 %q 壳: %q", ErrRuntimeID, RuntimeShell, s)
	}
	raw := strings.TrimPrefix(s, RuntimeShell)
	if raw == "" {
		return "", fmt.Errorf("%w: 壳内为空", ErrRuntimeID)
	}
	if strings.HasPrefix(raw, RuntimeShell) {
		return "", fmt.Errorf("%w: 壳内有壳（双包）: %q", ErrRuntimeID, s)
	}
	return raw, nil
}
