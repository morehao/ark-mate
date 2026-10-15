package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是 **v26「会话模型引用改平台行 id」** 的三条守卫（设计
// `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md` §5.4 定案「辰」的 §7 守卫⑥⑦⑧；
// 计划 `.dsh/docs/plans/2026-10-15-provider-model-identity-plan.md` §4.M3.T7）。
//
// 它们挡的失败模式**全是静默的**，而且与前两版不同：M2 坏在"起不来"，这一版坏在
// **"跑起来了、但用的是另一个模型"**：
//
//	⑥ `sessions.model_id` 又被写进"运行时的不透明值" ⇒ 库里出现底座词表的字符串，而且
//	   "路由键改名后旧会话失效"这条病会悄悄回来（无任何报错）；
//	⑦ `provider_id` 又被加回来（或 DTO/前端类型里留着一个恒空的 `provider`）⇒ 下一个读它的人
//	   会以为"这里存着会话用的是哪个供应商"；
//	⑧ "对不上平台清单"的入参**静默回落**到默认模型 ⇒ 用户以为选中了 A，实际跑的是 B——
//	   这正是本仓禁止的那一类偏差（也是有意偏离 QwenWork 四级回退的地方）。

// sessionModelIDForm 平台模型行 id 的形状（`mdl_` + 26 位 Crockford）。
var sessionModelIDForm = regexp.MustCompile(`^mdl_[0-9A-HJKMNP-TV-Z]{26}$`)

// TestSessionModelRefIsPlatformID 守卫⑥：会话的模型引用是**平台行身份**，写入点必须先经解析。
//
// 两半（都 fail-closed：解析不到即失败）：
//
//	① 实体层：`SessionEntity.ModelID` 带 `column:model_id`——实体标签写错编译期无感，
//	   只有运行时那条 SQL 才报 `no such column`；
//	② 写入点：`manager` 里那两处（建会话时落 rec、PATCH 时落元数据）**不得**把请求里的原值
//	   直接赋上去（`opt.Model.Model` / `*patch.Model`），必须先经 `resolveSessionModel` 解析成
//	   "平台行 + 运行时值"。直接赋值 = 把"运行时的不透明值"又写回库里（而它编译得过、跑得通）。
func TestSessionModelRefIsPlatformID(t *testing.T) {
	entityPath := filepath.Join(backendRoot(t), "internal", "model", "session.go")
	entity := readStripped(t, entityPath)
	if !strings.Contains(entity, "column:model_id") {
		t.Fatalf("%s 里找不到 `column:model_id` 标签（fail-closed：实体面是这条守卫的一半）", entityPath)
	}
	// 反向：provider_id 那一列不得回来（它在 v26 出列，见守卫⑦）。
	if strings.Contains(entity, "column:provider_id") {
		t.Fatalf("%s 里出现了 `column:provider_id`：v26 已让该列出列（v19 之后它没有任何写入点）", entityPath)
	}

	// 两个写入点：建会话（session_start.go）与 PATCH（sessions.go）。
	start := readStripped(t, filepath.Join(backendRoot(t), "internal", "manager", "session_start.go"))
	if !strings.Contains(start, "resolveSessionModel(") {
		t.Fatal("session_start.go 里找不到 `resolveSessionModel(`：建会话的模型入参必须先解析成" +
			"「平台行 + 运行时值」，否则库里又会存底座的不透明串")
	}
	if strings.Contains(start, "ModelID: opt.Model.Model,") {
		t.Fatal("session_start.go 里仍在把 `opt.Model.Model`（请求原值）直接落库——" +
			"它是**平台行 id**，落库前必须经解析（否则存回去的是运行时值）")
	}
	// 求解一次、用两处（落库用行 id、下发用运行时值）——判据是"解析的返回值真的被用到了"。
	for _, want := range []string{"platformModelID, modelCfg", "ModelID: platformModelID,"} {
		if !strings.Contains(start, want) {
			t.Fatalf("session_start.go 里找不到 %q：解析的产物必须**都被用上**"+
				"（行 id 落库、运行时值下发），少一个就是把其中一条链留成了旧形态", want)
		}
	}

	sess := readStripped(t, filepath.Join(backendRoot(t), "internal", "manager", "sessions.go"))
	if !strings.Contains(sess, "resolveSessionModel(ctx, sessionRuntime, *patch.Model)") {
		t.Fatal("sessions.go 的 PATCH 路径没有把 `*patch.Model` 当**平台行 id** 解析" +
			"（`resolveSessionModel(ctx, sessionRuntime, *patch.Model)`）")
	}
	if strings.Contains(sess, "r.ModelID = *patch.Model") {
		t.Fatal("sessions.go 里仍在把 `*patch.Model`（请求原值）直接落库——必须先解析")
	}
	// 解析失败必须**在落库之前**上抛（`appliedModelID` 只在解析成功后才被改写成行 id）。
	if !strings.Contains(sess, "appliedModelID = &platformID") {
		t.Fatal("sessions.go 里找不到 `appliedModelID = &platformID`：落库的必须是**解析后的行 id**"+
			"（直接落请求原值 = 把运行时的不透明串写回库里）")
	}
}

