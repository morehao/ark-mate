package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/config"
)

// 本文件是 `mate doctor` 的 **provider（供应商凭据）与模型清单**那一段（K2）。
//
// 分工是硬的：**事实由 `manager.CollectProviderDoctorFacts` 构造**（SQL、vault 与对账算法
// 都在那一侧），本文件只做两件事——打开只读句柄、把事实翻成三档结论与人话。该包的既有风格
// 是薄打印层，把 SQL 写进这里会让"体检的口径"与"运行时的口径"各长一套。
//
// 退出码语义**逐字沿用**既有口径：**"无法校验"记警告；事实与声明不符才判失败**。
// 逐条对应关系（写下判据，避免下一个人凭感觉调档）：
//
//	providers      行数 / 载体：读不到 → warn（无法校验）；库文件在、只读打开失败 → fail（事实坏了）
//	vault          可用 → ok；尚未创建且**没有任何密文引用** → warn；有密文引用却不可用 → fail
//	provider refs  悬空引用 = agent 声明了一个不存在的档案 → fail（`CredentialProblem` 同判据）
//	providers import 对账：只有"库里有、文件里没有"（写面新建后的快照滞后）→ warn；其余 → fail
//	vault orphans  孤儿密文（删除补偿失败的残留）→ warn：无害、不自动删，但要被看见
//	models <id>    物化状态与清单条数：未实测只读 → warn；其余 → ok（它是事实，不是不符）
//	models <id> 对账 取不到运行时广告 → warn（无法校验，**不许**当成"一条都没生效"）
//	                declared 非空而 advertised 一条都没有 → **单独判高危**（fail）
//	models <id> 高危 只有上面那种形态才出现的一条失败项（"写了完全没生效"）

// checkProviderSection 采集 provider 段的事实并翻成检查项。
//
// 只读：`store.OpenReadOnly` 不跑迁移；vault 探测不创建 keyfile（见两处注释）。
func checkProviderSection(opts doctorOptions) (manager.ProviderDoctorFacts, []doctorCheck) {
	st, storeCheck := openDoctorStore(opts)
	facts := manager.CollectProviderDoctorFacts(context.Background(), st, manager.ProviderDoctorOptions{
		Home: opts.Home, AgentDir: opts.AgentDir, DBPath: opts.DBPath,
	})
	if st != nil {
		defer func() { _ = st.Close() }()
	}

	var checks []doctorCheck
	if st == nil {
		// 没有 DB 事实：openDoctorStore 已经给出了结论（为什么读不到），不再叠一条同名项。
		checks = append(checks, storeCheck)
	} else {
		checks = append(checks, checkProviderCarrier(facts))
	}
	checks = append(checks, checkProviderVault(facts))
	checks = append(checks, checkProviderKeys(facts))
	checks = append(checks, checkProviderRefs(facts))
	checks = append(checks, checkProviderImport(facts))
	checks = append(checks, checkProviderOrphans(facts))
	// v12 新增的四条（T6.2）：缺端点、回退依据、迁移待处置事实、敏感文件清单。
	checks = append(checks, checkProviderBaseURL(facts))
	checks = append(checks, checkMigrationBackup(facts))
	checks = append(checks, checkMigrationNotes(facts))
	checks = append(checks, checkMigrationNotesV13(facts))
	checks = append(checks, checkSensitiveFiles(facts)...)
	checks = append(checks, checkModalityDeclaration(facts))
	checks = append(checks, checkRuntimeModels(facts)...)
	return facts, checks
}

