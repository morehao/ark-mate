# 请求上下文传递（`c` 与 `c.Request.Context()`）

> 从 `AGENTS.md` 硬约束的「请求上下文」一条展开（2026-10-27）。本文件是**这条规则的唯一事实源**：为什么 handler 传 `c.Request.Context()`、为什么 service 签名是 `context.Context`、为什么不得打开 `ContextWithFallback`。
> 机器判据见 [`guards.md`](guards.md) 的「请求上下文边界」两条。

## 规则（三条，无例外）

1. **handler 里需要往下游传 ctx 时，一律写 `c.Request.Context()`**。它是当前请求的标准 `context.Context`，可以被下游安全地长期持有。
2. **service 及更内层的签名一律是 `ctx context.Context`**，不得出现 `*gin.Context`。`c` 出不了入站带（controller / middleware / router）。
3. **不得打开 `engine.ContextWithFallback`**。`internal/router/router.go` 的 `gin.New()` 处就地写有理由，不要因为"想让 `c` 能用"而打开它。

异步/长活统一 `context.WithoutCancel(ctx)`（或 golib 的 `gincontext.AsyncContext(c)`），不要捕获 `c`。

## 为什么（三条理由，任何一条单独成立）

### 理由一：`*gin.Context` 是池化复用对象

gin 在请求结束后 `reset()` 并 `pool.Put(c)`，下一个请求 `ServeHTTP` 立刻写 `c.Request = req`。官方文档对此有明确禁令：

> 不要将 `*gin.Context` 存储在结构体中或跨 goroutine 边界传递。`gin.Context` 与 HTTP 请求/响应生命周期绑定，不适合并发使用。
> `gin.Context` 出于性能考虑会在请求之间复用……在 goroutine 中使用原始的 `gin.Context` 会导致数据竞争和不可预测的行为。

