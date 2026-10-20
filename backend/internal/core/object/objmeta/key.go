// Package objmeta 承载 `meta` 表里**跨层共用**的键名（纯值，零 I/O）。
//
// 为什么单独成包：这些键同时被内圈（`domain/session` 在启动钩子里置标记/清标记）与外圈
// （`bootstrap/maintenance` 的 doctor 读同一个标记）使用。放在 `persistence/model/enum.go`
// 会让领域 import 表形状包（C1 要消掉的那条边）；复制成两份字面量则会出现"域写了 A、doctor
// 读 B"的静默分叉——标记永远读不到，而两边各自的测试都绿。
//
// **不属于这里的东西**：迁移内核自己用的键（`MetaKeyImportMarker` 等）留在 `persistence/model`，
// 因为它们的消费面就是迁移（同层，无跨层共用问题）。
package objmeta

// KeyReadModelsRebuild **读模型需要重建**的标记（v21）：value = 让读模型失效的那一版号
// （如 `"21"`）。空/不存在 = 读模型与 events 一致。
//
// 为什么用"标记 + 启动重建"而不是在迁移里逐列搬迁：`runs`/`messages` 是 events 的**投影**，
// 口径变化（位次值域、白名单、ordinal）的正确处置是**重建**而不是"把旧口径的数搬成新口径"
// ——后者每个版本都要写一段一次性搬迁代码，而重建只有一个实现。
//
// 消费面是**唯一的**：`session.Plane.RebuildReadModels`（启动钩子 + `mate doctor
// --rebuild-read-models`）。写完即清（清标记与重建同事务边界内应尽力而为）。
const KeyReadModelsRebuild = "read_models_rebuild"

// KeySessionsModelRefBackfill **会话模型引用待回填**的标记（v26）：value = 置标记的那一版号
// （`"26"`）。空/不存在 = 已回填完成（或不需要回填）。
//
// 为什么需要一个标记，而不是"在迁移里就地改写"：v26 起 `sessions.model_id` 装的是**平台模型行
// id**（`mdl_…`），而存量装的是**底座的不透明值**。两者之间的换算是"平台行 → 运行时值"，要过
// adapter——而 `internal/persistence/store` **不得 import adapter**，所以迁移只能置标记，真正的
// 回填由持注册表的编排层在启动时做。
//
// 消费面是**唯一的**（同 KeyReadModelsRebuild 的纪律）：`session.Plane.BackfillSessionModelRefs`。
// **算不出来就不清标记**——"这次算不出来"不等于"永远算不出来"，打勾会把一次失败固化成事实。
const KeySessionsModelRefBackfill = "sessions_model_ref_backfill"
