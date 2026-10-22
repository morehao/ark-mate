// Package ctrsystem 承载控制面的**系统面**：诊断（`/meta`）与能力声明（`/capabilities`）。
//
// 两者都是设计 §7.3 的"声明面"：
//   - `/meta` 回答"这份部署是什么"（核心版本、前端构建 id、资产来源、数据目录、随包载荷）；
//   - `/capabilities` 回答"这个入口有哪些能力"，**两个入口同构**（id 集合与顺序完全一致），
//     只有 `available` 的值不同——浏览器入口如实回报"无托盘 / 无原生对话框"。
//
// 能力**执行面**不在这里：那是 `__MATE_DESKTOP__`（壳的 preload）。声明与执行分工见设计 §7.3。
//
// v3.1 起本包只做两件事：读请求头（入口标识）、调 `svcsystem` 用例、回信封。
// 契约类型在 `dto/dtosystem`，投影在 `service/svcsystem`（`app`/`cli` 因此不必 import 本包）。
package ctrsystem

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// CarrierHeader 壳的反代在转发 `/v1/*` 时打上的入口标识。
//
// 为什么用请求头而不是"核心自己的启动形态"：桌面安装里浏览器入口与桌面窗口打的是**同一个核心**，
// 只有"这次请求是从哪条通道来的"才能决定该回报哪一套能力。壳是唯一会把 REST 请求带凭据转进来的
// 组件（桌面态 restToken 只在壳手里），因此这个头是可信的入口声明。
const CarrierHeader = "X-Mate-Carrier"

// CarrierDesktop 桌面载体的标识值。
const CarrierDesktop = "desktop"

// SystemCtr 系统面控制器。
type SystemCtr interface {
	GetMeta(c *gin.Context)
	GetCapabilities(c *gin.Context)
}

var _ SystemCtr = (*systemCtr)(nil)

type systemCtr struct {
	meta svcsystem.MetaSvc
	caps svcsystem.CapabilitySvc
	// hostFacts 报告"宿主能不能抬起系统目录面板"；nil = 未知（当 false，能力声明退化）。
	hostFacts func() bool
}

// NewSystemCtr 构造系统面控制器：只收**已构造的用例**与一份宿主事实查询。
func NewSystemCtr(meta svcsystem.MetaSvc, caps svcsystem.CapabilitySvc, hostFacts func() bool) SystemCtr {
	return &systemCtr{meta: meta, caps: caps, hostFacts: hostFacts}
}

// GetMeta 诊断面：这份部署的版本、资产来源与数据目录。
func (h *systemCtr) GetMeta(c *gin.Context) {
	gincontext.Success(c, h.meta.Meta())
}

// GetCapabilities 声明面：两个入口**同构**（id 集合与顺序完全一致），只有 available 随入口不同。
//
// v14.5 起多了一类：`native-dialog` 的可用性由**宿主事实**给出（`hostFacts` 由装配层注入），
// 因为那个对话框是核心进程抬的——本机浏览器也能用，远程/SSH 才不行。
func (h *systemCtr) GetCapabilities(c *gin.Context) {
	desktop := strings.EqualFold(strings.TrimSpace(c.GetHeader(CarrierHeader)), CarrierDesktop)
	facts := svcsystem.CapabilityFacts{Desktop: desktop}
	if h.hostFacts != nil {
		facts.NativeDialog = h.hostFacts()
	}
	gincontext.Success(c, h.caps.Capabilities(facts))
}
