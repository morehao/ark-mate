package manager

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— §13.4 S4/S5：技能启停（overlay）与激活（正交） ———
//
// 两个概念分开，且都必须能对用户解释：
//
//	Enabled    平台要不要**投放**这个技能（定义文件里的 `enabled`；false = 不物化、不进 agent 能力目录）。
//	Activation 当前这个 agent 的环境**适不适用**（候选 runtime 里有没有可物化的 source）。
//	           —— 与 Enabled 正交：启用着的技能在某个 agent 上完全可能不适用，反之亦然。
//
// 禁用是 **overlay**：定义文件、正文、sources 一律保留，只把 `enabled` 写成 false。因此
// 「关掉一个技能」随时可逆（改回 true），不需要重新导入正文——这是与「删掉定义文件」的本质区别。
//
// 落盘 = 编辑用户文件（M10 纪律）：读原文件为 map → 只改 `enabled` 一个键 → 原子写回。
// **未知键原样保留**——用户在自己文件里加的字段不该被平台抹掉。

// SkillProjection 一处**已解析**的 skill 投影目标：runtime + 其有效投影形态。
//
// 为什么是「已解析」而不是原始声明：解析（覆盖链 + 占位符展开 + 校验）只在启动时做一次，
// 结果登记在这里供启停/重校/审计三处复用——三处各自再解析一遍必然出现"某处不展开占位符"
// 这类漂移。解析归 app（编排层），本结构只是结果。
type SkillProjection struct {
	Runtime    string
	Projection runtime.Projection
	// Err 解析该投影时的失败原因（非空 = 这个 runtime 声明了投影但根解不开：占位符变量未设置、
	// 落点模板非法…）。**仍然登记**它，是为了让审计视图能报"没配置"而不是让这个 runtime
	// 从列表里彻底消失——「我明明声明了，怎么哪儿都看不到」是最难排查的一种沉默。
	// 解析失败的投影不参与重校（Err 非空时 ReconcileSkillProjection 直接返回）。
	Err string
}

// Capability 把已解析的投影翻译成物化层要的平凡形态（P-1：capability 不 import runtime）。
func (p SkillProjection) MaterializeSpec() capability.MaterializeSpec {
	return capability.MaterializeSpec{
		Root:   p.Projection.Root,
		Layout: p.Projection.Layout,
		Format: p.Projection.Format,
		Owner:  p.Runtime,
		// 该投影方的历史归属名：物化层据此**认领**旧副本并在写回时把旧名字换成新名字。
		// 漏了它的后果很具体——`.mate-managed` 里那条旧名字永远留在集合里，于是
		// "集合空了才删目录"永不成立，**禁用/删除技能时副本删不掉**，底座照样读到已禁用的技能。
		OwnerAliases: runtime.LegacyAliasesOf(p.Runtime),
	}
}

// SetSkillProjections 登记物化目标（幂等覆盖；由 app 在启动解析后调用）。
func (s *Service) SetSkillProjections(ps []SkillProjection) {
	s.mu.Lock()
	s.skillProjections = append([]SkillProjection(nil), ps...)
	s.mu.Unlock()
}

// skillProjectionsSnapshot 取投影目标的副本。
func (s *Service) skillProjectionsSnapshot() []SkillProjection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SkillProjection(nil), s.skillProjections...)
}

// skillProjectionFor 取某 runtime 的已解析投影（ok=false = 本进程没登记它）。
func (s *Service) skillProjectionFor(runtimeID string) (SkillProjection, bool) {
	for _, p := range s.skillProjectionsSnapshot() {
		if p.Runtime == runtimeID {
			return p, true
		}
	}
	return SkillProjection{}, false
}

// ReconcileSkillProjection 按一处已解析投影重校物化（启动时与启停后调用，见 SetSkillEnabled）。
func (s *Service) ReconcileSkillProjection(p SkillProjection) (int, int, error) {
	if s.caps == nil || p.Err != "" {
		return 0, 0, nil
	}
	return capability.ReconcileSkills(p.MaterializeSpec(), s.caps.ListSkills())
}

// capabilityDirSnapshot 能力定义目录（memory 模式下是启停写回位置；sqlite 模式下是导入/导出目录）。
func (s *Service) capabilityDirSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capabilityDir
}

