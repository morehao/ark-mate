package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/golib/glog"
)

// registerSession 登记一个新会话，返回记录指针。
// EnableStore 启用 SQLite 持久层（P1 live 接线）。启用后：
//   - publish 以 store.AppendEvent 事务内分配的**会话内位次**为权威，bus 用 PublishAt 镜像同一号序；
//   - registerSession/markSession/UpdateSession 同步写库（sessions 为 DB 权威元数据镜像）；
//   - transcript recorder 视为停写（serve 装配时不再 SetTranscriptDir）。
//
// 须在 RestoreFromStore 之前、任何会话动作之前调用。
// attachStore 把库句柄接到**所有持有它的部件**上（Service 自己 + 定义机制 + 凭据机制 + 技能平面）。
//
// 为什么要有这个函数：这几处曾经都直接读 `s.st`，"设一次就够了"；拆层之后它们各持一个引用，
// **漏掉任一个的表现都是静默的**（该走库的走了文件、或反过来——定义只写进内存、凭据读不到库）。
// 装配路径与 `mate doctor --rebuild-read-models` 那条逃生口都必须经它，**不要各自赋值**。
func (s *Service) attachStore(st *store.Store) {
	s.st = st
	s.defs.SetStore(st)
	s.creds.SetStore(st)
	s.skillPlane.SetStore(st)
	s.providerPlane.SetStore(st)
	s.sessionPlane.SetStore(st)
}

func (s *Service) EnableStore(st *store.Store) {
	s.attachStore(st)
	// T5 的配置指纹随持久层一起装载（它并进代际键，重启后必须仍然生效——
	// 否则重启会把"已物化的清单"当成"没有清单"，下一轮凭空重换代际）。
	s.loadRuntimeConfigDigests(context.Background())
	// 装配期把启动面与当前声明对齐一次（v12：启动参数/内联内容/凭据 env 都由 adapter 经
	// `resource.Delivery` 装卸）。重启后平台必须重新交付——否则上一轮的交付物与新声明可能分叉，
	// 而界面显示的是"已同步"。
	if ids, err := s.runtimeIDsForSync(""); err == nil {
		for _, id := range ids {
			if _, serr := s.SyncRuntime(context.Background(), id); serr != nil {
				glog.Warnw(context.Background(), "runtime sync: 装配期同步失败", "runtime", id, "err", serr)
			}
		}
	}
}
