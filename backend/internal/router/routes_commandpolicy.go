package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrcommandpolicy"
)

// registerCommandPolicy 命令门禁（A2）读写面。
//
// 路径与"立即生效"回调由装配层交来：前者是配置事实（`MATE_COMMAND_POLICY`），后者归执行面
// （`sessionruntime.Orchestrator.SetCommandPolicy`，由 `assembly/services.go` 接成
// `svccommandpolicy.New` 的 `apply` 回调）。控制器只收**已构造的用例**（`NewCommandPolicyCtr(svc)`），
// 实例由装配根造——`app`/`router` 都不 import `service/svc*`。
func registerCommandPolicy(v1 *gin.RouterGroup, ctr ctrcommandpolicy.CommandPolicyCtr) {
	v1.GET("/command-policy", ctr.Get)
	v1.PUT("/command-policy", ctr.Put)
}
