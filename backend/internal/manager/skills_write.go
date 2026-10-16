package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// ——— 技能定义写面（P4b 之前的转发层） ———
//
// 写面主体（顺序与失败处理、校验闸门、载体 CAS、内存入表、物化重校）已落
// `domain/skill.Plane` 的 `WriteSkill` / `DeleteSkill`——域拥有"技能定义怎么生效"这条规则；
// manager 侧只留一行转发，调用点（`ctrcapability` 与 manager 内的测试）全部不变。
//
// **原文处理与参数类错误的归属（本批新定）**：`rawPatch` / `rawString` / `capWriteErrParam`
// 被 skill / mcp / automation **三个域**共用，已落 `mechanism/definition`
// （`RawPatch` / `RawString` / `InvalidParamErr`）。放进任何一个域都会让另外两个域反向 import 它
// ——`domain/*` 同层零 import 由守卫钉住，`skill` 与 `mcp` 更不许互相认识。

// TODO(P4b): 删转发（调用点直接依赖 skill.Plane）
//
// CreateSkill 新建一个 skill 定义（POST /skills）：id 必填，已存在 → `skill_exists`(40901)。
func (s *Service) CreateSkill(ctx context.Context, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error) {
	return s.skillPlane.WriteSkill(ctx, "", req, true)
}

// TODO(P4b): 删转发（调用点直接依赖 skill.Plane）
//
// UpdateSkill 覆盖写一个 skill 定义（PUT /skills/:id）。
//
// 语义：请求体里**出现的键**才改（未出现的保留原值）；`null` 删键；`""`/`[]` 显式清空。
// 路径 id 与 body id 不一致 → 40000（id 不可改：改 id 等于换一个资源）。
func (s *Service) UpdateSkill(ctx context.Context, id string, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error) {
	return s.skillPlane.WriteSkill(ctx, id, req, false)
}

// TODO(P4b): 删转发（调用点直接依赖 skill.Plane）
//
// DeleteSkill 删除一个 skill 定义（DELETE /skills/:id）：删定义 + 撤回物化副本 + 摘内存条目。
//
// 幂等：定义本来就不存在 → `deleted:false` 且无错（"让它不在"不要求"它曾在"）。
func (s *Service) DeleteSkill(ctx context.Context, id string) (*dtocapability.SkillDeleteResp, error) {
	return s.skillPlane.DeleteSkill(ctx, id)
}
