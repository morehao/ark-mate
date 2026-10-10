// 与控制面控制器（backend/internal/controller/ctr*）契约对齐的 REST 信封（gincontext 包装）。
// 成功 code=0；业务错误 code!=0（仍以 HTTP 200 返回）。
export interface Envelope<T> {
  code: number
  requestID: string
  msg: string
  data: T
}

/**
 * 业务码常量，唯一事实源是 `backend/pkg/code`（与 README「业务错误码」表一致，
 * 由后端的 TestFrontendBizCodesMatchRegistry 双向守卫本文件——改码必须三层同步）。
 *
 * 后端把「未归类错误」一律归到 Internal(50000)，信封里**不会**再出现 code=-1 这类
 * 取值；前端只需对 Success 与确有专门交互的码分支（当前只有 Unauthorized 触发令牌
 * 重填），其余码统一按 msg 展示。列全这套常量的意义是让分支写名字而不是魔数。
 */
export const BizCode = {
  Success: 0,
  /** 参数错误（缺失 / 类型或取值不合法）。 */
  InvalidParam: 40000,
  /** 请求语义非法（游标互斥、归属不符、状态机不允许）。 */
  InvalidRequest: 40001,
  /** 单操作员令牌缺失/无效（A6；HTTP 状态同时为 401）。 */
  Unauthorized: 40101,
  /** 资源不存在（会话 / agent / runtime / provider / MCP server）。 */
  NotFound: 40401,
  /** 当前形态明确不支持（典型：memory 模式下的 `/usage/summary`）。 */
  NotSupported: 40501,
  /** 与当前状态冲突或超出额度（在途轮次、上游超时）。 */
  Conflict: 40901,
  /** 服务内部错误（后端唯一兜底码）。 */
  Internal: 50000,
  /** 上游执行失败（runtime / MCP server 报错；含 `resource_sync_failed` 50001——同步失败）。 */
  UpstreamFailed: 50001,
  /** 上游连接失败（进程拉不起、连接断开、不可达）。 */
  BadGateway: 50201,
} as const

export type BizCodeValue = (typeof BizCode)[keyof typeof BizCode]

/** 归一后的请求错误（api 层抛出，code < 0 表示网络/解析层错误）。 */
export interface HttpErrorShape {
  code: number
  msg: string
}
