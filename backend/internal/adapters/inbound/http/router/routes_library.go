package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/adapters/inbound/http/controller/ctrlibrary"
)

// registerLibrary M2-4 Library 统一读模型。
//
// 单一 GET：能力清单是**派生视图**，没有写面（写面在各领域自己的端点上：/skills、/mcps…）。
// 面板与输入卡共用这一份读模型，避免"面板显示可用、输入卡里选不到"的两套视图漂移。
func registerLibrary(v1 *gin.RouterGroup, ctr ctrlibrary.LibraryCtr) {
	v1.GET("/library", ctr.ListLibrary)
}
