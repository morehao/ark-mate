package assembly

import (
	"net/http"

	"github.com/morehao/ark-mate/internal/control/identitymap"
	"github.com/morehao/ark-mate/internal/controller/ctrapp"
	"github.com/morehao/ark-mate/internal/controller/ctrartifact"
	"github.com/morehao/ark-mate/internal/controller/ctrattachment"
	"github.com/morehao/ark-mate/internal/controller/ctrautomation"
	"github.com/morehao/ark-mate/internal/controller/ctrcapability"
	"github.com/morehao/ark-mate/internal/controller/ctrcontrol"
	"github.com/morehao/ark-mate/internal/controller/ctrexport"
	"github.com/morehao/ark-mate/internal/controller/ctrlibrary"
	"github.com/morehao/ark-mate/internal/controller/ctrmaterial"
	"github.com/morehao/ark-mate/internal/controller/ctrmodel"
	"github.com/morehao/ark-mate/internal/controller/ctrruntime"
	"github.com/morehao/ark-mate/internal/controller/ctrsearch"
	"github.com/morehao/ark-mate/internal/controller/ctrsession"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/controller/ctrusage"
	"github.com/morehao/ark-mate/internal/controller/ctrworkspace"
	"github.com/morehao/ark-mate/internal/controller/ctrws"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/ark-mate/internal/middleware/authn"
	"github.com/morehao/ark-mate/internal/router"
)

// buildControllers 构造全部控制器（`startControllers` 的第二步）。
//
// 每条构造只传**已构造的用例**与交付层设施（握手来源判定 / 宿主事实）；
// 控制器内部不再 `New` 任何 `svc*`，也不接收 store/domain 平面。
func (as *assembly) buildControllers() router.Controllers {
	ctrls := as.services.BuildControllers(as.origin, func() bool {
		// 宿主能不能抬起系统目录面板：判据在领域里（绑回环 + 非 SSH + 有显示会话）。
		return as.planes.Picker.NativePickerMode() == string(workspace.NativePickerHost)
	})
	// 控制面身份面（决策 v38）：装配了持久层时把 `identitymap.Service` 接成 `/control/me` 冷路径
	// 开通的 onboarder（`*identitymap.Service` 的 OnboardErr 满足 ctrcontrol.Onboarder）。
	// 无持久层（memory 模式）→ 不接开通（nil），`/control/me` 退化为纯投影。
	if as.st != nil {
		ctrls.Control = ctrcontrol.NewControlCtr(identitymap.New(as.st, nil))
	}
	// WS 会话校验 + 集中会话登记（决策 v38、设计 §C2）：仅在有验签器时接入（桌面本地态 / 测试
	// 保持 nil = 不校验不登记）。重建 WS 控制器，注入 VerifySession（从 query 取 access_token 验签
	// 得 sid）与集中会话登记表（按 sid 登记连接，SLO 按 sid 断流）。
	if as.authenticator != nil {
		ctrls.WS = ctrws.NewWSCtr(as.services.Stream, ctrws.Options{
			OriginCheck:   as.origin.CheckFunc(),
			VerifySession: as.verifyWSSession,
			Sessions:      as.sessions,
		})
	}
	return ctrls
}

// BuildControllers 构造控制器集合（装配根与路由级测试共用同一套）。
//
// `origin` 可以为 nil（未启用来源矩阵）；`hostFacts` 为 nil 时能力声明按"宿主不支持"处理。
func (s Services) BuildControllers(origin *middleware.OriginPolicy, hostFacts func() bool) router.Controllers {
	if hostFacts == nil {
		hostFacts = func() bool { return false }
	}
	return router.Controllers{
		Session: ctrsession.NewSessionCtr(s.Session, s.SessionHistory, s.SessionDetail,
			s.SessionInteraction, s.SessionQueue, s.SessionMeta, s.SessionRead, s.SessionCapability),
		Search:     ctrsearch.NewSearchCtr(s.Search),
		Export:     ctrexport.NewExportCtr(s.Export),
		Workspace:  ctrworkspace.NewWorkspaceCtr(s.Workspace),
		Runtime:    ctrruntime.NewRuntimeCtr(s.Runtime),
		Model:      ctrmodel.NewModelCtr(s.Model, s.ModelDeclared),
		Provider:   ctrmodel.NewProviderCtr(s.Provider, s.ProviderProbe),
		Capability: ctrcapability.NewCapabilityCtr(s.Capability),
		App:        ctrapp.NewAppCtr(s.App, s.Market),
		Automation: ctrautomation.NewAutomationCtr(s.Automation),
		Library:    ctrlibrary.NewLibraryCtr(s.Library),
		Usage:      ctrusage.NewUsageCtr(s.Usage),
		System:     ctrsystem.NewSystemCtr(s.Meta, s.Capabilities, hostFacts),
		WS: ctrws.NewWSCtr(s.Stream, ctrws.Options{
			OriginCheck: origin.CheckFunc(),
		}),
		Attachment: ctrattachment.NewAttachmentCtr(s.Attachment),
		Material:   ctrmaterial.NewMaterialCtr(s.Material),
		Artifact:   ctrartifact.NewArtifactCtr(s.Artifact),
		Control:    ctrcontrol.NewControlCtr(nil),
	}
}

// verifyWSSession 是 WS 握手的会话校验（决策 v38、设计 §C2）：从握手 query 取 access_token，
// 本地验签（与 REST 中间件同一份 Authenticator / 同一套 JWKS）→ 返回 ark-iam SSO 会话标识 sid。
//
// 令牌走 query 的原因：浏览器原生 WebSocket **不能自定义 Authorization 头**，只能把令牌放进 URL
// （access_token 查询参数，业界对 WS 的通行做法；本仓 WS 与 REST 同机同源，不走第三方日志）。
//
// 返回约定（对齐 ctrws.Options.VerifySession 的三元组）：
//   - 令牌合法：返回 sid（可能为空——机器令牌无 sid，则不登记、SLO 撤不到）；
//   - 缺令牌 / 令牌无效：返回 401（未认证）；
//   - 密钥集不可用：返回 503（不伪装成 401，排障能区分"服务端取不到 JWKS"）。
func (as *assembly) verifyWSSession(r *http.Request) (sid string, status int, err error) {
	raw := r.URL.Query().Get("access_token")
	if raw == "" {
		return "", http.StatusUnauthorized, code.Error(code.Unauthorized, "缺少 access_token")
	}
	id, verr := as.authenticator.Verify(r.Context(), raw)
	if verr != nil {
		if authn.IsKeySourceUnavailable(verr) {
			return "", http.StatusServiceUnavailable, code.Error(code.BadGateway, "认证密钥集暂不可用")
		}
		return "", http.StatusUnauthorized, code.Error(code.Unauthorized, "令牌无效")
	}
	return id.SessionID, 0, nil
}
