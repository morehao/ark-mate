package manager

import (
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/pkg/acl"
)

// ——— P3a-2：运行期诊断的过渡期转发与端口适配 ———
//
// 诊断的**状态**（最近失败 / 归属歧义留痕）与**组装面**（快照 + "怎么修"）已落
// `internal/runtimeconn`（P3a-2 第一/二刀）。组装要读两样不属于执行面的东西，走两个窄端口：
//
//	CredentialSource  —— 「将被注入的模型凭据够不够」：供应商域的事实；
//	LiveClientSource  —— 「此刻哪条连接活着」：连接池的状态。P4a 正文第五十四刀起连接池
//	   自己就是这份读面的实现（`Pool.LiveClient`），本文件不再有它的适配器。
//
// 本文件只剩：三条一行转发 + 凭据端口的实现（实现放在这里而不是 `Service` 上分散的方法里，
// 是为了让"执行面要哪几个事实"一眼可见）。

// TODO(P4b): 删转发（调用点直接依赖 runtimeconn.Plane）
func (s *Service) recordRuntimeError(sessionID string, ae *acl.Error, tail []string) {
	s.runtimePlane.RecordRuntimeError(sessionID, ae, tail)
}

// TODO(P4b): 删转发（同上）
func (s *Service) noteScopeAmbiguous(detail string) { s.runtimePlane.NoteScopeAmbiguous(detail) }

// TODO(P4b): 删转发（同上）
func (s *Service) runtimeLastError() (runtimeconn.LastErr, bool) { return s.runtimePlane.LastError() }

// ——— 端口的实现（`runtimeconn` 的读面） ———

// AuthGuidance 实现 `runtimeconn.CredentialSource`。
func (s *Service) AuthGuidance() []dtomodel.AuthGuidanceResp { return s.authGuidanceFor() }
