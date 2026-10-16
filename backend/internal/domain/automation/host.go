package automation

import "context"

// ——— P2b：宿主端口（本域**不能** import domain/session） ———
//
// 自动化触发的动作是"建一个会话"，而会话属 `domain/session`——同层零 import 守卫
// （`TestDomainPackagesMustNotImportEachOther`）明令两个域不许互相认识：那样"这份数据归谁"
// 就没有答案了。所以本域只声明**它需要宿主做什么**，由装配侧（manager 的适配器）如实实现。
//
// 端口刻意只有四个动作，对应本域真实用到的四处跨域调用（多一个都是把编排拖进领域）：
//
//	CreateSession  —— 触发一次（prompt/cwd/model/origin 由定义给出）
//	CancelRun      —— max_run_duration 到点，走**既有**取消通道（不引第二套超时机制）
//	SessionRunning —— overlap 判据（上一次还在跑 → 跳过本次）
//	EmitRun        —— 一条**只走实时通道**的 `automation.run` 事件
//
// 最后一条尤其刻意：事件的身份与落库属会话域（`domain/session/publish.go` 是唯一铸号点），
// 本域只描述"发生了什么"，不碰 bus、不碰 eventbus 类型、不碰 `session.*` 常量。

// HostSessionReq 建一个自动化会话所需的最小入参。
//
// 刻意不是 `dtosession.SessionCreateReq`：那是对外 DTO，字段会随 API 变；这里要的是"自动化
// 触发"这件事的四个事实。
type HostSessionReq struct {
	Prompt string
	Cwd    string
	Model  string
	// Origin 会话来源标记（`automation:<id>`）——读面据此把会话归到触发它的自动化上。
	Origin string
}

// Host 宿主端口（装配期登记）。
type Host interface {
	// CreateSession 建一个会话，返回平台会话 id。
	CreateSession(ctx context.Context, req HostSessionReq) (sessionID string, err error)
	// CancelRun 取消某会话在途的那一轮（幂等；没有在途轮次时什么也不做）。
	CancelRun(ctx context.Context, sessionID string) error
	// SessionRunning 该会话此刻是否有一轮在跑（空 id → false）。
	SessionRunning(sessionID string) bool
	// EmitRun 发一条 automation.run 实时事件。
	//
	// 参数是"内容"而不是 `RunOutcome`：载荷由本域按自己的字段口径拼好（含脱敏闸门），
	// 宿主只负责**把它送进哪条通道**（事件类型常量、聚合 id 的形态、以哪一版号进帧都属会话域）。
	// 这样"事件类型常量"与"事件身份"都不在本域出现——本域不认识 `session.*` 契约。
	EmitRun(ctx context.Context, automationID string, version int64, payload map[string]any)
}
