package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrcommandpolicy"
)

// registerCommandPolicy 命令门禁（A2）读写面。
//
// 路径与"立即生效"回调由装配层交来：前者是配置事实（`MATE_COMMAND_POLICY`），后者归执行面
// （`manager.Orchestrator`）。控制器在自己的构造函数里造用例实例（`app`/`router` 都不 import svc）。
func registerCommandPolicy(v1 *gin.RouterGroup, ctr ctrcommandpolicy.CommandPolicyCtr) {
	v1.GET("/command-policy", ctr.Get)
	v1.PUT("/command-policy", ctr.Put)
}
