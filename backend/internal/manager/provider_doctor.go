package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/capability"
	goruntime "github.com/morehao/ark-mate/pkg/runtime"
)

// ——— K2：`mate doctor` 的 provider（供应商凭据）与模型清单体检 ———
//
// 分工：本文件是**事实的构造函数**（只读），`internal/app/command/doctor.go` 只负责排版。
// 该包是薄打印层——SQL、凭据分层与对账算法都不该出现在那里。
//
// 三条纪律（与 doctor 的既有口径逐条对齐）：
//
//	① **只读**：库由 `store.OpenReadOnly` 以 `mode=ro` 打开（不迁移）；凭据面只**读**，
//	   镜像权限过宽也只是如实报出（**不自动 chmod**）；不写 meta、不删任何遗留文件
//	   （旧密文是用户唯一一份旧密钥本体，删除不可逆——医生没有这个权限）。
//	② **只报不判**：本文件产出事实与三档结论，"哪一档算失败"由 doctor 的既有口径决定——
//	   "无法校验"记警告，**事实与声明不符**才判失败。
//	③ **取不到就记 unknown**：离线命令没有编排器、也没有会话，运行时广告的目录取不到。
//	   这时必须记"无法校验"，**不许**把"没取到"当成"底座一条都没有"（那是假红），
//	   也不许当成"两侧一致"（那是假绿）。判据是取样来源（`source=none` 就是没来源）。
//
// 载体口径：provider 档案的**事实源**按持久层模式分叉（v11）——sqlite 模式在 `providers` 表
// （本文件的可读面），memory 模式在 `<ProviderDir>/providers.json`。memory 模式下本文件**不代读**
// 那个文件（那是另一个载体的语义，代读会让"谁是权威"再次变浑）：DB 侧的事实全部记 unknown，
// 只保留与载体无关的那几条——托管镜像面与旧机制遗留物。

// ProviderDoctorOptions 一次只读体检的输入。
type ProviderDoctorOptions struct {
	// Home mate home（托管凭据文档的锚点，也是 runtime 隔离 env 的根；旧机制遗留的兜底锚点）。
	Home string
	// ProviderDir provider 定义目录。**旧机制遗留锚点的权威取法是它的父目录**（v10–v19 的取法，
	// 见 LegacySecretAnchor：档案目录在 `<home>/providers`，遗留物在 `<home>/…`）；为空时回落到 Home。
	ProviderDir string
	// DBPath 库文件路径（只为报告里能印出"载体是谁"；读取动作由调用方完成）。
	DBPath string
}

