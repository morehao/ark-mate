package archtest

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 本文件是 v20「凭据去 vault 化」的词汇收口守卫。
//
// v20 把密钥的权威从"文件 + AES-GCM"搬进库（`providers.key` 一列与 `mcp_secrets` 一张表），
// `internal/vault` 整个删除，`vault:<ns>/<name>` 这套引用词法退役为 `store:<ns>/<name>`。
// 为什么这值得一条守卫：撤掉一个机制之后**残留的调用点不会让任何测试变红**——它们要么已经被
// 改道（那条路不再被任何用例走到），要么活在文案与注释里（用户照着它去 `ls vault/`、去设
// `MATE_VAULT_KEY`，然后发现"文档里说的东西不存在"）。这与 v15 的 `conversation`、v19 的
// agent 面是同一种失效方式：**概念撤了，词还在**。
//
// 判据 = 剥注释后零命中 `vault`（大小写不敏感），只放三类正当残留（见白名单）：
//
//	① **一次性搬运**（`internal/persistence/store/legacy_vault.go`）：它必须认识旧格式，才能把用户盘上那份
//	   密文解出来搬进库——这是全仓唯一允许"知道 vault 是什么"的地方；
//	② **历史那一版**（`internal/persistence/store/migrate.go` 的历史 DDL 与 v20 收口语句、以及停在那一版的
//	   迁移夹具）：迁移内核纪律是「只追加、绝不回改历史」，v20 的前缀改写必须写出旧前缀才改得掉它，
//	   夹具也必须按旧版写才测得到改写；
//	③ **旧前缀的显式翻译点**：档案文件（`providers.json`、capability 目录里的 `mcp/*.json`）不在
//	   库里、迁移改不到它们，所以导入时必须认出 `vault:` 并翻译成 `store:`（顺带 WARN）。
//
// 文档（README / `docs/` / 决策记录）**不在扫描面里**——这是刻意的：它们必须能解释被撤掉的机制，
// 否则用户不知道 v20 删了什么、旧引用串该怎么理解。守卫管代码，文档管解释。
func TestNoVaultVocabulary(t *testing.T) {
	root, files := vocabularyScanFace(t)

	if len(files) < 100 {
		t.Fatalf("扫描面只拿到 %d 个文件——路径写错了？（fail-closed：解析不到即失败）", len(files))
	}

	// 先自检登记，再扫判据：登记烂了的话，"绿"是假的。
	vaultWhitelistSelfCheck(t, files)

	scanned := 0
	var hits []string
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("相对路径: %v", err)
		}
		relSlash := filepath.ToSlash(rel)
		src := strippedSourceOrFatal(t, path)
		if strings.HasSuffix(relSlash, "README.md") {
			// README 是契约文档：它**必须**能解释 v20 删了什么，但**不许**把退役机制重新
			// 写成当下的用法。见 readmeRetiredVaultQuotes 的注释。
			for _, quote := range readmeRetiredVaultQuotes {
				src = strings.ReplaceAll(src, quote, "")
			}
		}
		if allowedByVaultVocabularyWhitelist(t, relSlash, src) {
			continue
		}
		scanned++
		if idx := strings.Index(strings.ToLower(src), "vault"); idx >= 0 {
			line := 1 + strings.Count(src[:idx], "\n")
			hits = append(hits, relSlash+":"+itoa(line))
		}
	}
	if scanned == 0 {
		t.Fatal("一个文件都没扫到（fail-closed）")
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("v20 起密钥的权威是库（`providers.key` / `mcp_secrets`，盘上只有可重建的 "+
			"`credentials.json`）：引用词法用 `store:<ns>/<name>`，读密钥走 store 的凭据读写面。\n"+
			"以下位置剥注释后仍出现 `vault`：\n  %s\n"+
			"（确需保留的登记进 vaultVocabularyWhitelist **并写明理由**；空理由同样判红）",
			strings.Join(hits, "\n  "))
	}
}

// vaultVocabularyWhitelistEntry 一条登记：**目录/文件前缀**或**路径无关锚**恰好一种，外加理由。
//
//   - `path`：相对仓库根的前缀（豁免整棵目录时用）。只给**落点稳定**的登记——本次分层重构不动
//     `internal/persistence/store`、`internal/platform/capability`、`internal/bootstrap/app`。
//   - `anchor`：**路径无关锚**——一个只出现在**唯一一个**被扫描文件里的符号；含它的那个文件被豁免。
//     `manager` 包里的 `provider_doctor.go` 会搬到 `mechanism/doctor/`（设计 §3.4 文件去向表），
//     所以它只有锚、没有路径：符号跟着文件走，登记不会因为一次搬家而悬空。
//
// 两种锚都必须带非空判据（`strings.HasPrefix(rel, "")` 恒为真，空 `path` 会**静默豁免每一个文件**）；
// 锚型的"恰好一个文件 + 那个文件里真的有 `vault`"由 vaultWhitelistSelfCheck 在扫描前自检。
type vaultVocabularyWhitelistEntry struct {
	path   string
	anchor string
	reason string
}

