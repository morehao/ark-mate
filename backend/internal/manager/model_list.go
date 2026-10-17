package manager

import (
	"context"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— 应用层模型清单的写面（v12）：CRUD + **单一**默认模型 + 同步触发 ———
//
// 与 v11 的两处根本差别：
//
//	① **没有 runtime 维度**：一份清单投给所有已注册底座（ADR-1/B3）。于是 v11 里那套
//	   "runtime 是否已声明 ConfigProjection"的前置校验、逐 runtime 的物化、逐 runtime 的默认
//	   全部消失；
//	② **默认模型是单一值**（`meta.MetaKeyDefaultModel`，ADR-21）：它是**用户的偏好**，
//	   不是模型行的属性。`is_default` 只是写面的**触发字段**，不落进资源、不出现在读面
//	   （A14 的结构断言就钉这一点）。
//
// 清单需要 sqlite 持久层：memory 模式没有 `models` 表，所以那里的写面明确报 40501
// 而不是"内存生效、盘上没留痕"。

// ListModels 列出全部平台清单（**不分页**：清单上限 200 条，全量返回）。
func (s *Service) ListModels(ctx context.Context) ([]dtomodel.ModelResp, error) {
	// memory 模式没有清单载体：读面回空列表（写面在 `service/svcmodel`，那里报 40501）。
	if s.st == nil {
		return []dtomodel.ModelResp{}, nil
	}
	rows, err := s.st.ListModelDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	// **不再在 Go 侧排序**：顺序由 SQL 的 `ORDER BY provider_id, model_id` 给出（ADR-6）。
	// 这里再排一次会让"库里的顺序"与"对外顺序"变成两份可能分叉的事实。
	out := make([]dtomodel.ModelResp, 0, len(rows))
	for i := range rows {
		out = append(out, modelRespFromRow(&rows[i]))
	}
	return out, nil
}

// DefaultModelValueFor 返回某 runtime 当前默认模型的**不透明值**（无默认 → 空串）。
//
// v12 起默认是**单一值**（`meta.default_model`），所以这里没有"某 runtime 的默认"这回事——只是把
// 同一个平台模型 id 按该 runtime 的生成规则折成它认得不透明值。
//
// v26 起会话创建路径改走 `resolveSessionModel`（它一次给出"平台行 id + 运行时值"），本函数因此
// **委托**给同一条实现而不是自己再走一遍 `defaultModelRow` + `modelValueFor`：两份实现会在
// "某个底座不声明模型值形态"这类边界上分叉，而那种分叉只在一条路径上显现。保留它是为了这个
// 语义本身（"某 runtime 的默认模型值"）与既有诊断/测试判据。
func (s *Service) DefaultModelValueFor(ctx context.Context, runtimeID string) (string, error) {
	_, cfg, err := s.resolveSessionModel(ctx, runtimeID, "")
	return cfg.Model, err
}

// defaultModelRow 读出默认模型那一行（未设 / 指向已删模型 → (nil,false,nil)）。
func (s *Service) defaultModelRow(ctx context.Context) (*model.ModelEntity, bool, error) {
	if s.st == nil {
		return nil, false, nil
	}
	id, ok, err := s.st.DefaultModelID(ctx)
	if err != nil || !ok {
		return nil, false, err
	}
	row, found, err := s.st.GetModelDefinition(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	return row, true, nil
}

// modelValueFor 平台清单 → 运行时认得的不透明值（**下发与物化必须共用它**）。
//
// 生成规则**不属于内核**：它挂在注册表的第四张表上（`runtime.ModelValueAdapter`，见
// `pkg/runtime/model_value.go`）——dsh 是 `["<provider>","<model>"]` 这个 JSON 数组，别的底座
// 可能是 `<provider>/<model>` 这一串。内核只做分发，一个内建底座名都不认识。
//
// 这里曾经是一段 `switch runtimeID { case "dsh": …; case "opencode": …; default: 报错 }`：
// 接第三个底座的人会在 `default` 撞墙，然后**回内核加一个 case**。收口后由 archtest 的
// `TestInternalHasNoRuntimeIDSpecialCase` 钉住这一类特判。
//
// `ok=false`（该 id 没有登记实现）= 这个底座不消费平台的模型清单，**不是失败**；调用方按语义
// 选"回空串（不下发）"或"跳过这条"，所以这里返回的是可判别的错误而不是一个空值。
//
// 入参名 v24 从 `modelID` 改成 `model`：它接的是**上游模型名**（`models.model`），不是任何一行的
// id——改名之前这个名字让"这一列到底是不是 id"要靠读注释才知道。
func modelValueFor(reg *runtime.Registry, runtimeID, providerID, model string) (string, error) {
	a, ok := reg.ModelValue(runtimeID)
	if !ok {
		return "", acl.NewError(acl.ClassUnsupported, "model_value_unsupported",
			fmt.Sprintf("runtime %q 的模型值形态未声明：平台不为未知底座猜模型值的形状", runtimeID), nil)
	}
	return a.Value(providerID, model)
}

// ——— 内部工具 ———

// resolveSessionModel 把"会话请求里的**平台模型行 id**"解析成 (平台行 id, 运行时模型配置)。
//
// 语义（v26 定案「辰」，设计 §5.4）：
//
//	入参为空          → 用 `meta.default_model` 那一行；没有默认行 → ("", {}, nil) = **不下发**
//	                    模型（跟随底座自己的默认，与 v26 之前的"零回归那一支"逐字相同）；
//	入参非空但形状不对 → 40401 `model_not_found`；
//	入参非空但查不到行 → 40401 `model_not_found`（**复用既有码**，E9：不新增错误码——它此前的用途是
//	                    "PATCH/DELETE 打到不存在的条目"，这里扩到"会话引用了一个查不到的模型行"，
//	                    同一个码、同一份引导（刷新列表））。
//
// 返回值的第一项是**平台行 id**（落库的那一个）；第二项是**运行时的不透明值**，由 adapter 现算
// （`modelValueFor`）——它**不落库、不过 wire**（这正是「辰」的核心：库里只留身份）。
//
// 纪律：解析失败一律**上抛**，绝不静默回落到底座默认、也绝不静默换成别的模型——"界面显示 A、
// 实际用 B"正是本仓要消灭的静默偏差（这也是有意偏离 QwenWork 四级回退的那一处）。
func (s *Service) resolveSessionModel(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error) {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" {
		row, ok, err := s.defaultModelRow(ctx)
		if err != nil || !ok {
			// 读不出来 = 无法校验：不下发模型（不是把上一次的选择偷偷用上）。
			return "", runtime.ModelConfig{}, err
		}
		id, cfg, verr := s.modelValueForRow(runtimeID, row.ID, row.ProviderID, row.Model)
		if verr != nil {
			if ae, ok := acl.As(verr); ok && ae.Code == "model_value_unsupported" {
				// 该底座**不消费**平台模型清单 = 它没有模型语义：这里"不下发"是正确结论而不是失败
				// （与 v26 之前 `DefaultModelValueFor` 回空串的行为逐字相同——零回归的那一支）。
				return "", runtime.ModelConfig{}, nil
			}
			return "", runtime.ModelConfig{}, verr
		}
		return id, cfg, nil
	}
	if !strings.HasPrefix(trimmed, objid.Model) || !objid.IsPlatformID(trimmed) {
		// 形状就不对：它不可能是平台行 id（旧形态的不透明值 / 手抄错的串都会落到这里）。
		return "", runtime.ModelConfig{}, sessionModelNotFoundErr(trimmed)
	}
	row, found, err := s.st.GetModelDefinition(ctx, trimmed)
	if err != nil {
		return "", runtime.ModelConfig{}, err
	}
	if !found {
		return "", runtime.ModelConfig{}, sessionModelNotFoundErr(trimmed)
	}
	return s.modelValueForRow(runtimeID, row.ID, row.ProviderID, row.Model)
}

// modelValueForRow 把一条**平台模型行**折成 (行 id, 运行时配置)。
//
// 独立成函数而不是塞进 `resolveSessionModel` 的两个分支里：两条来源（显式 id / 默认行）必须走
// **同一段**翻译，否则"显式选的那条"与"默认那条"的运行时值会各长一套（而那种分叉只在某一条路径上显现）。
func (s *Service) modelValueForRow(runtimeID, rowID, providerID, model string) (string, runtime.ModelConfig, error) {
	v, err := modelValueFor(s.reg, runtimeID, providerID, model)
	if err != nil {
		return "", runtime.ModelConfig{}, err
	}
	// `Provider` 是**留痕语义**（仅展示/诊断，不进任何契约）：v26 之前它填的是恒空的
	// `SessionRec.ProviderID`，现在由**模型行**派生——值终于有来源，含义一个字没变。
	return rowID, runtime.ModelConfig{Provider: providerID, Model: v}, nil
}

// sessionModelNotFoundErr 会话引用了平台清单里查不到的模型。
//
// 与 `modelNotFoundErr`（"PATCH/DELETE 打到不存在的条目"）**同一个码**（40401），但文案分开：
// 这里的处境是"这条会话声明的模型已不在清单里"，出路是**重新选一个**（或先把模型声明回来），
// 而不是"你打错了 id"。码相同是刻意的（E9）——新增一个码要动 `pkg/code`↔README↔前端三条边。
func sessionModelNotFoundErr(modelID string) error {
	return acl.NewError(acl.ClassRequest, "model_not_found",
		fmt.Sprintf("会话声明的模型 %q 不在平台清单里（或它还不是平台模型 id）："+
			"请到「设置 · 模型」重新选一个——平台只接受自己声明过的模型", modelID), nil)
}

// modelRespFromRow 把一行模型条目投影成对外表示（**不含任何 runtime 维度**）。
//
// v13 起是**纯列投影，不可能失败**：没有 JSON 可比坏，于是"一行坏 body 打不开整页"
// （设计 P2 / 现象：整页 500）在结构上消失了。
func modelRespFromRow(row *model.ModelEntity) dtomodel.ModelResp {
	out := dtomodel.ModelResp{
		ID: row.ID, ProviderID: row.ProviderID, Model: row.Model,
		Name: row.Name, Note: row.Note,
		InputModalities: resource.ModalitiesFromColumn(row.InputModalities),
		// 容量原样透出（nil = null = 未声明）：界面据此如实标"容量由环境兜底（不是平台声明）"。
		MaxInputTokens: row.MaxInputTokens, MaxOutputTokens: row.MaxOutputTokens,
		Revision: row.Revision,
	}
	if !row.UpdatedAt.IsZero() {
		out.UpdatedAt = model.FormatTime(row.UpdatedAt)
	}
	return out
}

// ModelRow 读一条并投影（`found=false` = 不在册；错误只有载体读失败）。
//
// 与 `ModelEffect` 同一个过渡端口（`svcmodel.Host`）：行投影随**读面**搬进 `service/svcmodel`，
// 今天它仍在这里，因为 `ListModels`/`ModelsView`/体检的 `DeclaredModels` 都走这一支投影。
//
// 形状是"查得到/查不到"而不是"查不到就报错"：错误码与文案属于**用例**（写面提交后读不回来
// 是 40401），留在 `service/svcmodel` 一处，不让同一个码在两层各铸一遍。
func (s *Service) ModelRow(ctx context.Context, id string) (*dtomodel.ModelResp, bool, error) {
	row, found, err := s.st.GetModelDefinition(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	out := modelRespFromRow(row)
	return &out, true, nil
}

// ModelEffect 计算模型清单改动的生效面回执（只读、无副作用；判据与代际裁决同源）。
//
// 导出是**过渡期的端口实现**（`svcmodel.Host`，P4a 正文第五刀）：清单写面已搬进
// `service/svcmodel`，而"哪些会话会顺延"要问代际裁决（与连接/会话编排绑在一起，P5 才 dissolve）。
// 搬过去的那一天，这个方法连同 `svcmodel.Host` 一起消失。
func (s *Service) ModelEffect(defaultTouched bool) *dtomodel.ModelEffect {
	eff := &dtomodel.ModelEffect{Effect: "next_session"}
	o := s.orch
	// **快照读不持 `s.mu`**（P2b 前置）：`o` 是装配期回挂的引用，循环体只读快照与它；
	// 持锁请求会话快照会把"manager 锁 → session 锁"这条边固化下来（见
	// `manager_lock_order_test.go` 的说明：workspace 状态进域平面后就是锁序反转，表现是挂死）。
	recs := s.sessionPlane.Snapshot()
	for i := range recs {
		eff.AffectedSessions++
		if o == nil {
			continue
		}
		as := o.session(recs[i].ID)
		if as == nil {
			continue
		}
		if plan := o.planGenerationSwitch(as, &recs[i]); plan.Action == genActionDefer {
			eff.Deferred++
		}
	}
	if defaultTouched {
		eff.Note = "已保存：新会话创建时由平台下发这个默认模型（立即）；既有会话在下一个会话进程读到配置。**不是「已生效」**"
	} else {
		eff.Note = "已保存，将在下一个会话进程生效（配置在进程启动时读）；此刻底座的模型目录仍是旧的那份"
	}
	return eff
}

// ——— 派生只读：`GET /agents` 的 `default_model` ———
