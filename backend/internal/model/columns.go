package model

// ——— 模型清单的列形状（v3.1 P4 从 `internal/store` 上移到这里）———
//
// 为什么上移：`internal/service/svcmodel` 的写面用例需要一个"清单载体"的窄端口，而端口的方法
// 签名里带着这两个结构。它们本质上是**列与取值约定**（`internal/model` 的职责），不是 store
// 的私有实现细节；留在 store 会让用例层不得不 import store（层表判红）。
//
// 名字与字段一个未改：这次只是搬家，落库形状与摘要口径都不变。

type ModelColumns struct {
	ID              string
	ProviderID      string
	Model           string
	Name            string
	Note            string
	InputModalities string
	// MaxInputTokens / MaxOutputTokens 平台声明的容量（v25；nil = 未声明）。
	// 指针而不是 int64：nil 与 0 在这条链路上是**两件事**（0 不是合法容量，见 ModelEntity 的注释）。
	MaxInputTokens  *int64
	MaxOutputTokens *int64
}

// ModelContent 是模型行里**可变的内容列**（身份列 `provider_id`/`model` 不在其中——
// 改身份等于换一条资源，不在 PATCH 语义里）。
//
// 容量在这里是**解析后的最终值**（调用方已把"不改"折成原值、"清除"折成 nil）：本层不做指针语义，
// 它只写库。
type ModelContent struct {
	Name            string
	Note            string
	InputModalities string
	MaxInputTokens  *int64
	MaxOutputTokens *int64
}