—— [Gin 官方文档「上下文与取消」](https://gin-gonic.com/zh-cn/docs/server-config/context/)

### 理由二：传 `c` 会被 `database/sql` 长期持有（本仓实测的数据竞争）

这条不是理论风险，2026-10-27 在本仓实跑复现过。

`database/sql` 的 `Rows.initContextClose` 见到 `ctx.Done() != nil` 就**起一个 goroutine** 监听取消（`sql.go:2992`），`awaitDone` 在 `Rows` 生命周期内持续调 `ctx.Err()`。于是把 `c` 当 ctx 传进 DB 层时：

```
请求 A：handler 传 c → db.WithContext(c) → database/sql 起 awaitDone goroutine（读 c.Err()）
请求 A 结束：reset() + pool.Put(c)
请求 B：ServeHTTP 写 c.Request = req          ← 与请求 A 残留的 goroutine 竞争
```

`-race` 下的稳定报错：

```
Write at ... by goroutine 45:
  gin.(*Engine).ServeHTTP()          gin.go:669      ← c.Request = req
Previous read at ... by goroutine 133:
  gin.(*Context).Err()               context.go:1458
  database/sql.(*Rows).awaitDone()   sql.go:3020
```

**打开 `ContextWithFallback` 不解决问题，反而是它把这条读路径激活的**：开关打开前 `hasRequestContext()` 直接返回 false、不读 `c.Request`；打开后 `Err()` 转发到 `c.Request.Context()`，于是 gin 每次 `Err()` 都要读那个正在被复用的对象。

### 理由三：主流做法一致，且社区的痛点有同一份官方答复

- **Gin 官方文档**：「始终传播请求上下文。将 `c.Request.Context()` 传递给每个接受 `context.Context` 的函数——数据库调用、HTTP 客户端、gRPC 调用和任何 I/O 操作。」
- **[gin issue #2845](https://github.com/gin-gonic/gin/issues/2845)**：标题正是"能否用 `gin.Context` 代替 `c.Request.Context()`"。提问者的动机与本仓这条规则要解决的**完全相同**——"容易误传 `c`，想从工程上防住"。而 `gin.Context` 源码注释给出的方向是"请改用 `Request.Context()`"，不是"打开开关混用"。
- **`ContextWithFallback` 不是为这件事设计的**：它是 1.8 引入的 feature flag（[release notes](https://gin-gonic.com/zh-cn/blog/releases/release18/)），用途是让**中间件与 tracing/日志库**能从 `c.Value()` 取到请求 context 里的值。它并不把 `c` 变成"可安全长期持有的 ctx"。
- **大型生产项目的规范**：[gin-vue-admin 的 service 层规范](https://gitee.com/pixelmax/gin-vue-admin/blob/030412104c02355b23cbc8f1c44f692b71d07ab3/aiDoc/examples/backend/service-example.md) 写明「以 `ctx context.Context` 为首参……（API 层传 `c.Request.Context()`）」，并把「**在 Service 层接触 `gin.Context`**」列为常见错误。Kratos（`kgin` 适配层）与 go-zero（`logic` 层的 `ctx` 字段）同样不把 `*gin.Context` 暴露给业务层。

## 为什么不"统一传 `c`"（一次被否决的尝试）

**动机成立**：58 处 `c.Request.Context()` 让"传 `c` 还是 `c.Request.Context()`"成了一个判断题，团队里两个人会写出两种。

**方案与结果**：开 `ContextWithFallback` + 全部调用点改传 `c` + 两条静态守卫（内层禁 gin 类型、handler 禁 goroutine 捕获 `c`）。58 处改完后 `go test -race ./internal/controller/... ./internal/router/...` **稳定复现**理由二里的 DATA RACE，且该竞争本来只在个别测试路径出现，改动把它**扩散到了每一个走 DB 的 handler**。

**结论**：回退。判断题的正确答案只有一个（`c.Request.Context()`），用下面的方式消除认知负担，而不是改答案。

## 怎么消除那个"判断题"（正确做法）

规则本身只有一个答案，所以负担不在"选择"，而在"记住"。三件事各管一段：

| 手段 | 管什么 |
| --- | --- |
| 本条规则 + 两条静态守卫 | 内层出现 `*gin.Context`、handler 让 `c` 逃逸到 goroutine —— 当场红 |
| 代码片段（snippet） | 新 handler 从模板起手，模板里的 ctx 写法就是 `c.Request.Context()` |
| 评审口径 | 只看一句："往下游传的 ctx 是不是 `c.Request.Context()`" |

守卫覆盖不到的一处**已知局限**（写在守卫文件注释里）：`req := c.Request` 之后再交给 goroutine，语法上已无 gin 形参引用，守卫抓不到。那种情况最多把 `*http.Request` 带进 goroutine（不推荐，但不会带进池化的 `*gin.Context`）；真正的兜底是「内层签名零 gin 类型」。

## 与 golib 的关系

golib `biz/gcontext` 把租户作用域做成**随 `context.Context` 传递的类型化值**，其注释写明理由是"把隔离判定从 context 的动态类型（如 `*gin.Context`）变成显式值：任何 ctx 只要携带该值，解析结果一致"（`biz/gcontext/scope.go`）。这条设计取向与本分册一致：**ctx 应该是纯 `context.Context`**。

- `gincontext.WithRequestValue(c, key, value)`：跨 `http.Handler` 边界的唯一写入入口；
- `gincontext.AsyncContext(c)`：异步路径，**不持有 `*gin.Context`**；
- `gincontext.SetTenantScope(c, scope)`：写请求上下文（规范存储）+ 投影 gin Keys。

## 与 ark-iam 的差异（不要照抄它的写法）

ark-iam 走的是"service 收 `*gin.Context` + 开 `ContextWithFallback`"（`backend/apps/*/cmd/main.go` 各一行）。它之所以**还能跑**，是因为租户隔离层 `pkg/dbclient` 做了载体断言兜底、且身份有两层载体。这属于用兜底掩盖理由二的隐患，不是可复用经验：

| | ark-mate（本仓） | ark-iam |
| --- | --- | --- |
| handler 传什么 | `c.Request.Context()` | `c` |
| service 签名 | `context.Context` | `*gin.Context` |
| `ContextWithFallback` | **不得打开** | 15 处打开 |
| 数据竞争面 | 无（纯 ctx） | 靠 `dbclient` 兜底 |

## 姊妹项目 ark-trip 的独立佐证（同一结论的另一种落法）

`ark-trip` 选了"业务层传 `*gin.Context`"，但它**独立撞到了同一条竞争**，并在 gorm 插件层补了一个归一化器（`backend/pkg/dbclient/request_context.go`）。它的注释把机制写得很准：

> - 不开开关就直传给 `database/sql`：拿到的是**降级 context** —— `Done()`/`Err()` 恒 nil（查询不随请求取消），typed-key 取值一律读不到；
> - 开了开关再直传：`database/sql` 的 `Rows.awaitDone` 会在 handler 返回之后调用 `ctx.Err()`，与 gin 复用同一个 `*gin.Context` 时 `ServeHTTP` 写 `c.Request` 构成数据竞争（`go test -race` 稳定复现）。
> 结论：正确的做法不是"别传 `gin.Context`"，而是**在取用之前把它归一化成请求自身的 context**（`c.Request.Context()`）。

它的实现是在 gorm 的 `create/query/update/delete/row/raw` 六个 processor 之前，把 `db.Statement.Context` 里的 `*gin.Context` 换回 `gc.Request.Context()`。

**这恰好印证了本分册的结论**：不论前端（handler）写什么，最终喂给 `database/sql` 的必须是纯 `context.Context`。ark-mate 的做法是**在源头不产生**这个坏值（handler 直接传纯 ctx），ark-trip 的做法是**在汇合点清洗**它（gorm 插件归一化）。前者少一层机制，且 `redis`/HTTP 客户端这类非 gorm 消费者也不会漏（ark-trip 的注释里自己承认"不覆盖非 GORM 的 ctx 消费者"）。

> 若将来本仓出现必须接收 `*gin.Context` 的第三方回调（例如库只认 gin 类型），可以借鉴 ark-trip 的归一化器；但**不要**把它当成"允许 handler 传 `c`"的理由——那会让每个非 gorm 的 ctx 消费者都需要自己的兜底。

## 事故记录

- **2026-10-27**：尝试"统一传 `c`"，58 处改造完成后 `-race` 稳定报 DATA RACE（`database/sql` → `c.Err()`）。排查中一度把基线的偶发竞争误判为"改动引入"，用"回退调用点 / 只关开关 / 只回退 `router.go`"三组对照才定准。**教训：`-race` 结论必须看竞争链指向谁，不能只看"改之前绿、改之后红"。** 回退后保留两条守卫与 `router.go` 的就地禁令注释。
