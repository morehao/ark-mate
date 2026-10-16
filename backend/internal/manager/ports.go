package manager

// ——— 编译期断言搬去哪了（P4a 起） ———
//
// 这里曾有一组 `var _ svc<领域>.XxxSvc = (*Service)(nil)`（一度 9 条）。它们随 P4a 逐个删掉，
// 理由有两条，第二条是这一批才定下的：
//
//  1. **断言跟着实现走**。哪一片实现搬进了 `service/svc<领域>`，它的断言就跟着搬进去
//     （`svcsystem.Exporter`、`svcruntime.Runtime`、`svcprovider.Probe` 各自断言自己那片）——
//     留在这里的断言会变成"实现已经走了、断言还在替它占位"，下一个人据此以为实现仍在 manager。
//
//  2. **除控制器层外的层不许引用 `service/svc*`**（`internal/archtest/svc_layer_consumer_test.go`
//     钉着这条）。`manager` 是实现层，它 import 用例层只为了写一行断言，等于把一条本该单向的边
//     变成双向：用例层的任何改名都会牵动实现层，而"实现到底搬到哪一层了"重新变成不可见的。
//
// 断言本身没有消失，它**上移到了装配点**——`internal/app/app.go` 的 `router.Ports` 字面量：
//
//	Session: a.svc, Workspace: a.svc, … System: a.svc
//
// 那里 `a.svc` 少实现任何一个端口的方法都是当场编译失败，而且报错点就在"谁被交给了谁"那一行。
// 比留在本包更硬：本包的断言只证明"`*Service` 满足某个接口"，装配点的断言证明的是
// "**被交给路由的那个对象**满足路由要的每个端口"——后者才是真正要保的性质。
//
// 本包与端口的其余关系靠**结构满足**维持：方法签名逐字对得上（取值类型与上限入参都下沉在域里
// 或来自入参，见 `export_source.go` / `library.go` 的文件头）。改签名时两侧一起编译失败，
// 失败点在装配点。