// openDoctorStore 打开只读库句柄（K2 的唯一 IO 入口）。
//
// 返回的检查**总是**存在：库读不出来时它就是 `providers` 那一条的结论（不是"没检查"）。
// 三种"没有 DB 事实"的处境刻意分开说，因为处置完全不同：
//
//	memory 模式      —— 这是**配置选择**：档案载体是 `providers.json`，不是故障（warn）
//	库文件不存在     —— 还没跑过 serve/migrate（warn：无法校验）
//	库文件在、打不开 —— 事实坏了（fail：v11 起 DB 是档案事实源，读不到就等于档案面不可判定）
func openDoctorStore(opts doctorOptions) (*store.Store, doctorCheck) {
	if opts.StoreMode == config.StoreModeMemory {
		return nil, doctorCheck{"providers", doctorWarn,
			"memory 模式（MATE_STORE=memory，或 MATE_STORE 未设而库文件不存在/为空 → auto 解析成 memory）：" +
				"provider 档案的载体是 <AgentDir>/providers.json，DB 行数不适用——本段只报 vault 可达性与文件侧快照"}
	}
	if strings.TrimSpace(opts.DBPath) == "" {
		return nil, doctorCheck{"providers", doctorWarn, "未配置库文件路径（MATE_DB）：无法校验 provider 档案行"}
	}
	// 先自己判"文件在不在"：`store.OpenReadOnly` 的错误文案不该由这里按字符串猜。
	if _, err := os.Stat(opts.DBPath); err != nil {
		if os.IsNotExist(err) {
			return nil, doctorCheck{"providers", doctorWarn,
				fmt.Sprintf("库文件 %s 不存在：还没跑过 serve/migrate，provider 档案行无从校验", opts.DBPath)}
		}
		return nil, doctorCheck{"providers", doctorFail,
			fmt.Sprintf("库文件 %s 不可读：%v", opts.DBPath, err)}
	}
	st, err := store.OpenReadOnly(opts.DBPath)
	if err != nil {
		return nil, doctorCheck{"providers", doctorFail, "无法校验 provider 档案：" + err.Error()}
	}
	return st, doctorCheck{"providers", doctorOK, ""} // 内容由 checkProviderCarrier 拼
}

// checkProviderCarrier 档案数与载体（覆盖 openDoctorStore 的占位检查）。
func checkProviderCarrier(facts manager.ProviderDoctorFacts) doctorCheck {
	fileNote := fmt.Sprintf("providers.json=%s bak-v11=%s",
		existsWord(facts.ProvidersFile), existsWord(facts.ProvidersBackup))
	if facts.Carrier != "db" {
		return doctorCheck{"providers", doctorWarn,
			fmt.Sprintf("%s；文件侧 %s", orDash(facts.CarrierNote), fileNote)}
	}
	return doctorCheck{"providers", doctorOK,
		fmt.Sprintf("载体=DB（%s）：%d 条档案 %s；文件侧 %s",
			orDash(facts.DBPath), facts.ProviderRows, bracketList(facts.ProviderIDs), fileNote)}
}

// checkProviderVault vault 可达性（K5 的检查项之一：这一段无 fail）。
//
// 分档判据（把"主密钥可不可用"与"有没有真的丢了东西"分开）：
//
//	可用                                       → ok
//	不可用，密文目录里也确实一个 .enc 都没有     → warn（`vault.key` 是第一次写密文时才创建的，
//	                                             这时"没有主密钥"不丢任何东西）
//	不可用，但有实际存在的密文                    → fail（**密文永久不可恢复**：事实与声明不符）
//
// 注意"档案声明了 key_ref"**不算**"有密文"：那可能是引用悬空（密文从来没写成功过），
// 它的处置是"重设这条 provider 的 key"，由 `provider keys` 那一条报（见 checkProviderKeys）。
func checkProviderVault(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "vault"
	detail := fmt.Sprintf("锚点=%s 来源=%s：%s", orDash(facts.VaultHome), orDash(facts.VaultSource), orDash(facts.VaultNote))
	if facts.VaultUsable {
		return doctorCheck{name, doctorOK, detail}
	}
	if !strings.HasPrefix(facts.VaultDirScan, "ok") && !strings.HasPrefix(facts.VaultDirScan, "missing") {
		return doctorCheck{name, doctorWarn, detail +
			"；密文目录也读不动（" + facts.VaultDirScan + "）：无法判断有没有密文会因此不可恢复"}
	}
	if len(facts.CiphertextFiles) == 0 {
		return doctorCheck{name, doctorWarn, detail +
			"；密文目录里没有任何密文，所以这一项目前不丢东西（第一次写入密钥时主密钥会被自动创建）"}
	}
	return doctorCheck{name, doctorFail, fmt.Sprintf(
		"%s；但 vault 里实际存在 %d 个密文（%s）——**这些密钥不可恢复**，只能重设对应 provider 的 key",
		detail, len(facts.CiphertextFiles), bracketList(facts.CiphertextFiles))}
}

