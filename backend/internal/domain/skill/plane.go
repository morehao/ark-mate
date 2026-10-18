package skill

import (
	"fmt"

	"context"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/code"
)

// Plane 技能平面的**有状态主体**：持有能力注册表（技能定义都在这里）与投影登记处。
//
// 为什么注册表是注入的而不是自己造：**它是 skill 与 mcp 两个平面共用的那一份**（注册表里
// 同时装着技能与连接器定义），谁都不该 new 一个自己的——两份注册表 = 两个事实源。
// 注入在装配期完成、之后只读，所以不加锁。
type Plane struct {
	caps *capability.Registry
	defs *definition.Mechanism
	st   bool
	proj *Projections
}

// NewPlane 造一个技能平面。三个依赖都必须由装配期注入**同一个实例**：
// `caps` 是 skill 与 mcp 共用的注册表（两份 = 两个事实源）、`defs` 是定义机制、
// `st` 是库句柄（memory 模式为 nil）。任何一个是"自己 new 一份"都会让写面与读面各看一份状态。
func NewPlane(caps *capability.Registry, defs *definition.Mechanism) *Plane {
	return &Plane{caps: caps, defs: defs, proj: NewProjections()}
}

// SetStore 装配期登记库句柄。**必须在装配期调用**（库在 `NewService` 之后才装上）：
// 在构造时按值传一个当时的 `st` 会永远停在 nil，而表现是"该走库的走了文件"——静默。
// SetPersistence 标记本进程是否接了持久层（v3.1 P4：技能域只把它当**模式开关**用，
// 不读库——真正的读写归 `definition.DefinitionRepo` 的载体实现）。
func (m *Plane) SetPersistence(persistent bool) { m.st = persistent }

// Projections 投影登记处（技能平面的另一半状态）。
func (m *Plane) Projections() *Projections { return m.proj }

// Snapshot 当前登记的投影副本（域内自用的便捷入口）。
func (m *Plane) Snapshot() []Projection { return m.proj.Snapshot() }

// ListSkills 技能读面：把注册表里的技能定义投影成对外形态。
func (m *Plane) ListSkills() ([]dtocapability.SkillResp, error) {
	items := m.caps.ListSkills()
	out := make([]dtocapability.SkillResp, 0, len(items))
	for _, sk := range items {
		out = append(out, dtocapability.SkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body,
			Runtimes: append([]string(nil), sk.Runtimes...), Scope: sk.EffectiveScope(),
			Enabled: sk.IsEnabled(), Warnings: Warnings(sk),
		})
	}
	return out, nil
}

// ReconcileSkillProjection 按一处**已解析**投影重校物化（启动时与启停后调用，见 SetSkillEnabled）。
//
// 解析失败的投影（`Err` 非空）直接跳过：它**仍然登记**在平面临时列表里，好让审计视图报
// "没配置"而不是让这个 runtime 从列表里消失；但拿它去重校会把一个解不开的根写进盘上。
func (m *Plane) ReconcileSkillProjection(p Projection) (int, int, error) {
	if m.caps == nil || p.Err != "" {
		return 0, 0, nil
	}
	return capability.ReconcileSkills(p.MaterializeSpec(), m.caps.ListSkills())
}

// Tally 一次重校的计数（写成功 / 删掉 / 失败原因）。
type Tally struct {
	Written int
	Removed int
	Errors  []string
}

// ReconcileAll 按当前登记的全部投影重校一次物化（写面之后与启停共用同一套逻辑）。
//
// 失败**不回滚定义**：定义是对的，错的是某个 runtime 的目录（权限/占位/只读），如实把原因回带。

// reconcileSkillProjections 写面之后立刻重校物化（与 §13.4 S4 的启停共用同一套逻辑）。
//
// 失败**不回滚定义**：定义是对的，错的是某个 runtime 的目录（权限/占位/只读），如实把原因回带，
// 让用户知道"定义生效了，但那个底座没写出去"。
func (m *Plane) ReconcileAll() Tally {
	var t Tally
	for _, p := range m.Snapshot() {
		w, r, err := m.ReconcileSkillProjection(p)
		t.Written += w
		t.Removed += r
		if err != nil {
			t.Errors = append(t.Errors, fmt.Sprintf("物化重校失败（root=%s runtime=%s）：%v",
				p.Projection.Root, p.Runtime, err))
		}
	}
	return t
}

