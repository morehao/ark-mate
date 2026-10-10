package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— K2：`mate doctor` 的 provider（供应商凭据）与模型清单体检 ———
//
// 分工：本文件是**事实的构造函数**（只读），`internal/app/command/doctor.go` 只负责排版。
// 该包是薄打印层——SQL、vault 与对账算法都不该出现在那里。
//
// 三条纪律（与 doctor 的既有口径逐条对齐）：
//
//	① **只读**：库由 `store.OpenReadOnly` 以 `mode=ro` 打开（不迁移）；vault 只探测，
//	   **绝不创建 keyfile**（`vault.Open` 会在缺 keyfile 时创建主密钥——那是一个不可逆的落笔，
//	   体检命令没有这个权限）；不写 meta、不删任何密文。
//	② **只报不判**：本文件产出事实与三档结论，"哪一档算失败"由 doctor 的既有口径决定——
//	   "无法校验"记警告，**事实与声明不符**才判失败。
//	③ **取不到就记 unknown**：离线命令没有编排器、也没有会话，运行时广告的目录取不到。
//	   这时必须记"无法校验"，**不许**把"没取到"当成"底座一条都没有"（那是假红），
//	   也不许当成"两侧一致"（那是假绿）。判据是取样来源（`source=none` 就是没来源）。
//
// 载体口径：provider 档案的**事实源**按持久层模式分叉（v11）——sqlite 模式在 `providers` 表
// （本文件的可读面），memory 模式在 `<AgentDir>/providers.json`。memory 模式下本文件**不代读**
// 那个文件（那是另一个载体的语义，代读会让"谁是权威"再次变浑）：DB 侧的事实全部记 unknown，
// 只保留与载体无关的那一条——vault 可达性。

// ProviderDoctorOptions 一次只读体检的输入。
type ProviderDoctorOptions struct {
	// Home mate home（`vault.key` / `vault/` 的兜底锚点，也是 runtime 隔离 env 的根）。
	Home string
	// AgentDir agent 定义目录。**vault 锚点的权威取法是它的父目录**（与 `providerVault` 同源：
	// 档案目录在 `<home>/agents`，密钥在 `<home>/vault.key`）；为空时回落到 Home。
	AgentDir string
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
	// ProvidersFile `<AgentDir>/providers.json` 是否存在（v11 起它是导入/导出载体）。
	ProvidersFile bool `json:"providersFile"`
	// ProvidersBackup `<AgentDir>/providers.json.bak-v11` 是否存在（迁移前的快照，回退路径）。
	ProvidersBackup bool `json:"providersBackup"`

	// VaultHome vault 锚点（密文目录与 keyfile 都在它下面）。
	VaultHome string `json:"vaultHome"`
	// VaultUsable 主密钥是否可用（`MATE_VAULT_KEY` 或 keyfile）。
	VaultUsable bool `json:"vaultUsable"`
	// VaultSource 主密钥来源：env（`MATE_VAULT_KEY`）/ keyfile / none（尚未创建）。
	VaultSource string `json:"vaultSource"`
	// VaultNote 人话原因（不可用时给出出路）。
	VaultNote string `json:"vaultNote,omitempty"`
	// CiphertextRefs 引用密文的档案 id（`body.key_ref` 非空者；升序）。
	CiphertextRefs []string `json:"ciphertextRefs"`
	// CiphertextFiles `<vault>/providers/` 下实际存在的密文（去 `.enc` 的 id；升序）。
	CiphertextFiles []string `json:"ciphertextFiles"`
	// VaultDirScan 密文目录的扫描结论：ok / missing（还没有密文目录）/ unreadable: <原因>。
	// 它是"上面两个列表可不可信"的判据：目录读不动时**不许**把"扫不到"当成"没有密文"。
	VaultDirScan string `json:"vaultDirScan"`
	// MissingCiphertexts 声明了 `key_ref` 却没有对应密文文件的档案（那条 provider 注不出密钥）。
	MissingCiphertexts []string `json:"missingCiphertexts"`
	// NoKeyProviders 既没有 `key_ref` 也没有明文 `key` 的档案（尚未设置密钥）。
	// 它不是故障（档案可以先建后补密钥），但"开会话时才发现没密钥"值得提前说。
	NoKeyProviders []string `json:"noKeyProviders"`

	// DanglingRefs 悬空引用（agent 定义指向不存在的档案）。
	DanglingRefs []ProviderDanglingRef `json:"danglingRefs"`
	// ImportMismatch `meta.providers_import_mismatch` 的逐条内容（导入对账）。
	ImportMismatch []string `json:"importMismatch"`
	// ImportMismatchBenignOnly true = 不一致条目全是"库里有、文件里没有"这一类
	// （写面新建后文件还没导出，属正常快照滞后，不是故障）。
	ImportMismatchBenignOnly bool `json:"importMismatchBenignOnly"`
	// OrphanCiphertexts 孤儿密文：`<vault>/providers/*.enc` 里没有对应档案行的那些（升序）。
	OrphanCiphertexts []string `json:"orphanCiphertexts"`

	// Runtimes 逐 runtime 的模型配置物化状态与对账（runtime 升序）。
	Runtimes []ProviderRuntimeFacts `json:"runtimes"`
}

