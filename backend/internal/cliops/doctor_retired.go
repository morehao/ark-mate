package cliops

import (
	"context"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/assembly"
	"github.com/morehao/ark-mate/internal/config"
	doctorfacts "github.com/morehao/ark-mate/internal/mechanism/doctor"
)

// knownRuntimeIDs 在册 runtime id 全集：**全部内置适配** ∪ `runtimes.json` 的远程端点。
//
// 两条边都要，缺哪一条都会假红，而方向不同：
//   - 少了内置那半 → 在册的内置底座被当成"已下线"，报告把正常目录列成遗留；
//   - 少了远程那半 → 用户自己配的远程底座被当成"遗留"，同样假红。
//
// 内置那半取 `assembly.BuiltinSpecs()`（**全部**），理由与 `checkRetiredSection` 文件头第 ① 条一致。
func knownRuntimeIDs(cfg config.Config) []string {
	specs := assembly.BuiltinSpecs()
	ids := make([]string, 0, len(specs)+len(cfg.RemoteRuntimes))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	for _, rt := range cfg.RemoteRuntimes {
		ids = append(ids, rt.ID)
	}
	return ids
}

// 本文件是 `mate doctor` 的**存量体检**那一段（v18：第二个内置适配下线时新增）。
//
// 它回答的问题只有一个：**"这台机器上还留着哪些*不在册*底座的痕迹"**。为什么要有它：
// 删掉一个适配不会让磁盘与库里的存量消失，而 v18 拍板的口径是"读面宽容 + 不做破坏性迁移 +
// 存量要能被看见"（见决策 v18）。在此之前那批存量是**完全静默**的——`mate doctor` 逐底座采
// 事实的那两段（`collectRuntimeModelFacts` / `collectSensitiveFiles`）都按**注册表**枚举，
// 适配一删，那个底座在报告里就整段消失了：报告一切正常，而磁盘上的东西一个没少。
//
// 判据是「**在不在册**」，不是「它是不是某某」——事实由 `doctorfacts.CollectRetiredRuntimeFacts`
// 采集（那一侧连一个底座名都没有，理由写在该文件头：内核不许认识具体底座）。本文件只做两件事：
// 给出**在册集合**（判据的输入），把事实翻成四档人话。
//
// ## 在册集合从哪来（这是本段唯一的装配知识）
//
// ① **全部内置适配**（`AllBuiltinSpecs`，不是产品装配用的那一个）——与逐底座采事实的两段同口径：
// 构建期把产品钉在某一个上，不代表磁盘上没有别的底座的遗留；
// ② `runtimes.json` 的**远程端点**——它们是这台机器上**被声明为可用**的 id，留着它们的目录
// 不算遗留（不并进来会把用户自己配的远程底座报成"已下线底座的残留"）。
//
// ## 三档结论（沿用本命令"无法校验记警告"的既有口径）
//
//	没扫到 / 没读到存量   → ok（问题不存在，或者确实没有）
//	扫到存量              → **warn**：它不影响启动、不影响历史会话可读，所以**不是 fail**；
//	                        但每一条都给出"下一步该处置什么"
//	判据失效 / 读不动     → warn（在册集合空、目录读不动、库读不出来）——**不许**当成"没有"
//
// 为什么刻意不判 fail：存量是**事实**，不是"事实与声明不符"。把"磁盘上有个旧目录"判成失败会
// 让带存量数据的部署每次 `mate doctor` 都红，而唯一能变绿的动作是**删数据**——那正是用户明确
// 不要的（决策 v18：不做破坏性迁移）。
func checkRetiredSection(opts doctorOptions) []doctorCheck {
	// 与 provider 段共用同一个只读句柄开法（`mode=ro`、不跑迁移）；库读不出来时**不重复**报一条
	// 同名的说明——那一条由 provider 段给出（`openDoctorStore` 的结论），本段只用 `st == nil`
	// 把"无从校验"如实体现在各自的事实里（见 `CollectRetiredRuntimeFacts`）。
	st, _ := openDoctorStore(opts)
	if st != nil {
		defer func() { _ = st.Close() }()
	}
	facts := doctorfacts.CollectRetiredRuntimeFacts(context.Background(), st, doctorfacts.RetiredRuntimeOptions{
		Home:            opts.Home,
		KnownRuntimeIDs: opts.KnownRuntimeIDs,
		RuntimeSpecs:    assembly.BuiltinSpecs(),
		ProjectionDirs:  opts.ProjectionDirs,
	})
	return renderRetiredChecks(facts)
}

