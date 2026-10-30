package sessionruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/glog"
)

// ——— 会话内改能力的两份依赖（`svcsession.CapabilityEditDeps` 的实现，M3-8）———
//
// 适配器留在本层（而不是用例层）的理由与 `session_meta_deps.go` 同形：
//
//   - 端口的签名只吃**域/dto 类型**（`session.CapabilitySelection` / `dtosession.CapabilityWarningResp`），
//     所以它能在不认识用例层的地方实现（层守卫不许本层 import `service/svc*`）；
//   - "专家推荐怎么展开""某个技能对这个 runtime 可不可达"是**能力定义面**的事实，只有装配侧
//     看得见（注册表 + 投影 + 传输能力），用例层只认端口。

// ExpandCapabilities 按该会话钉住的 runtime 校验并物化选定。
//
// 与创建路径共用 `resolveExpertFor`：专家只填空缺、推荐里不可达的项跳过并告警。两条额外的
// **边界提示**只在"专家真的换了"时追加（每次改技能都弹一遍，用户很快就会忽略它）：
//
//   - 带角色设定的专家：人格只在会话首轮注入一次（P1 的边界），中途换专家不会重注入；
//   - 专家推荐的模型与当前模型不同：本接口不顺手改模型（会话内改模型是 `PATCH /sessions/:id`）。
func (s *Service) ExpandCapabilities(_ context.Context, sessionID string, sel *session.CapabilitySelection) (
	*session.CapabilitySelection, []dtosession.CapabilityWarningResp, error) {
	rec := s.sessionPlane.Rec(sessionID)
	if rec == nil {
		return nil, nil, failure.NewError(failure.ClassRequest, "session_not_found",
			fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	res, err := s.resolveExpertFor(rec.RuntimeID, sel)
	if err != nil {
		return nil, nil, err
	}
	warns := append([]dtosession.CapabilityWarningResp(nil), res.warnings...)
	if res.expert == nil || res.expert.ID == "" {
		return res.selection, warns, nil
	}
	previous := ""
	if cur := session.ParseCapabilitySelection(rec.CapabilityJSON); cur != nil && cur.Expert != nil {
		previous = *cur.Expert
	}
	if previous == res.expert.ID {
		return res.selection, warns, nil // 专家没换：这次只是改技能/MCP
	}
	if strings.TrimSpace(res.expert.Instructions) != "" {
		warns = append(warns, dtosession.CapabilityWarningResp{
			Kind: "expert", ID: res.expert.ID,
			Reason: "该专家的角色设定只在该会话首轮注入一次；会话中途换专家只更换能力预设，不会重注入人格",
		})
	}
	if res.expert.Model != "" && res.expert.Model != rec.ModelID {
		warns = append(warns, dtosession.CapabilityWarningResp{
			Kind: "model", ID: res.expert.Model,
			Reason: "专家推荐的模型不会在会话内切换；要换模型请用输入区的模型选择器",
		})
	}
	return res.selection, warns, nil
}

// UpdateCapability 落库能力选定：**失败只告警、不阻断响应**（端口签名因此没有返回值）。
//
// 两个细节与 `UpdateMeta` 逐字一致：脱离请求取消（请求断了不该让这次已经生效的变更丢在半路）
// 与 3 秒上限（写库卡住不能拖住 HTTP 响应）。内存里那条记录已经改了——落库失败只意味着
// "重启后这次编辑丢了"，而不是"这次编辑没生效"。
func (s *Service) UpdateCapability(ctx context.Context, id, capabilityJSON string) {
	if s.planes.Resources.Store() == nil {
		return
	}
	ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.planes.Resources.Store().UpdateSessionMeta(ctx2, id,
		model.SessionMetaPatch{CapabilityJSON: &capabilityJSON}); err != nil {
		glog.Warnw(ctx, "store: update session capability failed", "session", id, "err", err)
	}
}
