package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **升权请求必须过人工审批** 的守卫（A1 / v28）。
//
// 两种腐化都不会让任何既有测试变红：
//
//	① **记忆替升权裁决**：底座发来的升权请求只给 `once` 档（探针实测：options 恒
//	   allow-once / reject-once）。若让会话级记忆（"这类工具始终允许"）替它裁决，等于把用户当时
//	   那句话自动扩张成一个底座**刻意只肯给一次**的动作；
//	② **失败即降级重跑**：升权被拒之后"顺手用 `danger-full-access` 再跑一次"——那是把一次
//	   被拒绝的授权变成了一次更大的授权，而且用户看不到中间那一步。
//
// 判据三段，都 fail-closed。

// TestSandboxEscalationGoesThroughApproval 见文件头。
func TestSandboxEscalationGoesThroughApproval(t *testing.T) {
	root := backendRoot(t)

	// ① `AutoDecision` 必须先看"这次请求能不能被记住"（有没有 `always` 档），再看记忆。
	memPath := filepath.Join(root, "internal", "domain", "interaction", "permission_memory.go")
	mem := stripLineComments(readFileOrFatal(t, memPath))
	auto, ok := goFuncBody(mem, "func (m *PermissionManager) AutoDecision(")
	if !ok {
		t.Fatalf("%s 里找不到 `AutoDecision(`（改名了？本守卫 fail-closed）", memPath)
	}
	if !strings.Contains(auto, "offersOnlyOnceOptions(req)") {
		t.Fatalf("`AutoDecision` 没有先判「这次请求能不能被记住」：\n" +
			"升权请求只给 `once` 档（探针实测），让记忆替它裁决 = 把「这一类都行」自动扩张成" +
			"一个底座刻意只肯给一次的动作")
	}
	// 判据必须落在**选项的 kind** 上（协议字段），不是标题里的字（模型写的自由文本）。
	if !strings.Contains(mem, "strings.HasSuffix(o.Kind, \"_always\")") {
		t.Fatalf("`offersAlwaysOption` 的判据不是 `o.Kind` 的后缀：标题是模型写的自由文本，" +
			"拿它判「这是不是升权请求」只会得到一条时灵时不灵的规则")
	}

	// ② 生产代码里**不得**有"升权失败 → danger-full-access 重跑"这类分支。
	//
	//    判据：`danger-full-access` 这个字面量只许出现在**枚举定义**与它的注释里
	//    （`domain/interaction/native_permission.go` 的取值声明、`permission_policy.go` 的取值说明）。
	allowed := map[string]bool{
		"internal/domain/interaction/native_permission.go": true,
		"internal/domain/interaction/permission_policy.go": true,
		"internal/domain/interaction/command_policy.go":    true,
	}
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		relSlash := filepath.ToSlash(rel(root, p))
		if strings.HasSuffix(p, "_test.go") || strings.HasPrefix(relSlash, "internal/archtest/") || allowed[relSlash] {
			continue
		}
		if strings.Contains(stripLineComments(readFileOrFatal(t, p)), "danger-full-access") {
			t.Errorf("%s 里出现了 `danger-full-access`：升权被拒之后不许「顺手用完全访问再跑一次」——\n"+
				"那是把一次被拒绝的授权变成一次更大的授权，而用户看不到中间那一步", relSlash)
		}
	}

	// ③ 审批通道只有一条：权限请求必须经 `RegisterAndWait`（没有"某类权限自动放行"的短路）。
	//
	//    平台替用户允许**只有两处**：用户决策（`RegisterAndWait`）与**已配置的命令门禁**
	//    （A2：规则文件里显式写了 `allow`，见 `commandGateDecision`）。除此之外，`permission_flow.go`
	//    里出现 `Allowed: true` 都视为"默认值式放行"——所以判据按**函数体**切分：门禁函数里必须有
	//    `CommandAllow` 分支且允许，其余部分一个字都不许有。
	flow := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "sessionruntime", "permission_flow.go")))
	if !strings.Contains(flow, "o.perm.RegisterAndWait(") {
		t.Fatalf("`permission_flow.go` 里没有 `o.perm.RegisterAndWait(`：审批通道必须只有这一条")
	}
	gateBody, ok := goFuncBody(flow, "func (o *Orchestrator) commandGateDecision(")
	if !ok {
		t.Fatalf("`permission_flow.go` 里找不到 `commandGateDecision(`（门禁的允许出口必须在这里且只有这里）")
	}
	if !strings.Contains(gateBody, "interaction.CommandAllow") || !strings.Contains(gateBody, "Allowed: true") {
		t.Fatalf("`commandGateDecision` 没有「只在 CommandAllow 分支允许」的形状：\n" +
			"平台替用户允许必须可追溯到一条**显式配置**的规则，而不是一个无条件默认值")
	}
	if rest := strings.Replace(flow, gateBody, "", 1); strings.Contains(rest, "Allowed: true") {
		t.Fatalf("`permission_flow.go` 的门禁之外出现了 `Allowed: true`：那不是**用户**的决定，\n" +
			"也不是一条已配置的规则——平台替用户允许是这一族里最危险的一行代码（它看起来像「默认值」）")
	}
}
