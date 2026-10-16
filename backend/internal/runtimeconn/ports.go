package runtimeconn

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/pkg/acl"
)

// ——— P3a-2：执行面对外要的两个口子（装配期登记） ———
//
// 本域是**执行面**：它持连接与进程。但"诊断该给用户什么出路"要读两样**不属于**它的东西：
//
//	CredentialSource —— 「将被注入的模型凭据够不够」：那是供应商域的事实；
//	LiveClientSource —— 「此刻哪条连接活着、pid 是多少、stderr 尾巴是什么」：那是连接池的状态，
//	   而连接池本批仍留在 manager（P3a-2 第三刀才搬）。
//
// 两样都做成**端口**而不是让本域去 import manager/provider：执行面不该认识供应商档案的形态，
// 正如供应商域不该认识连接池。端口是窄的——各一到两个方法。

// CredentialSource 供应商凭据面的两个事实（诊断的"怎么修"要用）。
type CredentialSource interface {
	// CredentialProblem 「将被注入」的凭据是否悬空/缺密钥（nil = 没问题）。
	// 返回的错误里已含出路文本（原文照用，不在本域改写）。
	CredentialProblem(ctx context.Context) error
	// AuthGuidance 已连接时底座自报的鉴权方式（空 = 没声明）。
	AuthGuidance() []dtomodel.AuthGuidanceResp
}

// LiveClientSource 连接池的"此刻有活连接吗"读面。
type LiveClientSource interface {
	// LiveClient 某 runtime 当前的连接（未断开时）；pid 为子进程号（0 = 未登记）。
	LiveClient(runtimeID string) (client *acl.Client, pid int, live bool)
}

// SetCredentialSource 装配期登记凭据面。
func (pl *Plane) SetCredentialSource(c CredentialSource) { pl.cred = c }

// SetLiveClientSource 装配期登记连接池读面（编排器回挂时调——它在此之前还不存在）。
func (pl *Plane) SetLiveClientSource(l LiveClientSource) { pl.live = l }
