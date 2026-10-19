package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	goruntime "github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/store"
)

// ——— 「不在册底座」的存量体检（v18：第二个内置适配下线时新增） ———
//
// 背景：v18 删掉了一个内置适配。**删掉适配不会让磁盘与库里的存量消失**——用户 2026-10-12
// 拍板的口径是：读面宽容（历史会话照常列、照常读历史）、**不做破坏性迁移**，但存量要**能被看见**。
// 本文件就是那个"看得见"：回答"这台机器上还留着哪些**不在册**底座的痕迹"，以及"下一步该处置什么"。
//
// # 为什么判据是「不在册」，不是写死某个 id
//
// 这是本文件唯一要紧的设计决定，三条理由：
//   - **内核不许认识任何具体底座**。写死 id 会被 `TestInternalHasNoRuntimeIDSpecialCase` 判红，
//     而那条守卫的意图正是这个：否则每下线一个适配就要在内核里长一处特判，而那恰恰是
//     "没有 adapter 层"的定义（v18 收口删掉的那个 `switch runtimeID` 就是这么长出来的）。
//   - 写死 id 只能看见**这一次**下线的那个，下一次下线/换 id 要再改一次内核；判据取"在册集合"
//     之后，它**自动覆盖未来任何一次下线**，且接新底座时零改动。
//   - 在册集合由**调用方**给出（`RetiredRuntimeOptions.KnownRuntimeIDs`：注册表里的全部内置 id
//     ∪ `runtimes.json` 的远程端点）。内核连"在册集合从哪来、为什么是这些"都不需要知道。
//
// 于是本文件里**没有任何底座名，也没有任何底座配置文件名的字面量**（A9 禁词表同样钉着 `internal/**`）。
// 将来要加一条新的"存量痕迹"判据时，请沿用这个形状：**问"它在不在册"，不要问"它是不是某某"**。
//
// # 判据的失效方式（写下来，因为它不响）
//
// 这一段的结论是"没有遗留"时，看报告的人无法区分下面两件事：
//   - 真的没有遗留；
//   - 判据本身没生效（在册集合给错、隔离根位置变了、目录读不动被当成空）。
//
// 所以三处刻意做成"能自证"：① 报告里**印出在册集合**（判据错了看得见）；② 隔离根位置
// **从在册声明推**（换根时本判据自动跟上，而不是继续扫一个已经不用的目录）；③ 读不动一律记
// `unreadable`/`Notes`，**绝不**把"读不到"当成"没有"。

// retiredEntriesCap 每个遗留目录最多列出多少个顶层条目（报告给人看，不是 `du`）。
const retiredEntriesCap = 12

// retiredSamplesCap 会话样本最多列几条（条数另有 `SessionRefCount` 如实给出）。
const retiredSamplesCap = 10

// RetiredRuntimeOptions 存量体检的输入。
type RetiredRuntimeOptions struct {
	// Home mate home（隔离根在它下面）。trim 后为空 = 无法扫描，如实记 `no_home`。
	Home string
	// KnownRuntimeIDs **在册**的 runtime id 集合（空 = 判据失效，全部目录都会被算成遗留）。
	KnownRuntimeIDs []string
	// RuntimeSpecs 在册底座的声明（v3.1：机制层不 import adapter；装配根交下来）。
	// 空 = 只用"历史约定"那一档推隔离根。
	RuntimeSpecs []goruntime.Spec
	// ProjectionDirs 进程配置里的投影覆盖（键形如 `<runtime>:<kind>`；见 `internal/config`）。
	//
	// 传进来而不是在这里读一遍环境变量：体检的事实来源必须与运行期同一条解析链
	// （`config.Load`），自己再解析一遍等于把"看到的状态不是真的"引回来。
	ProjectionDirs map[string]string
}

