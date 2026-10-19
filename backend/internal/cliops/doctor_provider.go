package cliops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/internal/mechanism/credential"

	"github.com/morehao/ark-mate/internal/assembly"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/domain/provider"
	doctorfacts "github.com/morehao/ark-mate/internal/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/registry"
	"github.com/morehao/ark-mate/internal/store"
)

// 本文件是 `mate doctor` 的 **provider（供应商凭据）与模型清单**那一段（K2）。
//
// 分工是硬的：**事实由 `doctorfacts.CollectProviderDoctorFacts` 构造**（SQL、凭据分层与对账算法
// 都在那一侧），本文件只做两件事——打开只读句柄、把事实翻成三档结论与人话。该包的既有风格
// 是薄打印层，把 SQL 写进这里会让"体检的口径"与"运行时的口径"各长一套。
//
// 退出码语义**逐字沿用**既有口径：**"无法校验"记警告；事实与声明不符才判失败**。
// 逐条对应关系（写下判据，避免下一个人凭感觉调档）：
//
//	providers        行数 / 载体：读不到 → warn（无法校验）；库文件在、只读打开失败 → fail（事实坏了）
//	credentials      托管文档：条目齐、权限 0600 → ok；不在/过宽/有条目没写进去 → warn
//	                 （**镜像可由库重建**，任何一条都不是"密钥丢了"）；文档形状坏掉或读不动 → fail
//	provider keys    库里没有密钥的档案 → warn（尚未设置，档案可以先建后补）；全都有 → ok
//	mcp secrets      `mcp_secrets` 条目 / 孤儿：孤儿 → warn（无害、不自动删，但要被看见）
//	legacy secrets   旧机制遗留物仍在盘上 → warn（**已迁移，可删**；医生不替用户删）
//	providers import 对账：只有"库里有、文件里没有"（写面新建后的快照滞后）→ warn；其余 → fail
//	models <id>      物化状态与清单条数：未实测只读 → warn；其余 → ok（它是事实，不是不符）
//	models <id> 对账  取不到运行时广告 → warn（无法校验，**不许**当成"一条都没生效"）
//	                 declared 非空而 advertised 一条都没有 → **单独判高危**（fail）
//	models <id> 高危  只有上面那种形态才出现的一条失败项（"写了完全没生效"）