// checkProviderKeys 逐条档案的密钥可得性：DB 行存在但在 vault 里找不到密文 → warn（尚未设密钥）。
//
// 这一条与 `vault` 的分工：vault 回答"主密钥在不在"，它回答"每一条档案此刻能不能注出密钥"。
//
// v13 的形态变化：档案行**不再声明** `key_ref`（引用是 `providerRef(id)` 的纯函数副本），
// 所以"声明了引用却没有密文"这个状态**结构上不可能**——唯一的真问题是"这条档案还没设密钥"
// （v12 的 `missingCiphertexts` 那条失败项随之消失，判据换成 vault 目录清单）。
func checkProviderKeys(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "provider keys"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：密钥面无从校验"}
	}
	if len(facts.ProviderIDs) == 0 {
		return doctorCheck{name, doctorOK, "没有任何 provider 档案，密钥面为空"}
	}
	if !strings.HasPrefix(facts.VaultDirScan, "ok") && !strings.HasPrefix(facts.VaultDirScan, "missing") {
		// 密文目录读不动：**不许**把"扫不到"当成"没有密文"（那会把读权限问题报成"全都缺密钥"）。
		return doctorCheck{name, doctorWarn,
			"密文目录的扫描结论不可信（" + facts.VaultDirScan + "）：密钥面无法校验"}
	}
	if len(facts.NoKeyProviders) > 0 {
		return doctorCheck{name, doctorWarn, fmt.Sprintf(
			"%d 条档案在 vault 里没有密文（尚未设置密钥）：%s。引用它们的会话不会注入任何密钥变量"+
				"——要么补密钥，要么清空 agent 的凭据选择（用它底座自身的登录态）",
			len(facts.NoKeyProviders), bracketList(facts.NoKeyProviders))}
	}
	return doctorCheck{name, doctorOK, fmt.Sprintf(
		"%d 条档案在 vault 里都有对应密文（主密钥可用性见 vault 那一条）", len(facts.ProviderIDs))}
}

// checkProviderRefs 悬空引用：agent 定义里的 `provider` 指向不存在的档案。
//
// 判失败的判据与 `CredentialProblem` 同源（那条路在拨号前就会以 `credential_missing` 拒发）：
// 悬空引用必然表现为"本该注入的密钥静默缺失"，属事实与声明不符。
func checkProviderRefs(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "provider refs"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：悬空引用无从校验"}
	}
	if len(facts.DanglingRefs) == 0 {
		return doctorCheck{name, doctorOK, fmt.Sprintf("%d 条档案被引用的部分全部存在（无悬空引用）", facts.ProviderRows)}
	}
	items := make([]string, 0, len(facts.DanglingRefs))
	for _, d := range facts.DanglingRefs {
		items = append(items, fmt.Sprintf("%s→%s", d.AgentID, d.ProviderID))
	}
	return doctorCheck{name, doctorFail, fmt.Sprintf(
		"%d 条悬空引用（agent 声明了不存在的 provider 档案，那些会话会在拨号前报 credential_missing）：%s",
		len(items), sampleList(items))}
}

// checkProviderImport 导入对账（`meta.providers_import_mismatch` 的逐条报出）。
func checkProviderImport(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "providers import"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：导入对账无从校验"}
	}
	if len(facts.ImportMismatch) == 0 {
		return doctorCheck{name, doctorOK, "无记录的不一致（meta.providers_import_mismatch 为空）"}
	}
	verb := "不一致"
	status := doctorFail
	if facts.ImportMismatchBenignOnly {
		// 只有"库里有、文件里没有"：写面新建后 providers.json 还没导出，属正常快照滞后。
		verb = "快照滞后（写面新建的档案还没导回文件：跑 `mate provider export` 同步）"
		status = doctorWarn
	}
	return doctorCheck{name, status, fmt.Sprintf("导入对账 %s %d 条：%s", verb, len(facts.ImportMismatch), sampleList(facts.ImportMismatch))}
}