// SetSkillEnabled 启停一个 skill（§13.4 S4）：写回定义文件的 `enabled` + 更新注册表 + 立刻重校物化。
//
// 三件事的顺序是刻意的：
//  1. 先在**磁盘**上改（事实源先变，失败即整体失败，内存不动）；
//  2. 再改内存注册表（此后 agent 能力目录/物化都按新值走）；
//  3. 最后重校物化目标（把已物化的副本写进去或撤下来），重校失败**不**回滚前两步——
//     定义文件已经改了，此时谎报「失败」会让用户以为没生效，而重启后照样生效。失败原因随响应回带。
func (s *Service) SetSkillEnabled(ctx context.Context, id string, enabled bool) (*dtocapability.SkillToggleResp, error) {
	if id == "" {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_required", "skill id 必填", nil))
	}
	if s.caps == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "capability_unavailable", "能力数据面未装配", nil))
	}
	if _, ok := s.caps.SkillByID(id); !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_not_found",
			fmt.Sprintf("未知 skill %q", id), nil))
	}
	path, err := s.writeSkillEnabled(ctx, id, enabled)
	if err != nil {
		return nil, code.FromError(err)
	}
	after, _ := s.caps.SetSkillEnabled(id, enabled)

	resp := &dtocapability.SkillToggleResp{
		ID: after.ID, Enabled: after.IsEnabled(), Path: path,
	}
	// 立刻重校：禁用要**撤回**已物化的副本（否则 agent 下次拉起还看得见它），
	// 启用要把它写进去。重校按登记的目标逐个做，单个失败只记原因不中断。
	for _, p := range s.skillProjectionsSnapshot() {
		w, r, mErr := s.ReconcileSkillProjection(p)
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

// writeSkillEnabled 把 `enabled` 写进定义载体并返回定位串（memory 模式 = 文件路径，
// sqlite 模式 = `db:skill/<id>`）。
//
// **只动一个键**：正文/来源/告警/未知键原样保留——启停是 overlay，不是重新注册。
// 载体的落盘纪律（未知键保留 / 乐观锁 / 原子替换）全部委派给 `capability.DefinitionFile`
// 与 store 的事务 CAS（见 capability_repo.go），这里只负责"启停"这一件事的顺序与出路。
func (s *Service) writeSkillEnabled(ctx context.Context, id string, enabled bool) (string, error) {
	if s.st == nil {
		// memory 模式：启停必须写回定义文件，没有目录就无处写回（sqlite 模式不需要目录）。
		// 这条对外码 `skill_not_persistable` 在 sqlite 模式下不再可达——DB 里的技能都有行，
		// 没有行就没有这条能力（M6-3 的语义变化，README 有说明）。
		if s.capabilityDirSnapshot() == "" {
			return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
				fmt.Sprintf("skill %q 无法启停：本进程未装载能力数据面目录（MATE_CAPABILITIES），没有可持久化的定义文件", id), nil)
		}
	}
	repo, err := s.definitionRepoFor(capSubSkills)
	if err != nil {
		return "", err
	}
	doc, rev, existed, err := repo.open(ctx, id, map[string]any{"enabled": enabled})
	if err != nil {
		return "", err
	}
	if !existed {
		// 载体里没有它：代码注入/内存注册的技能无处写回。**不顺手新建**——启停不是定义的新建
		// 路径，"点一下开关就在库里多出一条定义"比报错难查得多。两个载体的出路文案不同
		// （一个要建文件、一个要用写面/导入落行），给出的动作必须各自可执行。
		if s.st != nil {
			return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
				fmt.Sprintf("skill %q 在 %s 没有定义行（代码注入/内存注册的技能无处写回）：先用写面或导入把它落成定义后再启停",
					id, repo.locator(id)), nil)
		}
		return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
			fmt.Sprintf("skill %q 没有定义文件 %s（代码注入/内存注册的技能无处写回）：请在能力目录里建成文件后再启停",
				id, repo.locator(id)), nil)
	}
	if _, conflict, err := repo.commit(ctx, id, doc, rev, existed); err != nil {
		return "", err
	} else if conflict {
		return "", definitionChangedErr(repo.locator(id))
	}
	return repo.locator(id), nil
}

// SkillActivation 一个 skill 对某 agent 的适用性（§13.4 S5；Enabled 与 Activation 正交）。
type SkillActivation struct {
	Applicable bool
	Reasons    []string
}

