package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrartifact"
)

// registerArtifact 产物面（v34）：
//   - POST /artifacts              登记一条产物（JSON：file_id 或 content_text；只建索引，不收流）
//   - GET  /artifacts/:id          产物详情（元数据 + 溯源）
//   - GET  /sessions/:id/artifacts 列某会话的全部产物
//
// 字节统一经 `POST /files`（ginupload）已落 filestore；本面只承载溯源（哪会话/哪轮/哪个工具）。
func registerArtifact(v1 *gin.RouterGroup, ctr ctrartifact.ArtifactCtr) {
	v1.POST("/artifacts", ctr.Register)
	v1.GET("/artifacts/:id", ctr.Get)
	v1.GET("/sessions/:id/artifacts", ctr.ListBySession)
}
