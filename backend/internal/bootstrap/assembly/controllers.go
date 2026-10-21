package assembly

import (
	"github.com/morehao/ark-mate/internal/core/credential"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrautomation"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrcapability"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrcommandpolicy"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrexport"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrlibrary"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrmodel"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrruntime"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrsearch"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrsession"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrsystem"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrusage"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrworkspace"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrws"
	"github.com/morehao/ark-mate/internal/delivery/middleware"
	"github.com/morehao/ark-mate/internal/delivery/router"
	"github.com/morehao/ark-mate/internal/domain/workspace"
)

// buildControllers 构造全部控制器（`startControllers` 的第二步）。
//
// 每条构造只传**已构造的用例**与交付层设施（凭据 / 握手来源判定 / 宿主事实）；
// 控制器内部不再 `New` 任何 `svc*`，也不接收 store/domain 平面。
func (as *assembly) buildControllers() router.Controllers {
	return as.services.BuildControllers(as.creds, as.origin, func() bool {
		// 宿主能不能抬起系统目录面板：判据在领域里（绑回环 + 非 SSH + 有显示会话）。
		return as.planes.Picker.NativePickerMode() == string(workspace.NativePickerHost)
	})
}

// BuildControllers 构造控制器集合（装配根与路由级测试共用同一套）。
//
// `origin` 可以为 nil（未启用来源矩阵）；`hostFacts` 为 nil 时能力声明按"宿主不支持"处理。
func (s Services) BuildControllers(creds *credential.Set, origin *middleware.OriginPolicy, hostFacts func() bool) router.Controllers {
	if hostFacts == nil {
		hostFacts = func() bool { return false }
	}
	return router.Controllers{
		Session: ctrsession.NewSessionCtr(s.Session, s.SessionHistory, s.SessionDetail,
			s.SessionInteraction, s.SessionQueue, s.SessionMeta, s.SessionRead),
		Search:        ctrsearch.NewSearchCtr(s.Search),
		Export:        ctrexport.NewExportCtr(s.Export),
		Workspace:     ctrworkspace.NewWorkspaceCtr(s.Workspace),
		CommandPolicy: ctrcommandpolicy.NewCommandPolicyCtr(s.CommandPolicy),
		Runtime:       ctrruntime.NewRuntimeCtr(s.Runtime),
		Model:         ctrmodel.NewModelCtr(s.Model, s.ModelDeclared),
		Provider:      ctrmodel.NewProviderCtr(s.Provider, s.ProviderProbe),
		Capability:    ctrcapability.NewCapabilityCtr(s.Capability),
		Automation:    ctrautomation.NewAutomationCtr(s.Automation),
		Library:       ctrlibrary.NewLibraryCtr(s.Library),
		Usage:         ctrusage.NewUsageCtr(s.Usage),
		System:        ctrsystem.NewSystemCtr(s.Meta, s.Capabilities, hostFacts),
		WS: ctrws.NewWSCtr(s.Stream, ctrws.Options{
			Credentials: creds,
			OriginCheck: origin.CheckFunc(),
		}),
	}
}
