package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 本文件是 v19「agent 概念整个并入 runtime」的两条**收口**守卫。
//
// 为什么需要它们：agent 面横跨约 200 个文件（表/列/DTO/控制器/服务端口/路由/CLI/前端类型/验收脚本），
// 而"漏一处"的表现几乎全是**静默**的——某条 REST 路径 404、某个 json 键消失让前端拿到 `undefined`、
// 会话身份少一项而投影仍然自洽。逐处 review 挡不住这类漏，机器判据能。
//
// 判据**不是**「grep 到英文词 agent 就红」（那是错的）：ACP 自己的词表里 `agent` 指的就是**底座进程**
// （SDK 的 `Agent` 接口、`AuthMethods` 的 `agent` 变体），`internal/infra/acp` 必须能照原文写；`~/.agents/skills`
// 是平台与底座的**共享投放根**，也与本概念无关。所以禁词表列的是**被撤掉的那批标识符**，
// 与 v15 的 `conversation`、v16 的 `capability_revision`、v17 的 `provider_session` 同形。
//
// P0（2026-10-16 分层重构前置）：本文件里对 `manager` 包内**具体文件**的精确路径依赖已清掉，
// 改成「包 + 符号」的**路径无关锚**——那批文件在 P1~P4 会搬进 `domain/*`、
// `mechanism/*`、`runtimeconn`（设计 §3.4 文件去向表）。为什么必须现在改：路径钉住的
// 登记在搬家后有两条**都不会报错**的下场——① 登记悬空（白名单里 `migrate_v19_test.go` 那条今天就
// 指向一个不存在的文件，没有任何判据会红）；② 有人为了让红变绿，把"按路径"放宽成"任一文件存在即可"，
// 于是整条判据悄悄不再检查任何东西。

// legacyAgentVocabulary 是 v19 起禁止的旧词（**小写化后**做子串匹配）。
//
// 每一项都对应一种具体的漏法，而不是"把 agent 相关的东西都列上"：
var legacyAgentVocabulary = []string{
	// ① 枚举与 meta：定义种类与两个"当前 agent/导入标记"的键。
	"definitionkindagent", "metakeydefaultagent", "metakeyimportedagents",
	// ② 这两个键的**落库字面量**（meta 表的 key 值、json 键）。
	"default_agent", "imported_agents",
	// ③ 平台自己起的派生量名字。
	"defaultagentid", "builtinagents",
	// ④ 持久层/内存类型与表名常量（今天已是 0，列在这里是为了它别长回来）。
	//    `agentrec` 是 v19 之前那类"每个 agent 一份平台事实"的内存记录，现在由**单实例**
	//    `runtimeBinding` 顶替——它长回来就意味着 agent 又有了独立身份。
	"agententity", "tablenameagent", "agentdao", "agentrec",
	// ⑤ 目录/配置：`AgentDir` 与 `MATE_AGENTS` 的 Go 侧名字（旧**目录名** `agents/` 允许出现在
	//    一次性导入的路径常量里，见白名单——但那个常量的**标识符**不该再叫 AgentDir）。
	"agentdir",
	// ⑥ 三层包名（dto/controller/service 端口）。
	"dtoagent", "ctragent", "svcagent",
	// ⑦ 身份列/字段的两种写法：`agent_id`（列名、json 键、日志键、路径段）与驼峰
	//    `AgentID`/`agentID`（小写化后同形）。`internal/infra/procreg` 与 `internal/infra/mcp` 的网关 scope 曾用它，
	//    而它们**不会让编译变红**——正是这条守卫存在的理由。
	"agent_id", "agentid",
	// ⑧ 路由段。刻意**不**写裸词 `agents`：`~/.agents/skills` 与旧的 `MATE_AGENTS` 都要放行。
	//    `/agents/` 只匹配**斜杠包围**的形态（`.agents/skills` 里 `agents` 前面是点，不命中）。
	"mate/agents", "/agents/",
}

