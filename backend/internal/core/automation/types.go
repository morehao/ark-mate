// Package automation 是自动化定义的**纯逻辑**部分：时间契约、触发判定与状态机。
//
// 零 IO、零时钟（所有"现在"都由调用方传入）：调度这种东西的正确性几乎全在边界上
// （DST 空档/回拨、停机跨天、同刻重复触发），只有把时钟与文件都挪出去，那些边界才能在
// 表驱动单测里被逐条钉死。定义装载与落盘在 `internal/domain/automation` 与用例层，
// 会话创建与事件发布在 `internal/sessionruntime`。
//
// 时间契约（ADR-3，v35 修订）：`once` / `daily` / `weekly` / `interval` / `monthly` + **IANA 时区**，
// **不引 cron**。v35 主动放弃 cron：cron 的真实成本在方言/规范化/DST 的组合爆炸，"每月第 2 个周三"
// 这类长尾用自然语言近似或告知暂不支持；`interval`（每 N 秒，N≥60，纯加时长、DST 无关）与
// `monthly`（每月某日，当月无此日跳过）覆盖"每两小时/每月 1 号"等高频诉求。
// 表达力换来的三条好处：无依赖、错误消息可读、"本地墙钟"这套语义能写清楚。
package automation

import (
	"github.com/morehao/ark-mate/internal/core/failure"
	"path/filepath"
	"sort"
	"time"
)

// 调度种类。**只有这五种**（ADR-3，v35 增 interval/monthly）：不引 cron 表达式。
const (
	KindOnce     = "once"
	KindDaily    = "daily"
	KindWeekly   = "weekly"
	KindInterval = "interval" // 每 EverySeconds 秒触发一次（≥60，DST 无关）
	KindMonthly  = "monthly"  // 每月 Day 日（1–31）的本地墙钟点；当月无此日则跳过
)

// 触发来源（事件载荷 `trigger`）。
const (
	TriggerScheduled = "scheduled" // 正常运行期间的到期
	TriggerRecovery  = "recovery"  // 进程停机期间错过、启动时补的最近一次（ADR-9）
	TriggerManual    = "manual"    // POST /automations/:id/run
)

// Schedule 触发时刻定义。
//
// `Timezone` **每个变体都必填**：不填就按 UTC 解释会让"每天 9 点"在另一个时区变成"每天 17 点"，
// 而用户无从察觉——所以缺时区是**报错**而不是回落（见 Validate）。
type Schedule struct {
	Kind         string     `json:"kind"`
	Timezone     string     `json:"timezone"`
	At           *time.Time `json:"at,omitempty"`            // kind=once
	Hour         *int       `json:"hour,omitempty"`          // kind=daily|weekly|monthly，本地墙钟
	Minute       *int       `json:"minute,omitempty"`        // kind=daily|weekly|monthly
	DaysOfWeek   []int      `json:"days_of_week,omitempty"`  // kind=weekly，0=周日（与 time.Weekday 对齐）
	EverySeconds *int       `json:"every_seconds,omitempty"` // kind=interval，≥60
	Day          *int       `json:"day,omitempty"`           // kind=monthly，1–31（当月无此日跳过）
}

