package manager

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