// renderRetiredChecks 把存量事实翻成检查项（纯函数，便于单测直接喂事实）。
func renderRetiredChecks(facts doctorfacts.RetiredRuntimeFacts) []doctorCheck {
	checks := []doctorCheck{
		checkRetiredHomeDirs(facts),
		checkRetiredSessions(facts),
		checkRetiredSkills(facts),
		checkRetiredProjectionDirs(facts),
	}
	// 「无法校验」与「判据失效」单列一条：它们不改变上面几条的结论，但决定那些结论**能不能信**。
	// 只在真的出现时输出（否则每次报告都多一行恒定的噪声——那是让人开始忽略报告的第一步）。
	if notes := retiredScanNotes(facts); len(notes) > 0 {
		checks = append(checks, doctorCheck{"retired scan", doctorWarn, strings.Join(notes, "；")})
	}
	return checks
}

// retiredScanNotes 汇总"无法校验"与"判据失效"，并给出各自的可执行出路。
func retiredScanNotes(facts doctorfacts.RetiredRuntimeFacts) []string {
	notes := append([]string{}, facts.Notes...)
	if len(facts.KnownRuntimeIDs) == 0 {
		// 判据失效是**响亮的错**：在册集合空 ⇒ 每个目录都会被算成遗留 ⇒ 报告必然假红。
		notes = append(notes, "**判据失效**：在册 runtime 集合为空（这台机器的注册表与 `runtimes.json` "+
			"都没给出 id），因此上面把**每个**隔离目录都算成了遗留——这条结论不可信，请当缺陷报")
	}
	return notes
}

// checkRetiredHomeDirs 磁盘上的遗留隔离目录（`<MATE_HOME>/runtime/<不在册 id>/`）。
//
// 为什么这一条最重要：**底座的配置与状态就在这个目录里**，其中可能包括它的原生凭据文档
// （明文密钥，与 `mate.db` 同等看待）。用户删掉平台适配之后，这些东西还在盘上——报告要做的
// 是把它指出来（含顶层条目清单，让人一眼看出里面是什么），**不由平台自动删**。
func checkRetiredHomeDirs(facts doctorfacts.RetiredRuntimeFacts) doctorCheck {
	const name = "retired runtime dirs"
	switch {
	case facts.HomeScan == "no_home":
		return doctorCheck{name, doctorWarn, "未配置数据目录（MATE_HOME）：隔离根无从扫描，**无法校验**"}
	case strings.HasPrefix(facts.HomeScan, "unreadable"):
		return doctorCheck{name, doctorWarn, fmt.Sprintf("%s：该根下的遗留**无法校验**（不当作没有）", facts.HomeScan)}
	case facts.HomeScan == "missing":
		return doctorCheck{name, doctorOK, fmt.Sprintf(
			"隔离根不存在（扫过 %s）：本机还没让平台拉起过任何底座，因此不可能有遗留",
			bracketList(facts.IsolationRoots))}
	}
	if len(facts.HomeDirs) == 0 {
		return doctorCheck{name, doctorOK, fmt.Sprintf(
			"%d 个隔离根下没有不在册底座的目录（在册：%s）", len(facts.IsolationRoots), bracketList(facts.KnownRuntimeIDs))}
	}
	parts := make([]string, 0, len(facts.HomeDirs))
	for _, d := range facts.HomeDirs {
		entries := "（目录为空）"
		switch {
		case d.More < 0:
			entries = "（目录内容读不动，无法列出）"
		case len(d.Entries) > 0:
			entries = bracketList(d.Entries)
			if d.More > 0 {
				entries += fmt.Sprintf(" 等 %d 项", len(d.Entries)+d.More)
			}
		}
		parts = append(parts, fmt.Sprintf("`%s`（%s）%s", d.Path, d.Runtime, entries))
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf(
		"%d 个不在册底座的遗留隔离目录：%s；里面有它的原生凭据文档时那是**明文密钥**（与 `mate.db` 同等看待）。"+
			"确认不再需要之后手工删除这些目录——平台**不自动删**（决策 v18：不做破坏性迁移）",
		len(facts.HomeDirs), strings.Join(parts, "；"))}
}

