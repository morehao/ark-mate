package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestGlobalToastSitsAboveModals 全局 toast 必须在**弹窗层之上**（2026-10-11 实测缺陷的守卫）。
//
// 现场：用户在「设置 · 模型」里点「保存」，什么反应都没有——一个请求都不发（前端那一半另修），
// 而唯一能解释原因的提示（「没有改动」）当时用 `zIndex: 90` 画，被弹窗遮罩（Modal 基础 1000、
// 嵌套一层 +200，本仓"设置"+"供应商/模型"两层实测就是 1000/1200）压到约 30% 亮度
// （两层遮罩叠加；`elementFromPoint` 命中的是 `.ant-modal-wrap`）。
//
// 为什么必须有机器判据：这条边**不会让任何别的测试变红**——toast 照样渲染、DOM 里查得到文案、
// 上面的业务逻辑全对，只是**画在了遮罩底下**。设置弹窗里每一个动作（供应商 / 模型 / Agent /
// 工作区 / 令牌）的成败提示都共享这一个浮层，所以判据钉在全局壳上，而不是各面板里。
//
// 三条断言（fail-closed：解析不到即失败，避免改名后守卫静默空转）：
//  1. `app.tsx` 里有一处 `const TOAST_Z = <n>`；
//  2. `n >= toastMinZ`：**至少与 antd 自己的全局浮层同档**（antd message/notification 用 2010），
//     因为 Modal 的层号随嵌套层数增长，固定写一个"够大的小数字"迟早还会被压住；
//  3. toast 那个元素真的用这个常量（`zIndex: TOAST_Z`），不许再写回字面量。
const toastMinZ = 2000

func TestGlobalToastSitsAboveModals(t *testing.T) {
	appPath := filepath.Join(frontendRoot(t), "apps", "web", "src", "app.tsx")
	b, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", appPath, err)
	}
	src := stripLineComments(string(b))

	// ① 常量存在且可解析。
	m := regexp.MustCompile(`const\s+TOAST_Z\s*=\s*(\d+)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("在 %s 里找不到 `const TOAST_Z = <数字>`：全局 toast 的层号必须有单一事实源"+
			"（本守卫 fail-closed：解析不到即失败，别让它变成空转）", appPath)
	}
	z, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("解析 TOAST_Z 失败: %v", err)
	}

	// ② 层号必须高于弹窗层。
	if z < toastMinZ {
		t.Fatalf("TOAST_Z = %d < %d：antd 的 Modal 基础层是 1000、每嵌套一层 +200（本仓实测设置弹窗两层 "+
			"= 1000/1200），toast 低于它就会被遮罩压暗到看不清（用户报的「点保存没反应」就是这条）。"+
			"要改小请先确认所有弹窗（含嵌套）的层号，并把证据写进本注释", z, toastMinZ)
	}

	// ③ toast 元素用的是常量，不是字面量。
	i := strings.Index(src, "{toast && (")
	if i < 0 {
		t.Fatalf("在 %s 里找不到全局 toast 的渲染块 `{toast && (`（改名了请同步本守卫）", appPath)
	}
	tail := src[i:]
	if end := strings.Index(tail, ")}"); end > 0 {
		tail = tail[:end]
	}
	if !strings.Contains(tail, "zIndex: TOAST_Z") {
		t.Fatalf("全局 toast 的样式没用 `zIndex: TOAST_Z`（找到的渲染块：%s）——"+
			"写成字面量就等于把这条边退回 2026-10-11 的缺陷", strings.TrimSpace(tail))
	}
}
