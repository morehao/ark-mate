package skill

import (
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"os"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/object/objtime"
)

// ——— §13.4 S2：物化体检（审计 / 复现面） ———
//
// 归属理由：它判的是"**技能**在每一处物化目标上的现状"，输入是技能定义 + 投影登记处，
// 输出是对外形态；没有任何会话或跨域状态参与。所以它属于技能平面，而不是某个用例包。

// declaredFormats 已实证的物化 format 列表（错误文案用）。
func declaredFormats() string { return strings.Join(capability.VettedFormats(), " / ") }

// SkillMaterialization 报告某 skill 在每一处已登记物化目标上的现状（只读，不写盘）。
//
// 为什么值得一个端点：物化是"平台事实源 → runtime 原生文件"的投影，投影完就没人知道
// "这个目录本该长什么样"。有了它就能回答三个具体问题：agent 实际读到的内容与注册表一致吗
// （`in_sync` / `tampered`）、产物是老版本还是当前版本（`stale`）、某处为什么没有产物
// （`missing` / `disabled` / `not_applicable` / `user_owned`）。修复不在这里做——重校与启停负责写。
func (m *Plane) SkillMaterialization(id string) (*MaterializationView, error) {
	if m.caps == nil {
		return nil, code.FromError(failure.NewError(failure.ClassService, "capability_unavailable", "能力数据面未装配", nil))
	}
	sk, ok := m.caps.SkillByID(id)
	if !ok {
		return nil, code.FromError(failure.NewError(failure.ClassRequest, "skill_not_found", fmt.Sprintf("未知 skill %q", id), nil))
	}
	resp := &MaterializationView{ID: sk.ID, Enabled: sk.IsEnabled()}
	projections := m.Snapshot()
	if len(projections) == 0 {
		resp.States = []MaterializationStateView{}
		resp.Notes = append(resp.Notes,
			"本进程没有解析出任何 skill 投影目标：所有已注册 runtime 都没声明 skill 投影，"+
				"或它们的投影根无法解析（占位符变量未设置）——技能只存在于注册表，未投影到任何 runtime 目录")
		return resp, nil
	}
	states := make([]MaterializationStateView, 0, len(projections))
	for _, p := range projections {
		states = append(states, materializationState(sk, p))
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

// materializationState 判定单个物化目标的现状（判定顺序即优先级，见 DTO 的 status 说明）。
//
// `dir`/`hash` 都由**投影形态**算出来（不再假设 `<root>/<id>`）：落点模板换了，审计视图跟着换，
// 两处不会漂移。
func materializationState(sk capability.Skill, p Projection) MaterializationStateView {
	spec := p.MaterializeSpec()
	st := MaterializationStateView{Root: p.Projection.Root, Runtime: p.Runtime}
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
		st.WrittenAt = objtime.Milli(prov.WrittenAt)
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