// SkillActivationFor 计算某 skill 在某 agent 候选 runtime 下的适用性，并给出**人话原因**。
//
// 与「可见性过滤」的区别：过滤会让人看不见（"技能怎么没了？"），这里给出
// 「不适用 + 为什么 + 怎么改」——不适用不是错误，是环境事实。
//
// 判定分层（按声明顺序取原因，多条并列时全部列出）：
//
//  1. 已禁用（enabled:false）——平台不投放；
//  2. 候选 runtime 里没有「该技能声明要给它 + 它在本进程登记了投影」的交集；
//     这一条要分清三种子情况（用户的改法完全不同）：技能没声明任何 runtimes 白名单但候选
//     runtime 一个都没投影（→ 去配 MATE_PROJECTION_DIRS / 换 runtime）、技能声明的白名单
//     与候选不相交（→ 改技能的 runtimes）、候选 runtime 未注册（→ 注册它）。
//
// `projections` 是**已解析**投影的 runtime 集合（键 = runtime id）。
func SkillActivationFor(sk capability.Skill, runtimes []string, inAgent map[string]bool,
	projections map[string]bool) SkillActivation {
	var reasons []string
	if !sk.IsEnabled() {
		reasons = append(reasons, "技能已被禁用（enabled:false）：改回 enabled:true 即可恢复（定义与正文一直保留）")
	}
	// 该技能的声明白名单（空 = 全部）。
	declared := append([]string(nil), sk.Runtimes...)
	sort.Strings(declared)
	var carried, noProjection, notDeclared []string
	for _, rt := range runtimes {
		if !inAgent[rt] {
			continue
		}
		if !sk.AppliesToRuntime(rt) {
			notDeclared = append(notDeclared, rt)
			continue
		}
		if !projections[rt] {
			noProjection = append(noProjection, rt)
			continue
		}
		carried = append(carried, rt)
	}
	if len(carried) == 0 {
		switch {
		case len(noProjection) > 0 && len(notDeclared) == 0:
			reasons = append(reasons, fmt.Sprintf(
				"候选 runtime %v 没有可用的 skill 投影登记（本进程没解析出它们的投影根）："+
					"用 MATE_PROJECTION_DIRS 指定，或确认该 runtime 确实声明了 skill 投影", noProjection))
		case len(notDeclared) > 0 && len(noProjection) == 0:
			reasons = append(reasons, fmt.Sprintf(
				"技能声明只给 %v 用，与该 agent 的候选 runtime %v 不相交——要投放就把它加进技能的 runtimes",
				declared, runtimes))
		case len(noProjection) > 0:
			reasons = append(reasons, fmt.Sprintf(
				"候选 runtime 里 %v 未被本技能声明（技能声明的是 %v），%v 又没有可用的 skill 投影登记",
				notDeclared, declared, noProjection))
		default:
			reasons = append(reasons, "该 agent 没有可用 skill 投影的候选 runtime（未注册 / 未声明 / 未解析出投影根）")
		}
	}
	return SkillActivation{Applicable: len(reasons) == 0, Reasons: reasons}
}

// declaredFormats 已实证的物化 format 列表（错误文案用）。
func declaredFormats() string { return strings.Join(capability.VettedFormats(), " / ") }

// ——— §13.4 S2：物化体检（审计 / 复现面） ———

// SkillMaterialization 报告某 skill 在每一处已登记物化目标上的现状（只读，不写盘）。
//
// 为什么值得一个端点：物化是"平台事实源 → runtime 原生文件"的投影，投影完就没人知道
// "这个目录本该长什么样"。有了它就能回答三个具体问题：agent 实际读到的内容与注册表一致吗
// （`in_sync` / `tampered`）、产物是老版本还是当前版本（`stale`）、某处为什么没有产物
// （`missing` / `disabled` / `not_applicable` / `user_owned`）。修复不在这里做——重校与启停负责写。
func (s *Service) SkillMaterialization(id string) (*dtocapability.SkillMaterializationResp, error) {
	if s.caps == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "capability_unavailable", "能力数据面未装配", nil))
	}
	sk, ok := s.caps.SkillByID(id)
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_not_found", fmt.Sprintf("未知 skill %q", id), nil))
	}
	resp := &dtocapability.SkillMaterializationResp{ID: sk.ID, Enabled: sk.IsEnabled()}
	projections := s.skillProjectionsSnapshot()
	if len(projections) == 0 {
		resp.States = []dtocapability.SkillMaterializationState{}
		resp.Notes = append(resp.Notes,
			"本进程没有解析出任何 skill 投影目标：所有已注册 runtime 都没声明 skill 投影，"+
				"或它们的投影根无法解析（占位符变量未设置）——技能只存在于注册表，未投影到任何 runtime 目录")
		return resp, nil
	}
	states := make([]dtocapability.SkillMaterializationState, 0, len(projections))
	for _, p := range projections {
		states = append(states, skillMaterializationState(sk, p))
	}
	sort.SliceStable(states, func(i, j int) bool {
		if states[i].Root != states[j].Root {
			return states[i].Root < states[j].Root
		}
		return states[i].Runtime < states[j].Runtime
	})
	resp.States = states
	return resp, nil
}

