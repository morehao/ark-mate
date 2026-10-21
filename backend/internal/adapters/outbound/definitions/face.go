// Package definitions 是**能力定义装载面**的落点（C3 4.5 后半：原 `drivers/registry` 按域拆开）。
//
// 它有两块，都只服务「能力定义从哪来、装进哪个注册表」：
//
//   - `Face`：定义机制（文件 / 库两种载体）+ 能力注册表 + 库句柄的组合面。装载、重装、
//     注册工具、以及"同一条定义问题只 WARN 一次"的记账都在这里。网关子进程每次取数前
//     调的就是它的 `Reload`。
//   - 三个**窄端口适配体**：`skillDefs` / `mcpDefs` / `automationFiles`。它们把机制层的宽面
//     收窄成域声明的端口，因为 Go 的返回类型不协变，而机制层禁止 import 域。
//
// 为什么这些不再和供应商 / 工作空间 / 模型面挤在一个包里：它们围着同一个机制转，
// 而 `resources.go`（资源同步面）与 `models_view.go`（模型读面）围着另一个（同步与清单）。
// 一个包同时持有两者时，"改定义装载"与"改模型交付"在 diff 里长得一模一样。
package definitions

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/morehao/ark-mate/internal/adapters/outbound/mechanism/definition"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/platform/logging"
	"github.com/morehao/golib/glog"
)

// Face 能力定义装载面。
//
// 三样东西必须**同时**在场才有意义：机制（定义载体从哪来）、注册表（装进哪）、库（sqlite
// 模式的事实源）。三者各持一个引用而不是各造一个，是为了让"核心与网关读同一份定义"这件事
// 在类型上就成立——两份注册表 = 两个事实源。
type Face struct {
	// *definition.Mechanism 被**内嵌**：目录（`Dir`/`SetDir`）、`DefinitionRepoFor`、
	// `CapabilityDirFor` 都是机制的既有面，转发一遍只会多一层同形代码。
	*definition.Mechanism

	caps  *capability.Registry
	store *store.Store

	// mu/warned 装载期"同一条定义问题只 WARN 一次"的记账。
	// 装载会被反复调用（启动、导入、**网关每次取数**），不记账就会把一条坏定义刷成日志洪水。
	mu     sync.Mutex
	warned map[string]bool
}

// New 造能力定义装载面（机制与注册表都在这里新建：它们是构造期封闭的）。
func New(caps *capability.Registry) *Face {
	return &Face{Mechanism: definition.New(), caps: caps, warned: make(map[string]bool)}
}

// Capabilities 本面的能力注册表（与 `skill`/`mcp` 平面共用同一份）。
func (f *Face) Capabilities() *capability.Registry { return f.caps }

// Store 本面当前接的库句柄（未接库 = nil，memory 模式）。
func (f *Face) Store() *store.Store { return f.store }

// EnableStore 接上库句柄。
//
// 与 `Planes.EnableStore` 的分工：那边把**同一份**句柄发给每个持有它的平面，这里只负责本面。
// 单点赋值的原因是过去几处各自读同一个字段，"设一次就够了"——拆开后漏掉任一个的表现都是静默的。
//
// 必须**同时**给机制与自己的字段：机制那份决定的是写面走库还是走文件（`DefinitionRepoFor`
// 按 `m.st != nil` 分派），本面这份决定的是装载面从哪儿读。只设一边的表现是静默的——
// sqlite 模式的写面会退回文件载体，`Locator` 于是给出一个文件路径而不是 `db:skill/<id>`。
func (f *Face) EnableStore(st *store.Store) {
	f.store = st
	f.Mechanism.SetStore(st)
}

// LoadDir 加载能力数据面目录并合并（dir 为空 = 仅代码注入/空表）。
// 返回三类合计加载数（坏文件跳过，对齐定义装载的容错口径）。
//
// **语义降级（M6-2）**：自 v8 起，sqlite 模式下本方法**不再是装载源**——能力定义的事实源是
// DB（`skills`/`mcp_servers`），本方法只剩两个用途：memory 模式的装载路径，以及导入/导出路径
// （`mate capability import|export`）。
func (f *Face) LoadDir(dir string) int {
	if f == nil || f.caps == nil {
		return 0
	}
	f.SetDir(dir)
	return f.caps.LoadDir(dir, skill.NormalizeRuntimeDecls).Total()
}

