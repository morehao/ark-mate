package manager

import (
	"github.com/morehao/ark-forge/internal/service/svcagent"
	"github.com/morehao/ark-forge/internal/service/svccapability"
	"github.com/morehao/ark-forge/internal/service/svcconversation"
	"github.com/morehao/ark-forge/internal/service/svcexport"
	"github.com/morehao/ark-forge/internal/service/svcruntime"
	"github.com/morehao/ark-forge/internal/service/svcsearch"
	"github.com/morehao/ark-forge/internal/service/svcusage"
	"github.com/morehao/ark-forge/internal/service/svcworkspace"
)

// 编译期断言：Service 必须满足控制面声明的全部领域端口。
//
// 为什么要有这几行：端口定义（internal/service/svc*）与实现（本包）分处两个包，
// 而 Go 是结构化满足——装配点若漏传某个端口，错误会推迟到 cmd 层才暴露，报错也只有
// "missing method"。把断言放在实现侧，端口一变形立刻在本包编译失败，定位成本最低。
//
// P4 把 Service 拆成多个领域实现后，这些断言随之改为各实现分别断言自己的端口。
var (
	_ svcagent.AgentSvc               = (*Service)(nil)
	_ svcconversation.ConversationSvc = (*Service)(nil)
	_ svcworkspace.WorkspaceSvc       = (*Service)(nil)
	_ svcruntime.RuntimeSvc           = (*Service)(nil)
	_ svccapability.CapabilitySvc     = (*Service)(nil)
	_ svcusage.UsageSvc               = (*Service)(nil)
	_ svcsearch.SearchSvc             = (*Service)(nil)
	_ svcexport.ExportSvc             = (*Service)(nil)
)