// skillMaterializationState 判定单个物化目标的现状（判定顺序即优先级，见 DTO 的 status 说明）。
//
// `dir`/`hash` 都由**投影形态**算出来（不再假设 `<root>/<id>`）：落点模板换了，审计视图跟着换，
// 两处不会漂移。
func skillMaterializationState(sk capability.Skill, p SkillProjection) dtocapability.SkillMaterializationState {
	spec := p.MaterializeSpec()
	st := dtocapability.SkillMaterializationState{Root: p.Projection.Root, Runtime: p.Runtime}
	if p.Err != "" {
		// 声明了投影但根解不开：如实报 not_configured，并把人话原因带上（这是**配置**问题，
		// 不是技能问题——所以不去检查磁盘上有没有产物）。
		st.Status = "not_configured"
		st.Detail = fmt.Sprintf("该 runtime 声明了 skill 投影但无法解析出根：%s", p.Err)
		return st
	}
	dir, dirErr := spec.DirFor(sk.ID)
	if dirErr != nil {
		st.Status = "not_applicable"
		st.Detail = fmt.Sprintf("该 runtime 的投影落点无法实例化：%v", dirErr)
		return st
	}
	st.Dir = dir
	content, materializable := capability.MaterializedContent(sk, spec.Format)
	materializable = materializable && sk.AppliesToRuntime(p.Runtime)
	if materializable {
		st.Hash = capability.PayloadHash(content)
	}
	_, statErr := os.Lstat(dir)
	exists := statErr == nil
	owners, managed := capability.ManagedDir(dir)
	st.Owners = owners

	switch {
	case !sk.IsEnabled():
		switch {
		case exists && managed:
			st.Status = "stale"
			st.Detail = "技能已禁用，但平台物化的副本还在（下一次重校撤回；也可再 PATCH enabled:true 恢复）"
		case exists:
			st.Status = "user_owned"
			st.Detail = "技能已禁用；该目录不带平台归属标记（用户自有内容，平台不动）"
		default:
			st.Status = "disabled"
			st.Detail = "技能已禁用：不投放，本就不该有产物"
		}
	case !materializable:
		st.Status = "not_applicable"
		if !sk.AppliesToRuntime(p.Runtime) {
			st.Detail = fmt.Sprintf("该技能的 runtimes 白名单（%v）不含 %s：不投放到这个 runtime（要投放就把它加进 runtimes）",
				sk.Runtimes, p.Runtime)
		} else {
			st.Detail = fmt.Sprintf("该 runtime 的投影声明了未经实证的 format %q：物化不出来（已实证：%s）",
				spec.Format, declaredFormats())
		}
	case !exists && !p.Projection.Tested:
		// 声明了投影、根也解析得出，但**形态没经真实底座端到端实测**：如实报 untested，
		// 不假装 projected（design doc：未实测 → 报 untested，不臆造）。
		st.Status = "untested"
		st.Detail = "投影形态尚未经真实底座端到端实测（产物本应在这里）：" +
			p.Projection.UnverifiedReason()
	case !exists:
		st.Status = "missing"
		st.Detail = "该物化但没有产物：下一次重校会写出"
	case !managed:
		st.Status = "user_owned"
		st.Detail = "目录已存在但不带平台归属标记：平台拒绝覆盖（改名/移走它，或换一个物化根）"
	default:
		// 有归属标记：看摘要。ReadProvenance 失败 = 无 sidecar（老产物）或 sidecar 坏。
		prov, err := capability.ReadProvenance(dir)
		if err != nil {
			st.Status = "stale"
			st.Detail = "产物没有可读的摘要 sidecar（老版本产物或 sidecar 损坏）：下一次重校会重写并补上摘要"
			if actual, hErr := capability.ComputeDirHash(dir); hErr == nil {
				st.ActualHash = actual
			}
			break
		}
		st.WrittenAt = prov.WrittenAt.UTC().Format(time.RFC3339)
		actual, hashErr := capability.ComputeDirHash(dir)
		switch {
		case hashErr != nil:
			// 软链逃逸/未知文件类型：算不出摘要本身就是"产物被动了"的证据。
			st.Status = "tampered"
			st.Detail = fmt.Sprintf("产物无法计算摘要：%v", hashErr)
		case actual != prov.Hash:
			st.Status = "tampered"
			st.ActualHash = actual
		case prov.Hash != st.Hash:
			st.Status = "stale"
			st.ActualHash = actual
			st.Detail = "产物摘要是老版本/别的 runtime 写的：与当前注册表不一致，下一次重校会改写"
		default:
			st.Status = "in_sync"
			st.ActualHash = actual
		}
	}
	return st
}
