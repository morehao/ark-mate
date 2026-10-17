package archtest

// G4 · 默认工作空间读作「默认」且不展示绝对路径（十项修正第 4 条）。
//
// 这一条跨了三处名字位（左栏行 / 选择器 / 输入区 chip）与一层"后端不许跟着改"的保护：
//
//	① `workspaceLabel` 的**函数体**里必须有 `is_default` 分支并返回 `'默认'`
//	   （定位函数体而不是全文：别处的 `is_default` 会把判据喂饱）；
//	② 选择器默认项的副标题是「平台分配」，不是 `ws.path`；`defaultWS.path` 在组件内零命中；
//	③ 后端 `defaultWorkspaceDisplayTitle` 仍返回散文口径「默认工作空间」——前端把行名本地化成
//	   「默认」不等于把 wire 也改了（那是语言中立不变量）。
//
// 反证（实跑）：删掉 workspaceLabel 的 is_default 分支 → ① 红；把 defaultWS.path 加回来 → ② 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultWorkspaceRendersLocalizedName(t *testing.T) {
	picker := readConsoleFile(t, "components", "WorkspacePicker.tsx")

	const decl = "export function workspaceLabel"
	start := strings.Index(picker, decl)
	if start < 0 {
		t.Fatalf("解析不到 %q——展示名的唯一出处改名了，本判据会静默空转", decl)
	}
	body := picker[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	} else {
		t.Fatalf("workspaceLabel 的函数体解析不到结尾——判据无法切片")
	}
	if !strings.Contains(body, "is_default") {
		t.Errorf("workspaceLabel 里没有 is_default 分支——默认工作空间仍会显示成 default-workspace")
	}
	if !strings.Contains(body, "'默认'") {
		t.Errorf("workspaceLabel 的默认分支没有返回 '默认'（需求 4）")
	}
	if !strings.Contains(picker, "'default-workspace'") {
		t.Errorf("WorkspacePicker 里找不到语言中立哨兵 'default-workspace'——默认分支会认不出库里的标题")
	}

	// ② 选择器项：默认行的副标题必须是「平台分配」。
	//
	// 直接钉表达式而不是"第一个 fg-ws-item-sub"：「当前」行也有同名的副标题位，
	// 按出现顺序取会被它喂饱（本守卫第一版正是这样假红——那份是 defaultWS 分支）。
	if !strings.Contains(picker, "ws.is_default ? '平台分配' : ws.path") {
		t.Errorf("选择器登记项没有「默认行显示平台分配、其余行显示路径」的分支——默认工作空间仍会回显绝对路径")
	}
	if strings.Contains(picker, "defaultWS.path") {
		t.Errorf("WorkspacePicker 里仍有 defaultWS.path——默认工作空间的路径又回到了选择器")
	}

	// ③ 后端：散文口径仍是「默认工作空间」。
	registryRaw, err := os.ReadFile(filepath.Join(backendRoot(t), "internal", "domain", "workspace", "registry.go"))
	if err != nil {
		t.Fatalf("读 workspace/registry.go 失败: %v", err)
	}
	registry := string(registryRaw)
	const fn = "func defaultWorkspaceDisplayTitle("
	fnStart := strings.Index(registry, fn)
	if fnStart < 0 {
		t.Fatalf("解析不到 %q——后端的展示名函数改名了", fn)
	}
	fnBody := registry[fnStart:]
	if end := strings.Index(fnBody, "\n}"); end >= 0 {
		fnBody = fnBody[:end]
	}
	if !strings.Contains(fnBody, "默认工作空间") {
		t.Errorf("defaultWorkspaceDisplayTitle 不再返回「默认工作空间」——散文口径被前端的口径覆盖了")
	}
}