// agentVocabularyWhitelistEntry 一条登记：**目录/文件前缀**或**路径无关锚**恰好一种，外加理由。
//
//   - `path`：相对仓库根的前缀（豁免整棵目录时用）。只给**落点稳定**的登记——本次分层重构不动
//     `internal/persistence/store`、`internal/delivery/router`、`internal/config`、`internal/dto`、`internal/delivery/controller`。
//   - `anchor`：**路径无关锚**——一个只出现在**唯一一个**被扫描文件里的符号；含它的那个文件被豁免。
//     `manager` 包里这几个落点会搬家（设计 §3.4 文件去向表），所以它们只有锚、没有路径：
//     符号跟着文件走，登记不会因为一次目录改名而悬空。
//
// 两种锚都必须带非空判据（`strings.HasPrefix(rel, "")` 恒为真，空 `path` 会**静默豁免每一个文件**）；
// 锚型的"恰好一个文件 + 那个文件里真的有被守词"由 agentVocabularyWhitelistSelfCheck 在扫描前自检。
type agentVocabularyWhitelistEntry struct {
	path   string
	anchor string
	reason string
}

// key 只用于报错文案。
func (w agentVocabularyWhitelistEntry) key() string {
	if w.anchor != "" {
		return "锚 " + w.anchor
	}
	return w.path
}