// ProviderDoctorFacts provider 与模型清单段的只读事实（`mate doctor --json` 的 `providers` 段）。
//
// 字段序即 JSON 键序（与 doctorReport 同一条确定性纪律）。所有列表都**已排序**且非 nil
// （空 = `[]`，机器消费方不必区分 null 与空）。
type ProviderDoctorFacts struct {
	// Carrier provider 档案的事实源：db（sqlite）/ file（memory）/ unknown。
	Carrier string `json:"carrier"`
	// CarrierNote 载体口径的一句话说明（memory/unknown 时说明为什么读不到 DB 事实）。
	CarrierNote string `json:"carrierNote,omitempty"`
	// DBPath 库文件路径（未配置 → 空）。
	DBPath string `json:"dbPath,omitempty"`
	// ProviderRows `providers` 表行数（Carrier != db 时无意义，恒 0）。
	ProviderRows int `json:"providerRows"`
	// ProviderIDs 档案 id（升序）。
	ProviderIDs []string `json:"providerIds"`
	// ModelRows 平台模型清单总行数（跨 runtime；K5 的迁移前检查项之一）。
	ModelRows int `json:"modelRows"`
	// RuntimeConfigDigestWritten `meta.runtime_config_digest` 是否被写过
	// （写过 = 这台机器上已经有物化过的模型配置；K5 的迁移前检查项之一）。
	RuntimeConfigDigestWritten bool `json:"runtimeConfigDigestWritten"`
	// ProvidersFile `<ProviderDir>/providers.json` 是否存在（v11 起它是导入/导出载体）。
	ProvidersFile bool `json:"providersFile"`
	// ProvidersBackup `<ProviderDir>/providers.json.bak-v11` 是否存在（迁移前的快照，回退路径）。
	ProvidersBackup bool `json:"providersBackup"`

	// LegacySecretAnchor 旧密钥机制的锚点（遗留目录与主密钥文件都在它下面；空 = 解析不出来）。
	// 报告里印的是**具体路径**，字段名刻意不带旧机制的名字。
	LegacySecretAnchor string `json:"legacySecretAnchor,omitempty"`
	// LegacySecretDir 旧密文目录是否还在（`<anchor>/<旧目录名>`）。
	LegacySecretDir bool `json:"legacySecretDir"`
	// LegacySecretKeyFile 旧主密钥文件是否还在（`<anchor>/<旧主密钥文件名>`）。
	LegacySecretKeyFile bool `json:"legacySecretKeyFile"`
	// LegacySecretCount 旧密文目录里仍有的密文条数（目录不在 → 0）。
	LegacySecretCount int `json:"legacySecretCount"`
	// LegacySecretNote 遗留物的处置建议（**已迁移，可删**；删之前先确认 providers 的 `has_key` 都对得上）。
	//
	// 医生**绝不替用户删**：密文是用户唯一一份旧密钥本体，删除不可逆；而"库里都有了"这件事
	// 由上面几个凭据事实回答，判断权在用户。
	LegacySecretNote string `json:"legacySecretNote,omitempty"`

	// ——— v20 凭据面：库是权威，`<MATE_HOME>/credentials.json` 是可重建镜像 ———

	// CredentialsPath 托管镜像路径（空 = mate home 解析不出来）。
	CredentialsPath string `json:"credentialsPath,omitempty"`
	// CredentialsExists 镜像文件是否存在。
	CredentialsExists bool `json:"credentialsExists"`
	// CredentialsPerm 八进制权限串（不存在 → 空）。
	CredentialsPerm string `json:"credentialsPerm,omitempty"`
	// CredentialsPermOK 权限是否恰好 0600（不存在或无法判定 → false，配 CredentialsNote 看）。
	CredentialsPermOK bool `json:"credentialsPermOk"`
	// CredentialsNote 镜像面的一句话（不存在 / 解析失败 / 权限过宽的原因与出路）。
	CredentialsNote string `json:"credentialsNote,omitempty"`
	// CredentialsRefs 镜像里的条目数（读不出来 → 0）。
	CredentialsRefs int `json:"credentialsRefs"`
	// ProviderKeysWithKey 库里有密钥的档案 id（升序）——`has_key` 的**唯一**权威判据。
	ProviderKeysWithKey []string `json:"providerKeysWithKey"`
	// McpSecretRefs 库 `mcp_secrets` 里的引用（升序）。
	McpSecretRefs []string `json:"mcpSecretRefs"`
	// MissingInCredentials 库里有、镜像里没有的引用（升序）：镜像陈旧或写入失败。
	//
	// 处置是"重跑一次 serve"（装配期会把库并进镜像）——所以它不是故障，是待对齐。
	MissingInCredentials []string `json:"missingInCredentials"`
	// MissingInStore 镜像里有、库里没有的引用（升序）：**只报，绝不自动回灌**。
	//
	// 两种可能：这条密钥是在 v20 迁移前手写进镜像的（那时镜像还没有），或者库那一份被删过。
	// 医生不替用户决定"该信哪一份"；出路是重新在面板里填一次（或把值写进库）。
	MissingInStore []string `json:"missingInStore"`
	// OrphanMcpSecrets `mcp_secrets` 里**没有任何 MCP 定义引用**的条目（升序）。
	//
	// 与档案孤儿同形：只报不删。删 MCP 定义时值不该跟着消失（用户可能正要把它挂到另一个
	// server 上），所以孤儿是**正常**的中间状态，但值得知道。
	OrphanMcpSecrets []string `json:"orphanMcpSecrets"`
	// NoKeyProviders 库里**没有密钥**的档案（尚未设置密钥）。
	//
	// 它不是故障（档案可以先建后补密钥），但"开会话时才发现没密钥"值得提前说。
	NoKeyProviders []string `json:"noKeyProviders"`

	// ImportMismatch `meta.providers_import_mismatch` 的逐条内容（导入对账）。
	ImportMismatch []string `json:"importMismatch"`
	// ImportMismatchBenignOnly true = 不一致条目全是"库里有、文件里没有"这一类
	// （写面新建后文件还没导出，属正常快照滞后，不是故障）。
	ImportMismatchBenignOnly bool `json:"importMismatchBenignOnly"`

	// Runtimes 逐 runtime 的清单同步结论与对账（runtime 升序）。
	Runtimes []ProviderRuntimeFacts `json:"runtimes"`

	// MissingBaseURL 缺端点的供应商 id（升序）。
	//
	// 它是 v12 新增的两类问题项之一（B1）：v11 里端点藏在逐底座的 `projections` 里，
	// 迁移后**推不出来**就留空——那些供应商的模型一条也不会被交付给任何底座，
	// 而界面上它看起来"就是一条供应商"。必须在体检里显式说出来，并给可执行的出路。
	MissingBaseURL []string `json:"missingBaseUrl"`

	// MigrationNotesV13 v13 迁移留下的**待人工处置事实**（`meta.migration_notes_v13`；不存在 → nil）。
	//
	// 与 v12 那份**分键**（不复用 `migration_notes`）：v13 覆盖 v12 的键等于把 v12 那些尚未
	// 处置的事实静默抹掉。这一段回答"列化有没有悄悄改语义"：被丢弃的 body 未知键、正文里
	// 曾有明文密钥的行（**只有 id，没有值**）、`created_at` 近似行、按默认 `text` 填的模态、
	// v12 里就是孤儿的 model 行。
	MigrationNotesV13 *store.MigrationNotesV13 `json:"migrationNotesV13,omitempty"`

	// MigrationNotes v12 迁移留下的**待人工处置事实**（`meta.migration_notes`；不存在 → nil）。
	//
	// 它回答的是"迁移有没有悄悄丢东西"：默认模型冲突（多底座默认不一致，已按 runtime id
	// 字典序取一）、归并时的显示名冲突、v12 无出口的 v11 字段（`extra`/`compat`/`package`…）。
	MigrationNotes *store.MigrationNotes `json:"migrationNotes,omitempty"`

	// MigrationBackup DB 迁移备份路径（v19 起 = `<db 目录>/backups/<db 文件名>.bak-v<版>-<ts>`；
	// 空 = 本机没迁移过或库无文件）。
	//
	// 这是**回退窗口的唯一依据**：v12 落地后旧二进制会因为 schema 版本前进而拒绝启动，
	// 唯一的退路就是恢复这份文件。v20 之前它**只管 DB**（密钥不在库内）；v20 起密钥进库，
	// 于是它与 `mate.db` 同级敏感（同名口径见 SensitiveFiles）。
	MigrationBackup string `json:"migrationBackup,omitempty"`

	// LegacyMigrationBackups **老布局**（v19 之前）留在库文件同级的迁移备份（新的在前）。
	//
	// 它们**仍是有效退路**（恢复到哪一份就退到哪一版），所以不是垃圾；只是不再按 v19 起的布局
	// 落盘。体检只报事实与处置建议（移到 `backups/` 或按需删），**绝不自动搬移或删除**——
	// 回退窗口的处置权在用户，医生是只读的。
	//
	// `omitempty`：这是**条件性**事实（通常一个都没有，与 MigrationBackup/MigrationNotes 同类），
	// 不加它会让 `--json` 的字节面对所有存量消费方无条件变宽。
	LegacyMigrationBackups []string `json:"legacyMigrationBackups,omitempty"`

	// SensitiveFiles 需要与 `mate.db` 同等看待的敏感文件（含权限位）。
	//
	// v12 新增了 dsh 的原生凭据文档（隔离根内，0600），v20 又加了托管凭据文档与**含明文的库**
	// 与它的迁移备份。它们都不是"平台的配置文件"，而是**明文密钥**——备份/同步整个目录即带走它，
	// 所以体检必须把它们与 `mate.db` 放在同一档看。
	SensitiveFiles []SensitiveFileFacts `json:"sensitiveFiles"`
}

