// 与 backend/internal/server/handler 契约对齐的 REST 信封（gincontext 包装）。
// 成功 code=0；业务错误 code!=0（仍以 HTTP 200 返回）。
export interface Envelope<T> {
  code: number
  requestID: string
  msg: string
  data: T
}

/** 业务码常量（与 backend 约定一致：0 为成功）。 */
export const BizCode = {
  Success: 0,
} as const

export type BizCodeValue = (typeof BizCode)[keyof typeof BizCode]

/** 归一后的请求错误（api 层抛出，code < 0 表示网络/解析层错误）。 */
export interface HttpErrorShape {
  code: number
  msg: string
}
