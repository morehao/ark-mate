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
// （SDK 的 `Agent` 接口、`AuthMethods` 的 `agent` 变体），`pkg/acl` 必须能照原文写；`~/.agents/skills`
// 是平台与底座的**共享投放根**，也与本概念无关。所以禁词表列的是**被撤掉的那批标识符**，
// 与 v15 的 `conversation`、v16 的 `capability_revision`、v17 的 `provider_session` 同形。

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
	//    `AgentID`/`agentID`（小写化后同形）。`pkg/procreg` 与 `pkg/mcp` 的网关 scope 曾用它，
	//    而它们**不会让编译变红**——正是这条守卫存在的理由。
	"agent_id", "agentid",
	// ⑧ 路由段。刻意**不**写裸词 `agents`：`~/.agents/skills` 与旧的 `MATE_AGENTS` 都要放行。
	//    `/agents/` 只匹配**斜杠包围**的形态（`.agents/skills` 里 `agents` 前面是点，不命中）。
	"mate/agents", "/agents/",
}

// agentVocabularyWhitelist 是**允许**出现上述旧词的扫描点（相对仓库根路径前缀）。
//
// 与 conversation / capability_revision 的白名单同一纪律：逐条写明理由（空理由同样判红）。
// 白名单是"这次确实改不了/不该改"的登记簿，不是"懒得改"的垃圾桶。
var agentVocabularyWhitelist = []struct {
	path   string
	reason string
}{
	{
		path: "backend/internal/store/migrate.go",
		reason: "迁移内核纪律：**追加迁移 = 在尾部新增，绝不回改历史**。所以这个文件里必然有两类旧词——" +
			"① v1–v18 的历史 DDL（建 `agents` 表、`sessions.agent_id` 列、`definition_revisions` 的 " +
			"`kind='agent'` 行）；② v19 自己的收口语句，它必须写**当时那一版**的名字才能撤掉它们：" +
			"`DROP TABLE IF EXISTS agents` / `DELETE ... WHERE kind='agent'` / " +
			"`DELETE FROM meta WHERE key IN ('default_agent','imported_agents')` / " +
			"`ALTER TABLE sessions DROP COLUMN agent_id`",
	},
	{
		path: "backend/internal/store/migrate_test.go",
		reason: "「停在 v19 之前」的历史迁移用例要用裸 SQL 造/读当时那一版的表与列" +
			"（DAO 已不认 `agents` 表与 `agent_id` 列，读不到历史形态），再断言 v19 之后它们**真的没了**" +
			"——改用新名字写夹具的话，这条用例就只是在验证“新形态能跑”，对迁移本身零覆盖。" +
			"这是守卫⑤ 那条纪律的测试侧镜像（同 `readLegacyConversation`）",
	},
	{
		path: "backend/internal/store/migrate_v19_test.go",
		reason: "同上的 v19 专项：夹具必须停在 v18 的形态（`agents` 行 + `sessions.agent_id`），" +
			"否则测不出 DROP COLUMN 与 DROP TABLE 的效果",
	},
	{
		path:   "backend/internal/store/migrate_v15_test.go",
		reason: historicalAgentFixtureReason,
	},
	{
		path:   "backend/internal/store/migrate_v16_test.go",
		reason: historicalAgentFixtureReason,
	},
	{
		path:   "backend/internal/store/migrate_v17_test.go",
		reason: historicalAgentFixtureReason,
	},
	{
		path: "backend/internal/router/router_test.go",
		reason: "反向断言「README 不得出现历史名 `/agents…`」的**判据字面量**本身——" +
			"这种守卫必须写出它禁的那个词，扫自己必然自证其罪（与 conversation 白名单里同一条理由同形）",
	},
	{
		path: "backend/pkg/config/config.go",
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
		path: "backend/internal/manager/reject_removed_keys.go",
		reason: removedKeyRefusalReason + "**拒绝**必须点名它拒的是哪个键，否则用户拿到的" +
			"只是一句「参数不对」，改不动",
	},
	{
		path: "backend/internal/manager/sessions.go",
		reason: removedKeyRefusalReason + "调用点要把接住的那个 raw 值交给拒绝函数——" +
			"这次交接在 Go 里至少要拼一次字段名（DTO 与拒绝面各自已登记，这里是第三处）",
	},
	{
		path: "backend/internal/manager/automation.go",
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
		path: "backend/internal/controller/ctrusage/usage.go",
		reason: removedKeyRefusalReason + "控制器要**点名**它拒的是哪个查询参数（与同文件里 " +
			"`group_by` 只认 `day` 的那条校验口径一致），否则用户只拿到一句「参数不对」，改不动",
	},
	{
		path: "backend/internal/manager/transcript_load_test.go",
		reason: "**磁盘上真实存在过的历史产物**：v19 之前的 transcript header 里就有 `agent_id` 字段" +
			"（它和 `runtime` 并排出现），用户机器上的旧 transcript 文件将来也要靠这条路径读回来。" +
			"夹具按当时那一版写，测的才是「旧文件还能装载」而不是「新 header 能解析」",
	},
	{
		path: "backend/internal/manager/automation_import_test.go",
		reason: "**停在 v19 之前的自动化定义夹具**：`target.agent_id` 是当年用户写在文件里的键，" +
			"这条用例测的正是「导入旧定义时把已下线的键清掉并 WARN」——它必须写出旧键，否则测不到清除",
	},
	{
		path: "backend/internal/manager/usage_removed_dimension_test.go",
		reason: negativeAssertionReason + "这条用例专门钉「用量面收到已下线的筛选键 → 40001 " +
			"`agent_selection_removed`」，构造请求就必须写出那个键",
	},
	{
		path: "backend/internal/router/rest_removed_agent_key_test.go",
		reason: negativeAssertionReason + "传输层这一半钉的是相反的义务：「已下线的键**原样透传**给 " +
			"service，不由控制器自作聪明地丢掉」（丢弃会让用户以为筛选生效了）——判据里必然出现键名",
	},
	{
		path: "backend/internal/manager/usage_test.go",
		reason: "反向断言的**判据字面量**：这条用例专门证明「带 `agent_id` → 40001 " +
			"`agent_selection_removed`」，它必须写出那个被撤掉的键名才能构造请求（与 `router_test.go` " +
			"里禁历史名的理由同形——守卫扫自己必然自证其罪）",
	},
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
// 清掉了什么。四处都是"指出历史"而不是"使用旧词"，与 `pkg/config` 的改名提示同形。
const removedKeyRefusalReason = "**显式拒绝面**："

// historicalAgentFixtureReason 是"停在 v19 之前"那批历史迁移夹具的共用理由（v15/v16/v17 各一份）。
//
// 为什么它们必须写出 `agent_id`：那三份夹具要在**当时那一版**的库上造行（v14 的 `conversations`、
// v16/v17 的 `sessions`），而 `agent_id` 在 v19 之前**是那些表的列**——改用今天的列集合写夹具，
// 测的就不再是"迁移能把老库带过来"，而是"新表能存东西"，对迁移本身零覆盖。
const historicalAgentFixtureReason = "「停在 v19 之前」的历史迁移用例：夹具要在**当时那一版**的表上" +
	"写裸 SQL（`conversations`/`sessions` 在 v19 之前都有 `agent_id` 列），再断言迁移之后它真的没了" +
	"（迁移内核纪律：夹具按当时那一版写，绝不回改历史；同 conversation/capability_revision 的测试侧镜像）"

// allowedByAgentVocabularyWhitelist 命中白名单即跳过（前缀匹配，便于豁免整棵目录）。
func allowedByAgentVocabularyWhitelist(t *testing.T, rel string) bool {
	t.Helper()
	for _, w := range agentVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %q 的理由为空——白名单是登记簿，不是垃圾桶", w.path)
		}
		if strings.HasPrefix(rel, w.path) {
			return true
		}
	}
	return false
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
// 而"空转的守卫"比没有守卫更坏（它给出虚假的安心）。
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

	scanned := 0
	var hits []string
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("相对路径: %v", err)
		}
		relSlash := filepath.ToSlash(rel)
		if allowedByAgentVocabularyWhitelist(t, relSlash) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", rel, err)
		}
		src := string(raw)
		if strings.HasSuffix(path, ".go") {
			src = stripLineComments(src)
		} else {
			src = stripTSComments(src)
		}
		scanned++
		lower := strings.ToLower(src)
		for _, banned := range legacyAgentVocabulary {
			if idx := strings.Index(lower, banned); idx >= 0 {
				line := 1 + strings.Count(src[:idx], "\n")
				hits = append(hits, relSlash+":"+itoa(line)+" 命中 "+banned)
				break
			}
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

// 会话身份的落点：实体 / DAO / 迁移 / 用量聚合。少查一处，那一处就能把 `agent_id` 带回来而不响。
var sessionIdentityFiles = []string{
	filepath.Join("internal", "model", "session.go"),
	filepath.Join("internal", "dao", "session.go"),
	filepath.Join("internal", "store", "usage.go"),
	filepath.Join("internal", "manager", "usage.go"),
}

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
//  4. 用量聚合（`store/usage.go` + `manager/usage.go`）：不得再按 agent 分组/回填（照旧按 runtime）。
//
// fail-closed：任一文件读不到、或实体里找不到 `column:runtime_id`，都当场判失败——"改名/搬迁后
// 这条守卫静默空转"是它唯一危险的失效方式。
func TestSessionIdentityIsRuntimeOnly(t *testing.T) {
	root := backendRoot(t)

	for _, rel := range sessionIdentityFiles {
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s（%v）——fail-closed：判据失去对象", rel, err)
		}
		src := stripLineComments(string(raw))

		// ③ 迁移是正向判据（白名单文件里必然有旧列名），单独处理。
		if rel == filepath.Join("internal", "store", "migrate.go") {
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
					"agent 概念已整体并入 runtime（见 docs/design-and-roadmap.md「决策变更 v19」）",
					rel, line, banned)
			}
		}

		// R1：实体那一份要**正向**证明身份还在（只看"没有 agent"是不够的——把 runtime_id
		// 一起删掉，零命中判据照样绿）。
		if rel == filepath.Join("internal", "model", "session.go") {
			if !strings.Contains(src, "column:runtime_id") {
				t.Fatalf("%s 里找不到 `column:runtime_id`——会话身份必须留在实体上（fail-closed：解析不到即失败）", rel)
			}
		}
	}
}