// SensitiveFileFacts 一个敏感文件的体检事实。
//
// `Perm` 是八进制权限串（如 `0600`）；`PermChecked=false` = 平台**无法**判定（Windows 没有可检查
// 的 mode）——那时如实说"无法校验"，既不假装合规也不假报失败（与 doctor 的既有口径一致）。
type SensitiveFileFacts struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Perm        string `json:"perm,omitempty"`
	PermChecked bool   `json:"permChecked"`
	// Expected 期望的权限（八进制串），供报告直接对照。
	Expected string `json:"expected"`
	// OK 该文件当前是否满足期望（不存在 = OK：没有落点就没有风险；无法判定 = 按"无法校验"报 warn）。
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// ProviderRuntimeFacts 某 runtime 的清单同步事实（状态 + 对账）。
//
// v12 起"能不能写"这件事不再由平台门控（没有 `ConfigProjection.Tested`/`Writable` 了）：
// 交付结论由 adapter 回带，平台如实转述。所以这一段的字段就是**端口结论的直投影**。
type ProviderRuntimeFacts struct {
	Runtime string `json:"runtime"`
	// Status applied | unchanged | unsupported | untested | failed。
	Status string `json:"status"`
	// Where 人话落点（"同步到哪了"）。
	Where string `json:"where,omitempty"`
	// Effect 清单通道的生效时刻（immediate | next_run | next_session | next_process）。
	Effect string `json:"effect,omitempty"`
	// CredentialChannel native_doc | env | none（凭据通道与清单分开报，ADR-14）。
	CredentialChannel string `json:"credentialChannel,omitempty"`
	// CredentialTested 该凭据通道是否已实测（false → 界面上只能说「待实测」）。
	CredentialTested bool `json:"credentialTested"`
	// Pending true = 平台已交付但底座还没读到（**不许说成"已生效"**）。
	Pending bool `json:"pending"`
	// ModelCount 平台声明的清单条数。
	ModelCount int `json:"modelCount"`
	// DeclaresModelInput 该底座的清单交付面是否声明模型的输入模态（ADR-12）。
	//
	// 它由各 adapter 自己声明（例如 dsh=true：平台总是写 `models[].input`；有的底座 false：
	// 平台不写、底座自己知道）。这一段存在的理由就是**如实区分两侧**：同一份平台清单在不同
	// 底座上的实际行为不同，用户不该以为自己勾的能力一定生效（在 false 的那一侧它只是平台记录）。
	DeclaresModelInput bool `json:"declaresModelInput"`
	// Note 人话说明（未同步过、未声明交付面、逐条 problem…）。
	Note string `json:"note,omitempty"`

	// AdvertisedKnown 本次是否真的取到了运行时广告的目录（离线命令 = false）。
	AdvertisedKnown bool `json:"advertisedKnown"`
	// AdvertisedCount 运行时广告的模型候选数（AdvertisedKnown=false 时无意义）。
	AdvertisedCount int `json:"advertisedCount"`
	// Missing 平台声明了、底座没广告（"写了没生效"）的条目。
	Missing []string `json:"missing,omitempty"`
	// DiffNote 差集不可得的原因（AdvertisedKnown=false 时必填）。
	DiffNote string `json:"diffNote,omitempty"`
	// HighRisk **写了完全没生效**的形态：declared 非空而 advertised 一条都没有。
	HighRisk bool `json:"highRisk"`
}