// agentVocabularyWhitelist 是**允许**出现上述旧词的扫描点。
//
// 与 conversation / capability_revision 的白名单同一纪律：逐条写明理由（空理由同样判红）。
// 白名单是"这次确实改不了/不该改"的登记簿，不是"懒得改"的垃圾桶。
var agentVocabularyWhitelist = []agentVocabularyWhitelistEntry{
	{
		path: "backend/internal/persistence/store/migrate.go",
		reason: "迁移内核纪律：**追加迁移 = 在尾部新增，绝不回改历史**。所以这个文件里必然有两类旧词——" +
			"① v1–v18 的历史 DDL（建 `agents` 表、`sessions.agent_id` 列、`definition_revisions` 的 " +
			"`kind='agent'` 行）；② v19 自己的收口语句，它必须写**当时那一版**的名字才能撤掉它们：" +
			"`DROP TABLE IF EXISTS agents` / `DELETE ... WHERE kind='agent'` / " +
			"`DELETE FROM meta WHERE key IN ('default_agent','imported_agents')` / " +
			"`ALTER TABLE sessions DROP COLUMN agent_id`",
	},
	{
		path: "backend/internal/persistence/store/migrate_test.go",
		reason: "「停在 v19 之前」的历史迁移用例要用裸 SQL 造/读当时那一版的表与列" +
			"（DAO 已不认 `agents` 表与 `agent_id` 列，读不到历史形态），再断言 v19 之后它们**真的没了**" +
			"——改用新名字写夹具的话，这条用例就只是在验证“新形态能跑”，对迁移本身零覆盖。" +
			"这是守卫⑤ 那条纪律的测试侧镜像（同 `readLegacyConversation`）",
	},
	{
		path: "backend/internal/persistence/store/migrate_v19_test.go",
		reason: "同上的 v19 专项：夹具必须停在 v18 的形态（`agents` 行 + `sessions.agent_id`），" +
			"否则测不出 DROP COLUMN 与 DROP TABLE 的效果",
	},
	{
		path:   "backend/internal/persistence/store/migrate_v15_test.go",
		reason: historicalAgentFixtureReason,
	},
	{
		path:   "backend/internal/persistence/store/migrate_v16_test.go",
		reason: historicalAgentFixtureReason,
	},
	{
		path:   "backend/internal/persistence/store/migrate_v17_test.go",
		reason: historicalAgentFixtureReason,
	},
	{
		path: "backend/internal/delivery/router/router_test.go",
		reason: "反向断言「README 不得出现历史名 `/agents…`」的**判据字面量**本身——" +
			"这种守卫必须写出它禁的那个词，扫自己必然自证其罪（与 conversation 白名单里同一条理由同形）",
	},
	{
		path: "backend/internal/config/config.go",
		reason: "一次性导入必须**指出旧目录名**：装配期要把 `<MATE_HOME>/agents/providers.json` 搬到新的 " +
			"`providers/` 目录（v19 的目录改名），所以这里必然出现旧路径字面量。这是“指出历史”而不是“用旧词”：" +
			"新代码路径一律只认 `ProviderDir`/`MATE_PROVIDERS`",
	},
	{
		path: "backend/internal/dto/dtosession/request.go",
		reason: removedKeyRefusalReason + "**接住**一个被撤掉的键只有一个办法：给它一个同名 raw 字段" +
			"（JSON 绑定默认丢弃未知键，删掉这个字段就等于把「用户以为指定了 agent」变成静默 no-op）",
	},
	{
		// 锚 = 拒绝函数的定义。它随 `reject_removed_keys.go` 一起搬进 `mechanism/definition`（P1b），
		// 所以这里不写路径：写路径的话，搬完就得回来改，而"回来改"正是 P0 要消灭的东西。
		anchor: "func rejectRemovedAgentSelection(",
		reason: removedKeyRefusalReason + "**拒绝**必须点名它拒的是哪个键，否则用户拿到的" +
			"只是一句「参数不对」，改不动",
	},
	{
		// 锚 = 那次交接本身（调用点把 raw 值交给拒绝函数）。它随 `sessions.go` 搬进 `domain/session`（P3b）。
		anchor: "rejectRemovedAgentSelection(req.AgentID)",
		reason: removedKeyRefusalReason + "调用点要把接住的那个 raw 值交给拒绝函数——" +
			"这次交接在 Go 里至少要拼一次字段名（DTO 与拒绝面各自已登记，这里是第三处）",
	},
	{
		// 锚 = 用量面那条拒绝函数的定义。它随 `usage.go` 的拒绝面搬进 `domain/session`（P3b-1b），
		// 所以这里不写路径：写路径的话搬完就得回来改，而"回来改"正是 P0 要消灭的东西。
		anchor: "func rejectRemovedUsageDimension(",
		reason: removedKeyRefusalReason + "用量面的 `?agent_id=` 是**读**面的被撤键：它没有 JSON " +
			"绑定那一层，控制器读不到就等于**静默忽略**——用户以为在看某个底座的用量，拿到的却是全量",
	},
	{
		// 锚 = 清除历史键的那个函数。它随 `automation.go` 搬进 `domain/automation`（P2b）。
		anchor: "rejectAgentIDInPatch",
		reason: removedKeyRefusalReason + "清除历史键时的那条 WARN 必须点名它清掉了什么——" +
			"只写「已下线的键已清除」，用户翻自己的自动化定义时对不上是哪一行",
	},
	{
		path: "backend/internal/dto/dtousage/request.go",
		reason: removedKeyRefusalReason + "用量面的 `?agent_id=` 是**读**面的被撤键（v19 撤掉 agent 维度后，" +
			"「按底座筛用量」不再有意义）：它没有 JSON 绑定那一层，控制器读不到就等于**静默忽略**——" +
			"用户以为在看某个底座的用量，拿到的却是全量。接住它同样要一个同名 raw 字段",
	},
	{
		path: "backend/internal/delivery/controller/ctrusage/usage.go",
		reason: removedKeyRefusalReason + "控制器要**点名**它拒的是哪个查询参数（与同文件里 " +
			"`group_by` 只认 `day` 的那条校验口径一致），否则用户只拿到一句「参数不对」，改不动",
	},
	{
		// 锚 = 那份**冻结的历史产物**本身：v19 之前的 transcript header 里 `agent_id` 与 `runtime`
		// 并排出现。它随 `transcript_load_test.go` 搬进 `domain/session`（P3c）。
		anchor: `"agent_id":"agent-rtx","runtime":"rtx"`,
		reason: "**磁盘上真实存在过的历史产物**：v19 之前的 transcript header 里就有 `agent_id` 字段" +
			"（它和 `runtime` 并排出现），用户机器上的旧 transcript 文件将来也要靠这条路径读回来。" +
			"夹具按当时那一版写，测的才是「旧文件还能装载」而不是「新 header 能解析」",
	},
	{
		// 锚 = 造那份夹具的助手（v19 之前的自动化定义就是它写出来的）。它随
		// `automation_import_test.go` 搬进 `mechanism/definition`（P1b）。
		anchor: "automationSeedDir",
		reason: "**停在 v19 之前的自动化定义夹具**：`target.agent_id` 是当年用户写在文件里的键，" +
			"这条用例测的正是「导入旧定义时把已下线的键清掉并 WARN」——它必须写出旧键，否则测不到清除",
	},
	{
		// 锚 = 那条反向断言用例的函数名。它随 `usage_removed_dimension_test.go` 搬进 `domain/session`（P3b）。
		anchor: "TestUsageSummaryRejectsRemovedAgentDimension",
		reason: negativeAssertionReason + "这条用例专门钉「用量面收到已下线的筛选键 → 40001 " +
			"`agent_selection_removed`」，构造请求就必须写出那个键",
	},
	{
		path: "backend/internal/delivery/router/rest_removed_agent_key_test.go",
		reason: negativeAssertionReason + "传输层这一半钉的是相反的义务：「已下线的键**原样透传**给 " +
			"service，不由控制器自作聪明地丢掉」（丢弃会让用户以为筛选生效了）——判据里必然出现键名",
	},
	// 原先还有一条针对 `manager` 包内那个用量用例（文件基名 `usage_test.go`）的登记，**已删除**：
	// 那个文件剥注释后**一个被守词都没有**（它里面唯一的裸词 `agent` 不在禁词表里；真正构造被撤键的是
	// `usage_removed_dimension_test.go`，已单独登记）。留着它就是一条"豁免不到任何东西"的死登记：
	// 它挡不住任何东西，却会在下一个被撤的词写进那个文件时**静默**顶掉判据。
	// 锚型登记的第 3 条自检（命中的文件里真的有被守词）就是为了让这种烂账当场红。
}

