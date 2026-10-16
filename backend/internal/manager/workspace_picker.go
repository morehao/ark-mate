package manager

import (
	"context"
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
func (s *Service) SetBindAddr(addr string) { s.picker.SetBindAddr(addr) }

// TODO(P4b): 删转发（调用点直接依赖 workspace.Picker）
func (s *Service) NativePickerMode() string { return s.picker.NativePickerMode() }

// TODO(P4b): 删转发（调用点直接依赖 workspace.Picker）
func (s *Service) PickDirectoryHost(ctx context.Context) (string, bool, error) {
	return s.picker.PickDirectoryHost(ctx)
}