// RetiredRuntimeFacts 「不在册底座」的存量事实（`mate doctor --json` 的 `retiredRuntime` 段）。
//
// 所有列表**已排序且非 nil**（空 = `[]`，机器消费方不必区分 null 与空）——与 `ProviderDoctorFacts`
// 同一条确定性纪律。
type RetiredRuntimeFacts struct {
	// KnownRuntimeIDs 本次判据用的在册集合（升序）。**印出来是为了让判据本身可核对**：
	// 报告说"没有遗留"而集合里恰好不包含某个还在用的 id 时，看报告的人应当能发现判据给错了。
	KnownRuntimeIDs []string `json:"knownRuntimeIds"`
	// IsolationRoots 本次实际扫过的隔离根（升序，绝对路径）。
	IsolationRoots []string `json:"isolationRoots"`
	// HomeScan 扫描结论：ok / missing（一个根都不存在）/ unreadable: <原因> / no_home。
	HomeScan string `json:"homeScan"`
	// HomeDirs 不在册底座的遗留隔离目录（升序）。
	HomeDirs []RetiredHomeDir `json:"homeDirs"`
	// SessionScan 会话面的读面结论：ok / unreadable / no_store。
	//
	// **必须逐面给**（与 `HomeScan` 同形）：用一个"库读得动吗"的全局判据会让**单面**读失败
	// （例如会话表读不动、skill 表正常）落进"没有存量"——那是这一段最坏的假绿形态，
	// 而且它恰恰只在出问题的那台机器上出现。
	SessionScan string `json:"sessionScan"`
	// SkillScan 定义面的读面结论（同上）：ok / unreadable / no_store。
	SkillScan string `json:"skillScan"`
	// SessionRefCount `sessions.runtime_id` 指向不在册 id 的行数（0 = 没有这类存量）。
	//
	// 后果如实写：这些会话**读得到**（列表、历史事件、导出都正常），但**发消息会报
	// `runtime_not_found`**——平台不替它猜一个底座接着跑（那是"静默换底座"，比报错更坏）。
	SessionRefCount int `json:"sessionRefCount"`
	// SessionSamples 上面那些会话的样本（最多 `retiredSamplesCap` 条，升序）。
	SessionSamples []RetiredRef `json:"sessionSamples"`
	// SkillRefs skill 定义里 `runtimes[]` 含不在册 id 的（升序，逐 (skill, runtime) 一条）。
	//
	// 后果：该 skill 不会被投放给任何底座（候选集为空），而它在界面上看起来只是"一条普通技能"。
	SkillRefs []RetiredRef `json:"skillRefs"`
	// ProjectionRefs 配置里的投影覆盖指向不在册 id 的（升序）。它们**永远不会被匹配上**，
	// 而配置解析不会因此报错（键的形状是合法的）。
	ProjectionRefs []RetiredRef `json:"projectionRefs"`
	// Notes 无法校验处的人话说明（每条对应一处"读不到"）；空 = 三处都读到了。
	Notes []string `json:"notes"`
}

// RetiredHomeDir 一个不在册底座的遗留隔离目录。
type RetiredHomeDir struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	// Entries 顶层条目名（升序，最多 `retiredEntriesCap` 条）。列出来是为了让人**一眼看出
	// 该不该删**：里面可能就有那个底座的原生凭据文档（明文密钥，与 `mate.db` 同等看待）。
	Entries []string `json:"entries"`
	// More 被截掉的条目数（`Entries` 满了才有意义）。
	More int `json:"more"`
}

// RetiredRef 一条指向不在册底座的引用。
type RetiredRef struct {
	// Kind 引用来自哪一面：session / skill / projection_dir。
	Kind string `json:"kind"`
	// ID 平台侧那条记录的 id（投影覆盖则是完整的配置键）。
	ID string `json:"id"`
	// Runtime 该引用里出现的那个**不在册**的 id。
	Runtime string `json:"runtime"`
}