// negativeAssertionReason 是"反向断言的判据字面量"的共用理由。
//
// 这类命中与「残留词汇」的区别在于**它想证明的正是那个词已经没了**：断言里不写出被禁的名字，
// 就没法构造出那条本该被拒绝的请求，于是守卫自己把自己逼成空转。
const negativeAssertionReason = "**反向断言的判据字面量**："

// removedKeyRefusalReason 是"显式拒绝面必须拼出被撤掉的键名"的共用理由（v19 的四处）。
//
// 为什么这类命中要放行而不是改掉：v19 撤掉 `agent_id` 之后，**最危险的结果不是报错，而是静默**——
// 写面带这个键的请求照旧 200，键被 JSON 绑定悄悄丢掉，用户以为"我给这个会话指定了 agent"。
// 所以本仓刻意留了一条显式拒绝（40001 `agent_selection_removed`），而它要成立就必须：
// ① 有一个同名的 raw 字段接住它；② 拒绝文案点它的名；③ 调用点把值交过去；④ 清历史键时说得清
// 清掉了什么。四处都是"指出历史"而不是"使用旧词"，与 `internal/config` 的改名提示同形。
const removedKeyRefusalReason = "**显式拒绝面**："

// historicalAgentFixtureReason 是"停在 v19 之前"那批历史迁移夹具的共用理由（v15/v16/v17 各一份）。
//
// 为什么它们必须写出 `agent_id`：那三份夹具要在**当时那一版**的库上造行（v14 的 `conversations`、
// v16/v17 的 `sessions`），而 `agent_id` 在 v19 之前**是那些表的列**——改用今天的列集合写夹具，
// 测的就不再是"迁移能把老库带过来"，而是"新表能存东西"，对迁移本身零覆盖。
const historicalAgentFixtureReason = "「停在 v19 之前」的历史迁移用例：夹具要在**当时那一版**的表上" +
	"写裸 SQL（`conversations`/`sessions` 在 v19 之前都有 `agent_id` 列），再断言迁移之后它真的没了" +
	"（迁移内核纪律：夹具按当时那一版写，绝不回改历史；同 conversation/capability_revision 的测试侧镜像）"