// checkProviderOrphans 孤儿密文（删除补偿失败的残留）。如实列出、不自动删。
//
// 为什么只记警告而不是失败：它**不影响任何一条档案的可用性**（没有任何行引用它），
// 但它是"删除只完成了一半"的证据，可能还有别的用途（人工核对、恢复误删），所以不判失败、
// 也不自动删——体检命令没有删除权。
func checkProviderOrphans(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "vault orphans"
	if !strings.HasPrefix(facts.VaultDirScan, "ok") && !strings.HasPrefix(facts.VaultDirScan, "missing") {
		return doctorCheck{name, doctorWarn, "密文目录的扫描结论不可信（" + facts.VaultDirScan + "）：孤儿密文无法校验"}
	}
	if len(facts.OrphanCiphertexts) == 0 {
		return doctorCheck{name, doctorOK, "没有孤儿密文（vault/providers/ 下每个 .enc 都有对应的档案行）"}
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf(
		"%d 个孤儿密文（`<vault>/providers/*.enc` 没有对应的档案行：删除补偿失败的残留）：%s；"+
			"体检不自动删——确认没用之后手工删掉对应 .enc",
		len(facts.OrphanCiphertexts), sampleList(facts.OrphanCiphertexts))}
}

// checkModalityDeclaration 两侧差异如实可见（ADR-12/A11）：模型输入模态在 dsh 上是**已声明**的
// 交付事实，在 opencode 上只是**平台记录**。
//
// 为什么要单列一条：同一份平台清单在两个底座上的实际行为不同，而"默认 text"会**覆盖** dsh 的
// 底座目录（老模型里本来能看图的现在收不了图，除非用户逐条勾选）。这一条让用户不必等到某个
// 会话失败才知道这件事——它回答的问题是"我勾的图片，到底有没有生效"。
func checkModalityDeclaration(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "模型输入模态"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：模态声明面无从校验"}
	}
	if len(facts.Runtimes) == 0 {
		return doctorCheck{name, doctorWarn, "没有已注册的底座：无法校验模态声明面"}
	}
	declared, recorded := []string{}, []string{}
	for _, rt := range facts.Runtimes {
		if rt.DeclaresModelInput {
			declared = append(declared, rt.Runtime)
		} else {
			recorded = append(recorded, rt.Runtime)
		}
	}
	detail := "平台清单里的 `input_modalities`（默认 `text`）"
	if len(declared) > 0 {
		detail += "；**已写进底座声明**（会覆盖底座目录：老模型里本来能看图的要用户逐条勾上）：" + bracketList(declared)
	}
	if len(recorded) > 0 {
		detail += "；**仅平台记录、未在该底座上声明**（该底座自己知道模型能力，平台不猜）：" + bracketList(recorded)
	}
	if len(recorded) > 0 {
		// 有"仅记录"的一侧 ⇒ 界面上必须说清，否则用户以为勾了图片就生效。
		return doctorCheck{name, doctorWarn, detail}
	}
	return doctorCheck{name, doctorOK, detail}
}

// checkRuntimeModels 逐 runtime 的清单物化状态与差集。
func checkRuntimeModels(facts manager.ProviderDoctorFacts) []doctorCheck {
	checks := []doctorCheck{{
		"models", doctorOK, fmt.Sprintf(
			"平台模型清单 %d 条（跨 runtime）；meta.runtime_config_digest %s",
			facts.ModelRows, writtenWord(facts.RuntimeConfigDigestWritten)),
	}}
	if facts.Carrier != "db" {
		checks[0] = doctorCheck{"models", doctorWarn,
			"载体不是 DB（" + orDash(facts.CarrierNote) + "）：平台模型清单与物化状态无从校验"}
	}
	for _, rt := range facts.Runtimes {
		checks = append(checks, checkRuntimeModelState(rt), checkRuntimeModelDiff(rt))
		if rt.HighRisk {
			checks = append(checks, doctorCheck{"models " + rt.Runtime + " 高危", doctorFail, fmt.Sprintf(
				"**写了完全没生效**：平台声明了 %d 条模型，底座此刻广告的候选一条都没有"+
					"（配置没被读到 / 落点写错了 / 不是新进程）——先看这一项的 path 与 digest，"+
					"再确认落点落盘成功且底座是**新拉起来的**进程", rt.ModelCount)})
		}
	}
	return checks
}

