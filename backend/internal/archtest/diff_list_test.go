package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件钉住「差异清单」不能腐烂（2026-10-11，P3-5/V3）。
//
// 设计文档 §7.3 有一张**桌面与浏览器的差异清单**：桌面多出来或不一样的东西、浏览器入口怎么完成
// 同一件事、以及每一项的靶。V3（单向一致性）此前是一句**空转的**口径——"逐个桌面独有能力在
// 浏览器端跑完"，但"逐个"是几个、哪些，没有清单，于是它只能靠人记得。有了清单还会有第二个失效
// 方式：**清单本身烂掉**——改了验收脚本里的断言标题、删了那条断言，文档照旧写着它，而没有任何
// 机制会因此变红（V3 于是重新变回口径）。
//
// 判据（fail-closed：解析不到清单/靶列即失败）：
//  ① 设计文档里能找到那张表（表头逐字匹配），且至少 4 行——行数变少到闻不出来时当场红；
//  ② 每行第 5 列（桌面靶）要么是 `无靶：<理由>`（理由 ≥ 10 字），要么是**若干反引号包起来的
//     断言标题**，且每个标题必须在 `frontend/scripts/desktop-ui-acceptance.mjs` 里逐字出现
//     （`check('…')` / `section('…')` 的实参）。
//
// 反证（改完手工过一遍）：① 把某行的靶标题改一个字 → 红；② 把 `无靶：` 那行的理由删空 → 红；
// ③ 删掉清单里一行 → 只在行数 < 4 时红（行的增减本身是产品决定，不该由守卫替你决定）。
// 注意本守卫**只钉桌面靶**：浏览器靶那一列是"等价路径怎么跑"的说明，形态更自由（有些项走
// `pnpm smoke:e2e`、有些走 `ui:accept` 的场景），强钉会更像噪声。

// diffListHeader 是 §7.3 差异清单的表头（逐字匹配；表头改了说明清单被重写，守卫该显式改）。
const diffListHeader = "| # | 桌面独有的东西 | 桌面侧落地 | 浏览器入口怎么完成 | 桌面靶（`pnpm ui:accept:desktop` 断言） | 浏览器靶（`pnpm ui:accept` 断言） |"

// tabletalkBackticked 抓反引号包起来的内容。
var tabletalkBackticked = regexp.MustCompile("`([^`]+)`")

// noTargetPrefix 是"这一项没有靶"的显式标记（必须跟理由）。
const noTargetPrefix = "无靶："

// TestDifferenceListHasDesktopTargets 见文件头。
func TestDifferenceListHasDesktopTargets(t *testing.T) {
	repoRoot := filepath.Dir(frontendRoot(t))

	docPath := filepath.Join(repoRoot, "docs", "desktop-architecture-design-20260928.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("读设计文档失败: %v——差异清单是 V3 的判据来源，读不到就该红", err)
	}
	lines := strings.Split(string(raw), "\n")

	header := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == diffListHeader {
			header = i
			break
		}
	}
	if header < 0 {
		t.Fatalf("在设计文档里找不到差异清单表头（期望逐字：%s）——表被改名/重写时请同步本守卫", diffListHeader)
	}

	scriptPath := filepath.Join(frontendRoot(t), "scripts", "desktop-ui-acceptance.mjs")
	scriptRaw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("读桌面靶脚本失败: %v", err)
	}
	script := string(scriptRaw)

	rows := 0
	for i := header + 2; i < len(lines); i++ { // 表头 + 分隔行
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 6 {
			t.Fatalf("差异清单第 %d 行列数 %d < 6（列约定：编号/差异/桌面侧落地/浏览器怎么完成/桌面靶/浏览器靶）", i+1, len(cells))
		}
		rows++
		id := strings.TrimSpace(cells[0])
		target := strings.TrimSpace(cells[4])

		if strings.HasPrefix(target, noTargetPrefix) {
			reason := strings.TrimSpace(strings.TrimPrefix(target, noTargetPrefix))
			if len([]rune(reason)) < 10 {
				t.Errorf("%s 的靶列写成「%s」但理由太短（%q）：没有靶的项必须写明为什么不需要，"+
					"否则它就是清单腐烂的入口", id, noTargetPrefix, reason)
			}
			continue
		}

		labels := tabletalkBackticked.FindAllStringSubmatch(target, -1)
		if len(labels) == 0 {
			t.Errorf("%s 的靶列既没有反引号包起来的断言标题、也不以「%s」开头：靶列只有这两种写法（%q）",
				id, noTargetPrefix, target)
			continue
		}
		for _, m := range labels {
			if !strings.Contains(script, m[1]) {
				t.Errorf("%s 的靶 `%s` 在 desktop-ui-acceptance.mjs 里找不到逐字匹配——"+
					"要么断言被改名/删掉、要么文档没跟上（V3 会因此重新变回口径）", id, m[1])
			}
		}
	}

	if rows < 4 {
		t.Errorf("差异清单只有 %d 行（阈值 ≥ 4）：清单被删空时 V3 就没人执行了", rows)
	}
}