// key 只用于报错文案。
func (w vaultVocabularyWhitelistEntry) key() string {
	if w.anchor != "" {
		return "锚 " + w.anchor
	}
	return w.path
}

// vaultVocabularyWhitelist 是**允许**出现 `vault` 的扫描点与理由。
//
// 每一处都要能回答「这是谁的原文」：是**历史那一版**的名字（迁移与夹具），还是**用户磁盘上的
// 东西**（旧密文与旧 keyfile），还是**必须认出旧词才能翻译**的翻译点。白名单是"这次确实不该改"
// 的登记簿，不是"懒得改"的垃圾桶。
var vaultVocabularyWhitelist = []vaultVocabularyWhitelistEntry{
	{
		path: "backend/internal/persistence/store/legacy_vault.go",
		reason: "**一次性搬运**：它必须认识旧格式（目录名、keyfile 名与环境变量、AES-GCM 的 AAD " +
			"绑定、引用串前缀、main key 的两种编码）才能把用户盘上那份密文解出来写进 `providers.key` " +
			"与 `mcp_secrets`。这是全仓唯一允许知道 vault 是什么的地方——它只读不删，单条失败与缺主密钥" +
			"都只记问题、不中断整批",
	},
	{
		path: "backend/internal/persistence/store/legacy_vault_test.go",
		reason: "**停在 v20 之前的真实产物夹具**：那份密文字节串是用当年真实的加密实现 + 固定主密钥" +
			"预先造好的**冻结工件**，解不开它就说明搬运逻辑与当年那份实现分叉了哪怕一位。夹具按当时" +
			"那一版写，测的才是「旧文件还能装载」",
	},
	{
		path: "backend/internal/persistence/store/migrate.go",
		reason: "**历史那一版的 DDL 与 v20 的收口语句**：v19 及更早的语句里就有 `vault:` 引用串，" +
			"而 `UPDATE mcp_servers SET body = replace(body,'vault:','store:')` 必须写出旧前缀才改得掉" +
			"（迁移内核纪律：只追加，绝不回改历史语句）",
	},
	{
		path: "backend/internal/persistence/store/migrate_test.go",
		reason: "**迁移夹具**：断言「旧前缀被改写」「旧引用被翻译」的用例必须按当时那一版写旧前缀，" +
			"否则测不到改写；v13 建表列集合的冻结断言也在这一类里",
	},
	{
		path: "backend/internal/bootstrap/assembly/assembly.go",
		reason: "**装配期的一次性搬运**：启动时调 `st.ImportLegacyVault(ctx, anchor)` 并把它搬了" +
			"几条、哪几条失败（只带引用名与条数，绝不含值）打成 WARN。这是全仓唯一的调用点——" +
			"装配层知道旧物在哪（`LegacySecretAnchor`），搬运器知道怎么解",
	},
	{
		// 锚 = 报告面那个导出函数的签名。`provider_doctor.go` 随 P1a 从 `manager` 搬进
		// `mechanism/doctor/`，写路径的话搬完就得回来改，而"回来改"正是 P0 要消灭的东西；
		// 副作用是这条登记从"钉住一个文件"变成"钉住那个符号"，`LegacySecretPaths` 被删/改名时当场红。
		anchor: "func LegacySecretPaths(",
		reason: "**只读体检的遗留物报告面**：医生必须**指名** `vault/` 与 `vault.key` 这两个具体" +
			"路径，用户才知道盘上该删什么（报告是「已迁移，可删」，绝不代删）。它与「显式拒绝面必须" +
			"点名被撤掉的键」同形：只说「有遗留物」等于让用户去猜",
	},
	{
		path: "backend/internal/platform/capability/secretref.go",
		reason: "**旧前缀的唯一翻译点**：档案文件（`providers.json`、能力目录里的 `mcp/*.json`）不在" +
			"库里、v20 迁移改不到它们，所以导入时必须认出旧前缀并翻成 `store:`（顺带 WARN）。" +
			"常量名与注释都写着「这是全仓唯一一处翻译点」",
	},
	{
		path: "backend/internal/platform/capability/secretref_test.go",
		reason: "**翻译点的判据**：它必须写出旧前缀才能证明「认得出、翻得对、并且告警」——" +
			"与 `router_test.go` 里禁历史名的理由同形（守卫扫自己必然自证其罪）",
	},
	{
		path: "backend/internal/persistence/store/migrate_v13_test.go",
		reason: "**停在 v13 那一版的历史夹具**：它用裸 SQL 造一条「文件里只留引用」形态的历史行" +
			"（`key_ref` 指旧前缀），测的正是「旧行的密钥落点被搬进 `key` 列」这条链路；" +
			"夹具按当时那一版写才测得到搬运",
	},
	{
		path: "backend/internal/persistence/store/credentials_test.go",
		reason: "**迁移夹具**：读库用例沿用它自己造出来的历史行（那些行的 `body` 里带旧前缀），" +
			"断言读出来已是 `store:` 形态",
	},
}