// checkRuntimeModelState 单个 runtime 的**同步结论**（v12：交付结论由 adapter 回带，平台如实转述）。
func checkRuntimeModelState(rt manager.ProviderRuntimeFacts) doctorCheck {
	name := "models " + rt.Runtime
	detail := fmt.Sprintf("status=%s effect=%s pending=%t credential=%s(tested=%t) where=%s model_count=%d declares_input=%t",
		orDash(rt.Status), orDash(rt.Effect), rt.Pending, orDash(rt.CredentialChannel), rt.CredentialTested,
		orDash(rt.Where), rt.ModelCount, rt.DeclaresModelInput)
	switch rt.Status {
	case "failed":
		return doctorCheck{name, doctorFail, detail + "：同步失败" + noteSuffix(rt.Note)}
	case "unsupported":
		return doctorCheck{name, doctorWarn, detail + "：该底座未声明模型清单交付面（平台不为它猜交付形态）" + noteSuffix(rt.Note)}
	case "untested":
		return doctorCheck{name, doctorWarn, detail + "：尚未同步过或该落点未实测" + noteSuffix(rt.Note)}
	default:
		return doctorCheck{name, doctorOK, detail + noteSuffix(rt.Note)}
	}
}

// checkRuntimeModelDiff 对账：**取不到运行时广告就记"无法校验"，不许猜**。
func checkRuntimeModelDiff(rt manager.ProviderRuntimeFacts) doctorCheck {
	name := "models " + rt.Runtime + " 对账"
	if !rt.AdvertisedKnown {
		return doctorCheck{name, doctorWarn, orDash(rt.DiffNote)}
	}
	if rt.HighRisk {
		// 高危那条会**单独**再报一次（见 checkRuntimeModels）：这里只给差集的原始计数。
		return doctorCheck{name, doctorFail, fmt.Sprintf(
			"declared=%d advertised=0：平台声明的 %d 条一条都没进目录（高危，见下一条）",
			rt.ModelCount, rt.ModelCount)}
	}
	detail := fmt.Sprintf("declared=%d advertised=%d missing=%d",
		rt.ModelCount, rt.AdvertisedCount, len(rt.Missing))
	if len(rt.Missing) > 0 {
		return doctorCheck{name, doctorWarn, detail + "：有**声明了但底座没广告**的条目（写了没生效）：" + bracketList(rt.Missing)}
	}
	return doctorCheck{name, doctorOK, detail + "：清单里的条目都出现在运行时目录里（底座自带目录的条目平台不管）"}
}

// shortDigest 摘要太长，报告里只留前 12 位（完整值在 `mate doctor --json` 的 facts 里）。
func shortDigest(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12] + "…"
}

// noteSuffix 把 note 拼成一句可读的后缀（空则不拼）。
//
// **只取第一句、并截断**：runtime 的 Note 里常常是整段实测记录（dsh 那条有近 300 字），
// 全量铺进体检报告会把它变成没人读的墙。完整文本在 `mate doctor --json` 的 facts 里。
func noteSuffix(note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return ""
	}
	if i := strings.IndexAny(note, "。；\n"); i > 0 {
		note = note[:i]
	}
	const max = 80
	runes := []rune(note)
	if len(runes) > max {
		note = string(runes[:max]) + "…"
	}
	return "；" + note
}

// existsWord 文件存在性的中文词（报告里不出现 true/false）。
func existsWord(ok bool) string {
	if ok {
		return "有"
	}
	return "无"
}

// writtenWord meta 键是否被写过。
func writtenWord(ok bool) string {
	if ok {
		return "已写过（这台机器上已经有物化过的模型配置）"
	}
	return "未写过"
}

// bracketList 用【】包住一个列表（空列表显示成"（空）"）。
func bracketList(items []string) string {
	if len(items) == 0 {
		return "（空）"
	}
	return "【" + sampleList(items) + "】"
}

