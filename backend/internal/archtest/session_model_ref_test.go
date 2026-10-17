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
//	② 写入点：会话的两个写面（建会话时落 rec、PATCH 时落元数据）**不得**把请求里的原值
//	   直接赋上去（`opt.Model.Model` / `*patch.Model`），必须先经 `resolveSessionModel` 解析成
//	   "平台行 + 运行时值"。直接赋值 = 把"运行时的不透明值"又写回库里（而它编译得过、跑得通）。
//
// P0：两个写入点原先各钉着一个精确文件路径（`session_start.go` / `sessions.go`）。那两个文件随会话域
// 搬进 `domain/session`（设计 §3.4），钉路径在搬家后只会"读不到文件"（假红）。判据改锚在
// **符号**上：建会话那一处锚 `startSessionOn`（建会话写路径，落 rec 就在它体内），PATCH 那一处
// 锚 `UpdateSession`（PATCH 写路径），两者都由 `uniqueSourceFile` 保证全仓恰好一处声明
// （0 处 = 判据失去对象；≥2 处 = 第二个真相源）。
func TestSessionModelRefIsPlatformID(t *testing.T) {
	internalDir := filepath.Join(backendRoot(t), "internal")
	entityPath := filepath.Join(backendRoot(t), "internal", "model", "session.go")
	entity := readStripped(t, entityPath)
	if !strings.Contains(entity, "column:model_id") {
		t.Fatalf("%s 里找不到 `column:model_id` 标签（fail-closed：实体面是这条守卫的一半）", entityPath)
	}
	// 反向：provider_id 那一列不得回来（它在 v26 出列，见守卫⑦）。
	if strings.Contains(entity, "column:provider_id") {
		t.Fatalf("%s 里出现了 `column:provider_id`：v26 已让该列出列（v19 之后它没有任何写入点）", entityPath)
	}

	// 两个写入点：建会话（符号锚 `startSessionOn`）与 PATCH（符号锚 `UpdateSession`）。
	start := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		`(?m)^func \(l \*Lifecycle\) StartSession\(`, "建会话的落 rec 写路径 `Lifecycle.StartSession`"))
	if !strings.Contains(start, "l.resolveModel(") {
		t.Fatal("建会话写路径里找不到 `l.resolveModel(`：模型入参必须先解析成" +
			"「平台行 + 运行时值」，否则库里又会存底座的不透明串")
	}
	// 适配器那一段：域只认端口，实现必须直连 `resolveSessionModel`（缺任一段都等于绕过解析）。
	wiring := readStripped(t, filepath.Join(backendRoot(t), "internal", "sessionruntime", "orchestrator.go"))
	if !strings.Contains(wiring, "svc.planes.Providers.ResolveSessionModel") {
		t.Fatal("装配点没有把 `svc.planes.Providers.ResolveSessionModel` 接到 `Lifecycle.ResolveModel` 端口")
	}
	if strings.Contains(start, "ModelID: opt.Model.Model,") {
		t.Fatal("建会话写路径里仍在把 `opt.Model.Model`（请求原值）直接落库——" +
			"它是**平台行 id**，落库前必须经解析（否则存回去的是运行时值）")
	}
	// 求解一次、用两处（落库用行 id、下发用运行时值）——判据是"解析的返回值真的被用到了"。
	for _, want := range []string{"platformModelID, modelCfg", "ModelID: platformModelID,"} {
		if !strings.Contains(start, want) {
			t.Fatalf("建会话写路径里找不到 %q：解析的产物必须**都被用上**"+
				"（行 id 落库、运行时值下发），少一个就是把其中一条链留成了旧形态", want)
		}
	}

	sess := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		declREFor("UpdateSession").String(), "PATCH 写面 `UpdateSession`"))
	// P4a 正文第四十四刀把 PATCH 写面搬进了 `service/svcsession`（`Meta.UpdateSession`），模型解析
	// 改经 `MetaDeps.ResolveModel` 端口——**判据一字不改，锚跟着对象走**：链路变成两段，判据也分两段。
	// 缺任一段都有一种静默失效：只钉用例侧，适配器忘了转发就没人拦；只钉适配器侧，用例自己
	// 把请求原值落库也看不出来。
	if !strings.Contains(sess, "ResolveModel(ctx, sessionRuntime, *patch.Model)") {
		t.Fatal("PATCH 路径没有把 `*patch.Model` 当**平台行 id** 交给解析端口" +
			"（`... ResolveModel(ctx, sessionRuntime, *patch.Model)`）")
	}
	depsSrc := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		`(?m)^func \(s \*Service\) ResolveModel\(`, "模型解析端口的实现 `ResolveModel`"))
	if !strings.Contains(depsSrc, "s.planes.Providers.ResolveSessionModel(ctx, runtimeID, modelID)") {
		t.Fatal("`ResolveModel` 适配器没有走 `provider.Plane.ResolveSessionModel`：解析链断在适配器上时，" +
			"表现正是「把底座的不透明串写回库里」——而两端各自看都像在正常工作")
	}
	if strings.Contains(sess, "r.ModelID = *patch.Model") {
		t.Fatal("PATCH 路径里仍在把 `*patch.Model`（请求原值）直接落库——必须先解析")
	}
	// 解析失败必须**在落库之前**上抛（`appliedModelID` 只在解析成功后才被改写成行 id）。
	if !strings.Contains(sess, "appliedModelID = &platformID") {
		t.Fatal("PATCH 路径里找不到 `appliedModelID = &platformID`：落库的必须是**解析后的行 id**" +
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
//
// P0：本条原先钉着一个**目录字面量**（四个承载文件都从它下面读）。目录级依赖
// 在搬家后最危险——目录还在、文件搬走了，扫描面变成"读一个不存在的路径"（假红）或干脆
// 什么也看不见（静默空转）。四个承载文件一律改按**符号**定位：`resolveSessionModel`（解析本
// 体）、`startSessionOn` / `UpdateSession`（两个写面）、`modelIDByValue`（目录注解产出点）。
func TestModelSelectionIsLimitedToPlatformList(t *testing.T) {
	internalDir := filepath.Join(backendRoot(t), "internal")
	// ① 拒绝路径存在且聚在一处：定义 + 两个调用面。
	modelList := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		declREFor("ResolveSessionModel").String(), "`ResolveSessionModel` 的实现"))
	if c := strings.Count(modelList, "func (pl *Plane) ResolveSessionModel("); c != 1 {
		t.Fatalf("`ResolveSessionModel` 应恰好定义 1 处，实际 %d 处（多一处就是第二个真相源）", c)
	}
	for _, want := range []string{
		"SessionModelNotFoundErr(trimmed)", // 形状不对 → 拒
		"if !found {",                      // 查不到行 → 拒
		"objid.Model",                      // 形状判据：平台模型行 id 的前缀
	} {
		if !strings.Contains(modelList, want) {
			t.Fatalf("`resolveSessionModel` 的实现里找不到 %q：解析失败的**显式拒绝**路径必须存在"+
				"（静默回落 = 用户以为选中了 A、实际跑 B）", want)
		}
	}
	// ①' **"拒绝"必须真的是出口，而不是"拒绝之前先悄悄回落"**（2026-10-15 反证实跑换来的一条）：
	// 光断言"拒绝路径存在"是不够的——在它前面插一段 `if row, ok := defaultModelRow(...) { … }`
	// 之后，上面那三处字符串**原样还在**，而运行时行为已经变成静默回落（占位符式守卫的典型死法）。
	// 所以判据落在 `resolveSessionModel` 的**函数体**上（整文件里有别的 `defaultModelRow` 消费者，
	// 混在一起数会把它们算进来）：默认行的读点恰好一处（空入参那一支），且它出现在**第一个**拒绝
	// 出口之前；`sessionModelNotFoundErr` 恰好两处（形状不对 / 查不到行，两条都不能少）。
	body, ok := goFuncBody(modelList, "func (pl *Plane) ResolveSessionModel(")
	if !ok {
		t.Fatal("解析不出 `ResolveSessionModel` 的函数体（fail-closed：结构判据必须看得见它）")
	}
	if c := strings.Count(body, "SessionModelNotFoundErr(trimmed)"); c != 2 {
		t.Fatalf("`ResolveSessionModel` 里 `SessionModelNotFoundErr(trimmed)` 应恰好 2 处"+
			"（形状不对 + 查不到行），实际 %d 处——少一处就有一条入参能「悄悄过」，"+
			"多一处通常意味着有人加了第三条路径而不是复用", c)
	}
	if c := strings.Count(body, "DefaultModelRow("); c != 1 {
		t.Fatalf("`ResolveSessionModel` 里 `DefaultModelRow(` 应恰好 1 处（**空入参那一支**），"+
			"实际 %d 处——多出来的那处几乎一定是「查不到就回落默认」，那正是本守卫要挡的静默偏差", c)
	}
	if i, j := strings.Index(body, "DefaultModelRow("), strings.Index(body, "SessionModelNotFoundErr(trimmed)"); i > j {
		t.Fatal("`DefaultModelRow(` 必须出现在**第一个拒绝出口之前**（它属于空入参那一支）：" +
			"出现在拒绝之后 = 「先拒、再回落」，同样是把用户的显式选择换成了另一个模型")
	}
	// ② 两个写面都走它（少一个就是"某一条路径还能把不透明值落库"）。
	start := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		`(?m)^func \(l \*Lifecycle\) StartSession\(`, "建会话的落 rec 写路径 `Lifecycle.StartSession`"))
	sess := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		declREFor("UpdateSession").String(), "PATCH 写面 `UpdateSession`"))
	// P4a 正文第四十四/五十六刀之后两条写面的链路都分两段（用例/域 → 端口 → 适配器），判据随之分两段。
	depsSrc := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		`(?m)^func \(s \*Service\) ResolveModel\(`, "模型解析端口的实现 `ResolveModel`"))
	wiring := readStripped(t, filepath.Join(backendRoot(t), "internal", "sessionruntime", "orchestrator.go"))
	if !strings.Contains(start, "l.resolveModel(") || !strings.Contains(sess, "ResolveModel(") ||
		!strings.Contains(depsSrc, "ResolveSessionModel(") ||
		!strings.Contains(wiring, "svc.planes.Providers.ResolveSessionModel") {
		t.Fatal("建会话与 PATCH 两条写面都必须经 `ResolveSessionModel`（PATCH 那一半现在是" +
			"「用例调端口 `ResolveModel` → 适配器调 `provider.Plane.ResolveSessionModel`」两段，缺任一段都等于绕过解析）")
	}
	// ③ 复用既有错误码（E9）：`model_not_found` 仍映射到 40401。
	aclCode := readStripped(t, filepath.Join(backendRoot(t), "pkg", "code", "acl.go"))
	if !strings.Contains(aclCode, `"model_not_found":`) {
		t.Fatal("`pkg/code/acl.go` 的 notFoundCodes 里没有 model_not_found：它必须仍提档到 40401" +
			"（会话引用了一个查不到的模型行 = 同一个码、同一份引导：刷新列表）")
	}
	// ④ 目录注解的产出点**只有一处**（`modelIDByValue`）：第二个产出点迟早与它分叉。
	catalog := readStripped(t, uniqueSourceFile(t, internalDir, nil,
		declREFor("modelIDByValue").String(), "目录注解产出点 `modelIDByValue`"))
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

// goFuncBody 抽出以 `signature` 开头的那段函数体（到下一个行首 `func ` 为止）。
//
// 为什么需要它：结构类判据（"这个调用点只能有一处、且必须在那个调用之前"）**必须落在函数体内**——
// 同一个文件里常有别的合法消费者（`defaultModelRow` 就有），在整文件上数会把它算进来。
// 解析不到即 `ok=false`（调用方 fail-closed：函数改名/搬家时必须有人回来对齐这条判据）。
func goFuncBody(src, signature string) (string, bool) {
	i := strings.Index(src, signature)
	if i < 0 {
		return "", false
	}
	rest := src[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		rest = rest[:j+1]
	}
	return rest, true
}
