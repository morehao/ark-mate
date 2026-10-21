package session

import "time"

// Meta 会话元数据（**与存储形态无关**的投影：sqlite 走库、memory 走内存镜像，取数方负责抹平）。
//
// 为什么这个值对象在**域**里，而不是在用它做端口签名的那一层：导出用例
// （`internal/app/system` 的 `source.go` 的 `Source`）需要一个"只带导出要用的字段"的视图，
// 而它的**实现**在 `manager`。类型若留在用例层的端口里，实现包就得反过来 import 用例层才能
// 写出方法签名——那正是 R4 要消灭的反向依赖（"实现包拿端口类型当签名"）。下沉到域之后：
// 用例层声明端口用它、实现层写方法用它，两边都只向下 import。
//
// 字段是导出产物真正用到的那几个（`ExportFile` 的 id/title/runtime_id/status + 文件名与
// 时间戳的生成）：多一个字段就意味着导出用例开始依赖它不该知道的东西。
type Meta struct {
	ID        string
	Title     string
	RuntimeID string
	Status    string
	Created   time.Time
	Updated   time.Time
}