// checkProviderBaseURL 「缺端点」问题项（B1）。
//
// v11 的端点藏在逐底座的 `projections` 里，迁移时推不出来就留空——那些供应商的模型一条也不会
// 被交付给任何底座，而它看起来只是"一条普通的供应商"。这条把它显式摆出来（fail-loud，附出路）。
func checkProviderBaseURL(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "providers 端点"
	if len(facts.MissingBaseURL) == 0 {
		return doctorCheck{name, doctorOK, "每条供应商都有可用的 `base_url`"}
	}
	return doctorCheck{name, doctorFail, fmt.Sprintf(
		"%d 条供应商缺 `base_url`：%s。它们的模型**不会被交付给任何底座**（不假装同步成功）——"+
			"在「设置 · 模型」的供应商块给它们各补一个 OpenAI 兼容端点",
		len(facts.MissingBaseURL), bracketList(facts.MissingBaseURL))}
}

// checkMigrationBackup 打印 DB 迁移备份路径（§回退窗口的唯一依据）。
//
// 「无法校验」的口径：没迁移过 = 不需要备份（OK，不是 warn）；迁移过但找不到备份文件
// = **回退窗口缺失**（fail：v12 之后旧二进制会拒绝启动，没有备份就只能前滚）。
func checkMigrationBackup(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "迁移备份"
	if facts.MigrationBackup != "" {
		return doctorCheck{name, doctorOK, "可回退依据：`" + facts.MigrationBackup +
			"`（**只管 DB**：密钥不在其内，而本方案不触碰 vault，所以回退不必恢复 vault）"}
	}
	if facts.RuntimeConfigDigestWritten || facts.ProviderRows > 0 {
		return doctorCheck{name, doctorWarn, "本机已有资源数据但没有找到 `mate.db.bak-v12-*` / `mate.db.bak-v13-*`：" +
			"若这个库是从旧版本升上来的，说明备份已被清理——**回退窗口已关闭**（schema 前进之后只能前滚；" +
			"逃生门是 `mate provider export`）"}
	}
	return doctorCheck{name, doctorOK, "没有需要备份的迁移（这个库不是从旧版本升上来的）"}
}

// checkMigrationNotes 迁移留下的待人工处置事实（默认模型冲突 / 归并冲突 / 无出口字段）。
//
// 迁移**不阻塞**这些（B1/R3），但它们必须被看见——否则"未迁移字段"就成了一句没人能验证的话。
func checkMigrationNotes(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "迁移对账"
	n := facts.MigrationNotes
	if n == nil {
		return doctorCheck{name, doctorOK, "没有 v12 迁移留下的待处置事实"}
	}
	parts := []string{}
	if len(n.DefaultModelConflicts) > 0 {
		parts = append(parts, "默认模型冲突："+strings.Join(n.DefaultModelConflicts, "；"))
	}
	if len(n.ModelNameConflicts) > 0 {
		parts = append(parts, fmt.Sprintf("模型显示名冲突 %d 条：%s", len(n.ModelNameConflicts), bracketList(n.ModelNameConflicts)))
	}
	if len(n.UnmigratedFields) > 0 {
		parts = append(parts, "v12 无出口的 v11 字段（值仍在 `mate.db.bak-v12-*` 里）："+bracketList(n.UnmigratedFields))
	}
	if len(parts) == 0 {
		return doctorCheck{name, doctorOK, "v11 → v12 的逐组对账无冲突（没有丢字段、没有默认模型归并冲突）"}
	}
	return doctorCheck{name, doctorWarn, "迁移时留下了需要你看一眼的事实（**不阻塞**，也不假装迁移过）：" +
		strings.Join(parts, "；")}
}

