package archtest

// 本文件钉住**命令门禁编辑面真的存在**（O20 后半，2026-10-17）。
//
// 门禁接线之后还有第二种"有实现、没人能改"：服务端读写面与设置面板各自写好了，但入口没挂
// （设置分节没有这一项、或面板没被渲染）——用户永远只能手写 JSON，而 `docs/unimplemented.md`
// 的 O20 会看起来"已经关了"。
//
// 判据（fail-closed）：
//
//	① 路由：`GET/PUT /command-policy` 都在 `routes_commandpolicy.go` 里；
//	② 设置分节：`settingsView.tsx` 的 `SETTINGS_SECTIONS` 有 `id: 'command'`；
//	③ 渲染：`SettingsDialog.tsx` 在 `command` 这一节渲染 `CommandPolicyPanel`；
//	④ 面板：调用 `getCommandPolicy(` 与 `updateCommandPolicy(`，且保留样板断言（不是只发
//	   name/pattern/decision 三个字段——那会静默丢掉加载期校验）。
//
// 反证（实跑）：从 `SETTINGS_SECTIONS` 删掉 command → ② 红；面板不再调用 update → ④ 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommandPolicyEditorIsWired 见文件头。
func TestCommandPolicyEditorIsWired(t *testing.T) {
	root := filepath.Dir(backendRoot(t))
	read := func(rel string) string {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s 失败: %v（判据失去对象）", rel, err)
		}
		return string(raw)
	}

	routes := read("backend/internal/adapters/inbound/http/router/routes_commandpolicy.go")
	if !strings.Contains(routes, `v1.GET("/command-policy"`) || !strings.Contains(routes, `v1.PUT("/command-policy"`) {
		t.Errorf("routes_commandpolicy.go 没有同时注册 GET/PUT /command-policy——读写面缺一半")
	}

	view := stripTSComments(read("frontend/packages/console/src/console/settingsView.tsx"))
	if !strings.Contains(view, "id: 'command'") {
		t.Errorf("settingsView.tsx 没有 id: 'command' 的分节——面板没有入口")
	}

	dialog := stripTSComments(read("frontend/packages/console/src/components/settings/SettingsDialog.tsx"))
	if !strings.Contains(dialog, "<CommandPolicyPanel") {
		t.Errorf("SettingsDialog.tsx 没有渲染 <CommandPolicyPanel——分节在、内容不在")
	}

	panel := stripTSComments(read("frontend/packages/console/src/components/settings/CommandPolicyPanel.tsx"))
	if !strings.Contains(panel, "getCommandPolicy(") || !strings.Contains(panel, "updateCommandPolicy(") {
		t.Errorf("CommandPolicyPanel.tsx 没有同时调用 get/update——编辑面缺一半")
	}
	for _, keep := range []string{"match_samples", "not_match_samples"} {
		if !strings.Contains(panel, keep) {
			t.Errorf("CommandPolicyPanel.tsx 不再保留 %s：保存会静默丢掉加载期样板断言", keep)
		}
	}
}
