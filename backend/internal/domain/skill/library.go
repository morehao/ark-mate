package skill

// Query 一次 Library 查询（R4：这个值对象的下沉是刻意的）。
//
// `Library` 是**跨领域**的聚合视图（skill/mcp/agent/tool），它的端口声明在
// `internal/app/library`，而实现（join 三张表 + 逐 runtime 可达性）在 `manager`。
// 类型若留在端口包里，实现包就得反向 import 端口包才能写出方法签名——"实现包拿端口类型当签名"
// 正是 R4 要断掉的那条依赖。下沉到域之后：端口包用它、实现包用它，两边都只向下 import。
//
// 字段就这两个：多一个字段都意味着 Library 读面开始依赖它不该知道的筛法。
type Query struct {
	// Kind 空 = 全部；否则须是 dtolibrary.ValidKinds 之一（未知值报 40000，不静默返回空表）。
	Kind string
	// Q 对 id/name/description 做大小写不敏感子串匹配。
	Q string
}