// Target 一次触发要建什么样的会话。
//
// `Cwd` 在**创建定义时**钉死，触发时不再解析"当前工作空间"：openwork 在这件事上吃过的亏
// （`automations.ts:124-130` 的注释：切换活跃工作空间会静默改掉所有 automation 的目标）
// 是同一类缺陷，不该复现（ADR-6）。
//
// **没有 runtime_hint、也没有 agent_id**：底座由构建期固定（backend/internal/runtime/build.go），
// 会话不再有 agent 身份（v19）。老定义里若还留着这两个键：读面宽容（本结构丢弃它），
// 写面显式拒绝（manager 的闸门 → 40001 + 指路），存量残留由写面自愈清除。
type Target struct {
	Cwd    string `json:"cwd"`
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`

	// Delivery 投递模式（v35）：`""`/`session` = 投递回既有会话（共享会话历史，**缺省主语义**）；
	// `new_session` = 新建会话跑（隔离，显式备选）。
	Delivery string `json:"delivery,omitempty"`
	// SessionID delivery=session 时必填：绑定被投递的原会话（写面校验其存在）。
	SessionID string `json:"session_id,omitempty"`
}

// 投递模式取值（v35，ADR v35 ③）。
const (
	// DeliverySession 投递回既有会话（缺省；空串也归一到这里）。
	DeliverySession = "session"
	// DeliveryNewSession 新建会话跑（显式备选，行为与 v35 之前逐字相同）。
	DeliveryNewSession = "new_session"
)

// NormalizeDelivery 把空串归一为缺省投递模式（`session`）。非法值**不在此处报错**——留给 Validate
// 显式报错（不静默回落：投递语义是用户意图，猜错等于把会话投到错的地方）。
func NormalizeDelivery(d string) string {
	if d == "" {
		return DeliverySession
	}
	return d
}

// Automation 定义文件（`<AutomationDir>/<id>.json`）的完整形态。
//
// 后半段是**运行态 overlay**：平台写回，人工不必编辑。刻意与定义同文件——运维打开一个文件
// 就能看到"上次什么时候跑的、跑成没成"；拆成两份文件必然出现"删了定义忘了状态"。
type Automation struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Schedule Schedule `json:"schedule"`
	Target   Target   `json:"target"`

	// MaxRunDuration 本轮的外层超时（Go duration 字符串；空 = 只用既有 `MATE_RUN_TIMEOUT`）。
	// **不引入第二套超时机制**：默认交给已有看门狗，这个字段只是可选的收紧，上限 60m。
	MaxRunDuration string `json:"max_run_duration,omitempty"`

	// —— 运行态 overlay（平台写回；未知键一并保留）——
	State string `json:"state,omitempty"` // active | inactive | needs_attention | archived
	// LastFiredAt 最近一次**认领**的 occurrence，不论结果（ADR-10 的推进点，也是
	// "这个 occurrence 是否已经触发过"的唯一判据）。名字沿用"fired"而不是"claimed"：
	// 定义文件里对用户而言它读作"上次是什么时候跑的"，而 skipped/overlap 也是"跑过一趟"。
	LastFiredAt         *time.Time `json:"last_fired_at,omitempty"`
	LastSessionID       string     `json:"last_session_id,omitempty"`
	LastStatus          string     `json:"last_status,omitempty"` // completed | failed | interrupted | skipped
	LastReason          string     `json:"last_reason,omitempty"` // 最近一次没跑成/失败的原因（人话）
	ConsecutiveFailures int        `json:"consecutive_failures,omitempty"`
	// RecentRuns 最近若干次触发的留痕（最新在前，最多 RecentRunsMax 条）。
	//
	// 与 `Last*` 有意的冗余：`Last*` 是"一眼看最近一次"的字段（面板列表直接用），
	// `RecentRuns` 是"最近这些次分别怎么了"（`GET /automations/:id/runs`）。
	// 两者由**同一个写入函数**一起生成，不存在各写一半的可能。
	RecentRuns []RunRecord `json:"recent_runs,omitempty"`
}

// ReservedKeys 平台独占的运行态键：用户写面（PUT/PATCH）**不得**写它们。
//
// 不设这条闸门的后果不是"数据难看"，而是**认领机制可被绕过**：`last_fired_at` 就是
// "这个 occurrence 已经触发过"的判据，客户端能写它就能让同一次触发再来一遍（重复跑有副作用的
// prompt），或者反过来把一条 automation 永久卡住不跑。`state` 不在此列——启用/停用/归档
// 本来就是用户操作（`needs_attention` 由平台写，用户可把它改回 `active` 表示"我修好了"）。
var ReservedKeys = []string{
	"last_fired_at", "last_session_id", "last_status", "last_reason",
	"consecutive_failures", "recent_runs",
}

// IsReservedKey 见 ReservedKeys。
func IsReservedKey(k string) bool {
	for _, r := range ReservedKeys {
		if r == k {
			return true
		}
	}
	return false
}

// MaxRunDurationLimit 单轮外层超时的上限（对齐 openwork 的运行时上限）。
const MaxRunDurationLimit = 60 * time.Minute

// Validate 校验定义的可执行性（调度 + 目标面）。跑不了的定义在**装载期**就该被拒，
// 而不是等到触发时每 30s 报一次错（那正是 ADR-10 要避免的刷屏形态）。
func (a *Automation) Validate() error {
	if a.ID == "" {
		return failure.NewError(failure.ClassRequest, "invalid_param", "automation 缺少 id", nil)
	}
	if err := a.Schedule.Validate(); err != nil {
		return err
	}
	if a.Target.Prompt == "" {
		return failure.NewError(failure.ClassRequest, "invalid_param", "automation 缺少 target.prompt（触发时没有可执行的内容）", nil)
	}
	if err := a.Target.Validate(); err != nil {
		return err
	}
	if _, err := a.RunDuration(); err != nil {
		return err
	}
	return nil
}

// Validate 校验目标面（投递模式 + 工作空间）。**纯结构校验**（不查会话是否存在——那要 IO，
// 归写面在宿主端口上做；这里只保证定义自身自洽）。
//
//   - `new_session`：cwd 必填**绝对路径**（ADR-6：工作空间必须在创建时钉死，触发时不再解析
//     活跃工作空间——openwork 在这件事上吃过亏）；
//   - `session`（含空串）：session_id 必填（缺 = `delivery_session_unset`，ADR v35 ④：老定义
//     装进来会进 needs_attention，等用户迁移，**不silently建新会话**）；cwd 不要求。
func (t Target) Validate() error {
	switch NormalizeDelivery(t.Delivery) {
	case DeliverySession:
		if t.SessionID == "" {
			return failure.NewError(failure.ClassRequest, "delivery_session_unset",
				"target.delivery=session 需要 target.session_id（投递回哪个会话）；"+
					"老定义若本意是批处理请改成 delivery=new_session 并给 target.cwd", nil)
		}
	case DeliveryNewSession:
		if t.Cwd == "" {
			return failure.NewError(failure.ClassRequest, "invalid_param",
				"delivery=new_session 缺少 target.cwd：工作空间必须在创建时钉死（触发时不再解析活跃工作空间）", nil)
		}
		if !filepath.IsAbs(t.Cwd) {
			return failure.NewError(failure.ClassRequest, "invalid_param",
				"automation 的 target.cwd 必须是绝对路径，得到 "+t.Cwd, nil)
		}
	default:
		return failure.NewError(failure.ClassRequest, "invalid_param",
			"target.delivery 必须是 session | new_session（或留空=session），得到 "+t.Delivery, nil)
	}
	return nil
}

// RunDuration 解析外层超时（空 = 0 = 不额外限制；负数/超上限 → 报错）。
func (a *Automation) RunDuration() (time.Duration, error) {
	if a.MaxRunDuration == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(a.MaxRunDuration)
	if err != nil {
		return 0, failure.NewError(failure.ClassRequest, "invalid_param",
			"max_run_duration 不是合法的 duration（如 30m）："+a.MaxRunDuration, nil)
	}
	if d < 0 {
		return 0, failure.NewError(failure.ClassRequest, "invalid_param", "max_run_duration 不能为负数", nil)
	}
	if d > MaxRunDurationLimit {
		return 0, failure.NewError(failure.ClassRequest, "invalid_param",
			"max_run_duration 上限 "+MaxRunDurationLimit.String()+"（超过就交给 MATE_RUN_TIMEOUT 看门狗）", nil)
	}
	return d, nil
}

// Validate 校验调度定义本身。
//
// 失败一律带码（`invalid_timezone` / `invalid_schedule`）：调度错误是用户可修的输入问题，
// 前端要能按码把它们指到对应字段，而不是显示一句笼统的"参数错误"。
func (s Schedule) Validate() error {
	switch s.Kind {
	case KindOnce, KindDaily, KindWeekly, KindInterval, KindMonthly:
	default:
		return failure.NewError(failure.ClassRequest, "invalid_schedule",
			"schedule.kind 必须是 once | daily | weekly | interval | monthly，得到 "+s.Kind, nil)
	}
	if _, err := s.Location(); err != nil {
		return err
	}
	switch s.Kind {
	case KindOnce:
		if s.At == nil || s.At.IsZero() {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "once 需要 schedule.at（绝对时刻）", nil)
		}
	case KindInterval:
		// interval 是纯时长（从上次触发/创建时刻起加 N 秒），与墙钟、DST 无关；不需要 hour/minute。
		if s.EverySeconds == nil {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "interval 需要 schedule.every_seconds（秒，≥60）", nil)
		}
		if *s.EverySeconds < 60 {
			return failure.NewError(failure.ClassRequest, "invalid_schedule",
				"schedule.every_seconds 最小 60（每 60 秒以内的高频轮询不做，避免短间隔任务堆积）", nil)
		}
	case KindMonthly:
		if s.Hour == nil || s.Minute == nil {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "monthly 需要 schedule.hour 与 schedule.minute（本地墙钟）", nil)
		}
		if s.Day == nil {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "monthly 需要 schedule.day（1–31，当月无此日则跳过）", nil)
		}
		if *s.Day < 1 || *s.Day > 31 {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "schedule.day 需在 1–31", nil)
		}
		if *s.Hour < 0 || *s.Hour > 23 {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "schedule.hour 需在 0–23", nil)
		}
		if *s.Minute < 0 || *s.Minute > 59 {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "schedule.minute 需在 0–59", nil)
		}
	case KindDaily, KindWeekly:
		if s.Hour == nil || s.Minute == nil {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", s.Kind+" 需要 schedule.hour 与 schedule.minute（本地墙钟）", nil)
		}
		if *s.Hour < 0 || *s.Hour > 23 {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "schedule.hour 需在 0–23", nil)
		}
		if *s.Minute < 0 || *s.Minute > 59 {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "schedule.minute 需在 0–59", nil)
		}
	}
	if s.Kind == KindWeekly {
		if len(s.DaysOfWeek) == 0 {
			return failure.NewError(failure.ClassRequest, "invalid_schedule", "weekly 需要 schedule.days_of_week（0=周日）", nil)
		}
		for _, d := range s.DaysOfWeek {
			if d < 0 || d > 6 {
				return failure.NewError(failure.ClassRequest, "invalid_schedule", "schedule.days_of_week 取值需在 0–6（0=周日）", nil)
			}
		}
	}
	return nil
}

// Location 解析 IANA 时区。**失败即报错，绝不回落 UTC**（回落会让"每天 9 点"变成"每天 17 点"
// 且用户无从察觉；宁可让用户看见 `invalid_timezone` 并改对）。
//
// 容器里没有 `/usr/share/zoneinfo` 时，`time.LoadLocation` 会失败——本项目自带 `time/tzdata`
// 兜底（约 +450KB 二进制），所以这里能拿到完整时区库（见 M4-7）。
func (s Schedule) Location() (*time.Location, error) {
	if s.Timezone == "" {
		return nil, failure.NewError(failure.ClassRequest, "invalid_timezone",
			"schedule.timezone 必填（IANA 名，如 Asia/Shanghai）：不填会让"+s.Kind+"按 UTC 解释", nil)
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return nil, failure.NewError(failure.ClassRequest, "invalid_timezone",
			"未知时区 "+s.Timezone+"（需 IANA 名，如 Asia/Shanghai / UTC）", nil)
	}
	return loc, nil
}

// Normalize 就地规范化（去重 + 升序）。**规范化必须发生在算/存之前**，否则
// `[1,1,3]` 与 `[1,3]` 会表现成两条不同的定义（而它们语义完全相同）。
func (s *Schedule) Normalize() {
	if s.Kind != KindWeekly || len(s.DaysOfWeek) == 0 {
		return
	}
	seen := make(map[int]bool, len(s.DaysOfWeek))
	out := make([]int, 0, len(s.DaysOfWeek))
	for _, d := range s.DaysOfWeek {
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Ints(out)
	s.DaysOfWeek = out
}

// Occurrence 一次触发时刻。
//
// `At` 是绝对时刻；`Wall` 是它在该时区的本地墙钟表示（诊断与预览展示用，避免前端各自换算出
// 三个不一样的答案）。`Warnings` 记录该时刻在计算中被 DST 修正过的事实（见 occurrencesAround）。
type Occurrence struct {
	At       time.Time `json:"at"`
	Wall     string    `json:"wall"`
	Timezone string    `json:"timezone"`
	Warnings []string  `json:"warnings,omitempty"`
}
