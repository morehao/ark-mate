package manager

import (
	"github.com/morehao/ark-mate/internal/service/svccapability"
	"github.com/morehao/ark-mate/internal/service/svclibrary"
	"github.com/morehao/ark-mate/internal/service/svcmodel"
	"github.com/morehao/ark-mate/internal/service/svcprovider"
	"github.com/morehao/ark-mate/internal/service/svcsession"
	"github.com/morehao/ark-mate/internal/service/svcworkspace"
)

// 编译期断言：实现方必须满足控制面声明的全部领域端口。
//
// 为什么要有这几行：端口定义（internal/service/svc*）与实现（本包）分处两个包，
// 而 Go 是结构化满足——装配点若漏传某个端口，错误会推迟到 cmd 层才暴露，报错也只有
// "missing method"。把断言放在实现侧，端口一变形立刻在本包编译失败，定位成本最低。
//
// **P4a 正文第二刀**（2026-10-16）：`svcruntime.RuntimeSvc` 的断言也已删掉——底座面
// （身份 + 诊断）的实现搬进了 `internal/service/svcruntime`。**断言跟着实现走**，两条都是。
//
// **P4a 正文第一刀**（2026-10-16）：`svcsystem.SystemSvc` 的断言**已从这里删掉**——
// 导出的用例实现搬进了 `internal/service/svcsystem`（真实现：校验/错误码出口/渲染），
// `*Service` 只实现它的 `Source` 端口（`export_source.go`）。断言随之移到用例层：
// `var _ SystemSvc = (*Exporter)(nil)`。**这正是 P4a 的形状**：端口断言跟着实现走。
//
// **P4a 第一批**（2026-10-16）：包集按 ADR-2 收口——`svcruntime` 拆出 `svcprovider` 与
// `svcmodel`（一个控制器只用到一半的两件事不该共用一个接口），三个单方法面
// `svcsearch`/`svcusage` 并入 `svcsession`、`svcexport` 并入新的 `svcsystem`。
// 于是断言从 8 条变成 9 条：**拆出来的包各自断言自己那片**，一个控制器漏实现一眼可见。
//
// **现状如实写**：下面 9 条断言如今**全部**打在同一个 `*Service` 上——P4 那次"拆成多个领域实现"
// 并没有发生，`*Service` 仍是单结构体。按域拆包与按域拆结构是两件事，后者是 P4a 的正文
// （把实现上提到各自的 `svc<领域>`）；真拆的那天，这些断言随之改为各实现分别断言自己的端口。
var (
	_ svcsession.SessionSvc       = (*Service)(nil)
	_ svcworkspace.WorkspaceSvc   = (*Service)(nil)
	_ svcprovider.ProviderSvc     = (*Service)(nil)
	_ svcmodel.ModelSvc           = (*Service)(nil)
	_ svccapability.CapabilitySvc = (*Service)(nil)
	_ svclibrary.LibrarySvc       = (*Service)(nil)
)