// ProviderDanglingRef 一条悬空引用（agent → 不存在的 provider 档案 id）。
type ProviderDanglingRef struct {
	AgentID    string `json:"agentId"`
	ProviderID string `json:"providerId"`
}

// ProviderRuntimeFacts 某 runtime 的模型清单事实（状态 + 对账）。
type ProviderRuntimeFacts struct {
	Runtime string `json:"runtime"`
	// Declared 该 runtime 是否声明了模型配置落点（false = T5 不可达，平台清单对它没有兑现通道）。
	Declared bool `json:"declared"`
	// Tested 落点是否经真实底座实测（false = **只读**：生成候选但不落盘）。
	Tested bool `json:"tested"`
	// Writable 现在能不能真的写（Declared && Tested）。
	Writable bool `json:"writable"`
	// Channel 交付通道（config_dir_read | cli_patch_arg | env_content）。
	Channel string `json:"channel,omitempty"`
	// Path 配置文件绝对路径（未声明时为空）。
	Path string `json:"path,omitempty"`
	// Digest 已落盘（或候选）内容的摘要；空 = 从未物化。
	Digest string `json:"digest,omitempty"`
	// GeneratedAt 最近一次物化的时刻（RFC3339Nano）。
	GeneratedAt string `json:"generatedAt,omitempty"`
	// Pending 恒 true 直到有新进程真正读到它（**不许说成"已生效"**）。
	Pending bool `json:"pending"`
	// ModelCount 平台声明的清单条数（该 runtime）。
	ModelCount int `json:"modelCount"`
	// Note 人话说明（未实测只读、未声明落点…）。
	Note string `json:"note,omitempty"`

	// AdvertisedKnown 本次是否真的取到了运行时广告的目录（离线命令 = false）。
	AdvertisedKnown bool `json:"advertisedKnown"`
	// AdvertisedCount 运行时广告的模型候选数（AdvertisedKnown=false 时无意义）。
	AdvertisedCount int `json:"advertisedCount"`
	// DeclaredNotAdvertised 平台声明了、底座没广告（"写了没生效"）的条数。
	DeclaredNotAdvertised int `json:"declaredNotAdvertised"`
	// AdvertisedNotDeclared 底座有、平台没声明（opencode 上属常态）的条数。
	AdvertisedNotDeclared int `json:"advertisedNotDeclared"`
	// WorstSeverity declared_not_advertised 里最重的一档（error | degrade | info | ""）。
	WorstSeverity string `json:"worstSeverity,omitempty"`
	// DiffNote 差集不可得的原因（AdvertisedKnown=false 时必填）。
	DiffNote string `json:"diffNote,omitempty"`
	// HighRisk **写了完全没生效**的形态：declared 非空而 advertised 一条都没有。
	HighRisk bool `json:"highRisk"`
}