// CollectProviderDoctorFacts 采集 provider / 模型清单的只读事实。
//
// `st` 可以为 nil（memory 模式 / 库读不出来）：那时 DB 派生的事实全部记 unknown，
// 只保留载体无关的那几条（镜像面与旧机制遗留）。**本函数不写任何东西**（唯一的落笔是 SQLite
// 只读连接可能重建的 `-shm`/`-wal` 两个共享内存面文件，见 `store.OpenReadOnly` 的注释）。
func CollectProviderDoctorFacts(ctx context.Context, st *store.Store, opts ProviderDoctorOptions) ProviderDoctorFacts {
	providerDir := strings.TrimSpace(opts.ProviderDir)
	facts := ProviderDoctorFacts{
		Carrier:              "unknown",
		CarrierNote:          "没有可读的持久层：provider 档案的行数、悬空引用与对账都无从校验",
		DBPath:               strings.TrimSpace(opts.DBPath),
		ProviderIDs:          []string{},
		NoKeyProviders:       []string{},
		ProviderKeysWithKey:  []string{},
		McpSecretRefs:        []string{},
		MissingInCredentials: []string{},
		MissingInStore:       []string{},
		OrphanMcpSecrets:     []string{},
		ImportMismatch:       []string{},
		Runtimes:             []ProviderRuntimeFacts{},
		MissingBaseURL:       []string{},
		SensitiveFiles:       []SensitiveFileFacts{},
	}

	// 文件侧（与载体无关，两种模式都要看）：providers.json 与它的迁移前备份是否存在。
	if providerDir != "" {
		facts.ProvidersFile = providerFileExists(providerDir)
		facts.ProvidersBackup = fileExists(filepath.Join(providerDir, providersBackupName))
	}

	// ——— 凭据面（v20）：库是权威（`providers.key` + `mcp_secrets`），镜像是可重建副本 ———
	// 先读镜像（不需要库），再在下面的 DB 段里与库对账。
	mirror := readCredentialsMirror(opts.Home)
	facts.CredentialsPath, facts.CredentialsExists, facts.CredentialsPerm = mirror.Path, mirror.Exists, mirror.Perm
	facts.CredentialsPermOK, facts.CredentialsNote, facts.CredentialsRefs = mirror.PermOK, mirror.Note, len(mirror.Raw)

	// ——— DB 侧（sqlite） ———
	if st != nil {
		facts.Carrier = "db"
		facts.CarrierNote = ""
		if rows, err := st.ListProviderDefinitions(ctx); err != nil {
			// 读不出来（旧 schema 没这张表 / 库坏了）不是"0 条"——如实记 unknown。
			facts.Carrier = "unknown"
			facts.CarrierNote = "读 providers 行失败：" + err.Error() +
				"（v11 之前的库没有这张表；跑一次新版本 serve 会迁移，或恢复备份）"
		} else {
			for i := range rows {
				facts.ProviderIDs = append(facts.ProviderIDs, rows[i].ID)
				if strings.TrimSpace(rows[i].BaseURL) == "" {
					facts.MissingBaseURL = append(facts.MissingBaseURL, rows[i].ID)
				}
				// `has_key` 的**唯一**权威判据就是这一列非空（`key_ref` 是派生量）。
				if strings.TrimSpace(rows[i].Key) == "" {
					facts.NoKeyProviders = append(facts.NoKeyProviders, rows[i].ID)
				} else {
					facts.ProviderKeysWithKey = append(facts.ProviderKeysWithKey, rows[i].ID)
				}
			}
			sort.Strings(facts.ProviderIDs)
			sort.Strings(facts.MissingBaseURL)
			sort.Strings(facts.NoKeyProviders)
			sort.Strings(facts.ProviderKeysWithKey)
			facts.ProviderRows = len(facts.ProviderIDs)
			// 迁移留下的待处置事实 + 回退依据（两者都读 meta，读不到只告警不影响其余事实）。
			if notes, ok, err := st.MigrationNotesV12(ctx); err == nil && ok {
				facts.MigrationNotes = notes
			}
			if notes, ok, err := st.MigrationNotesV13(ctx); err == nil && ok {
				facts.MigrationNotesV13 = notes
				if notes.BackupPath != "" {
					facts.MigrationBackup = notes.BackupPath
				}
			}
			if facts.MigrationBackup == "" {
				if list := st.MigrationBackupPaths(); len(list) > 0 {
					facts.MigrationBackup = list[0]
				}
			}
			// 老布局遗留（v19）：仍然是有效退路，只报不处置（见字段注释）。
			facts.LegacyMigrationBackups = st.MigrationBackupPathsLegacy()
		}
		if models, err := st.ListModelDefinitions(ctx); err == nil {
			facts.ModelRows = len(models)
		}
		if _, ok, err := st.GetMeta(ctx, model.MetaKeyRuntimeConfigDigest); err == nil {
			facts.RuntimeConfigDigestWritten = ok
		}
		if raw, ok, err := st.GetMeta(ctx, model.MetaKeyProvidersImportMismatch); err == nil && ok && strings.TrimSpace(raw) != "" {
			for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
				if strings.TrimSpace(line) != "" {
					facts.ImportMismatch = append(facts.ImportMismatch, line)
				}
			}
			facts.ImportMismatchBenignOnly = providerMismatchBenignOnly(facts.ImportMismatch)
		}
	}
	facts.SensitiveFiles = collectSensitiveFiles(opts.Home, providerDir, st)

	// ——— 凭据面（载体无关的部分）：镜像 ↔ 库对账 + 旧机制遗留 ———
	var mcpRefs []string
	if st != nil {
		if secrets, err := st.ListMcpSecrets(ctx); err == nil {
			for i := range secrets {
				mcpRefs = append(mcpRefs, secrets[i].Ref)
			}
			sort.Strings(mcpRefs)
		}
	}
	facts.McpSecretRefs = append([]string{}, mcpRefs...)
	facts.MissingInCredentials, facts.MissingInStore = reconcileCredentialsMirror(
		mirror, facts.ProviderKeysWithKey, mcpRefs)
	if st != nil {
		facts.OrphanMcpSecrets = orphanMcpSecrets(ctx, st, mcpRefs)
	}

	// 旧机制留在盘上的遗留物：**只报不删**（唯一一份旧密钥本体，删除不可逆）。
	anchor := LegacySecretAnchor(opts.Home, providerDir)
	facts.LegacySecretAnchor = anchor
	facts.LegacySecretDir, facts.LegacySecretKeyFile, facts.LegacySecretCount = legacySecretLeftovers(anchor)
	if facts.LegacySecretDir {
		facts.LegacySecretNote = "旧密钥机制的目录还在（本版已不再读写它）。密钥已搬进库" +
			"（本次报告里的 providerKeysWithKey / mcpSecretRefs 就是库里的现状）；" +
			"在「设置 · 模型」里逐条确认密钥状态都对得上（面板上写着「API 密钥已配置/缺失」，" +
			"与本次报告的 providerKeysWithKey 是同一批）之后，可自行删除该目录" +
			"与同目录下的主密钥文件——医生不会替用户删。"
	} else if facts.LegacySecretKeyFile {
		facts.LegacySecretNote = "旧机制的主密钥文件还在，但密文目录已不在：它已没有任何用途，可自行删除。"
	}

	// ——— 模型清单：逐 runtime 的物化状态 + 对账 ———
	facts.Runtimes = collectRuntimeModelFacts(ctx, st, opts.Home)
	return facts
}

