package runtimeconn

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/golib/glog"
)

// ——— 执行面：**只读取样**一次已有会话的配置项（P5a 取样面）———
//
// 模型目录的 resume 通路需要"恢复一条已有会话 → 读配置项 → 关闭"，而它是**纯机制**：
// 不认识候选顺序、不认识缓存与拒绝值，也不决定哪条会话值得试。那三件事属编排/域，留在
// `domain/provider`（目录编排）与 `sessionruntime`（候选枚举）里；执行面只提供这一条动作。
//
// 为什么不让编排层自己拼 `LoadSession`/`Close`：那段代码要按能力位二选一（`load` 优先、
// `resume` 兜底）、要保证取样结束一定关闭，而"忘了关"的表现是**底座侧留下活跃会话**——
// 下一次取样会撞上自己，且没有任何测试会因此变红（`session/close` 是解除活跃态，不删记录）。
// 收成一条动作后，关闭是这一层的责任，调用方没有漏掉它的机会。

// SampleConfigOptions 恢复/装载一条已有会话，读它的配置项，然后关闭。
//
// 两条前提由调用方保证（它们不是这一层的事实）：
//   - `rc` 已经拨号成功且 `Client != nil`；
//   - `rawSessionID` 是**底座原文**（平台侧的 `rt_` 壳由调用方解掉）。
//
// **不新建会话**（session/new 会在两家 runtime 里落一条持久记录，而它们都没有 delete）。
// 能力位不支持 resume/load 时返回 `resume_session_unsupported`：那是"该交付面不支持读"的
// 事实，不是故障——调用方据此把说明文案与留痕级别分开。
func SampleConfigOptions(ctx context.Context, rc session.Conn, rawSessionID, cwd string) ([]acp.ConfigOption, error) {
	if rc == nil || rc.Client() == nil {
		return nil, acp.NewError(acp.ClassConnection, "runtime_not_connected",
			"执行面没有可用的客户端连接，无法取样模型目录", nil)
	}
	caps := rc.Client().Capabilities()
	if !caps.SessionResume && !caps.Raw.LoadSession {
		return nil, acp.NewError(acp.ClassUnsupported, "resume_session_unsupported",
			"该 runtime 不支持 session/resume|session/load，无法在不新建会话的前提下读取目录", nil)
	}
	var sess *acp.Session
	var err error
	if caps.Raw.LoadSession {
		sess, err = rc.Client().LoadSession(ctx, rawSessionID, acp.SessionOptions{Cwd: cwd})
	} else {
		sess, err = rc.Client().ResumeSession(ctx, rawSessionID, acp.SessionOptions{Cwd: cwd})
	}
	if err != nil {
		return nil, err
	}
	// 取样用完即释放（close 只解除活跃态，不删该会话的持久记录）。
	defer func() {
		if cerr := sess.Close(context.WithoutCancel(ctx)); cerr != nil {
			glog.Debugw(ctx, "目录取样会话 close 失败（忽略）", "runtime", rc.Spec().ID, "err", cerr)
		}
	}()
	return sess.ConfigOptions(), nil
}