// TestSessionsHasNoProviderIDColumn 守卫⑦：`sessions.provider_id` 在任何一层都不许回来。
//
// 判据四面（都 fail-closed）：真 schema 无此列；实体/DAO/DTO/前端类型剥注释后零
// `provider_id` / `ProviderID` / `provider?:`。
//
// 为什么值得钉：v19 删掉 agent 面之后这一列**没有任何写入点**（恒空），而"恒空"在界面上与
// "没设置"长得一模一样——下一个读它的人会以为"这里存着会话用的是哪个供应商"，于是又把它填起来，
// 于是同一件事就有了第二份事实源（模型行已经在说它挂在谁下面）。
//
// 白名单只放历史：`internal/store/migrate*.go`（v15 建表与 v16 的 RENAME 必须写当时那一版的名字，
// v26 的 DROP 也必须写它）与停在旧版的测试夹具。
func TestSessionsHasNoProviderIDColumn(t *testing.T) {
	db := migratedSchemaDB(t)
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'provider_id'`).
		Row().Scan(&n); err != nil {
		t.Fatalf("读 sessions 列集合失败（fail-closed）：%v", err)
	}
	if n != 0 {
		t.Fatal("真实 schema 上 `sessions.provider_id` 又出现了：v19 之后它没有任何写入点，" +
			"v26 已让它出列——供应商由**模型行**给出（`models.provider_id`）")
	}
	if err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'model_id'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("`sessions.model_id` 必须仍在（它指的是 `models.id`）: n=%d err=%v", n, err)
	}

	// 文本面：实体 / DAO / DTO / 前端类型。
	files := []string{
		filepath.Join(backendRoot(t), "internal", "model", "session.go"),
		filepath.Join(backendRoot(t), "internal", "dao", "session.go"),
		filepath.Join(backendRoot(t), "internal", "dto", "dtosession", "response.go"),
		filepath.Join(backendRoot(t), "internal", "dto", "dtosession", "request.go"),
		filepath.Join(frontendRoot(t), "packages", "types", "src", "session.ts"),
	}
	banned := []string{"provider_id", "providerid", "provider?:"}
	for _, path := range files {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("扫描面成员不存在：%s（%v）——守卫失去对象，fail-closed", path, err)
		}
		src := string(mustReadFile(t, path))
		if strings.HasSuffix(path, ".go") {
			src = stripLineComments(src)
		} else {
			src = stripTSComments(src)
		}
		low := strings.ToLower(src)
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("%s 剥注释后仍出现 %q：`sessions.provider_id` 在任何一层都不许回来"+
					"（它恒空，留着只会让人以为「这里存着供应商」）", path, b)
			}
		}
	}
}

// mustReadFile 读一个文件，读不到即失败（守卫的扫描面成员必须存在）。
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", path, err)
	}
	return raw
}

// TestModelSelectionIsLimitedToPlatformList 守卫⑧：会话写面必须存在"不认识的模型即拒"的
// **显式路径**，且目录注解只有一个产出点。
//
// 为什么这是唯一判据（前端置灰不算）：服务端静默回落到默认模型时，请求**照样 200**、
// 界面**照样显示选中了**——用户以为跑的是 A，实际是 B。所以必须有"解析不到就报错"的代码，
// 而且它得真的连到两个写面上（建会话 + PATCH）。
//
// 反证（实跑）：把 `resolveSessionModel` 的拒绝分支改成"回落默认" → 本用例红。
func TestModelSelectionIsLimitedToPlatformList(t *testing.T) {
	dir := filepath.Join(backendRoot(t), "internal", "manager")
	// ① 拒绝路径存在且聚在一处：定义 + 两个调用面。
	modelList := readStripped(t, filepath.Join(dir, "model_list.go"))
	if c := strings.Count(modelList, "func (s *Service) resolveSessionModel("); c != 1 {
		t.Fatalf("`resolveSessionModel` 应恰好定义 1 处，实际 %d 处（多一处就是第二个真相源）", c)
	}
	for _, want := range []string{
		"sessionModelNotFoundErr(trimmed)", // 形状不对 → 拒
		"if !found {",                      // 查不到行 → 拒
		"objid.Model",                      // 形状判据：平台模型行 id 的前缀
	} {
		if !strings.Contains(modelList, want) {
			t.Fatalf("model_list.go 里找不到 %q：解析失败的**显式拒绝**路径必须存在"+
				"（静默回落 = 用户以为选中了 A、实际跑 B）", want)
		}
	}
	// ② 两个写面都走它（少一个就是"某一条路径还能把不透明值落库"）。
	start := readStripped(t, filepath.Join(dir, "session_start.go"))
	sess := readStripped(t, filepath.Join(dir, "sessions.go"))
	if !strings.Contains(start, "resolveSessionModel(") || !strings.Contains(sess, "resolveSessionModel(") {
		t.Fatal("建会话与 PATCH 两条写面都必须经 `resolveSessionModel`")
	}
	// ③ 复用既有错误码（E9）：`model_not_found` 仍映射到 40401。
	aclCode := readStripped(t, filepath.Join(backendRoot(t), "pkg", "code", "acl.go"))
	if !strings.Contains(aclCode, `"model_not_found":`) {
		t.Fatal("`pkg/code/acl.go` 的 notFoundCodes 里没有 model_not_found：它必须仍提档到 40401" +
			"（会话引用了一个查不到的模型行 = 同一个码、同一份引导：刷新列表）")
	}
	// ④ 目录注解的产出点**只有一处**（`modelIDByValue`）：第二个产出点迟早与它分叉。
	catalog := readStripped(t, filepath.Join(dir, "model_catalog.go"))
	if c := strings.Count(catalog, "func (s *Service) modelIDByValue("); c != 1 {
		t.Fatalf("`modelIDByValue` 应恰好定义 1 处，实际 %d 处", c)
	}
	if c := strings.Count(catalog, "val.ModelID = "); c != 1 {
		t.Fatalf("`ConfigOptionValueResp.ModelID` 的赋值点应恰好 1 处（都在同一张注解表上），实际 %d 处", c)
	}
	// 多命中按 fail-closed：重复的值必须被**删掉**（而不是留第一条）。
	if !strings.Contains(catalog, "dup[v] = true") || !strings.Contains(catalog, "delete(seen, v)") {
		t.Fatal("目录注解没有 fail-closed 的「多命中不收录」：两条平台行派生出同一个运行时值时，" +
			"「随便挑一条」就是静默选错模型")
	}
}