// LegacySecretAnchor 旧密钥机制的锚点：**ProviderDir 的父目录**优先（与 v13–v19 的取法逐字同源，
// `internal/store/legacy_vault.go` 的搬运也用同一个根），ProviderDir 为空时回落到 mate home。
//
// 之所以不复用 `MATE_HOME`：用户把 `MATE_PROVIDERS` 指到别处时，历史实现是按"provider 目录的
// 父目录"找主密钥的——遗留物与搬运都得跟着它，否则会去报一个当年根本没写过的路径。
func LegacySecretAnchor(home, providerDir string) string {
	if providerDir != "" {
		return filepath.Dir(filepath.Clean(providerDir))
	}
	return strings.TrimSpace(home)
}

// LegacySecretPaths 旧机制遗留物的两个位置：密文目录与主密钥文件（锚点为空 → 两个空串）。
//
// 导出它是为了让**诊断文案与用例构造共享同一份"它们在哪儿"**：医生报出的锚点、用例造出的
// 遗留物、以及 `internal/store` 的搬运扫描的必须是同一批磁盘文件。三处各写一遍路径，
// 迟早会有一处漂移——而漂移的表现是"医生报没有遗留物，而用户明明看得见它们"。
func LegacySecretPaths(anchor string) (dir, keyFile string) {
	a := strings.TrimSpace(anchor)
	if a == "" {
		return "", ""
	}
	return filepath.Join(a, "vault"), filepath.Join(a, "vault.key")
}

// legacySecretLeftovers 旧机制的遗留物事实（目录、主密钥文件、密文条数）。
//
// 两个文件名与 `internal/store/legacy_vault.go` 的常量**逐字一致**：那边的搬运是唯一读它们的地方，
// 这里是唯一"报出来"的地方。两处字面量必须同源——它们指向的是同一批磁盘文件（该文件在 store 侧
// 是白名单内的历史面，所以那一份定义不动；见 v20 词规）。
func legacySecretLeftovers(anchor string) (dirExists, keyFileExists bool, count int) {
	dir, keyFile := LegacySecretPaths(anchor)
	if dir == "" {
		return false, false, 0
	}
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		dirExists = true
		count = countLegacyCiphertexts(filepath.Join(dir, "providers"))
	}
	if _, err := os.Lstat(keyFile); err == nil {
		keyFileExists = true
	}
	return dirExists, keyFileExists, count
}

// countLegacyCiphertexts 数 `<...>/providers/*.enc`（读不动 → 0，理由由 LegacySecretCount 的
// 语义承担：它只是"还剩几条"的参考值，不是判据）。
func countLegacyCiphertexts(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".enc") {
			n++
		}
	}
	return n
}

// credentialsMirrorFacts 托管镜像的只读事实（路径/存在/权限/条目数/人话说明）。
type credentialsMirrorFacts struct {
	Path   string
	Exists bool
	Perm   string
	PermOK bool
	Note   string
	// Raw 镜像里的原始 `引用 → 值` 映射（**只用来比对引用集合，值不进任何报告**）。
	Raw map[string]string
}

// readCredentialsMirror 只读读取托管镜像。
//
// 它**刻意不复用 `pkg/credentials.Doc`**：那个句柄的语义是"可写面"（权限不合即失败），
// 而医生要做的是"读得动就读、读不动如实说"——权限过宽在这里是**要报出来的事实**，
// 不是让整段体检报错的理由。读取只解析顶层字符串映射，不解释任何值的含义。
func readCredentialsMirror(home string) credentialsMirrorFacts {
	out := credentialsMirrorFacts{Raw: map[string]string{}}
	home = strings.TrimSpace(home)
	if home == "" {
		out.Note = "mate home 解析不出来（不知道该去哪儿找凭据文档）"
		return out
	}
	out.Path = filepath.Join(home, CredentialsFileName)
	fi, err := os.Lstat(out.Path)
	if err != nil {
		if os.IsNotExist(err) {
			out.Note = "尚未生成（平台在第一次写入密钥时会建它）；库里的密钥**不受它影响**，" +
				"下一次 `mate serve` 会把库里的并进来"
			return out
		}
		out.Note = "不可读：" + err.Error()
		return out
	}
	out.Exists = true
	out.Perm = fmt.Sprintf("%04o", fi.Mode().Perm())
	switch {
	case fi.Mode().Perm()&0o077 == 0:
		out.PermOK = true
	case fi.Mode().Perm()&0o077 == 0o077:
		// 权限过宽是**硬失败**（`pkg/credentials` 拒绝读它）：这里如实报出来，并给出出路。
		out.PermOK = false
		out.Note = "权限过宽（" + out.Perm + "）：平台会拒绝读它（这是硬约束——" +
			"凭据文档是可自由复制的明文）。`chmod 600` 之后重启即可恢复"
	default:
		out.PermOK = false
		out.Note = "权限不是 0600（" + out.Perm + "）：平台要求恰好 0600（group/other 不得有任何位）。" +
			"`chmod 600` 之后重启即可恢复"
	}
	raw, err := os.ReadFile(out.Path)
	if err != nil {
		out.Note = "读不动：" + err.Error()
		return out
	}
	if err := json.Unmarshal(raw, &out.Raw); err != nil {
		out.Note = "内容不是 `引用 → 值` 的 JSON 对象（" + err.Error() + "）：它可重建，" +
			"在面板里重新保存一次密钥就会重写；库里的密钥不受影响"
		out.Raw = map[string]string{}
	}
	return out
}