// checkRetiredSessions 历史会话里指向不在册底座的执行事实。
//
// 后果如实写出来，因为它是**读面正常、用到才报错**这一类里的典型：会话照常列、历史照常读，
// 只有下一轮 prompt 才会 `runtime_not_found`——不提前说，用户会以为是"这条会话坏了"。
func checkRetiredSessions(facts doctorfacts.RetiredRuntimeFacts) doctorCheck {
	const name = "retired sessions"
	if facts.SessionScan != "ok" {
		return doctorCheck{name, doctorWarn, fmt.Sprintf(
			"会话面 %s：**无法校验**（不当作没有）——续聊时才发现存量缺失是最坏的一种", facts.SessionScan)}
	}
	if facts.SessionRefCount == 0 {
		return doctorCheck{name, doctorOK, "没有会话的 `runtime_id` 指向不在册底座"}
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf(
		"%d 条会话的 `runtime_id` 指向不在册底座（样本：%s）；这些会话**读得到**（列表与历史正常），"+
			"但**发消息会报 `runtime_not_found`**——平台不替它猜一个底座接着跑（静默换底座比报错更坏）。"+
			"要接着用就新开一个会话（历史可用导出/回落 memo 带过去），要清理则按平台自有接口归档/删除",
		facts.SessionRefCount, bracketList(retiredSampleIDs(facts.SessionSamples)))}
}

// checkRetiredSkills skill 定义里声明给不在册底座的运行面。
//
// 后果：候选集为空 ⇒ 这条 skill **永远不会被投放**，而它在界面上看起来只是"一条普通技能"。
func checkRetiredSkills(facts doctorfacts.RetiredRuntimeFacts) doctorCheck {
	const name = "retired skills"
	if facts.SkillScan != "ok" {
		return doctorCheck{name, doctorWarn, fmt.Sprintf(
			"定义面 %s：**无法校验**（不当作没有）", facts.SkillScan)}
	}
	if len(facts.SkillRefs) == 0 {
		return doctorCheck{name, doctorOK, "没有 skill 定义的 `runtimes[]` 指向不在册底座"}
	}
	parts := make([]string, 0, len(facts.SkillRefs))
	for _, r := range facts.SkillRefs {
		parts = append(parts, fmt.Sprintf("%s→%s", r.ID, r.Runtime))
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf(
		"%d 处 skill 的 `runtimes[]` 指向不在册底座（%s）：该 skill 不会被投放给任何底座"+
			"（候选集为空），而界面上它看起来只是一条普通技能——改 `runtimes` 或删掉这条定义即可",
		len(facts.SkillRefs), bracketList(parts))}
}

// checkRetiredProjectionDirs 配置里的投影覆盖指向不在册底座（键形如 `<runtime>:<kind>`）。
//
// 这类键**形状合法、所以配置解析不报错**，只是永远匹配不上——"我配了投影目录、技能却没投放"
// 在这里就能被解释掉。
func checkRetiredProjectionDirs(facts doctorfacts.RetiredRuntimeFacts) doctorCheck {
	const name = "retired projection dirs"
	if len(facts.ProjectionRefs) == 0 {
		return doctorCheck{name, doctorOK, "没有投影覆盖指向不在册底座"}
	}
	parts := make([]string, 0, len(facts.ProjectionRefs))
	for _, r := range facts.ProjectionRefs {
		parts = append(parts, fmt.Sprintf("%s→%s", r.ID, r.Runtime))
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf(
		"%d 处投影覆盖（`MATE_PROJECTION_DIRS` / 配置文件）指向不在册底座（%s）：这些覆盖**永远不会生效**，"+
			"而配置解析不会因此报错——删掉它们，或把底座名改成在册的那个",
		len(facts.ProjectionRefs), bracketList(parts))}
}

// retiredSampleIDs 取出样本里的会话 id（升序来自采集侧）。
func retiredSampleIDs(refs []doctorfacts.RetiredRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.ID)
	}
	return out
}