// firstHitInAgentVocabulary 返回 src（**已剥注释**）里按禁词表顺序第一个命中的旧词与它的下标。
//
// 抽出来是为了让"主扫描"与"登记自检"共用同一份判据：两处各写一遍，迟早在"谁算命中"上漂开——
// 而漂开的方向恰好是自检看不见的那种。
func firstHitInAgentVocabulary(src string) (string, int) {
	lower := strings.ToLower(src)
	// 2026-10-18：`docs/agents/` 是仓库**文档分册目录**（从仓库根 `AGENTS.md` 拆出），与 v19 撤掉的
	// `/agents/` 路由无关。按**等长**占位替换后再扫：既不误伤文档路径，也不会放过任何真正的
	// `/agents/...`（只吃掉 `docs/` 前缀后的那一段，命中下标仍与原文逐字对齐）。
	lower = strings.ReplaceAll(lower, "docs/agents/", "docs/xxxxxx/")
	for _, banned := range legacyAgentVocabulary {
		if idx := strings.Index(lower, banned); idx >= 0 {
			return banned, idx
		}
	}
	return "", -1
}

// allowedByAgentVocabularyWhitelist 命中白名单即跳过。
//
// 匹配两种锚：`path` 是前缀（对 `rel`），`anchor` 是**内容**（对 `src`）。两处都带非空判据：空 `path` 的
// `strings.HasPrefix(rel, "")` 恒为真，会把**每一个**文件都静默豁免掉。
func allowedByAgentVocabularyWhitelist(t *testing.T, rel, src string) bool {
	t.Helper()
	for _, w := range agentVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %q 的理由为空——白名单是登记簿，不是垃圾桶", w.key())
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

// agentVocabularyWhitelistSelfCheck 是**登记自检**（在扫判据之前跑），三条全部 fail-closed：
//
//  1. 理由非空——白名单是登记簿，不是垃圾桶；
//  2. 锚型登记必须命中**恰好一个**被扫描文件。0 个 = 文件没了 / 那个符号被改名了：登记已经悬空，
//     **而悬空是静默的**（同一个文件里 `migrate_v19_test.go` 那条今天就指向一个不存在的文件，
//     没有任何判据会红）；≥2 个 = 锚太宽——换一个包放一个同符号的文件就能蹭到整份豁免，
//     那比原来的路径登记**更松**，所以必须当场红；
//  3. 命中的那个文件剥注释后必须**真的**含被守词。否则这条登记豁免不到任何东西，留着只会在下一个
//     被撤的词写进来时静默顶掉判据（`usage_test.go` 那条就是这样，已删）。
//
// 前缀型登记这里只查理由：它们指向的是本次分层重构**不动**的落点，"前缀悬空"是另一批账
// （同一条纪律将来要一起收）。
func agentVocabularyWhitelistSelfCheck(t *testing.T, files []string) {
	t.Helper()
	for _, w := range agentVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %q 的理由为空——白名单是登记簿，不是垃圾桶", w.key())
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
				"这条登记已经悬空：悬空**不会**让任何判据变红（它只会让本来该豁免的那处旧词变成假红），"+
				"而「下一个人照着一条悬空的登记去找事实源」正是这类禁区登记最容易烂掉的方式。"+
				"请把锚重新指到那个落点上（按符号，不按路径）", w.anchor)
		case len(got) > 1:
			t.Fatalf("白名单锚 %q 命中 %d 个文件：%v——锚太宽。按锚登记的前提是「含这个符号的文件"+
				"**恰好一个**」；放宽了就等于多开一条豁免通道（换一个包放一个同符号的文件即可），"+
				"那比路径登记更松。请换一个只出现在目标文件里的符号", w.anchor, len(got), got)
		}
		if banned, _ := firstHitInAgentVocabulary(strippedSourceOrFatal(t, got[0])); banned == "" {
			t.Fatalf("白名单锚 %q 定位到 %s，但那里剥注释后**一个被守词都没有**——这条登记已经豁免不到"+
				"任何东西。留着的死登记挡不住任何东西，却会在下一个被撤的词写进来时**静默**顶掉判据："+
				"要么删掉它，要么把锚指到真正需要豁免的那一处", w.anchor, got[0])
		}
	}
}

