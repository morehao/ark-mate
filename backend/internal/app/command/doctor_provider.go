package command

import (
	"context"
	"fmt"
	"os"
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

// checkProviderKeys 逐条档案的密钥可得性：声明了引用却找不到密文 → fail；尚未设密钥 → warn。
//
// 这一条与 `vault` 的分工：vault 回答"主密钥在不在"，它回答"每一条档案此刻能不能注出密钥"。
// 悬空的 `key_ref`（文件/行说有、vault 里没有）是**事实与声明不符**，必然表现为会话
// `credential_missing`，所以判失败。
func checkProviderKeys(facts manager.ProviderDoctorFacts) doctorCheck {
	name := "provider keys"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：密钥引用面无从校验"}
	}
	if len(facts.ProviderIDs) == 0 {
		return doctorCheck{name, doctorOK, "没有任何 provider 档案，密钥面为空"}
	}
	if !strings.HasPrefix(facts.VaultDirScan, "ok") && !strings.HasPrefix(facts.VaultDirScan, "missing") {
		// 密文目录读不动：**不许**把"扫不到"当成"没有密文"（那会把读权限问题报成引用悬空）。
		return doctorCheck{name, doctorWarn,
			"密文目录的扫描结论不可信（" + facts.VaultDirScan + "）：密钥引用面无法校验"}
	}
	if len(facts.MissingCiphertexts) > 0 {
		return doctorCheck{name, doctorFail, fmt.Sprintf(
			"%d 条档案声明了密钥引用（key_ref）但 vault 里没有对应密文（这些 provider 此刻注不出密钥，建会话会报 credential_missing）：%s",
			len(facts.MissingCiphertexts), bracketList(facts.MissingCiphertexts))}
	}
	if len(facts.NoKeyProviders) > 0 {
		return doctorCheck{name, doctorWarn, fmt.Sprintf(
			"%d 条档案尚未设置密钥（%s）：引用它们的会话不会注入任何密钥变量——要么补密钥，要么清空 agent 的凭据选择（用它底座自身的登录态）",
			len(facts.NoKeyProviders), bracketList(facts.NoKeyProviders))}
	}
	return doctorCheck{name, doctorOK, fmt.Sprintf(
		"%d 条档案的密钥引用都有对应密文（主密钥可用性见 vault 那一条）", len(facts.ProviderIDs))}
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

// checkRuntimeModelState 单个 runtime 的物化状态（path / digest / pending / writable / tested）。
func checkRuntimeModelState(rt manager.ProviderRuntimeFacts) doctorCheck {
	name := "models " + rt.Runtime
	detail := fmt.Sprintf("declared=%t tested=%t writable=%t channel=%s path=%s digest=%s pending=%t model_count=%d",
		rt.Declared, rt.Tested, rt.Writable, orDash(rt.Channel), orDash(rt.Path), orDash(shortDigest(rt.Digest)), rt.Pending, rt.ModelCount)
	if !rt.Declared {
		return doctorCheck{name, doctorWarn, detail + "：该 runtime 未声明模型配置落点（T5 不可达，平台清单对它没有兑现通道）" + noteSuffix(rt.Note)}
	}
	if !rt.Tested {
		return doctorCheck{name, doctorWarn, detail +
			"：落点**未实测 ⇒ 只读**（平台会生成候选，但不落盘、也不改启动参数）" + noteSuffix(rt.Note)}
	}
	return doctorCheck{name, doctorOK, detail + noteSuffix(rt.Note)}
}

// checkRuntimeModelDiff 差集：**取不到运行时广告就记"无法校验"，不许猜**。
func checkRuntimeModelDiff(rt manager.ProviderRuntimeFacts) doctorCheck {
	name := "models " + rt.Runtime + " 对账"
	if !rt.Declared {
		return doctorCheck{name, doctorWarn, "该 runtime 未声明模型配置落点：差异无从谈起"}
	}
	if !rt.AdvertisedKnown {
		return doctorCheck{name, doctorWarn, orDash(rt.DiffNote)}
	}
	if rt.HighRisk {
		// 高危那条会**单独**再报一次（见 checkRuntimeModels）：这里只给差集的原始计数。
		return doctorCheck{name, doctorFail, fmt.Sprintf(
			"declared=%d advertised=0：平台声明的 %d 条一条都没进目录（高危，见下一条）",
			rt.ModelCount, rt.ModelCount)}
	}
	detail := fmt.Sprintf("declared=%d advertised=%d declared_not_advertised=%d advertised_not_declared=%d",
		rt.ModelCount, rt.AdvertisedCount, rt.DeclaredNotAdvertised, rt.AdvertisedNotDeclared)
	switch {
	case rt.WorstSeverity == "error":
		return doctorCheck{name, doctorFail, detail + "：有**声明了但底座没广告**的条目（硬白名单路由上选了会直接报错）——写了没生效"}
	case rt.WorstSeverity == "degrade":
		return doctorCheck{name, doctorWarn, detail + "：有声明了但底座没广告的条目（咨询性目录：能力会被静默降级成 text-only）"}
	case rt.DeclaredNotAdvertised > 0:
		return doctorCheck{name, doctorWarn, detail + "：有声明了但底座没广告的条目"}
	default:
		return doctorCheck{name, doctorOK, detail + fmt.Sprintf("：清单与运行时目录一致（底座自带目录另有 %d 条，平台不管它）", rt.AdvertisedNotDeclared)}
	}
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
