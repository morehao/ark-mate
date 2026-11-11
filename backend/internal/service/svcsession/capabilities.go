package svcsession

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
)

// ——— M3-8：会话内改能力（技能 / MCP / 专家）———
//
// 这一层负责的是**请求语义**：三态补丁怎么合、什么时候算"没变"、失败要带哪个码。
// 它不碰注册表（专家推荐展开、可达性事实在 `sessionruntime` 的适配器里），也不碰编排
// （"下一轮生效"是代际迁移那条既有路径的事）。
//
// 生效链一句话：**写 `sessions.capability_json` 就完事了**。`maybeMigrateGeneration` 在每个
// 轮次边界比「记录里的选定算出的指纹」与「句柄此刻挂着的指纹」，不等就切、忙就顺延、切不动就
// 回退旧代际继续跑。所以这里**没有** pending 列，也没有"待生效"状态——它是个派生量。
type CapabilityEditSvc interface {
	// UpdateCapabilities 会话内改能力（逐 kind 三态补丁；返回改后的会话快照）。
	//
	// 失败**零副作用**：校验（专家存在/启用、技能与 MCP 可达）全部在写库之前。
	UpdateCapabilities(ctx context.Context, id string, patch dtosession.SessionCapabilitiesPatch) (*dtosession.SessionResp, error)
}

// CapabilityEditDeps 会话内改能力的两份依赖（窄端口）。
//
//	`ExpandCapabilities` 是**能力定义面**的事实（专家定义、技能投影、MCP 传输），只有装配侧知道；
//	                     它同时给出本次编辑的**边界提示**（人格不重注入、推荐模型不切换）。
//	`UpdateCapability`    是持久层动作，且带"写库失败只告警、不阻断响应"的口径（同 `UpdateMeta`）。
type CapabilityEditDeps interface {
	// ExpandCapabilities 按该会话钉住的 runtime 校验并物化选定：专家推荐只填空缺、不可达推荐
	// 跳过并告警；专家不存在/已禁用**上抛**（那时一个字节都不该落库）。
	//
	// 返回的 warnings 含两类：推荐被跳过的项，以及"换专家"这次动作的边界提示。
	ExpandCapabilities(ctx context.Context, sessionID string, sel *session.CapabilitySelection) (
		*session.CapabilitySelection, []dtosession.CapabilityWarningResp, error)

	// UpdateCapability 落库能力选定（失败只告警，所以没有返回值）。
	UpdateCapability(ctx context.Context, id, capabilityJSON string)
}

// CapabilityEdit 会话内改能力的用例。
type CapabilityEdit struct {
	plane *session.Plane
	deps  CapabilityEditDeps
}

// NewCapabilityEdit 造用例（依赖事实由装配点交下来）。
func NewCapabilityEdit(plane *session.Plane, deps CapabilityEditDeps) CapabilityEditSvc {
	return &CapabilityEdit{plane: plane, deps: deps}
}

var _ CapabilityEditSvc = (*CapabilityEdit)(nil)