// strippedSourceOrFatal 读文件并剥注释（`.go` 走 `stripLineComments`，其余走 `stripTSComments`），
// 读不到即 fatal。
//
// 剥注释是这几条词汇判据的**共同前提**：注释里写"v19 之前这里叫 agent_id"是**解释**，不是残留词汇
// （前端那批解释性注释正是这么写的）。抽出来共用，是因为"哪一半算源码"一旦在两处漂开，
// 漂掉的那一半就是静默的。本包的 `vault_vocabulary_v20_test.go` 也用它。
func strippedSourceOrFatal(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（fail-closed：判据失去对象）", path, err)
	}
	src := string(raw)
	if strings.HasSuffix(path, ".go") {
		return stripLineComments(src)
	}
	return stripTSComments(src)
}

// anchoredFiles 在扫描面里找出**含锚**的文件（按内容匹配，与路径无关；结果已排序）。
//
// 锚是"这个落点独有的符号"：文件搬家时符号跟着走，所以登记不会随目录改名而失效。它比路径登记
// **严**的地方有两处，都由调用方断言：① 恰好命中一个文件；② 那个文件里真的还有被守的东西
// （见 agentVocabularyWhitelistSelfCheck 与 `sessionIdentityUsageAnchor` 的用法）。
// 本包的 `vault_vocabulary_v20_test.go` 也用它。
func anchoredFiles(t *testing.T, files []string, anchor string) []string {
	t.Helper()
	var got []string
	for _, path := range files {
		if strings.Contains(strippedSourceOrFatal(t, path), anchor) {
			got = append(got, path)
		}
	}
	sort.Strings(got)
	return got
}