// CollectProviderDoctorFacts 采集 provider / 模型清单的只读事实。
//
// `st` 可以为 nil（memory 模式 / 库读不出来）：那时 DB 派生的事实全部记 unknown，
// 只保留载体无关的 vault 探测。**本函数不写任何东西**（唯一的落笔是 SQLite 只读连接可能
// 重建的 `-shm`/`-wal` 两个共享内存面文件，见 `store.OpenReadOnly` 的注释）。
func CollectProviderDoctorFacts(ctx context.Context, st *store.Store, opts ProviderDoctorOptions) ProviderDoctorFacts {
	agentDir := strings.TrimSpace(opts.AgentDir)
	facts := ProviderDoctorFacts{
		Carrier:            "unknown",
		CarrierNote:        "没有可读的持久层：provider 档案的行数、悬空引用与对账都无从校验",
		DBPath:             strings.TrimSpace(opts.DBPath),
		ProviderIDs:        []string{},
		CiphertextRefs:     []string{},
		CiphertextFiles:    []string{},
		MissingCiphertexts: []string{},
		NoKeyProviders:     []string{},
		DanglingRefs:       []ProviderDanglingRef{},
		ImportMismatch:     []string{},
		OrphanCiphertexts:  []string{},
		Runtimes:           []ProviderRuntimeFacts{},
	}
	facts.VaultHome = providerVaultHome(opts.Home, agentDir)

	// 文件侧（与载体无关，两种模式都要看）：providers.json 与它的迁移前备份是否存在。
	if agentDir != "" {
		facts.ProvidersFile = providerFileExists(agentDir)
		facts.ProvidersBackup = fileExists(filepath.Join(agentDir, providersBackupName))
	}

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
				var body map[string]any
				if err := json.Unmarshal([]byte(rows[i].Body), &body); err != nil {
					// 正文非法：既不声称它有密钥，也不声称它没有——只在 id 面上出现。
					continue
				}
				switch {
				case bodyString(body, "key_ref") != "":
					facts.CiphertextRefs = append(facts.CiphertextRefs, rows[i].ID)
				case bodyString(body, "key") != "":
					// vault 不可用时的降级路径：正文里还留着明文（写面会报 problem，这里不重复判）。
				default:
					facts.NoKeyProviders = append(facts.NoKeyProviders, rows[i].ID)
				}
			}
			sort.Strings(facts.ProviderIDs)
			sort.Strings(facts.CiphertextRefs)
			sort.Strings(facts.NoKeyProviders)
			facts.ProviderRows = len(facts.ProviderIDs)
		}
		if models, err := st.ListModelDefinitions(ctx, ""); err == nil {
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
		facts.DanglingRefs = danglingProviderRefs(ctx, st, facts.ProviderIDs)
	}

	// ——— vault（载体无关） ———
	facts.VaultUsable, facts.VaultSource, facts.VaultNote = probeVault(facts.VaultHome)
	facts.CiphertextFiles, facts.VaultDirScan = scanProviderCiphertexts(facts.VaultHome)
	facts.OrphanCiphertexts = orphanProviderCiphertexts(facts.CiphertextFiles, facts.ProviderIDs)
	for _, id := range facts.CiphertextRefs {
		if !containsString(facts.CiphertextFiles, id) {
			facts.MissingCiphertexts = append(facts.MissingCiphertexts, id)
		}
	}
	sort.Strings(facts.MissingCiphertexts)

	// ——— 模型清单：逐 runtime 的物化状态 + 对账 ———
	facts.Runtimes = collectRuntimeModelFacts(ctx, st, opts.Home)
	return facts
}

// providerVaultHome vault 锚点：**AgentDir 的父目录**优先（与 `providerVault` 逐字同源），
// AgentDir 为空时回落到 mate home。
func providerVaultHome(home, agentDir string) string {
	if agentDir != "" {
		return filepath.Dir(filepath.Clean(agentDir))
	}
	return strings.TrimSpace(home)
}