// CollectRetiredRuntimeFacts 采集「不在册底座」的存量事实。
//
// **只读**：只 `stat`/`readdir`/`SELECT`（`st` 为 nil 时如实记"无从校验"，不当成"没有"）。
func CollectRetiredRuntimeFacts(ctx context.Context, st *store.Store, opts RetiredRuntimeOptions) RetiredRuntimeFacts {
	known := normalizeRuntimeIDs(opts.KnownRuntimeIDs)
	facts := RetiredRuntimeFacts{
		KnownRuntimeIDs: sortedIDs(known),
		IsolationRoots:  []string{},
		HomeDirs:        []RetiredHomeDir{},
		SessionSamples:  []RetiredRef{},
		SkillRefs:       []RetiredRef{},
		ProjectionRefs:  []RetiredRef{},
		Notes:           []string{},
		SessionScan:     "ok",
		SkillScan:       "ok",
	}
	facts.IsolationRoots = retiredIsolationRoots(strings.TrimSpace(opts.Home), opts.RuntimeSpecs)
	homeDirs, homeScan, homeNotes := retiredHomeDirs(facts.IsolationRoots, known)
	facts.HomeDirs, facts.HomeScan = homeDirs, homeScan
	if facts.HomeScan == "no_home" {
		facts.IsolationRoots = []string{}
	}
	facts.Notes = append(facts.Notes, homeNotes...)
	facts.ProjectionRefs = retiredProjectionRefs(opts.ProjectionDirs, known)

	// DB 侧：没有可读的持久层时**不许**把"读不到"当成"没有存量"——那是这一整段最坏的假绿形态。
	if st == nil {
		facts.SessionScan, facts.SkillScan = "no_store", "no_store"
		facts.Notes = append(facts.Notes,
			"没有可读的持久层：会话与 skill 定义里的存量引用无从校验（不是「没有」）")
		return facts
	}
	refs, samples, err := retiredSessionRefs(ctx, st, known)
	if err != nil {
		facts.SessionScan = "unreadable"
		facts.Notes = append(facts.Notes, "读会话行失败："+err.Error()+"——这一类存量无法校验")
	} else {
		facts.SessionRefCount, facts.SessionSamples = refs, samples
	}
	skillRefs, err := retiredSkillRefs(ctx, st, known)
	if err != nil {
		facts.SkillScan = "unreadable"
		facts.Notes = append(facts.Notes, "读 skill 定义失败："+err.Error()+"——这一类存量无法校验")
	} else {
		facts.SkillRefs = skillRefs
	}
	return facts
}

// normalizeRuntimeIDs 归一在册集合（trim、去空、去重）。
func normalizeRuntimeIDs(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// sortedIDs 把集合摊成升序切片（报告里的在册集合要可核对，顺序必须确定）。
func sortedIDs(known map[string]bool) []string {
	out := make([]string, 0, len(known))
	for id := range known {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// retiredIsolationRoots 返回本次要扫的隔离根（升序）。
//
// 两处来源，缺一不可：
//   - **从在册声明推**：取每个 `Spec.Isolation` 里 `IsolationPath` 条目 `Path` 的**父目录**
//     （`Path` 的最后一段是该底座自己的目录名）。于是隔离根跟着**声明**走——将来换根位置，
//     本判据自动跟上，而不是继续扫一个已经没人用的目录（那会让"没有遗留"变成假的）。
//   - **历史约定兜底**：`<MATE_HOME>/runtime`。**已下线的那份声明已经不在了**，它的落点推不出来，
//     只能按平台自 v11 起把本地子进程隔离开在 `<MATE_HOME>/runtime/<id>/` 的约定补一次。
//     这一条是**约定**，不是从注册表推出来的事实——改约定时这里也要跟着改。
func retiredIsolationRoots(home string, specs []goruntime.Spec) []string {
	// trim 后为空 = **无法扫描**（不是"扫过、没有"）：这时一个根都不给，由
	// `retiredHomeDirs` 记 `no_home`。`filepath.Join("", "runtime")` 会拼出相对路径
	// `runtime`（相对**进程 CWD**）——那会把一个与本机数据目录无关的目录当成判据来源。
	if strings.TrimSpace(home) == "" {
		return []string{}
	}
	seen := map[string]bool{}
	roots := []string{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		roots = append(roots, dir)
	}
	// ① 从在册声明推（父目录去重）。
	for _, spec := range specs {
		for _, v := range spec.Isolation {
			if v.Kind != goruntime.IsolationPath || len(v.Path) < 2 {
				continue
			}
			add(filepath.Join(append([]string{home}, v.Path[:len(v.Path)-1]...)...))
		}
	}
	// ② 历史约定兜底（见函数注释：这一条不是从注册表推出来的）。
	add(filepath.Join(home, "runtime"))
	sort.Strings(roots)
	return roots
}

// retiredHomeDirs 逐个隔离根列出**不在册**底座的遗留目录，返回（目录、扫描结论、无法校验说明）。
//
// `known` 为空是**判据失效**（调用方没给在册集合），那时全部目录都会被算成遗留——报出来是
// 响亮的错，比静默报"没有"好（`mate doctor` 的命令层同样会把这种情况说出来）。
//
// **扫描结论与"列目录"必须出自同一次 `ReadDir`**（这是本函数返回三个值的唯一理由）：分成
// "先 `Stat` 判存在、再读"两步时，一个**存在但读不动**的根会先被数成"存在"、再读失败只留一条
// note——于是 `HomeScan` 报 `ok`、`HomeDirs` 为空，外观与"扫过、没有遗留"**逐字相同**。
// 那正是这一段最坏的假绿形态。
func retiredHomeDirs(roots []string, known map[string]bool) ([]RetiredHomeDir, string, []string) {
	out := []RetiredHomeDir{}
	notes := []string{}
	if len(roots) == 0 {
		return out, "no_home", notes
	}
	readable, unreadable := 0, ""
	for _, root := range roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // 这个根还没被建出来（本机从没让平台拉起过底座时是正常的）
			}
			if unreadable == "" {
				unreadable = root + ": " + err.Error()
			}
			notes = append(notes, "隔离根读不动（"+root+"）："+err.Error()+"——该根下的遗留无法校验")
			continue
		}
		readable++
		for _, e := range ents {
			// 只看目录：隔离根下的普通文件不是平台建的东西（那是用户自己放的），不冒充"遗留"。
			if !e.IsDir() || known[e.Name()] {
				continue
			}
			dir := RetiredHomeDir{Runtime: e.Name(), Path: filepath.Join(root, e.Name()), Entries: []string{}}
			if inner, err := os.ReadDir(dir.Path); err == nil {
				for _, ie := range inner {
					if len(dir.Entries) >= retiredEntriesCap {
						dir.More++
						continue
					}
					dir.Entries = append(dir.Entries, ie.Name())
				}
			} else {
				dir.More = -1 // -1 = 目录内容读不动（"多个条目"与"读不到"不能混为一谈）
			}
			out = append(out, dir)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runtime != out[j].Runtime {
			return out[i].Runtime < out[j].Runtime
		}
		return out[i].Path < out[j].Path
	})
	switch {
	case unreadable != "":
		return out, "unreadable: " + unreadable, notes
	case readable == 0:
		return out, "missing", notes
	default:
		return out, "ok", notes
	}
}