// checkMigrationNotesV13 v13（providers/models 列化）留下的待人工处置事实。
//
// 与 v12 那条**分键分项**（`meta.migration_notes_v13`）：列化不做静默改变语义的事，但这几类
// 事实必须被人看见——被丢弃的 body 未知键、正文里**曾有明文密钥**的行（值只在备份库里）、
// `created_at` 近似行、按默认 `text` 填的模态（它会覆盖底座 catalog）、v12 里就是孤儿的 model 行。
//
// 明文那一条**判 warn 而不是 fail**：库现在**不含**明文（这正是修复），代价是那些供应商
// 此刻没有密钥（会报 credential_missing）——出路是重设一次，旧值可从备份库取回。
// 文案指到具体备份文件，**绝不回显密钥值**。
func checkMigrationNotesV13(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "迁移对账 v13"
	n := facts.MigrationNotesV13
	if n == nil {
		return doctorCheck{name, doctorOK, "没有 v13 迁移留下的待处置事实"}
	}
	backup := n.BackupPath
	if backup == "" {
		backup = facts.MigrationBackup
	}
	parts := []string{}
	if len(n.LegacyPlaintextKeys) > 0 {
		where := "迁移前的备份库"
		if backup != "" {
			where = "`" + backup + "`"
		}
		parts = append(parts, fmt.Sprintf(
			"%d 条供应商的密钥原来是**明文存在库里的**（v13 不搬它，新表没有能放明文的列）：%s；"+
				"它们此刻没有密钥（相关会话会报 credential_missing），旧值可从 %s 的 `providers.body.key` 取回，然后在「设置 · 模型」里重设一次",
			len(n.LegacyPlaintextKeys), bracketList(n.LegacyPlaintextKeys), where))
	}
	if len(n.DroppedBodyKeys) > 0 {
		parts = append(parts, "被丢弃的 body 未知键（值仍在备份库里）："+bracketList(n.DroppedBodyKeys))
	}
	if len(n.ModalityDefaulted) > 0 {
		parts = append(parts, fmt.Sprintf(
			"模型输入模态 %d 条按默认 `text` 填（这会**覆盖底座目录**：老模型里本来能看图的现在收不了图）：%s；"+
				"要图片就到「设置 · 模型」里逐条勾上", len(n.ModalityDefaulted), bracketList(n.ModalityDefaulted)))
	}
	if len(n.OrphanModels) > 0 {
		parts = append(parts, fmt.Sprintf(
			"孤儿模型行 %d 条（`provider_id` 指向不存在的供应商，迁移**不阻断**但谁都交付不了）：%s",
			len(n.OrphanModels), bracketList(n.OrphanModels)))
	}
	if len(n.CreatedAtApproximated) > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d 行的 `created_at` 取的是 `updated_at` 近似值（v11/v12 没有插入时刻的留痕，如实登记）：%s",
			len(n.CreatedAtApproximated), bracketList(n.CreatedAtApproximated)))
	}
	if len(parts) == 0 {
		return doctorCheck{name, doctorOK, "v12 → v13 的逐行对账无冲突（没有丢字段、没有明文密钥、没有孤儿行）"}
	}
	return doctorCheck{name, doctorWarn, "v13 列化留下了需要你看一眼的事实（**不阻塞**，也不假装迁移过）：" +
		strings.Join(parts, "；")}
}

// checkSensitiveFiles 敏感文件清单（v12 新增一处明文落点）。
//
// 权限宽于 0600 → **fail**（不是 warn）：dsh 对可被属主之外读取的凭据文档是**拒启**，
// 而这份文档里是明文密钥。无法判定权限（Windows）→ warn，如实说"无法校验"。
func checkSensitiveFiles(facts manager.ProviderDoctorFacts) []doctorCheck {
	if len(facts.SensitiveFiles) == 0 {
		return []doctorCheck{{"敏感文件", doctorOK, "没有需要单独看待的明文凭据落点"}}
	}
	out := make([]doctorCheck, 0, len(facts.SensitiveFiles))
	for _, f := range facts.SensitiveFiles {
		name := "敏感文件 " + filepath.Base(f.Path)
		switch {
		case !f.Exists:
			out = append(out, doctorCheck{name, doctorOK, "尚未创建（" + f.Path + "）：没有落点就没有风险"})
		case !f.PermChecked:
			out = append(out, doctorCheck{name, doctorWarn, f.Path + "：" + f.Note})
		case f.OK:
			out = append(out, doctorCheck{name, doctorOK, f.Path + " 权限 " + f.Perm + "（期望 " + f.Expected + "）"})
		default:
			out = append(out, doctorCheck{name, doctorFail, f.Path + " 权限 " + f.Perm + "：" + f.Note})
		}
	}
	return out
}