// reconcileCredentialsMirror 对账"库里的引用"与"镜像里的引用"，两个方向都如实报。
//
// 两个方向都**不自动修**：
//
//	missingInCredentials（库里有、镜像没有）：重跑一次 serve 的装配期会补齐（幂等合并）；
//	missingInStore（镜像有、库里没有）：**不用它**（权威是库），只报出引用名让用户处置——
//	    这一条是"面板保存失败/库被删过/迁移前手写的镜像"的共同外观，医生没有资格替用户选值。
func reconcileCredentialsMirror(m credentialsMirrorFacts, providerIDsWithKey, mcpRefs []string) (missingInCredentials, missingInStore []string) {
	missingInCredentials, missingInStore = []string{}, []string{}
	known := make(map[string]string, len(m.Raw))
	for ref, value := range m.Raw {
		known[ref] = value
	}
	want := make(map[string]bool, len(providerIDsWithKey)+len(mcpRefs))
	for _, id := range providerIDsWithKey {
		want[providerRef(id)] = true
	}
	for _, ref := range mcpRefs {
		want[ref] = true
	}
	for ref := range want {
		if _, ok := known[ref]; !ok {
			missingInCredentials = append(missingInCredentials, ref)
		}
	}
	for ref, value := range known {
		if !want[ref] || strings.TrimSpace(value) == "" {
			// 空值条目也算"镜像里有而库里没有"：它是"清空没写完"的残留，同样不该被采用。
			missingInStore = append(missingInStore, ref)
		}
	}
	sort.Strings(missingInCredentials)
	sort.Strings(missingInStore)
	return missingInCredentials, missingInStore
}

// orphanMcpSecrets 列出 `mcp_secrets` 里没有任何 MCP 定义引用的条目。
//
// 引用集合从**定义正文**里现读（`secret_env` / `secret_headers` 的值）：`mcp_secrets` 表里
// 没有反向列，而定义才是引用的事实源。读不动定义时返回空（**不报孤儿**——宁可漏报，
// 也不要在读不到定义时把全部密钥说成孤儿）。
func orphanMcpSecrets(ctx context.Context, st *store.Store, refs []string) []string {
	out := []string{}
	if len(refs) == 0 {
		return out
	}
	rows, err := st.ListMcpServerDefinitions(ctx)
	if err != nil {
		return out
	}
	used := make(map[string]bool, len(rows))
	for i := range rows {
		var m capability.McpServer
		if json.Unmarshal([]byte(rows[i].Body), &m) != nil {
			continue
		}
		for _, bindings := range []map[string]string{m.SecretEnv, m.SecretHeaders} {
			for _, ref := range bindings {
				used[strings.TrimSpace(ref)] = true
			}
		}
	}
	for _, ref := range refs {
		if !used[ref] {
			out = append(out, ref)
		}
	}
	sort.Strings(out)
	return out
}

// providerMismatchBenignOnly 判断导入对账的条目是不是**全是**"快照滞后"那一类。
//
// 为什么要分档：`meta.providers_import_mismatch` 是**导入那一刻**的结论，而写面（面板/REST）
// 之后新建的档案不会回写 `providers.json`——于是"库里有、文件里没有"在正常使用下必然出现，
// 把它判成失败会让 `mate doctor` 在每次用过写面之后都红（假红比漏报更能让人忽略这条命令）。
// 反过来，"文件里有、库里没有"与"type/key_ref 不一致"是真不一致，必须判失败。
//
// 判据是**写入侧的同一条字符串**（`reconcileProviderRows` 拼的），所以这里用常量而不是
// 各自手抄：两处漂移会让这条分档悄悄失效。
func providerMismatchBenignOnly(entries []string) bool {
	if len(entries) == 0 {
		return false
	}
	for _, e := range entries {
		if !strings.Contains(e, providerMismatchExtraMarker) {
			return false
		}
	}
	return true
}

// collectRuntimeModelFacts 逐 runtime 采集模型清单事实。
//
// 这里**构造一个只读的 Service**（不拨号、不落盘）：物化状态与清单读数必须与
// `GET /models` 走同一条取值面——自己再拼一套 SQL 等于把"看到的状态不是真的"引回来。
// 三处只为读而做的事，逐条写下理由：
//
//	`ApplyIsolationEnv(home)`：落点声明里的 `${...}` 占位符只认该 runtime 自己的 Launch.Env
//	  （fail-closed）。不注入的话 `ResolveConfigProjection` 会报错、
//	  于是所有 runtime 都从报告里**静默消失**——那是最坏的假绿形态。
//	`EnableStore(st)`：把 `meta.runtime_config_digest` 读进内存（否则"已物化"会被报成"从未物化"）。
//	  它只碰内存与注册表，不写盘。
//	**不**走 `LoadProviderDefinitionsFromStore`：那条路会经 `loadProviders` 读凭据面与库，
//	  而体检要的是"清单事实"，不是"装载档案"（装载会在缺镜像时按库重建镜像——那是落笔）。
//	  这里只装载每个 runtime 的清单。
func collectRuntimeModelFacts(ctx context.Context, st *store.Store, home string) []ProviderRuntimeFacts {
	out := []ProviderRuntimeFacts{}
	if st == nil {
		return out
	}
	// 体检刻意用**全部内置**注册表（`AllBuiltinRegistry`）而不是产品装配用的 `ProductRegistry`：
	// doctor 回答的是"**这台机器上**有哪些底座的配置物化痕迹 / 敏感文件"，不是"本二进制只认哪一个"。
	// 构建期把产品钉在 dsh 上，并不代表磁盘上不再有另一份底座的遗留物——那恰恰是体检该如实报出来的
	// 东西（换成 ProductRegistry 会让它们整段消失，表现为"报告一切正常"，而磁盘上的东西一个没少）。
	reg := goruntime.AllBuiltinRegistry()
	reg.ApplyIsolationEnv(strings.TrimSpace(home))
	svc := NewService(reg, nil)
	defer svc.coalescer.Close()
	svc.EnableStore(st)

	states := svc.RuntimeConfigStates(ctx)
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, svc.runtimeModelFacts(ctx, id, states[id]))
	}
	return out
}