// checkProviderSection 采集 provider 段的事实并翻成检查项。
//
// 只读：`store.OpenReadOnly` 不跑迁移；凭据面只读（镜像权限过宽只如实报出，**不自动 chmod**）。
func checkProviderSection(opts doctorOptions) (doctorfacts.ProviderDoctorFacts, []doctorCheck) {
	st, storeCheck := openDoctorStore(opts)
	// 逐 runtime 的清单事实由实现侧提供（它要造一个只读 Service 去读 `GET /models` 的同一条取值面）。
	facts := doctorfacts.CollectProviderDoctorFacts(context.Background(), st, doctorfacts.ProviderDoctorOptions{
		Home: opts.Home, ProviderDir: opts.ProviderDir, DBPath: opts.DBPath,
		// 三个"两侧约定的字符串"由**组合根**从供应商域交下去（机制层不得 import 领域）：
		// 档案文件名、迁移备份名、导入对账的那条判据标记。
		ProvidersFileName:   provider.ProvidersFileName,
		BackupName:          provider.ProvidersBackupName,
		MismatchExtraMarker: provider.ProviderMismatchExtraMarker,
		RuntimeSpecs:        assembly.BuiltinSpecs(),
	}, func(ctx context.Context, st *store.Store, home string) []doctorfacts.ProviderRuntimeFacts {
		return registry.CollectRuntimeModelFacts(ctx, st, home, assembly.BuiltinRegistry())
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
	checks = append(checks, checkCredentialsMirror(facts))
	checks = append(checks, checkProviderKeys(facts))
	checks = append(checks, checkMcpSecrets(facts))
	checks = append(checks, checkLegacySecretLeftovers(facts))
	checks = append(checks, checkProviderImport(facts))
	// v12 新增的四条（T6.2）：缺端点、回退依据、迁移待处置事实、敏感文件清单。
	checks = append(checks, checkProviderBaseURL(facts))
	checks = append(checks, checkMigrationBackup(facts))
	// v19 新增：老布局遗留的迁移备份（只报不处置）。
	checks = append(checks, checkLegacyMigrationBackups(facts))
	checks = append(checks, checkMigrationNotes(facts))
	checks = append(checks, checkMigrationNotesV13(facts))
	checks = append(checks, checkSensitiveFiles(facts)...)
	checks = append(checks, checkModalityDeclaration(facts))
	// v25 新增：容量声明面（未声明 → 用底座兜底；只报不判红）。
	checks = append(checks, checkModelCapacity(facts))
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
				"provider 档案的载体是 <ProviderDir>/providers.json，DB 行数不适用——本段只报凭据文档面与文件侧快照"}
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
func checkProviderCarrier(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
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

// checkCredentialsMirror 托管凭据文档（`<MATE_HOME>/credentials.json`）的形态与库的对账。
//
// 分档判据（**"镜像不对"绝不等于"密钥丢了"**——权威在库，镜像可重建）：
//
//	不存在                                     → warn（还没写过密钥；下次 serve 会按库建它）
//	权限过宽 / 内容不是 JSON 对象 / 读不动       → fail（平台**拒绝读它**：那是硬约束，
//	                                            而这条分层会因此整层失效，用户填的密钥不被采用）
//	条目齐、权限合规                            → ok
//	库里有而镜像里没有                          → warn（镜像陈旧：重跑一次 serve 即补齐）
//	镜像里有而库里没有                          → warn（**不自动回灌**：报出引用名让用户处置）
func checkCredentialsMirror(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
	name := "credentials"
	base := fmt.Sprintf("路径=%s 权限=%s 条目=%d", orDash(facts.CredentialsPath),
		permWord(facts.CredentialsExists, facts.CredentialsPerm), facts.CredentialsRefs)

	// 文档本身坏掉（不是"还没有"）时判失败：分层读到它就会硬失败，用户填的密钥会**不被采用**。
	// 判据是"它存在但平台读不了"，与"它还不存在"（正常中间态）严格分开。
	if facts.CredentialsExists && !facts.CredentialsPermOK {
		return doctorCheck{name, doctorFail, base + "：**权限不合，平台拒绝读它**——" + orDash(facts.CredentialsNote)}
	}
	if facts.CredentialsExists && facts.CredentialsRefs == 0 {
		return doctorCheck{name, doctorFail, base + "：**内容不是 `引用 → 值` 的 JSON 对象**——" + orDash(facts.CredentialsNote)}
	}
	if !facts.CredentialsExists {
		return doctorCheck{name, doctorWarn, base + "：" + orDash(facts.CredentialsNote)}
	}
	if len(facts.MissingInCredentials) > 0 || len(facts.MissingInStore) > 0 {
		detail := base
		if len(facts.MissingInCredentials) > 0 {
			detail += fmt.Sprintf("；库里有而镜像里没有 %d 条（镜像陈旧：重跑一次 `mate serve` 会按库补齐）：%s",
				len(facts.MissingInCredentials), bracketList(facts.MissingInCredentials))
		}
		if len(facts.MissingInStore) > 0 {
			detail += fmt.Sprintf("；镜像里有而库里没有 %d 条（**不自动回灌**——权威是库）：%s；%s",
				len(facts.MissingInStore), bracketList(facts.MissingInStore),
				mirrorOnlyRemedy(facts.MissingInStore))
		}
		return doctorCheck{name, doctorWarn, detail}
	}
	return doctorCheck{name, doctorOK, base + "：与库一致（层序：继承环境 > 托管文档 > <cwd>/.env > <MATE_HOME>/.env）"}
}

// mirrorOnlyRemedy 给出"镜像里有而库里没有"的**可执行**出路（按引用类别分开说）。
//
// 为什么必须分开：两类引用的出路完全不同，合成一句（"在「设置 · 模型」或 MCP 面板里重新填一次"）
// 会把一半的用户指向**不存在的入口**——
//   - `providers/<id>`：密钥在「设置 · 模型」里有写入口，重填一次即与库对齐；
//   - `store:mcp/<name>`（MCP 密钥）：值的写面这一批**还没有**（面板只能改引用名），能做的只有
//     把定义里的引用改成 `env:<ENV_NAME>`（值由进程环境或 `.env` 提供）。
//
// 把用户做不到的那一条写成出路，等于没有出路——所以这里逐类说，且只说真能做到的。
func mirrorOnlyRemedy(refs []string) string {
	var providers, mcpSecrets []string
	for _, r := range refs {
		if strings.HasPrefix(r, credential.ProviderMirrorRefPrefix) {
			providers = append(providers, r)
			continue
		}
		mcpSecrets = append(mcpSecrets, r)
	}
	parts := make([]string, 0, 2)
	if len(providers) > 0 {
		parts = append(parts, fmt.Sprintf(
			"供应商密钥（%s）到「设置 · 模型」里重新填一次即对齐，顺手清掉镜像里那条手写项",
			bracketList(providers)))
	}
	if len(mcpSecrets) > 0 {
		parts = append(parts, fmt.Sprintf(
			"MCP 密钥（%s）把定义里的引用改成 env:<ENV_NAME>（值由进程环境或 `.env` 提供）"+
				"——库里那份值没有写入口，所以镜像里这条留着只会反复报同一句",
			bracketList(mcpSecrets)))
	}
	return strings.Join(parts, "；")
}

// checkProviderKeys 逐条档案的密钥可得性：库那一列是空的 → warn（尚未设密钥）。
//
// v20 的判据只有一条：`providers.key` 非空（`key_ref` 是派生量，反推不出"有没有值"）。
// 它把 v19 那种"逐条去问密文目录"的间接判据换成了直接判据——"声明了引用却没有密钥"
// 这个状态在结构上不存在，剩下的真问题只是"这条档案还没设密钥"。
func checkProviderKeys(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
	name := "provider keys"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：密钥面无从校验"}
	}
	if len(facts.ProviderIDs) == 0 {
		return doctorCheck{name, doctorOK, "没有任何 provider 档案，密钥面为空"}
	}
	if len(facts.NoKeyProviders) > 0 {
		return doctorCheck{name, doctorWarn, fmt.Sprintf(
			"%d 条档案还没有密钥（`providers.key` 为空）：%s。引用它们的会话不会注入任何密钥变量"+
				"——在「设置 · 模型」里补一条；或者让一个环境变量提供（名字按 "+
				"`MATE_<档案 id 折大写>_<8 位哈希>_API_KEY` 派生，设好之后面板会在这条档案上写明"+
				"生效的是哪个变量；层序上它优先于面板里那条）；或者把引用它的模型条目改挂到别的供应商",
			len(facts.NoKeyProviders), bracketList(facts.NoKeyProviders))}
	}
	return doctorCheck{name, doctorOK, fmt.Sprintf(
		"%d 条档案都有密钥（库那一列非空 = 唯一判据）", len(facts.ProviderIDs))}
}

// checkMcpSecrets MCP 密钥面：条目数与孤儿（定义都没了的那些）。
//
// 为什么只记警告：孤儿**不影响任何一条 server 的可用性**，而且删定义时值留着是**正常**的
// （用户可能正要把它挂到另一个 server 上）。但它值得被看见——库里躺着一份没人用的密钥，
// 与"我以为删掉那个 server 就把密钥也删了"是两种不同的认知。
//
// 同时如实说出**没有写入口**这件事：面板里能改引用（键名），值的写面这一批还没有，
// 所以出路只有两条（写进库 / 改用 `env:` 引用），不能让用户以为"在界面上找不到就是坏了"。
func checkMcpSecrets(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
	name := "mcp secrets"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：库里的 MCP 密钥面无从校验"}
	}
	if len(facts.McpSecretRefs) == 0 {
		return doctorCheck{name, doctorOK, "库里没有任何 MCP 密钥条目（mcp_secrets 为空）"}
	}
	detail := fmt.Sprintf("mcp_secrets %d 条：%s", len(facts.McpSecretRefs), bracketList(facts.McpSecretRefs))
	if len(facts.OrphanMcpSecrets) > 0 {
		return doctorCheck{name, doctorWarn, detail + fmt.Sprintf(
			"；其中 %d 条没有任何 MCP 定义引用（孤儿）：%s——无害也不自动删；"+
				"确认没用之后可以自行删除对应行",
			len(facts.OrphanMcpSecrets), bracketList(facts.OrphanMcpSecrets))}
	}
	return doctorCheck{name, doctorOK, detail}
}

// checkLegacySecretLeftovers 旧密钥机制的遗留物（v20 起平台不再读写它们）。
//
// **只报不判失败、也绝不替用户删**：那是用户唯一一份旧密钥本体，删除不可逆；而"库里都已经有了"
// 这件事由 `provider keys` / `mcp secrets` / `credentials` 三条回答。所以这一条永远是 warn
// （有遗留物时），并把处置权与确认依据写在文案里。
func checkLegacySecretLeftovers(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
	name := "legacy secrets"
	if !facts.LegacySecretDir && !facts.LegacySecretKeyFile {
		return doctorCheck{name, doctorOK, "没有旧密钥机制的遗留物（旧密文目录与主密钥文件都不在）"}
	}
	found := []string{}
	if facts.LegacySecretDir {
		found = append(found, fmt.Sprintf("旧密文目录（%d 个密文）", facts.LegacySecretCount))
	}
	if facts.LegacySecretKeyFile {
		found = append(found, "旧主密钥文件")
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf("锚点=%s 仍在盘上：%s。%s",
		orDash(facts.LegacySecretAnchor), strings.Join(found, "、"), orDash(facts.LegacySecretNote))}
}

// permWord 权限位的展示（不存在 → `-`）。
func permWord(exists bool, perm string) string {
	if !exists || strings.TrimSpace(perm) == "" {
		return "-"
	}
	return perm
}

// checkProviderImport 导入对账（`meta.providers_import_mismatch` 的逐条报出）。
//
// v19 已删掉「供应商悬空引用」那一条检查：现在"哪条档案被引用"的唯一判据是**模型条目**的
// `provider_id` 外键，它在 store 的同一事务里判定（`DeleteProviderDefinitionIfUnreferenced`），
// 不可能留下悬空引用；而旧的"agent 声明了一个不存在的档案"随 agent 面整体删除。
func checkProviderImport(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
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

// checkModalityDeclaration 两侧差异如实可见（ADR-12/A11）：模型输入模态在**声明了交付面**的
// 底座上是已声明的交付事实，在没声明的那一侧只是**平台记录**。
//
// 为什么要单列一条：同一份平台清单在不同底座上的实际行为不同，而"默认 text"会把底座目录里
// 原本的收图能力**覆盖掉**（老模型里本来能看图的现在收不了图，除非用户逐条勾选）。这一条让
// 用户不必等到某个会话失败才知道这件事——它回答的问题是"我勾的图片，到底有没有生效"。
func checkModalityDeclaration(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
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

// checkModelCapacity 容量声明面（v25）：未声明的模型会落到**底座自己的兜底**，而不是平台事实。
//
// 为什么要单列一条：路由键加上保留前缀之后（v24–v25 的路由键隔离），每条路由都是平台手写的声明，
// 底座内置目录**再也命中不了**——于是"容量"不再有"恰好命中目录时侥幸拿到真值"这条退路：平台不说，
// 就是底座那句自注 "a guess by construction" 的默认值（dsh 的 262144/32768）。这条体检回答的是
// "我看到的上下文上限，到底是不是我设的"。
//
// **不判红**：没声明是一个合法状态（用户可能有意让环境决定），所以最高只到 warn——但要把条数与
// 具体是哪些**说出来**，并给出路（「设置 · 模型」逐条填）。
func checkModelCapacity(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
	name := "模型容量声明"
	if facts.Carrier != "db" {
		return doctorCheck{name, doctorWarn, "载体不是 DB（" + orDash(facts.CarrierNote) + "）：容量声明面无从校验"}
	}
	if facts.ModelRows == 0 {
		return doctorCheck{name, doctorWarn, "平台模型清单为空：没有可校验的容量声明"}
	}
	none, half := facts.ModelsWithoutCapacity, facts.ModelsPartialCapacity
	if len(none) == 0 && len(half) == 0 {
		return doctorCheck{name, doctorOK,
			fmt.Sprintf("清单里 %d 条模型都声明了输入/输出上限", facts.ModelRows)}
	}
	detail := fmt.Sprintf("清单共 %d 条：**%d 条未声明容量**（用底座兜底——那不是平台事实）%s",
		facts.ModelRows, len(none), bracketList(none))
	if len(half) > 0 {
		detail += fmt.Sprintf("；**%d 条只声明了一半**（用户以为设过了，实际只有一半是自己的值）%s",
			len(half), bracketList(half))
	}
	detail += "；出路：「设置 · 模型」里逐条填「输入上限/输出上限」（留空 = 继续由环境兜底）"
	return doctorCheck{name, doctorWarn, detail}
}

// checkRuntimeModels 逐 runtime 的清单物化状态与差集。
func checkRuntimeModels(facts doctorfacts.ProviderDoctorFacts) []doctorCheck {
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
func checkRuntimeModelState(rt doctorfacts.ProviderRuntimeFacts) doctorCheck {
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
func checkRuntimeModelDiff(rt doctorfacts.ProviderRuntimeFacts) doctorCheck {
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
func checkProviderBaseURL(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
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
func checkMigrationBackup(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
	name := "迁移备份"
	if facts.MigrationBackup != "" {
		return doctorCheck{name, doctorOK, "可回退依据：`" + facts.MigrationBackup +
			"`（恢复到哪一份就退到哪一版；v20 起密钥进库，所以这份备份与 `mate.db` 同级敏感，" +
			"别把它放进共享/同步目录）"}
	}
	if facts.RuntimeConfigDigestWritten || facts.ProviderRows > 0 {
		return doctorCheck{name, doctorWarn, "本机已有资源数据但没有找到迁移备份（`<数据目录>/backups/mate.db.bak-v*`，" +
			"或老布局的 `mate.db.bak-v*`）：若这个库是从旧版本升上来的，说明备份已被清理——" +
			"**回退窗口已关闭**（schema 前进之后只能前滚；逃生门是 `mate provider export`）"}
	}
	return doctorCheck{name, doctorOK, "没有需要备份的迁移（这个库不是从旧版本升上来的）"}
}

// checkLegacyMigrationBackups 报老布局（v19 之前）留在库文件同级的迁移备份。
//
// 判 warn 而不是 fail，也**不做任何搬移/删除**：那些文件是**有效退路**（恢复到哪一份就退到
// 哪一版），只是不再按新布局落盘。医生的职责是让人看见"这里还有 7 个文件、你可以把它们收进
// `backups/` 或按需删掉"，处置权在用户——自动搬移会改掉回退路径，自动删除更是直接关门。
func checkLegacyMigrationBackups(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
	name := "备份布局"
	if len(facts.LegacyMigrationBackups) == 0 {
		return doctorCheck{name, doctorOK, "迁移备份都在 `<数据目录>/backups/` 下（或本机没有备份）"}
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf(
		"老布局遗留 %d 个迁移备份仍在数据目录根下（v19 起新备份落 `<数据目录>/backups/`）：%s；"+
			"它们**照旧可用于回退**，可自行移进 `backups/` 或按需删除——`mate doctor` 不动它们",
		len(facts.LegacyMigrationBackups), bracketList(facts.LegacyMigrationBackups))}
}

// checkMigrationNotes 迁移留下的待人工处置事实（默认模型冲突 / 归并冲突 / 无出口字段）。
//
// 迁移**不阻塞**这些（B1/R3），但它们必须被看见——否则"未迁移字段"就成了一句没人能验证的话。
func checkMigrationNotes(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
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
func checkMigrationNotesV13(facts doctorfacts.ProviderDoctorFacts) doctorCheck {
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
func checkSensitiveFiles(facts doctorfacts.ProviderDoctorFacts) []doctorCheck {
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