// TestNoAgentVocabulary 词汇收口（v19 的机器判据之一）。
//
// 扫描面 = `vocabularyScanFace`（backend 全部 Go 源码 `internal/**`+`pkg/**`、前端界面源码根、
// `packages/{types,api}/src`、`README.md`）+ **前端验收脚本**（`frontend/scripts/*.mjs`）。
//
// 为什么额外加上验收脚本：那是本仓"看过界面"的**唯一**路径（前端没有测试框架），而脚本里一条
// 已经删掉的 `/agents/...` 路径**不会报错**——它会让那个场景静默跳过或"碰巧绿"，正是本仓
// 2026-10-11 实测过的假绿形态（`cmd` 场景）。所以这里对它也要求零命中。
//
// 判据是**剥注释后**的零命中：注释里写"v19 之前这里叫 agent_id"是**解释**，不是残留词汇
// （前端那批解释性注释正是这么写的）。
//
// fail-closed：扫描面解析不到（文件数为 0）即失败——否则改名结束后这条守卫会静默空转，
// 而"空转的守卫"比没有守卫更坏（它给出虚假的安心）。白名单自己的悬空/过宽/死登记同样判失败
// （见 agentVocabularyWhitelistSelfCheck）：那三种烂法今天都不会让任何判据变红。
func TestNoAgentVocabulary(t *testing.T) {
	root, files := vocabularyScanFace(t)
	if len(files) < 100 {
		t.Fatalf("扫描面只拿到 %d 个文件——路径写错了？（fail-closed：解析不到即失败）", len(files))
	}

	scripts, err := filepath.Glob(filepath.Join(frontendRoot(t), "scripts", "*.mjs"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("扫不到 frontend/scripts/*.mjs（%v）——验收脚本那一半判据会静默空转（fail-closed）", err)
	}
	files = append(files, scripts...)

	// 先自检登记，再扫判据：登记烂了的话，"绿"是假的。
	agentVocabularyWhitelistSelfCheck(t, files)

	// 判据自检：文档分册路径必须放行，真正的 `/agents/...` 必须仍被判红——两者共用
	// firstHitInAgentVocabulary，这条自检钉的正是上面那条占位替换的边界（漂了当场红）。
	for _, tc := range []struct{ src, want string }{
		{"见 `docs/agents/build-and-runtime.md`", ""},
		{"POST /agents/import", "/agents/"},
	} {
		if got, _ := firstHitInAgentVocabulary(tc.src); got != tc.want {
			t.Fatalf("agent 词汇判据自检失败：%q 的命中 = %q，期望 %q——占位替换的边界漂了", tc.src, got, tc.want)
		}
	}

	scanned := 0
	var hits []string
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("相对路径: %v", err)
		}
		relSlash := filepath.ToSlash(rel)
		src := strippedSourceOrFatal(t, path)
		if allowedByAgentVocabularyWhitelist(t, relSlash, src) {
			continue
		}
		scanned++
		if banned, idx := firstHitInAgentVocabulary(src); banned != "" {
			line := 1 + strings.Count(src[:idx], "\n")
			hits = append(hits, relSlash+":"+itoa(line)+" 命中 "+banned)
		}
	}
	if scanned == 0 {
		t.Fatal("一个文件都没扫到（fail-closed）")
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("v19 起 agent 概念整体并入 runtime（底座构建期固定，用户不可自定义），"+
			"相关的表/列/类型/包/路由/键都已撤掉。\n"+
			"以下位置剥注释后仍出现被撤掉的标识符（确需保留的，登记进 agentVocabularyWhitelist"+
			" **并写明理由**——例如“这是历史那一版的 DDL”或“这是别人的原文”；空理由同样红）\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// sessionIdentityFiles 是落点**稳定**的那几处会话身份落点（相对 backend 根）。
//
// 本次分层重构不动 `internal/persistence/model`、`internal/persistence/dao`、`internal/persistence/store`（设计 §3.4），所以它们按路径
// 登记是安全的；会搬家的那一处（用量聚合）走 sessionIdentityUsageAnchor。
//
// 注意 `internal/persistence/store/migrate.go` 是 P0 补进来的：下面 TestSessionIdentityIsRuntimeOnly 里那条
// "必须真的有一条 `DROP COLUMN agent_id`"的分支此前**从未执行**过（那张表里一直没有它，
// 分支是死代码——实测把期望值改成不存在的串，测试照样绿），而这条正向断言正是 migrate.go 被
// 白名单放行之后唯一的补偿。删掉迁移里那行语句不会有任何别的测试变红。
var sessionIdentityFiles = []string{
	filepath.Join("internal", "persistence", "model", "session.go"),
	// v3.3 起写面（upsert 的列集合）从 dao 搬到 store：判据必须跟着实现走，否则它会在
	// 只剩列词汇的 dao/session.go 上静默空转。
	filepath.Join("internal", "persistence", "store", "sessions.go"),
	filepath.Join("internal", "persistence", "store", "usage.go"),
	filepath.Join("internal", "persistence", "store", "migrate.go"),
}

// sessionIdentityUsageAnchor 定位第四处落点「用量聚合」。
//
// 它**只有锚、没有路径**：用量聚合那一份在 P0 时点住在 `manager` 包的 `usage.go` 里，
// 方案 §3.4 的去向是 `domain/session/`（文件去向对照表）。路径登记在搬家当天就会失效——而失效的
// 表现是 `os.ReadFile` 失败、守卫红得莫名其妙，逼着人回来改（P0 的全部目的就是让后面 11 批不必再动
// 守卫）。锚是那个文件独有的口径常量，跟着文件走。锚命中不是恰好一个即 fatal。
const sessionIdentityUsageAnchor = "usageContextSemantics"

// TestSessionIdentityIsRuntimeOnly 会话身份只剩 `runtime_id`（v19 的机器判据之二）。
//
// 为什么单列一条（而不是只靠 TestNoAgentVocabulary）：那条守卫靠**白名单放行**了 `migrate.go`，
// 而"身份只剩 runtime_id"这件事必须**正向**成立——白名单文件里恰好写着 `DROP COLUMN agent_id`，
// 零命中判据在那里是无效的。所以这条走**逐文件结构判据**：
//
//  1. 实体（`model/session.go`）：必须有 `column:runtime_id`，不得有 `agent_id`/`AgentID`；
//  2. DAO（`dao/session.go`）：列映射里不得有 agent 身份；
//  3. 迁移（`store/migrate.go`）：必须**真的**有一条 `ALTER TABLE sessions DROP COLUMN agent_id`
//     ——删掉这条语句不会有任何别的测试变红，而用户库里的那一列会一直留着；
//  4. 用量聚合（`store/usage.go` + 用量那一份）：不得再按 agent 分组/回填（照旧按 runtime）。
//
// fail-closed：任一落点读不到、锚命中不是恰好一个、或实体里找不到 `column:runtime_id`，
// 都当场判失败——"改名/搬迁后这条守卫静默空转"是它唯一危险的失效方式。
func TestSessionIdentityIsRuntimeOnly(t *testing.T) {
	backend := backendRoot(t)
	faceRoot, files := vocabularyScanFace(t)

	// 落点 = 稳定的路径 + 一处按锚定位的（报错文案里给出 rel，判据作用在绝对路径上）。
	type identityTarget struct {
		rel  string
		path string
		// kind：该落点额外要求的**正向**判据（"" = 只查"不得有 agent 身份"）。
		kind string
	}
	targets := make([]identityTarget, 0, len(sessionIdentityFiles)+1)
	for _, rel := range sessionIdentityFiles {
		kind := ""
		switch rel {
		case filepath.Join("internal", "persistence", "model", "session.go"):
			kind = "entity"
		case filepath.Join("internal", "persistence", "store", "migrate.go"):
			kind = "migration"
		}
		targets = append(targets, identityTarget{rel: rel, path: filepath.Join(backend, rel), kind: kind})
	}
	anchored := anchoredFiles(t, files, sessionIdentityUsageAnchor)
	if len(anchored) != 1 {
		t.Fatalf("用量聚合落点的锚 %q 命中 %d 个文件：%v——期望**恰好一个**。"+
			"0 个 = 那个文件没了、或口径常量被改名（fail-closed：本条判据会静默失去对象）；"+
			"多于一个 = 锚太宽（换一个文件写同一行就能绕开「不得再出现 agent 身份」）",
			sessionIdentityUsageAnchor, len(anchored), anchored)
	}
	usageRel, err := filepath.Rel(faceRoot, anchored[0])
	if err != nil {
		t.Fatalf("相对路径: %v", err)
	}
	targets = append(targets, identityTarget{rel: filepath.ToSlash(usageRel), path: anchored[0]})

	for _, tg := range targets {
		raw, err := os.ReadFile(tg.path)
		if err != nil {
			t.Fatalf("读不到 %s（%v）——fail-closed：判据失去对象", tg.rel, err)
		}
		src := stripLineComments(string(raw))

		// ③ 迁移是正向判据（白名单文件里必然有旧列名），单独处理。
		if tg.kind == "migration" {
			const want = "ALTER TABLE sessions DROP COLUMN agent_id"
			if !strings.Contains(src, want) {
				t.Fatalf("v19 的迁移里找不到 `%s`——删掉它不会有别的测试变红，"+
					"而用户库里的 `sessions.agent_id` 会一直留着（身份没真收口）", want)
			}
			continue
		}

		for _, banned := range []string{"agent_id", "AgentID", "agentID"} {
			if idx := strings.Index(src, banned); idx >= 0 {
				line := 1 + strings.Count(src[:idx], "\n")
				t.Fatalf("v19 起会话身份只有 `runtime_id`，但 %s:%d 仍出现 %q——"+
					"agent 概念已整体并入 runtime（见 docs/decisions.md「决策 v19」）",
					tg.rel, line, banned)
			}
		}

		// R1：实体那一份要**正向**证明身份还在（只看"没有 agent"是不够的——把 runtime_id
		// 一起删掉，零命中判据照样绿）。
		if tg.kind == "entity" {
			if !strings.Contains(src, "column:runtime_id") {
				t.Fatalf("%s 里找不到 `column:runtime_id`——会话身份必须留在实体上（fail-closed：解析不到即失败）", tg.rel)
			}
		}
	}
}