// LoadFromStore 从持久层装载能力定义（sqlite 模式的事实源，M6-2）。
func (f *Face) LoadFromStore(ctx context.Context, importDir string) int {
	n, _ := f.reload(ctx, importDir, glog.InfoLevel)
	return n
}

// Reload 重新装载定义（**网关每次取数前**调用；M7-2）。
//
// 与 `LoadFromStore` 是同一份实现，只有日志档位不同：网关每次 tools/list / tools/call 都会触发，
// 用 Info 档会把日志刷成噪声——而噪声的代价是"真出问题时没人看日志"。
func (f *Face) Reload(ctx context.Context) (int, error) {
	return f.reload(ctx, "", glog.DebugLevel)
}

// RegisterTool 注册/覆盖一个工具目录条目（平台 builtin / 观察兜底 / MCP live）。
func (f *Face) RegisterTool(t capability.Tool) error {
	return f.caps.RegisterTool(&t)
}

// reload 装载内核（公开入口与网关共用；level 只作用于汇总行）。
func (f *Face) reload(ctx context.Context, importDir string, level glog.Level) (int, error) {
	if f == nil || f.caps == nil || f.store == nil {
		return 0, fmt.Errorf("能力注册表或持久层未初始化")
	}
	st := f.store
	if importDir != "" {
		f.SetDir(importDir)
	}
	// 装进**独立的**新注册表再整体换入（不是"先 Reset 再逐条注册"）：后者会在共享面上留下
	// 一个空的/半装的可观测中间态，而网关每次取数前都重装定义 ⇒ 并发调用必然撞上它。
	fresh := capability.NewRegistry()
	total := 0

	skills, err := st.ListSkillDefinitions(ctx)
	if err != nil {
		f.warnOnce("store:skills", "capability: 从持久层读 skill 定义失败（能力面将为空）", "err", err)
		return total, err
	}
	for i := range skills {
		row := skills[i]
		var sk capability.Skill
		if err := json.Unmarshal([]byte(row.Body), &sk); err != nil {
			f.warnOnce("skill:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		// id 以**行主键**为准：body 里的 id 与行主键不一致时，行主键是事实（它是 CAS 与审计的键）。
		if sk.ID != row.ID {
			if sk.ID != "" {
				f.warnOnce("skill-id:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", sk.ID)
			}
			sk.ID = row.ID
		}
		skill.NormalizeRuntimeDecls(&sk)
		// SourceFile 刻意**不填**：DB 化之后"文件来源"不再成立，库视图的来源口径在 M6-5/M6-6
		// 统一重定义为 db / builtin / imported。
		if err := fresh.RegisterSkill(&sk); err != nil {
			f.warnOnce("skill-rej:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	mcps, err := st.ListMcpServerDefinitions(ctx)
	if err != nil {
		f.warnOnce("store:mcps", "capability: 从持久层读 mcp server 定义失败", "err", err)
		return total, err
	}
	for i := range mcps {
		row := mcps[i]
		var m capability.McpServer
		if err := json.Unmarshal([]byte(row.Body), &m); err != nil {
			f.warnOnce("mcp:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp server 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		if m.ID != row.ID {
			if m.ID != "" {
				f.warnOnce("mcp-id:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", m.ID)
			}
			m.ID = row.ID
		}
		if err := fresh.RegisterMcpServer(&m); err != nil {
			f.warnOnce("mcp-rej:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	// 一次换面：此后所有读者看到的是完整的新目录。
	f.caps.ReplaceAll(fresh)
	logging.At(ctx, level, "capability: 定义已从持久层装载", "skills", len(skills), "mcps", len(mcps), "adopted", total)
	return total, nil
}

// warnOnce 同一条定义问题在一个进程内只 WARN 一次（key 须含 id 与 revision）。
//
// key 里带 revision 是刻意的：**同一条定义改坏了两次要报两次**。
func (f *Face) warnOnce(key, msg string, args ...any) {
	f.mu.Lock()
	seen := f.warned[key]
	f.warned[key] = true
	f.mu.Unlock()
	if seen {
		return
	}
	glog.Warnw(context.Background(), msg, args...)
}