// retiredSessionRefs 数出 `sessions.runtime_id` 指向不在册 id 的行（+ 样本）。
//
// 为什么这条判据值得有：这类会话**读得到**（列表与历史正常），只有下一轮 prompt 才会报
// `runtime_not_found`——"表面正常、用到才炸"正是体检该提前说出来的形态。
func retiredSessionRefs(ctx context.Context, st *store.Store, known map[string]bool) (int, []RetiredRef, error) {
	rows, err := st.ListSessions(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	count := 0
	samples := []RetiredRef{}
	for i := range rows {
		id := strings.TrimSpace(rows[i].RuntimeID)
		if id == "" || known[id] {
			continue
		}
		count++
		if len(samples) < retiredSamplesCap {
			samples = append(samples, RetiredRef{Kind: "session", ID: rows[i].ID, Runtime: id})
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].ID < samples[j].ID })
	return count, samples, nil
}

// retiredSkillRefs 挑出 `runtimes[]` 里含不在册 id 的 skill 定义。
//
// 只看 `runtimes[]`：更老的 `sources[].runtime` 形状由 `mate capability migrate` 负责
// （那是**迁移**，不是体检——体检只如实报告今天的读面看到什么）。
func retiredSkillRefs(ctx context.Context, st *store.Store, known map[string]bool) ([]RetiredRef, error) {
	rows, err := st.ListSkillDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := []RetiredRef{}
	for i := range rows {
		var body struct {
			Runtimes []string `json:"runtimes"`
		}
		if err := json.Unmarshal([]byte(rows[i].Body), &body); err != nil {
			continue // 定义体读不出来不是本段的事（形状体检另有其项）
		}
		for _, r := range body.Runtimes {
			r = strings.TrimSpace(r)
			if r == "" || known[r] {
				continue
			}
			out = append(out, RetiredRef{Kind: "skill", ID: rows[i].ID, Runtime: r})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Runtime < out[j].Runtime
	})
	return out, nil
}

// retiredProjectionRefs 挑出配置里指向不在册 id 的投影覆盖（键形如 `<runtime>:<kind>`）。
//
// 这类键**形状合法、所以配置解析不报错**，但它永远不会被匹配上——"我明明配了投影目录，
// 技能却没投放"在这里就能被解释掉。
func retiredProjectionRefs(dirs map[string]string, known map[string]bool) []RetiredRef {
	out := []RetiredRef{}
	for key := range dirs {
		id, _, ok := strings.Cut(strings.TrimSpace(key), ":")
		if !ok {
			continue // 无 `:` 的形状由配置解析拒绝，不在本段重复判一次
		}
		if id = strings.TrimSpace(id); id == "" || known[id] {
			continue
		}
		out = append(out, RetiredRef{Kind: "projection_dir", ID: key, Runtime: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
