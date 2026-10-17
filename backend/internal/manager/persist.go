package manager

import (
	"github.com/morehao/ark-mate/internal/store"
)

// EnableStore 启用 SQLite 持久层（P1 live 接线）。启用后：
//   - publish 以 store.AppendEvent 事务内分配的**会话内位次**为权威，bus 用 PublishAt 镜像同一号序；
//   - registerSession/markSession/UpdateSession 同步写库（sessions 为 DB 权威元数据镜像）；
//   - transcript recorder 视为停写（serve 装配时不再 SetTranscriptDir）。
//
// 须在 RestoreFromStore 之前、任何会话动作之前调用。
//
// 库句柄的接线分两半，**都从这里进**：
//
//   - 会话域（本层拥有）：`sessionPlane.SetStore`；
//   - 领域平面与资源同步面（P5a 起归 `internal/registry`）：`planes.EnableStore` 一次接全
//     （定义机制 / 凭据机制 / 技能平面 / 供应商域 / 工作空间域 / 自动化域 + 同步指纹装载 +
//     装配期同步）。
//
// 为什么必须是一个入口：这几处曾经都直接读 `s.st`，"设一次就够了"；拆层之后它们各持一个
// 引用，**漏掉任一个的表现都是静默的**（该走库的走了文件、或反过来——定义只写进内存、凭据
// 读不到库）。装配路径与 `mate doctor --rebuild-read-models` 那条逃生口都必须经它，
// **不要各自赋值**。
func (s *Service) EnableStore(st *store.Store) {
	s.st = st
	s.sessionPlane.SetStore(st)
	s.planes.EnableStore(st)
}
