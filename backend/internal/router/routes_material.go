package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrmaterial"
)

// registerMaterial 资料库面（v33）：
//   - GET    /materials/nodes            列目录（?parent=&limit=&offset=，一层子节点）
//   - GET    /materials/nodes/:id        节点详情（元数据 + 按 kind 裁剪的载体）
//   - POST   /materials/nodes            建节点（folder/doc/table/link/page；drive 走"先 /files 再 drive_upload_id"）
//   - PATCH  /materials/nodes/:id        改名 / 移动 / 改正文
//   - DELETE /materials/nodes/:id        删节点（folder 级联删子树；幂等）
//   - GET    /materials/search           关键词检索（**非语义**；?q=&limit=&offset=）
//
// **上传已收口**（产物溯源 P0′）：原 `POST /materials/upload` 删除，上传文件成 drive 节点改为两步式
// ——先 `POST /files`（ginupload 收字节）拿 file_id，再 `POST /materials/nodes {node_kind:drive, drive_upload_id}`。
//
// 路径前缀用 `/materials` 而非 `/library`：后者是能力清单（`GET /library`，svclibrary）的读模型，
// 两者是不同的东西（词汇收口见 docs/agents/vocabulary.md）。
func registerMaterial(v1 *gin.RouterGroup, ctr ctrmaterial.MaterialCtr) {
	v1.GET("/materials/nodes", ctr.ListNodes)
	v1.POST("/materials/nodes", ctr.CreateNode)
	v1.GET("/materials/nodes/:id", ctr.GetNode)
	v1.PATCH("/materials/nodes/:id", ctr.PatchNode)
	v1.DELETE("/materials/nodes/:id", ctr.DeleteNode)
	v1.GET("/materials/search", ctr.Search)
	// P3 富节点：table 行 CRUD + page 发布/查看。
	v1.GET("/materials/nodes/:id/rows", ctr.GetRows)
	v1.PUT("/materials/nodes/:id/rows", ctr.PutRows)
	v1.POST("/materials/nodes/:id/publish", ctr.Publish)
	v1.GET("/materials/nodes/:id/view", ctr.View)
}