// runtimeModelFacts 单个 runtime 的清单 + 物化状态 + 对账（**只读**）。
//
// 单独成方法是为了让"高危"那条判据能被真实夹具（带编排器的 rig + 伪造的运行时目录）
// 端到端跑到——`CollectProviderDoctorFacts` 里的 Service 天然没有编排器，永远走 unknown 分支。
func (s *Service) runtimeModelFacts(ctx context.Context, runtimeID string, state RuntimeConfigState) ProviderRuntimeFacts {
	facts := ProviderRuntimeFacts{
		Runtime: runtimeID,
		// 该底座声明不声明输入模态：取它自己的 Spec 字段（**不是**任何静态能力基线）。
		DeclaresModelInput: declaresModelInput(s.reg, runtimeID),
		Status:             state.Status,
		Where:              state.Where,
		Effect:             state.Effect,
		CredentialChannel:  state.Credential.Channel,
		CredentialTested:   state.Credential.Tested,
		Pending:            state.Pending,
		Note:               strings.Join(state.Problems, "；"),
	}
	if declared, err := s.ListModels(ctx); err != nil {
		facts.DiffNote = "读平台清单失败：" + err.Error()
		return facts
	} else {
		facts.ModelCount = len(declared)
	}

	// 对账只对**构建期固定那个底座**有意义：别的 runtime 没有执行面（读不到广告），如实报
	// "无法校验"，而不是拿选中底座的目录去比对另一张表。
	if runtimeID != goruntime.SelectedRuntime() {
		facts.DiffNote = unknownModelDiffNote("该 runtime 不是本二进制构建期固定的那个，本进程没有它的执行面")
		return facts
	}
	sample, err := s.advertisedSample(ctx)
	switch {
	case err != nil:
		facts.DiffNote = "运行时目录读不到（" + err.Error() + "）：**无法校验**差集（既不是空、也不是不符）"
	case !sample.Known():
		// 没有来源（离线命令没有编排器 / 没有会话）：**不许**当成"底座一条都没有"。
		facts.DiffNote = unknownModelDiffNote(sample.Note)
	default:
		facts.AdvertisedKnown = true
		facts.AdvertisedCount = len(sample.Models)
		// 高危：**写了完全没生效**（声明了 N 条、底座一条都没广告）。
		facts.HighRisk = facts.ModelCount > 0 && facts.AdvertisedCount == 0
		facts.Missing = missingDeclaredModels(ctx, s, sample)
	}
	return facts
}

// declaresModelInput 该 runtime 的 Spec 是否声明"平台总是写模型的输入模态"。
//
// 未注册的 runtime（远程端点没在这个二进制里）→ false：**不许**把"没见过"当成"会写"。
func declaresModelInput(reg *goruntime.Registry, runtimeID string) bool {
	if reg == nil {
		return false
	}
	spec, ok := reg.Get(runtimeID)
	if !ok {
		return false
	}
	return spec.DeclaresModelInput
}

// missingDeclaredModels 声明了却没出现在运行时广告里的候选（机器面，人话化）。
//
// 判据是**同一份生成规则**（`modelValueFor`）：平台声明 → 该 runtime 认得的不透明值，
// 再与广告集合比对。两处各写一遍迟早分叉。
func missingDeclaredModels(ctx context.Context, s *Service, sample advertisedSample) []string {
	declared, err := s.ListModels(ctx)
	if err != nil {
		return nil
	}
	advertised := map[string]bool{}
	for _, m := range sample.Models {
		advertised[m.Value] = true
	}
	var out []string
	for i := range declared {
		v, err := modelValueFor(s.reg, sample.Runtime, declared[i].ProviderID, declared[i].Model)
		if err != nil || v == "" {
			continue
		}
		if !advertised[v] {
			out = append(out, declared[i].ProviderID+"/"+declared[i].Model)
		}
	}
	sort.Strings(out)
	return out
}

// unknownModelDiffNote 差集取不到事实一侧时的说明（**"无法校验"**，不是任何一侧为空）。
//
// 为什么把这句话集中在一处：它是本文件最重要的一条纪律的落点——离线命令没有编排器/会话，
// 运行时广告取不到；这时既不能报"两侧一致"（假绿），也不能报"一条都没生效"（假红）。
func unknownModelDiffNote(reason string) string {
	if strings.TrimSpace(reason) == "" {
		reason = "本次没有取到运行时广告的目录"
	}
	return "**无法校验**：" + reason +
		"（离线体检取不到运行时广告——这既不是「两侧一致」，也不是「一条都没生效」；" +
		"发一条消息或跑 `GET /models?refresh=1` 才有事实一侧）"
}