// writeSkillEnabled 把 `enabled` 写进定义载体并返回定位串（memory 模式 = 文件路径，
// sqlite 模式 = `db:skill/<id>`）。
//
// **只动一个键**：正文/来源/告警/未知键原样保留——启停是 overlay，不是重新注册。
// 载体的落盘纪律（未知键保留 / 乐观锁 / 原子替换）全部委派给 `capability.DefinitionFile`
// 与 store 的事务 CAS（见 capability_repo.go），这里只负责"启停"这一件事的顺序与出路。
func (m *Plane) WriteSkillEnabled(ctx context.Context, id string, enabled bool) (string, error) {
	if !m.st {
		// memory 模式：启停必须写回定义文件，没有目录就无处写回（sqlite 模式不需要目录）。
		// 这条对外码 `skill_not_persistable` 在 sqlite 模式下不再可达——DB 里的技能都有行，
		// 没有行就没有这条能力（M6-3 的语义变化，README 有说明）。
		if m.defs.Dir() == "" {
			return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
				fmt.Sprintf("skill %q 无法启停：本进程未装载能力数据面目录（MATE_CAPABILITIES），没有可持久化的定义文件", id), nil)
		}
	}
	repo, err := m.defs.DefinitionRepoFor(definition.CapSubSkills)
	if err != nil {
		return "", err
	}
	doc, rev, existed, err := repo.Open(ctx, id, map[string]any{"enabled": enabled})
	if err != nil {
		return "", err
	}
	if !existed {
		// 载体里没有它：代码注入/内存注册的技能无处写回。**不顺手新建**——启停不是定义的新建
		// 路径，"点一下开关就在库里多出一条定义"比报错难查得多。两个载体的出路文案不同
		// （一个要建文件、一个要用写面/导入落行），给出的动作必须各自可执行。
		if m.st {
			return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
				fmt.Sprintf("skill %q 在 %s 没有定义行（代码注入/内存注册的技能无处写回）：先用写面或导入把它落成定义后再启停",
					id, repo.Locator(id)), nil)
		}
		return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
			fmt.Sprintf("skill %q 没有定义文件 %s（代码注入/内存注册的技能无处写回）：请在能力目录里建成文件后再启停",
				id, repo.Locator(id)), nil)
	}
	if _, conflict, err := repo.Commit(ctx, id, doc, rev, existed); err != nil {
		return "", err
	} else if conflict {
		return "", definition.DefinitionChangedErr(repo.Locator(id))
	}
	return repo.Locator(id), nil
}

// SetSkillEnabled 启停一个 skill（§13.4 S4）：写回定义文件的 `enabled` + 更新注册表 + 立刻重校物化。
//
// 三件事的顺序是刻意的：
//  1. 先在**磁盘**上改（事实源先变，失败即整体失败，内存不动）；
//  2. 再改内存注册表（此后 agent 能力目录/物化都按新值走）；
//  3. 最后重校物化目标（把已物化的副本写进去或撤下来），重校失败**不**回滚前两步——
//     定义文件已经改了，此时谎报「失败」会让用户以为没生效，而重启后照样生效。失败原因随响应回带。
func (m *Plane) SetSkillEnabled(ctx context.Context, id string, enabled bool) (*dtocapability.SkillToggleResp, error) {
	if id == "" {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_required", "skill id 必填", nil))
	}
	if m.caps == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "capability_unavailable", "能力数据面未装配", nil))
	}
	if _, ok := m.caps.SkillByID(id); !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_not_found",
			fmt.Sprintf("未知 skill %q", id), nil))
	}
	path, err := m.WriteSkillEnabled(ctx, id, enabled)
	if err != nil {
		return nil, code.FromError(err)
	}
	after, _ := m.caps.SetSkillEnabled(id, enabled)

	resp := &dtocapability.SkillToggleResp{
		ID: after.ID, Enabled: after.IsEnabled(), Path: path,
	}
	// 立刻重校：禁用要**撤回**已物化的副本（否则 agent 下次拉起还看得见它），
	// 启用要把它写进去。重校按登记的目标逐个做，单个失败只记原因不中断。
	for _, p := range m.Snapshot() {
		w, r, mErr := m.ReconcileSkillProjection(p)
		resp.Written += w
		resp.Removed += r
		if mErr != nil {
			resp.MaterializeErrors = append(resp.MaterializeErrors,
				fmt.Sprintf("物化重校失败（root=%s runtime=%s）：%v", p.Projection.Root, p.Runtime, mErr))
		}
	}
	if enabled {
		resp.NextAction = "已启用：声明了候选 runtime 的 agent 下次拉起即可见；已物化的副本已按当前物化目标写出"
	} else {
		resp.NextAction = "已禁用：定义文件与正文保留（可随时改回 enabled:true 恢复）；已物化副本已撤回"
	}
	return resp, nil
}