// vaultWhitelistSelfCheck 是**登记自检**（在扫判据之前跑），全部 fail-closed：
//
//  1. 理由非空——登记必须能回答「这是谁的原文」；
//  2. 锚型登记必须命中**恰好一个**被扫描文件。0 个 = 文件没了 / 那个符号被改名了：登记已经悬空，
//     **而悬空是静默的**（同一条纪律下，`agentVocabularyWhitelist` 里 `migrate_v19_test.go` 那条
//     今天就指向一个不存在的文件而没有任何判据会红）；≥2 个 = 锚太宽——换一个包放一个同符号的文件
//     就能蹭到整份豁免，比原来的路径登记**更松**，必须当场红；
//  3. 命中的那个文件剥注释后必须**真的**含 `vault`。否则这条登记豁免不到任何东西，留着只会在下一个
//     被撤的词写进来时静默顶掉判据。
//
// 前缀型登记这里只查理由：它们指向的是本次分层重构**不动**的落点，"前缀悬空"是另一批账。
func vaultWhitelistSelfCheck(t *testing.T, files []string) {
	t.Helper()
	for _, w := range vaultVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %q 的理由是空的——登记必须能回答「这是谁的原文」", w.key())
		}
		if w.anchor == "" {
			if w.path == "" {
				t.Fatalf("白名单里有既没有 path 也没有 anchor 的登记（理由：%q）——"+
					"两种锚都没有的登记会静默豁免**每一个**文件", w.reason)
			}
			continue
		}
		got := anchoredFiles(t, files, w.anchor)
		switch {
		case len(got) == 0:
			t.Fatalf("白名单锚 %q 在扫描面里**一个文件都没命中**——文件没了，或者那个符号被改名了。"+
				"这条登记已经悬空：悬空**不会**让任何判据变红（只会让本来该豁免的那处旧词变成假红）。"+
				"请把锚重新指到那个落点上（按符号，不按路径）", w.anchor)
		case len(got) > 1:
			t.Fatalf("白名单锚 %q 命中 %d 个文件：%v——锚太宽。按锚登记的前提是「含这个符号的文件"+
				"**恰好一个**」；放宽了就等于多开一条豁免通道（换一个包放一个同符号的文件即可）。"+
				"请换一个只出现在目标文件里的符号", w.anchor, len(got), got)
		}
		if !strings.Contains(strings.ToLower(strippedSourceOrFatal(t, got[0])), "vault") {
			t.Fatalf("白名单锚 %q 定位到 %s，但那里剥注释后已经没有 `vault` 了——这条登记豁免不到任何"+
				"东西。留着的死登记挡不住任何东西，却会在下一个被撤的词写进来时**静默**顶掉判据："+
				"要么删掉它，要么把锚指到真正需要豁免的那一处", w.anchor, got[0])
		}
	}
}

// readmeRetiredVaultQuotes 是 README 里**只作为历史引用**出现的旧机制物件的**精确**写法。
//
// 为什么给 README 一组精确 token，而不是整份豁免、也不是"反引号里随便写"：这份文档必须能解释
// v20 删了什么（读者要知道旧引用串怎么理解、为什么盘上还留着旧目录与旧 keyfile），但**不能**把
// 退役机制重新写成当下的用法。精确 token 让"解释"与"重新规定"在文本上可分——只有这几串允许，
// 别的写法（尤其是不带撤回语境的散文与当前时态）一律红。旧机制在 README 里只能作为**具体物件**
// 被指名（路径、前缀、标识符、已删的错误码），不能作为一个"概念"被散文讨论。
var readmeRetiredVaultQuotes = []string{
	"`vault:`",
	"`vault/`",
	"`vault.key`",
	"`vault/providers/<id>.enc`",
	"`vault/providers/*.enc`",
	"`vault_unavailable`",
	"`internal/vault`",
	"`key_ref: \"vault:providers/<id>\"`",
}

// allowedByVaultVocabularyWhitelist 命中白名单即跳过。
//
// 匹配两种锚：`path` 是前缀（对 `rel`），`anchor` 是**内容**（对 `src`）。两处都带非空判据：空 `path` 的
// `strings.HasPrefix(rel, "")` 恒为真，会把**每一个**文件都静默豁免掉。
func allowedByVaultVocabularyWhitelist(t *testing.T, rel, src string) bool {
	t.Helper()
	for _, w := range vaultVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %s 的理由是空的——登记必须能回答「这是谁的原文」", w.key())
		}
		if w.path != "" && strings.HasPrefix(rel, w.path) {
			return true
		}
		if w.anchor != "" && strings.Contains(src, w.anchor) {
			return true
		}
	}
	return false
}
