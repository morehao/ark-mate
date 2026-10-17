package sessionruntime

import (
	"github.com/morehao/ark-mate/internal/service/svcsession"
)

// detailSvc 按**装配点的形状**造一次详情读面用例（P4a 正文第四十刀）。
//
// 与 `listLibrary` / `toolCatalog` / `mcpCatalog` 同一手法：用例实例由装配点（`ctrsession` 的
// 构造函数）造，而 manager 的这一批用例要验的是「**装配起来的那份 Service + 真用例**」这条链路，
// 所以中间这一步如实复现，而不是让测试自己另造一份依赖组合（那样验的就不是同一件事了）。
//
// `svc` 同时是那三份事实的提供者——它实现 `svcsession.InteractionSource`
// （`PendingPermissions` / `PermissionMemories` / `PendingElicitations`，那两份状态归执行面持有）。
func detailSvc(svc *Service) svcsession.DetailSvc {
	return svcsession.NewDetail(svc.sessionPlane, svc)
}

// interactionSvc 同理造一次**动作面**用例（P4a 正文第四十一刀）。
//
// `svc` 同时是两个窄端口的提供者：只读三条（`InteractionSource`）与投递三条
// （`InteractionDelivery`）——它在 `manager` 侧就是这两族适配器的落点。
func interactionSvc(svc *Service) svcsession.InteractionSvc {
	return svcsession.NewInteraction(svc.sessionPlane, svc)
}

// queueSvc 同理造一次**排队输入**用例（P4a 正文第四十二刀；ADR-10 起多一个投递窄端口）。
// 依赖事实两个：会话域平面（队列状态与事件出口）+ `svc` 自己（`DeliverQueuedNow`）。
func queueSvc(svc *Service) svcsession.QueueSvc {
	return svcsession.NewQueue(svc.sessionPlane, svc)
}

// metaSvc 同理造一次**元数据写面**用例（P4a 正文第四十四刀）：域平面 + 三方法窄端口。
// `svc` 既是平面的事实来源，也是那三份依赖的提供者（`MetaDeps`）。
func metaSvc(svc *Service) svcsession.MetaSvc {
	return svcsession.NewMeta(svc.sessionPlane, svc)
}