// probeVault 只读探测 vault 可达性：**绝不创建 keyfile**。
//
// 三种来源分开报（它们的处置完全不同）：
//
//	env     `MATE_VAULT_KEY` 覆盖：解析得了就行，不读也不建 keyfile；
//	keyfile `<home>/vault.key` 存在：走 `vault.Open` 的既有硬化校验（拒符号链接、拒过宽权限）；
//	none    两种都没有：密文还写不出来，但**这本身不是坏**——vault.key 是"第一次写入密文"时才创建的。
//
// 为什么 none 不能直接当失败：判定要结合"有没有密文"。一台还没有任何 key_ref 的机器上
// 报 fail 是假红；而有 key_ref 却读不到主密钥是**密文永久不可恢复**（真 fail）——
// 这一步只回答"主密钥可不可用"，两者的合并留给调用方（`mate doctor` 的 `vault` 段）。
func probeVault(home string) (usable bool, source, note string) {
	if strings.TrimSpace(os.Getenv(vault.EnvKey)) != "" {
		if _, err := vault.ParseKey(os.Getenv(vault.EnvKey)); err != nil {
			return false, "env", vault.EnvKey + " 非法（" + err.Error() + "）：改成 32 字节的 base64 或 hex，或清掉它改用 <mate home>/vault.key"
		}
		return true, "env", vault.EnvKey + " 可用（不读也不建 keyfile）"
	}
	if strings.TrimSpace(home) == "" {
		return false, "none", "mate home 解析不出来（既没有 vault.key，也不知道该去哪儿找）"
	}
	keyPath := filepath.Join(home, vault.KeyFileName)
	if _, err := os.Lstat(keyPath); err != nil {
		if os.IsNotExist(err) {
			return false, "none", keyPath + " 尚未创建（平台在**第一次写入密文**时才创建主密钥；" +
				"如果这台机器上已有 key_ref 引用，说明密钥文件被删了——密文不可恢复，只能重设各 provider 的 key）"
		}
		return false, "none", keyPath + " 不可读：" + err.Error()
	}
	st, err := vault.Open(home)
	if err != nil {
		return false, "keyfile", keyPath + " 不可用：" + err.Error() +
			"（vault 的既有硬化校验：拒符号链接、拒 group/other 权限位；修好前带密钥的档案写面与密文解密都会失败）"
	}
	return true, "keyfile", keyPath + " 可用（引用 " + st.Dir() + "）"
}

// scanProviderCiphertexts 列出 `<vault>/providers/*.enc` 里实际存在的密文 id。
//
// 为什么不复用 `vault.Store`：装载 vault 会在缺 keyfile 时**创建主密钥**（写入），而这里只是
// "那个目录里有哪些文件"——与主密钥可不可用是两件事（密文在而主密钥丢了，正是最需要报出来的形态）。
//
// 第二项返回**扫描结论**：`missing`（还没有密文目录）与 `unreadable: …` 必须分开——
// 前者是真的没有密文，后者是"扫不到"，把后者当成前者会把读权限问题报成"引用悬空"（假红）。
func scanProviderCiphertexts(home string) (files []string, scan string) {
	files = []string{}
	if strings.TrimSpace(home) == "" {
		return files, "unknown：mate home 解析不出来"
	}
	entries, err := os.ReadDir(filepath.Join(home, vault.DirName, "providers"))
	if err != nil {
		if os.IsNotExist(err) {
			return files, "missing"
		}
		return files, "unreadable: " + err.Error()
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".enc") {
			continue
		}
		files = append(files, strings.TrimSuffix(e.Name(), ".enc"))
	}
	sort.Strings(files)
	return files, "ok"
}