// collectSensitiveFiles 列出需要与 `mate.db` 同等看待的敏感文件及其权限（v12 的新增面）。
//
// 为什么把 dsh 的凭据文档列进来：v12 起密钥多了一处**明文落点**
// （`<MATE_HOME>/runtime/dsh/.credentials.yaml`，权限 0600）。v20 起平台自己的那份也不再加密
// （`<MATE_HOME>/credentials.json` 与库 `mate.db`），所以备份/同步那个目录就会连明文一起带走——
// 体检必须把它们与 `mate.db` 放在同一档看，让"我以为密钥是加密存的"这句话当场失效。
//
// 哈希已完备的两条纪律这里都体现：① 权限宽于 0600 → **fail-loud**（dsh 对可被属主之外读取的
// 文档是**拒启**，不是告警）；② 平台无法判定权限（Windows）→ 如实记"无法校验"，不假绿也不假红。
func collectSensitiveFiles(home, providerDir string, st *store.Store) []SensitiveFileFacts {
	out := []SensitiveFileFacts{}
	root := strings.TrimSpace(home)
	if root == "" {
		root = filepath.Dir(strings.TrimSpace(providerDir))
	}
	if strings.TrimSpace(root) == "" || root == "." {
		return out
	}
	// 逐个已注册 runtime 的**原生凭据文档**（声明在 `CredentialSpec.NativeDoc` 里，
	// 占位符用该 runtime 自己的隔离 env 展开——与物化时同一条解析链，绝不落到用户自己的目录）。
	// 同 `collectRuntimeModelFacts`：体检看全部内置底座，不按构建期选择裁剪（那是产品装配的事）。
	reg := goruntime.AllBuiltinRegistry()
	for _, spec := range reg.List() {
		if strings.TrimSpace(spec.Credential.NativeDoc) == "" {
			continue
		}
		path := resolveDoctorDocPath(spec, root)
		if path == "" {
			continue
		}
		out = append(out, statSensitiveFile(path))
	}
	// 平台自己那份托管凭据文档（v20：明文、0600，权限不合平台会拒绝读它）。
	out = append(out, statSensitiveFile(filepath.Join(root, CredentialsFileName)))
	// 库本体与它的迁移备份（v20 起库内含明文密钥，于是它们也是**密钥面**）。
	//
	// 这一条在 2026-10-14 收尾时从"刻意不列"改成"必须列"：原先的理由是"sqlite 文件的常规权限是
	// 0644，按 0600 判会给所有正常安装报假红"——那个前提随 `pkg/dbclient` 在**打开时收口到 0600**
	// 而失效（`tightenFilePerms`，含首次建库）。现在"权限宽"是一个**真事实**：密钥此刻确实可被属主
	// 之外的人读走，所以按 fail 报是诚实的，修法也明确（`chmod 600`，或让任何一条命令打开一次库，
	// 平台会自己收口并打 WARN）。
	if st != nil {
		if dbPath := st.Path(); dbPath != "" {
			out = append(out, statSensitiveDBFile(dbPath))
			if dir := st.BackupDir(); dir != "" {
				// 备份按 `*.bak-*` 展开：只报**已存在**的条目（没有备份不是问题）。
				matches, _ := filepath.Glob(filepath.Join(dir, "*.bak-*"))
				sort.Strings(matches)
				for _, m := range matches {
					out = append(out, statSensitiveDBFile(m))
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// resolveDoctorDocPath 展开某 runtime 的凭据文档路径（占位符只认该 runtime 自己的隔离 env）。
func resolveDoctorDocPath(spec goruntime.Spec, root string) string {
	launch := map[string]string{}
	for _, kv := range spec.Launch.Env {
		if name, val, ok := strings.Cut(kv, "="); ok {
			launch[strings.TrimSpace(name)] = val
		}
	}
	// doctor 是只读命令、不应用隔离表：占位符里只有 `${MATE_HOME}` 这类根可以从入参推出来，
	// 其余（`$DSH_HOME` 之类）在启动器里可能没值 → 这时**跳过**而不是猜一个路径。
	lookup := func(name string) (string, bool) {
		if v, ok := launch[name]; ok && strings.TrimSpace(v) != "" {
			return v, true
		}
		if name == "MATE_HOME" && root != "" {
			return root, true
		}
		return "", false
	}
	p, err := goruntime.ExpandProjectionRoot(spec.Credential.NativeDoc, lookup)
	if err != nil || strings.TrimSpace(p) == "" {
		return ""
	}
	return p
}

// statSensitiveFile 采集一个敏感文件的存在性与权限（八进制串）。
// sensitiveNoteNativeDoc 原生凭据文档（底座那一侧）的宽权限备注。
const sensitiveNoteNativeDoc = "权限宽于 0600：这份文档里有**明文密钥**，且底座对「可被属主之外读取」的文档是**拒启**；" +
	"立刻 `chmod 600` 它"

// sensitiveNoteDB 库与它的迁移备份的宽权限备注（v20 起库内含明文密钥）。
const sensitiveNoteDB = "权限宽于 0600：这份文件里有**明文密钥**（v20 起密钥进库，库与备份是同一批数据）；" +
	"立刻 `chmod 600` 它，或让任何一条命令（如 `mate serve`）打开一次库——平台会在打开时收口到 0600 并打一条 WARN"

// statSensitiveFile 原生凭据文档/托管文档的体检事实。
func statSensitiveFile(path string) SensitiveFileFacts {
	return statSensitiveFileWith(path, sensitiveNoteNativeDoc)
}

// statSensitiveDBFile 库文件或迁移备份的体检事实（同一套判据，备注不同：底座拒启那条不适用于我们自己的库）。
func statSensitiveDBFile(path string) SensitiveFileFacts {
	return statSensitiveFileWith(path, sensitiveNoteDB)
}

func statSensitiveFileWith(path, wideNote string) SensitiveFileFacts {
	f := SensitiveFileFacts{Path: path, Expected: "0600"}
	info, err := os.Stat(path)
	if err != nil {
		f.OK = true // 不存在 = 没有落点 = 没有风险
		return f
	}
	f.Exists = true
	if runtime.GOOS == "windows" {
		// Windows 没有可检查的 mode（dsh 官方口径也是"跳过而不是伪造"）。
		f.PermChecked = false
		f.OK = true
		f.Note = "平台无法判定权限（Windows 没有可检查的 mode）：**无法校验**，不当作合规也不当作失败"
		return f
	}
	f.PermChecked = true
	f.Perm = fmt.Sprintf("%04o", info.Mode().Perm())
	f.OK = info.Mode().Perm()&0o077 == 0
	if !f.OK {
		f.Note = wideNote
	}
	return f
}