// UpdateCapabilities 会话内改能力。
//
// 顺序是判据，不能换：
//
//	① 读当前选定 → 合并补丁（纯内存，无副作用）；
//	② 物化（专家推荐填空缺）+ 可达性校验（`ValidateSelection`，域内判断）——**都在写库之前**；
//	③ 与当前等价 → 直接回快照（不写库、不换代际：点一下已经选中的 chip 不该白起一个进程）；
//	④ 写内存 + 落库。
//
// 第 ③ 步的"等价"把 `nil` 与 `{}` 当同一件事（都回落默认），但**写库时绝不把清空写成空串**：
// 空串是"未声明"，代际迁移对未声明会把目标指纹回落成句柄当前值 → 判"没变化" → 清空能力
// 永远不生效。`MarshalCapabilitySelection` 对空选定给的是 `{}`（非空串），正是这里要的形态。
func (c *CapabilityEdit) UpdateCapabilities(ctx context.Context, id string,
	patch dtosession.SessionCapabilitiesPatch) (*dtosession.SessionResp, error) {
	rec := c.plane.Rec(id)
	if rec == nil {
		return nil, sessionNotFoundErr(id)
	}
	domainPatch, err := capabilityPatchOf(patch)
	if err != nil {
		return nil, code.FromError(err)
	}
	current := session.ParseCapabilitySelection(rec.CapabilityJSON)
	merged := session.ApplyCapabilityPatch(current, domainPatch)
	expanded, warnings, xerr := c.deps.ExpandCapabilities(ctx, id, merged)
	if xerr != nil {
		return nil, code.FromError(xerr)
	}
	// 可达性用**物化后**的选定判：专家推荐里被跳过的项已经变成 warnings，剩下的都必须真的能用。
	// 判据在域内（与建会话同一份），所以"面板上显示不可达"与"这里被拒"永远同向。
	if verr := c.plane.ValidateSelection(expanded, rec.RuntimeID); verr != nil {
		return nil, code.FromError(verr)
	}

	if !session.EquivalentCapabilitySelection(current, expanded) {
		capabilityJSON := session.MarshalCapabilitySelection(expanded)
		// 回调里只动字段（`Mutate` 的规矩：持锁区不许 I/O）；落库在锁外。
		if !c.plane.Mutate(id, func(r *session.SessionRec) {
			r.CapabilityJSON = capabilityJSON
			r.UpdatedAt = time.Now().UTC()
		}) {
			// 读到了又改不到：只可能是两条语句之间被删（与 `Meta.UpdateSession` 同一出口）。
			return nil, sessionNotFoundErr(id)
		}
		c.deps.UpdateCapability(ctx, id, capabilityJSON)
	}

	snap, _, ok := c.plane.SessionSnapshot(id)
	if !ok {
		return nil, sessionNotFoundErr(id)
	}
	out := sessionRespDTO(snap)
	if out.Capabilities != nil && len(warnings) > 0 {
		out.Capabilities.Warnings = warnings
	}
	return &out, nil
}

// capabilityPatchOf 把 wire 形态的三态补丁翻成域形态（形状不对 → 40001 `invalid_param`）。
//
// 只做"这个键出现了吗、值是什么类型"，不做业务判断：语义（合并、校验）在下面与域里。
func capabilityPatchOf(p dtosession.SessionCapabilitiesPatch) (session.CapabilityPatch, error) {
	var out session.CapabilityPatch
	var err error
	if out.Skills, err = optionalIDsOf(p.Skills, "skills"); err != nil {
		return session.CapabilityPatch{}, err
	}
	if out.Mcps, err = optionalIDsOf(p.Mcps, "mcps"); err != nil {
		return session.CapabilityPatch{}, err
	}
	if out.Expert, err = optionalStringOf(p.Expert, "expert"); err != nil {
		return session.CapabilityPatch{}, err
	}
	if out.Apps, err = optionalIDsOf(p.Apps, "apps"); err != nil {
		return session.CapabilityPatch{}, err
	}
	return out, nil
}

// optionalIDsOf 解析一个「可缺席的 id 列表」：键缺失 = 不改；`null` = 未声明；数组 = 该列表。
func optionalIDsOf(raw json.RawMessage, key string) (session.OptionalIDs, error) {
	if len(raw) == 0 {
		return session.OptionalIDs{}, nil
	}
	var v *[]string
	if err := json.Unmarshal(raw, &v); err != nil {
		return session.OptionalIDs{}, failure.NewError(failure.ClassRequest, "invalid_param",
			fmt.Sprintf("参数错误: %s 需要字符串数组或 null（收到 %s）", key, string(raw)), nil)
	}
	return session.OptionalIDs{Set: true, Value: v}, nil
}

// optionalStringOf 解析一个「可缺席的字符串」：键缺失 = 不改；`null` = 未声明；字符串 = 该值。
func optionalStringOf(raw json.RawMessage, key string) (session.OptionalString, error) {
	if len(raw) == 0 {
		return session.OptionalString{}, nil
	}
	var v *string
	if err := json.Unmarshal(raw, &v); err != nil {
		return session.OptionalString{}, failure.NewError(failure.ClassRequest, "invalid_param",
			fmt.Sprintf("参数错误: %s 需要字符串或 null（收到 %s）", key, string(raw)), nil)
	}
	return session.OptionalString{Set: true, Value: v}, nil
}
