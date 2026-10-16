package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/workspace"
)

// ——— 宿主原生目录选择器：P3d 过渡期的转发层 ———
//
// 判定与命令配方（`nativePickerMode` / `planNativePick`）、宿主事实采样（`pickEnv`）、以及真的
// spawn 宿主命令取回路径（`PickDirectoryHost`）已落 `domain/workspace.Picker`（P2a-16）。
//
// **判定必须在宿主侧**这条口径连同它的守卫 `TestDirectoryPickerIsTwoStateHostResolved` 一起保留
// ——那条守卫按**符号**锚定（`nativePickerMode` 全仓恰好一处声明 + 判定/配方/取消语义逐条可查），
// 所以搬包不会让它空转。
//
// 本文件只剩转发；`bindAddr` 也随选择器搬走了（它此前挂在 `Service` 上、与 `sessions` 共用
// `Service.mu`，而"监听地址"与"会话表"本来没有必须原子这一说）。

// TODO(P4b): 删转发（调用点直接依赖 workspace.Picker）
func (s *Service) PickDirectoryHost(ctx context.Context) (string, bool, error) {
	return s.picker.PickDirectoryHost(ctx)
}

// Picker 宿主目录选择器平面的**事实出口**：它的判定与命令配方是领域实现（P2a-16 搬进
// `domain/workspace`），`Service` 上那份同名转发已按读模型口径删除（P4a 正文第十五刀）
// ——"宿主要不要抬系统面板"是**事实读取**，没有校验与错误码，调用点直接用平面。
func (s *Service) Picker() *workspace.Picker { return s.picker }
