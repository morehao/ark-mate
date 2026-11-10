package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrapp"
)

// registerApps 应用定义面（一期 A+Slot）：REST 六接口，与 `/experts` 同构（设计 §5.8）。
//
//	GET    /apps            列表（含禁用；has_page 派生；不含 settings_schema 正文）
//	POST   /apps            新建（id 已存在 → 40901 app_exists；校验/白名单 → 40001 app_invalid）
//	GET    /apps/:id        详情（含 settings_schema 全量；不存在 → 40401 app_not_found）
//	PUT    /apps/:id        增量改（只改出现键；CAS revision 不中 → 40901 definition_changed）
//	DELETE /apps/:id        删除（幂等；成员按设计 §5.2 解除归属）
//	PATCH  /apps/:id        启停（40401 app_not_found）
//
// 二期 C 生命周期（WP-C6）：
//
//	POST   /apps/install    从本地来源目录装包（body {dir, enable}；app_invalid / app_runtime_unavailable）
//	DELETE /apps/:id/uninstall  卸载（先停运行期+清包目录再删定义；幂等）
func registerApps(v1 *gin.RouterGroup, ctr ctrapp.AppCtr) {
	v1.GET("/apps", ctr.ListApps)
	v1.POST("/apps", ctr.CreateApp)
	v1.GET("/apps/:id", ctr.GetApp)
	v1.PUT("/apps/:id", ctr.UpdateApp)
	v1.DELETE("/apps/:id", ctr.DeleteApp)
	v1.PATCH("/apps/:id", ctr.SetAppEnabled)
	// 生命周期：install 用静态子路径（POST 树无 :id，避免与 GET/PUT 的 :id 冲突）；
	// uninstall 挂在 :id 下的静态子段。
	v1.POST("/apps/install", ctr.InstallApp)
	v1.DELETE("/apps/:id/uninstall", ctr.UninstallApp)
}