// orphanProviderCiphertexts 列出密文文件里**没有对应档案行**的那些。
//
// 这一类只可能是**删除补偿失败的残留**（删除顺序是"先摘内存 → 再删行 → 最后删密文"，
// 最后一步失败会留下它）。如实列出、**不自动删**：密文是不是还有别的用途（人工核对、
// 恢复某条误删的档案）只有用户知道，体检命令没有删除权。
func orphanProviderCiphertexts(files, knownIDs []string) []string {
	out := []string{}
	known := make(map[string]bool, len(knownIDs))
	for _, id := range knownIDs {
		known[id] = true
	}
	for _, id := range files {
		if !known[id] {
			out = append(out, id)
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

// danglingProviderRefs 找悬空引用：agent 定义里的 `provider` 指向不存在的档案。
//
// 它与 `CredentialProblem` 是同一条事实的两个时点：那边在**拨号前**拦下来（会话报
// credential_missing），这边在**体检时**提前把它摆出来（人还没建会话就能看见）。
// 判据取自 DB 的 agent 行（sqlite 模式下它才是定义的权威）——种子 agent（dsh/opencode）
// 不落行且 `provider` 恒为空，所以这里不需要再并一份种子表。
func danglingProviderRefs(ctx context.Context, st *store.Store, knownIDs []string) []ProviderDanglingRef {
	out := []ProviderDanglingRef{}
	if st == nil {
		return out
	}
	rows, err := st.ListAgentDefinitions(ctx)
	if err != nil {
		return out
	}
	known := make(map[string]bool, len(knownIDs))
	for _, id := range knownIDs {
		known[id] = true
	}
	for i := range rows {
		var body map[string]any
		if err := json.Unmarshal([]byte(rows[i].Body), &body); err != nil {
			continue
		}
		pid := bodyString(body, "provider")
		if pid == "" || known[pid] {
			continue
		}
		out = append(out, ProviderDanglingRef{AgentID: rows[i].ID, ProviderID: pid})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].ProviderID < out[j].ProviderID
	})
	return out
}

// collectRuntimeModelFacts 逐 runtime 采集模型清单事实。
//
// 这里**构造一个只读的 Service**（不拨号、不落盘）：物化状态与清单读数必须与
// `GET /models` 走同一条取值面——自己再拼一套 SQL 等于把"看到的状态不是真的"引回来。
// 三处只为读而做的事，逐条写下理由：
//
//	`ApplyIsolationEnv(home)`：落点声明里的 `${OPENCODE_CONFIG_DIR}` / `${DSH_HOME}` 只认
//	  该 runtime 自己的 Launch.Env（fail-closed）。不注入的话 `ResolveConfigProjection` 会报错、
//	  于是所有 runtime 都从报告里**静默消失**——那是最坏的假绿形态。
//	`EnableStore(st)`：把 `meta.runtime_config_digest` 读进内存（否则"已物化"会被报成"从未物化"）。
//	  它只碰内存与注册表，不写盘。
//	**不**走 `LoadAgentDefinitionsFromStore`：那条路会经 `loadProviders → providerVault` **创建
//	  vault.key**（写入！），而体检没有落笔权。对账借读的 agent 由 `agentForRuntime` 从 DB 行里取，
//	  这里只装载每个 runtime 的清单。
func collectRuntimeModelFacts(ctx context.Context, st *store.Store, home string) []ProviderRuntimeFacts {
	out := []ProviderRuntimeFacts{}
	if st == nil {
		return out
	}
	reg := runtime.DefaultRegistry()
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
		Runtime:  runtimeID,
		Declared: state.Declared,
		Tested:   state.Tested,
		Writable: state.Writable,
		Channel:  state.Channel,
		Path:     state.Path,
		Digest:   state.Digest,
		Pending:  state.Pending,
		Note:     state.Note,
	}
	facts.GeneratedAt = state.GeneratedAt
	if declared, err := s.ListModels(ctx, runtimeID); err != nil {
		facts.DiffNote = "读平台清单失败：" + err.Error()
		return facts
	} else {
		facts.ModelCount = len(declared)
	}

	agentID := s.agentForRuntime(runtimeID)
	sample, err := s.advertisedSample(ctx, agentID)
	switch {
	case err != nil:
		facts.DiffNote = "运行时目录读不到（" + err.Error() + "）：**无法校验**差集（既不是空、也不是不符）"
	case !sample.Known():
		// 没有来源（离线命令没有编排器 / 没有会话）：**不许**当成"底座一条都没有"。
		facts.DiffNote = unknownModelDiffNote(sample.Note)
	default:
		facts.AdvertisedKnown = true
		facts.AdvertisedCount = len(sample.Models)
		// 高危：**写了完全没生效**（声明了 N 条、底座一条都没广告）。它单独报，不与差集混在一起。
		facts.HighRisk = facts.ModelCount > 0 && facts.AdvertisedCount == 0
		if diff, derr := s.reconcileModels(ctx, runtimeID, agentID); derr == nil {
			facts.DeclaredNotAdvertised = len(diff.DeclaredNotAdvertised)
			facts.AdvertisedNotDeclared = len(diff.AdvertisedNotDeclared)
			for _, e := range diff.DeclaredNotAdvertised {
				if providerSeverityRank(e.Severity) > providerSeverityRank(facts.WorstSeverity) {
					facts.WorstSeverity = e.Severity
				}
			}
			facts.DiffNote = ""
		} else {
			facts.AdvertisedKnown = false
			facts.DiffNote = "对账失败：" + derr.Error()
		}
	}
	return facts
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

// providerSeverityRank 严重度排序（error > degrade > info > 空），取"最重的一条"给结论用。
func providerSeverityRank(severity string) int {
	switch severity {
	case severityError:
		return 3
	case severityDegrade:
		return 2
	case severityInfo:
		return 1
	}
	return 0
}
