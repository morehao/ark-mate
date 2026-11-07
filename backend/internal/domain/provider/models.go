package provider

import (
	"context"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"strings"

	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/object/objtime"

	"github.com/morehao/ark-mate/internal/core/resource"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 平台模型清单的**读面与解析链**（P4a 正文第五十九刀：从 `internal/manager/model_list.go` 搬入）———
//
// 这一族回答「平台声明了哪些模型、某条会话该用哪一行、这一行折成该底座认的什么值」。它与
// 供应商档案同属一个域（`models.provider_id` 是外键式的挂靠），所以落在这里而不是用例层：
//
//   - 用例层（`service/svcmodel`）只做请求校验、错误码出口与响应装配；
//   - 解析链（`ResolveSessionModel`）同时被会话用例（`svcsession.MetaDeps.ResolveModel`）、
//     模型目录（`model_catalog.go`）与体检（`provider_model_facts.go`）消费——**只有一份实现**，
//     放在域里才不会出现"某一条路径自己解释那个串"。
//
// 清单需要 sqlite 持久层：memory 模式没有 `models` 表，读面回空列表、写面在用例层报 40501。

// ListModels 列出全部平台清单（**不分页**：清单上限 200 条，全量返回）。
func (pl *Plane) ListModels(ctx context.Context) ([]ModelItem, error) {
	// memory 模式没有清单载体：读面回空列表（写面在 `service/svcmodel`，那里报 40501）。
	if pl.st == nil {
		return []ModelItem{}, nil
	}
	rows, err := pl.st.ListModelDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	// **不再在 Go 侧排序**：顺序由 SQL 的 `ORDER BY provider_id, model_id` 给出（ADR-6）。
	// 这里再排一次会让"库里的顺序"与"对外顺序"变成两份可能分叉的事实。
	out := make([]ModelItem, 0, len(rows))
	for i := range rows {
		out = append(out, modelRespFromRow(&rows[i]))
	}
	return out, nil
}

// ModelRow 读一条并投影（`found=false` = 不在册；错误只有载体读失败）。
//
// 形状是"查得到/查不到"而不是"查不到就报错"：错误码与文案属于**用例**（写面提交后读不回来
// 是 40401），留在 `service/svcmodel` 一处，不让同一个码在两层各铸一遍。
func (pl *Plane) ModelRow(ctx context.Context, id string) (*ModelItem, bool, error) {
	if pl.st == nil {
		return nil, false, nil
	}
	row, found, err := pl.st.GetModelDefinition(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	out := modelRespFromRow(row)
	return &out, true, nil
}

// DefaultModelRow 读出默认模型那一行（未设 / 指向已删模型 → (nil,false,nil)）。
func (pl *Plane) DefaultModelRow(ctx context.Context) (*model.ModelEntity, bool, error) {
	if pl.st == nil {
		return nil, false, nil
	}
	id, ok, err := pl.st.DefaultModelID(ctx)
	if err != nil || !ok {
		return nil, false, err
	}
	row, found, err := pl.st.GetModelDefinition(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	return row, true, nil
}

// ModelValueFor 平台清单 → 运行时认得的不透明值（**下发与物化必须共用它**）。
//
// 生成规则**不属于内核**：它挂在注册表的第四张表上（`runtime.ModelValueAdapter`）——dsh 是
// `["<provider>","<model>"]` 这个 JSON 数组，别的底座可能是 `<provider>/<model>` 这一串。
// 内核只做分发，一个内建底座名都不认识。
//
// `ok=false`（该 id 没有登记实现）= 这个底座不消费平台的模型清单，**不是失败**；调用方按语义
// 选"回空串（不下发）"或"跳过这条"，所以这里返回的是可判别的错误而不是一个空值。
func ModelValueFor(reg *runtime.Registry, runtimeID, providerID, model string) (string, error) {
	a, ok := reg.ModelValue(runtimeID)
	if !ok {
		return "", failure.NewError(failure.ClassUnsupported, "model_value_unsupported",
			fmt.Sprintf("runtime %q 的模型值形态未声明：平台不为未知底座猜模型值的形状", runtimeID), nil)
	}
	return a.Value(providerID, model)
}

// ModelValueFor 见包级 `ModelValueFor`（用本平面持有的注册表）。
func (pl *Plane) ModelValueFor(runtimeID, providerID, model string) (string, error) {
	return ModelValueFor(pl.reg, runtimeID, providerID, model)
}

// ModelValueForRow 把一条**平台模型行**折成 (行 id, 运行时配置)。
//
// 独立成方法而不是塞进 `ResolveSessionModel` 的两个分支里：两条来源（显式 id / 默认行）必须走
// **同一段**翻译，否则"显式选的那条"与"默认那条"的运行时值会各长一套。
func (pl *Plane) ModelValueForRow(runtimeID, rowID, providerID, model string) (string, runtime.ModelConfig, error) {
	v, err := pl.ModelValueFor(runtimeID, providerID, model)
	if err != nil {
		return "", runtime.ModelConfig{}, err
	}
	// `Provider` 是**留痕语义**（仅展示/诊断，不进任何契约）。
	return rowID, runtime.ModelConfig{Provider: providerID, Model: v}, nil
}

// ResolveSessionModel 把"会话请求里的**平台模型行 id**"解析成 (平台行 id, 运行时模型配置)。
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
// ——它**不落库、不过 wire**（这正是「辰」的核心：库里只留身份）。
//
// 纪律：解析失败一律**上抛**，绝不静默回落到底座默认、也绝不静默换成别的模型。
func (pl *Plane) ResolveSessionModel(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error) {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" {
		row, ok, err := pl.DefaultModelRow(ctx)
		if err != nil || !ok {
			// 读不出来 = 无法校验：不下发模型（不是把上一次的选择偷偷用上）。
			return "", runtime.ModelConfig{}, err
		}
		id, cfg, verr := pl.ModelValueForRow(runtimeID, row.ID, row.ProviderID, row.Model)
		if verr != nil {
			if ae, ok := failure.As(verr); ok && ae.Code == "model_value_unsupported" {
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
		return "", runtime.ModelConfig{}, SessionModelNotFoundErr(trimmed)
	}
	row, found, err := pl.modelRowByID(ctx, trimmed)
	if err != nil {
		return "", runtime.ModelConfig{}, err
	}
	if !found {
		return "", runtime.ModelConfig{}, SessionModelNotFoundErr(trimmed)
	}
	return pl.ModelValueForRow(runtimeID, row.ID, row.ProviderID, row.Model)
}

// modelRowByID 按平台行 id 读一行（memory 模式没有清单载体 → 不在册）。
func (pl *Plane) modelRowByID(ctx context.Context, id string) (*model.ModelEntity, bool, error) {
	if pl.st == nil {
		return nil, false, nil
	}
	return pl.st.GetModelDefinition(ctx, id)
}

// DefaultModelValueFor 返回某 runtime 当前默认模型的**不透明值**（无默认 → 空串）。
//
// v12 起默认是**单一值**（`meta.default_model`），所以这里没有"某 runtime 的默认"这回事——只是把
// 同一个平台模型 id 按该 runtime 的生成规则折成它认得不透明值。委托给 `ResolveSessionModel`
// 而不是自己再走一遍：两份实现会在"某个底座不声明模型值形态"这类边界上分叉。
func (pl *Plane) DefaultModelValueFor(ctx context.Context, runtimeID string) (string, error) {
	_, cfg, err := pl.ResolveSessionModel(ctx, runtimeID, "")
	return cfg.Model, err
}

// SessionModelNotFoundErr 会话引用了平台清单里查不到的模型。
//
// 与 `modelNotFoundErr`（"PATCH/DELETE 打到不存在的条目"）**同一个码**（40401），但文案分开：
// 这里的处境是"这条会话声明的模型已不在清单里"，出路是**重新选一个**（或先把模型声明回来），
// 而不是"你打错了 id"。码相同是刻意的（E9）。
func SessionModelNotFoundErr(modelID string) error {
	return failure.NewError(failure.ClassRequest, "model_not_found",
		fmt.Sprintf("会话声明的模型 %q 不在平台清单里（或它还不是平台模型 id）："+
			"请到「设置 · 模型」重新选一个——平台只接受自己声明过的模型", modelID), nil)
}

// modelRespFromRow 把一行模型条目投影成对外表示（**不含任何 runtime 维度**）。
//
// v13 起是**纯列投影，不可能失败**：没有 JSON 可比坏，于是"一行坏 body 打不开整页"在结构上消失了。
func modelRespFromRow(row *model.ModelEntity) ModelItem {
	out := ModelItem{
		ID: row.ID, ProviderID: row.ProviderID, Model: row.Model,
		Name: row.Name, Note: row.Note,
		InputModalities: resource.ModalitiesFromColumn(row.InputModalities),
		// 容量原样透出（nil = null = 未声明）：界面据此如实标"容量由环境兜底（不是平台声明）"。
		MaxInputTokens: row.MaxInputTokens, MaxOutputTokens: row.MaxOutputTokens,
		// 思考能力（v39）纯列投影：档位串折成规范序数组透出。
		SupportsReasoning: row.SupportsReasoning,
		ReasoningEfforts:  resource.ReasoningEffortsFromColumn(row.ReasoningEfforts),
		DefaultEffort:     row.DefaultEffort,
		Revision:          row.Revision,
	}
	if !row.UpdatedAt.IsZero() {
		out.UpdatedAt = objtime.Milli(row.UpdatedAt)
	}
	return out
}
