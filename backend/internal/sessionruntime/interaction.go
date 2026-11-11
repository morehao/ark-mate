package sessionruntime

// ——— 会话交互面的**剩余形状**（P4a 正文第四十一刀之后）———
//
// 这一族共七条，都已归 `service/svcsession`，本文件只剩两样**适配器**：
//
//	① 三个事实出口（`PendingPermissions` / `PermissionMemories` / `PendingElicitations`）：
//	   `svcsession.InteractionSource` 的实现（那两份状态归执行面持有，用例层只认这三条只读事实）；
//	② 三个投递口（`ResolvePermission` / `ResolveElicitation` / `RevokeMemory`）：
//	   `svcsession.InteractionDelivery` 的实现（把决定投给底座 = 投递面，第四十一刀）。
//
// 两者的签名只吃**域值对象**与基础类型，所以适配器能留在这里——本包不许 import `service/svc*`
// （层守卫 `TestOnlyControllersImportServicePackages` 钉着），端口一旦用 svc 层自己的值类型，
// 适配器就没有落点了。旧的那批 DTO 版方法（`GetSession` 等四条 + 这三条）已随第四十/四十一刀删除。

// ——— 会话交互面的**剩余形状**（P4a 正文第四十刀之后）———
//
// 只读四条（`GetSession` / `ListPendingPermissions` / `ListPermissionMemories` /
// `ListPendingElicitations`）已收进 `service/svcsession`（`DetailSvc` + `InteractionSource`），
// 连同两个投影 `toPermissionResp` / `toElicitationResp` 一起搬走——投影只该有一份。
//
// 本文件此后只剩两样：
//
//	① 三个**事实出口**（`PendingPermissions` / `PermissionMemories` / `PendingElicitations`）：
//	   它们是 `svcsession.InteractionSource` 的实现（那两份状态归执行面持有，用例层只认这三条窄端口）；
//	② 三条**动作面**（`ResolvePermission` / `ResolveElicitation` / `RevokeMemory`）：它们要走
//	   `Orchestrator` 把决策**投递给底座**，属于编排，随 `Orchestrator` 在 P5 一起收口
//	   （§3.1.58 的结论：编排既不是执行面、也不是「读面」）。

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/protocol"
	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
)

// ResolvePermission 投递一次权限决策（`svcsession.InteractionDelivery` 的实现）。
//
// 名字与端口方法**同名**是刻意的：`Service` 上原来那个 DTO 版方法（`SessionSvc` 的声明）已随
// P4a 正文第四十一刀删除，名字腾出来了；同名让"谁实现谁"一眼对上。
//
// 为什么适配器留在 `manager`：端口三个签名只吃**域值对象**与基础类型，所以它能在不认识用例层的
// 地方实现（层守卫不许 manager import svc）。`orch == nil` 的那个码留在适配器里——它描述的是
// "这个进程没装编排器"这个**装配事实**，不是用例层的判据；真正来自域的 `ok=false`（挂起项不存在/
// 已过期/不属于该会话）才由用例层翻译成 `permission_not_found`。
func (s *Service) ResolvePermission(sessionID, callID, optionID string, always, allowed bool) (interaction.DecisionOutcome, bool, error) {
	if s.orch == nil {
		return interaction.DecisionOutcome{}, false, failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil)
	}
	out, ok, err := s.orch.ResolvePermission(sessionID, callID, optionID, always, allowed)
	if err != nil {
		return interaction.DecisionOutcome{}, false, err
	}
	// 只有 `Kind`/`Selected` 是执行面的事实；`Allowed`/`Rejected`/`Always`/`OptionID` 是请求参数
	// 的回带，用例层自己就有，不必在这里过一遍（少一处可以传错的地方）。
	return interaction.DecisionOutcome{Kind: out.Kind, OptionID: out.Selected}, ok, nil
}

// PendingPermissions 直接透传编排器的挂起权限（供 GetSession/ListPendingPermissions 复用）。
func (s *Service) PendingPermissions(id string) []interaction.PermissionItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingPermissions(id)
}

// PendingElicitations 直接透传编排器的挂起结构化提问（供 ListPendingElicitations 复用）。
func (s *Service) PendingElicitations(id string) []interaction.ElicitationItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PendingElicitations(id)
}

// ResolveElicitation 投递一次结构化提问决策（`svcsession.InteractionDelivery` 的实现）。
func (s *Service) ResolveElicitation(sessionID, elicitationID, action string, content map[string]any, late bool) (bool, error) {
	if s.orch == nil {
		return false, failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil)
	}
	return s.orch.ResolveElicitation(sessionID, elicitationID, action, content, late)
}

// AskElicitation 发起一次外部 source 结构化提问并阻塞等用户决策（`svcsession.InteractionDelivery`
// 的实现；answerer 桥接入口）。
//
// 归属解析（native→platform）：优先用调用方给的平台会话 id；为空（answerer 插件占位 `-`）时用
// `runtimeSessionID`（底座原生 id）经 `platformSessionIDForProvider` 反查——那里是唯一允许做
// `rt_` 壳包装的边界函数（§4.5.3 / id_shape 守卫白名单），所以解析留在这里，不在用例层。
func (s *Service) AskElicitation(ctx context.Context, sessionID, runtimeSessionID string, req protocol.ElicitationRequest) (protocol.ElicitationOutcome, error) {
	if s.orch == nil {
		return protocol.ElicitationOutcome{}, failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil)
	}
	sid := sessionID
	if sid == "" {
		sid = s.orch.platformSessionIDForProvider(runtimeSessionID)
		if sid == "" {
			return protocol.ElicitationOutcome{}, failure.NewError(failure.ClassRequest,
				"elicitation_scope_required", "外部结构化提问无法归属会话（native id 未匹配）", nil)
		}
	}
	return s.orch.AskElicitation(ctx, sid, req)
}

// PermissionMemories 直接透传编排器的会话级权限记忆（供 GetSession 复用；REST 见 ListPermissionMemories）。
func (s *Service) PermissionMemories(id string) []interaction.MemoryItem {
	if s.orch == nil {
		return nil
	}
	return s.orch.PermissionMemories(id)
}

// RevokeMemory 撤销会话级权限记忆（`svcsession.InteractionDelivery` 的实现；title+kind 均空 = 全部）。
func (s *Service) RevokeMemory(sessionID, title, kind string) (bool, error) {
	if s.orch == nil {
		return false, failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil)
	}
	return s.orch.RevokeMemory(sessionID, title, kind)
}
