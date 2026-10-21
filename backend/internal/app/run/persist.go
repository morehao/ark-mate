package run

import "github.com/morehao/ark-mate/internal/domain/session"

// EnableStore 启用 SQLite 持久层（P1 live 接线）。启用后：
//   - publish 以 store.AppendEvent 事务内分配的**会话内位次**为权威，bus 用 PublishAt 镜像同一号序；
//   - registerSession/markSession/UpdateSession 同步写库（sessions 为 DB 权威元数据镜像）；
//   - transcript recorder 视为停写（serve 装配时不再 SetTranscriptDir）。
//
// 须在 RestoreFromStore 之前、任何会话动作之前调用。
//
// 库句柄的接线分两半，**都从这一个入口进**：
//
//   - 会话域（本层拥有）：`sessionStore` 落到 `sessionPlane.SetStore`（域端口 `session.Store`），
//     `resourceStore` 落到资源同步面的窄库口；
//   - 领域平面（装配层拥有）：`planes` 这个动作把它那份库接全（定义机制 / 凭据机制 / 技能平面 /
//     供应商域 / 工作空间域 / 自动化域 + 同步指纹装载 + 装配期同步）。
//
// 两个参数都是端口（阶段 0.1）：本层不认 `*store.Store`，也不认具体平面容器。
// 为什么必须是一个入口：这几处曾经都直接读 `s.resourceStore`，"设一次就够了"；拆层
// 之后它们各持一个引用，**漏掉任一个的表现都是静默的**（该走库的走了文件、或反过来——定义只写
// 进内存、凭据读不到库）。装配路径与 `mate doctor --rebuild-read-models` 那条逃生口都必须经它，
// **不要各自赋值**。
func (s *Service) EnableStore(sessionStore session.Store, resourceStore ResourceStore, planes PlaneStore) {
	s.sessionPlane.SetStore(sessionStore)
	s.resourceStore = resourceStore
	if planes != nil {
		planes()
	}
}
