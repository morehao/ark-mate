// 本文件是 opencode 的**模型清单交付实现**（`resource.Syncer`）：生成配置、自校验、落盘 + 内联注入。
//
// 本包是纯数据 + 纯函数子包：不 import `internal/**`（archtest 的 TestAdapterHasNoStoreLiterals 守），
// 也不 import 父包 `pkg/runtime`（父子 import 环，约束⑦）。改启动面的两件事（写文件 / 注入内联
// 内容）只能经端口传进来的 `resource.Delivery` 完成。
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/morehao/ark-mate/pkg/resource"
)

// EnvConfigContent opencode 的**内联配置**环境变量名。
//
// 为什么它是 opencode adapter 的常量而不是内核的字符串（T1：`internal/**` 零命中）：
// 「配置可以从哪个环境变量内联进来」是 opencode 的私有知识。同时导出它，是因为平台侧还有一处
// 需要拼这个名字（会话的权限 argv 面），那处应当 import 本常量而不是各写一份字面量。
//
// 机制与官方文档的关系（**据实登记**）：2.0.26 的二进制里确有 `process.env.OPENCODE_CONFIG_CONTENT`
// （它进配置来源对象 `{directory, project, file, content}`，优先级高于平台写的任何文件），但**官方
// 文档没有记载它**——官方推荐的接线是全局配置文件。旁证这处不对称是真实的：同一份随包 AI 文档为
// `cli.json` 记了 `OPENCODE_CLI_CONFIG_CONTENT`，却没为 `opencode.json` 记对应变量。
// 因此它是"机制存在、官方未记载"的通道：保留它是因为它压过项目级发现（用户仓库里的文件顶不掉平台
// 清单），且隔离表里有一条空的同名变量（不注入就会被用户 shell 的值顶掉）。
const EnvConfigContent = "OPENCODE_CONFIG_CONTENT"

// ProviderPackage opencode 侧加载 OpenAI 兼容 route 用的**官方原生**包路径。
//
// 为什么不沿用 v11 的裸 npm 名 `@ai-sdk/openai-compatible`（ADR-11 点名的第 ② 处偏差）：
// 官方 V2 的原生写法是内置包路径（Providers 指南 Custom 示例）；裸 npm 名只是被 2.0.26 二进制里
// 的别名表接住（`"@ai-sdk/openai-compatible" → "@opencode/ai/providers/openai-compatible"`），
// 把"今天恰好被兼容"当成契约，底座清理兼容层即静默失效。协议本期只有 openai 一种，
// 所以这个字符串由 adapter 内置，不再让用户为「协议」付一个自由串的代价（ADR-3）。
const ProviderPackage = "@opencode/ai/providers/openai-compatible"

// ConfigSchema 顶层 `$schema`（编辑器集成用；**不要**拿它去推断 V2 字段名——
// opencode 官方逐字警告它可能描述 V1）。
const ConfigSchema = "https://opencode.ai/config.json"

// generatorVersion 生成器代数（模板契约的版本）。
//
// 它与 v11 保持同一个值，这是**刻意的**：`_arkMate` 归属标记属于等价性 golden 的 A 部分
// （未点名处必须逐字节相同），而"模板改了要不要 +1"这个判断会把它变成 A 部分差异。
// 该值只影响归属标记本身（代际键由编排层按内容摘要驱动），所以保持不动是安全的。
const generatorVersion = 1

// filePerm 平台写下的配置文件权限（清单不是密钥，但落点在平台独占根里，从紧无代价）。
const filePerm = os.FileMode(0o600)

// Options 构造同步实现所需的**已解析落点**与开关。
//
// 路径由父包解析后传入（见 `pkg/runtime.ResolveConfigLocation`）：adapter 拿不到注册表，
// 也不该自己拼 `HOME`（那正是"平台写到用户真实目录"的入口，见 AGENTS.md 的隔离纪律）。
type Options struct {
	// ConfigPath 配置文件绝对路径（opencode.json 的落点）。空 = 只走内联注入这一条通道。
	ConfigPath string
}

// NewSyncer 构造 opencode 的清单同步实现（装配形态见 `pkg/runtime/registerBuiltinSyncers`）。
func NewSyncer(opts Options, delivery resource.Delivery) resource.Syncer {
	return &syncer{configPath: strings.TrimSpace(opts.ConfigPath), delivery: delivery}
}

type syncer struct {
	configPath string
	delivery   resource.Delivery
	// 注入面的**进程内影子**。`resource.Delivery` 只有两个写方法（没有读回），所以"这次到底改没改"
	// 只能自己记：契约②要求幂等（同一份 Set 重复调用收敛、不换代际），而"首发注入"确实是一次改动
	// （新进程才会读到），"重复注入同一个值"不是。零值 `lastEnv == ""` 恰好等于"当前注入面为空"
	// ——启动期注册表里那条空的隔离变量正是这个形态，所以首次空集撤回会正确地报 unchanged。
	mu      sync.Mutex
	lastEnv string
}

// trackInjection 记录本次注入的值并返回"与上次相比是否变了"。
func (s *syncer) trackInjection(v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.lastEnv != v
	s.lastEnv = v
	return changed
}

// SyncModels 生成 opencode 配置 → 自校验 → 落盘 + 内联注入；清单为空时**撤回**。
//
// 五条纪律（与 v11 的 `runtime_config.go` 同一套，只是搬进了 adapter）：
//
//	① 生成是**纯函数**（不读盘、不写盘、不看时钟）→ 结果可被 golden 逐字节钉住；
//	② 自校验不省（用同一族的解析器把生成的字节读回来）——模板/转义 bug 只有解析器抓得到；
//	③ 自校验失败 → **一个字节都不写**（fail-loud，不半写）；
//	④ 落盘走「同目录 temp + 原子 rename」；
//	⑤ **空集必撤回**（契约③）：清内联注入 + 删文件——什么都不做会让上一份配置继续生效，
//	   那是 v11 的真实缺陷（用户删光了模型、界面上空了，底座仍按旧清单工作）。
func (s *syncer) SyncModels(_ context.Context, set resource.Set) (resource.Outcome, error) {
	if len(set.Models) == 0 {
		return s.revoke()
	}
	data, err := buildConfig(set)
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("生成 opencode 配置失败：%w", err)
	}
	if err := selfCheck(data); err != nil {
		return resource.Outcome{}, fmt.Errorf("opencode 配置自校验失败（未写任何字节）：%w", err)
	}

	fileChanged := false
	if s.configPath != "" {
		prev, rerr := os.ReadFile(s.configPath)
		switch {
		case rerr == nil:
			fileChanged = !bytesEqual(prev, data)
		case os.IsNotExist(rerr):
			fileChanged = true
		default:
			return resource.Outcome{}, fmt.Errorf("读上一份 opencode 配置失败（%s）：%w", s.configPath, rerr)
		}
		if fileChanged {
			if werr := writeFileAtomic(s.configPath, data, filePerm); werr != nil {
				return resource.Outcome{}, fmt.Errorf("写 opencode 配置失败（%s）：%w", s.configPath, werr)
			}
		}
	}
	// 内联注入**每次同步都要设**：注入住在注册表的内存里，重启后必须靠一次同步重建；
	// 同键替换（不是追加）→ 重复设同一个值是收敛的，不产生新字节也不换代际。
	changed := s.trackInjection(string(data)) || fileChanged
	if err := s.setEnv(EnvConfigContent, string(data)); err != nil {
		return resource.Outcome{}, fmt.Errorf("注入 opencode 内联配置失败（%s）：%w", EnvConfigContent, err)
	}

	status := resource.StatusApplied
	note := "已写入并交给底座；**新进程启动时读它**——此刻底座的目录仍是旧的那份"
	if !changed {
		status = resource.StatusUnchanged
		note = "内容与上一份相同（幂等重放）：没有重写任何字节"
	}
	return resource.Outcome{
		Status: status,
		Where:  s.where(),
		Effect: resource.EffectNextProcess,
		// 凭据走 opencode 官方的 `providers.<route>.env` 引用 + 平台子进程 env 注入（ADR-4/Q1'：
		// 这是它的**终局形态**，不是待换成原生凭据文档的过渡）。env 通道是官方字段、
		// 且平台的同键替换已实测（H6 ② 的反证面），故如实报 tested。
		Credential: resource.CredentialOutcome{
			Channel: resource.ChannelEnv,
			Status:  status,
			Where:   "子进程环境变量（逐供应商引用名，见配置里的 env 字段）",
			Effect:  resource.EffectNextProcess,
			Tested:  true,
		},
		Problems: []string{note},
	}, nil
}

// revoke 契约③「空集必撤回」在 opencode 上的形态。
//
// 为什么必须**同时**撤注入与删文件（H4 的 Plan B）：两个底座上空清单的语义都极坏——
// opencode 的目录空会让 `loadCatalog` 重试 5s 后抛 `No models are available`（起不了会话）。
// 所以正确形态是"回到没有平台清单"，而**绝不能留下空的注入内容**：那条 `OPENCODE_CONFIG_CONTENT=`
// 是一个"显式给了空配置"的声明，比不注入更坏。
//
// 幂等判据 = 文件是否原本存在 或 本次是否真的撤掉了注入（注入面没有读回，只能靠进程内影子）。
func (s *syncer) revoke() (resource.Outcome, error) {
	existed := false
	if s.configPath != "" {
		if _, err := os.Stat(s.configPath); err == nil {
			existed = true
		} else if !os.IsNotExist(err) {
			return resource.Outcome{}, fmt.Errorf("检查 opencode 配置失败（%s）：%w", s.configPath, err)
		}
	}
	injChanged := s.trackInjection("")
	if err := s.setEnv(EnvConfigContent, ""); err != nil {
		return resource.Outcome{}, fmt.Errorf("撤回 opencode 内联配置失败（%s）：%w", EnvConfigContent, err)
	}
	if existed {
		if err := os.Remove(s.configPath); err != nil && !os.IsNotExist(err) {
			return resource.Outcome{}, fmt.Errorf("删除 opencode 配置失败（%s）：%w", s.configPath, err)
		}
	}
	status := resource.StatusUnchanged
	if existed || injChanged {
		status = resource.StatusApplied
	}
	return resource.Outcome{
		Status: status,
		Where:  s.where(),
		Effect: resource.EffectNextProcess,
		Credential: resource.CredentialOutcome{
			Channel: resource.ChannelNone,
			Status:  status,
			Where:   "无（清单为空，未引用任何供应商凭据）",
			Effect:  resource.EffectNextProcess,
			Tested:  true,
		},
		Problems: []string{"平台清单为空：已撤回内联注入并删除配置文件（空配置会让 opencode 起不了会话）"},
	}, nil
}

// where 人话落点（给用户看的"同步到哪了"）。
func (s *syncer) where() string {
	if s.configPath == "" {
		return "子进程内联配置（" + EnvConfigContent + "）"
	}
	return s.configPath + "（平台独占该文件）＋ 同一份内容经 " + EnvConfigContent + " 内联给新进程"
}

// setEnv 走端口改启动面；delivery 为 nil（单测只验生成）时退化成无操作。
func (s *syncer) setEnv(key, value string) error {
	if s.delivery == nil {
		return nil
	}
	return s.delivery.SetEnv(key, value)
}

// ——— 生成（纯函数） ———

// configDoc opencode `opencode.json` 的结构（生成侧的唯一形态）。
//
// 语义要点（v2，逐条对过随包 2.0.26 载荷）：
//   - 顶层键是 **`providers`（复数）**：v2 把 v1 的 `provider` 改了名；
//   - `providers.<route>.models` 是**追加（upsert）**语义：声明的条目进目录、命中同 id 的内置条目
//     则以它为基底覆盖。所以对账时 `advertised_not_declared` 默认非空（内置目录里的模型会一直在），
//     这不是故障；
//   - 顶层 `model` 是默认模型（短形式 `<route>/<model>`）；
//   - 未知顶层键被 schema **剥掉**（zod 默认行为），所以归属标记 `_arkMate` 不影响解析。
type configDoc struct {
	Schema    string                 `json:"$schema"`
	Providers map[string]providerDoc `json:"providers"`
	Model     string                 `json:"model,omitempty"`
	Ownership map[string]any         `json:"_arkMate,omitempty"`
}

// providerDoc opencode 的一个 provider 路由声明。
//
// **没有 `options` 字段**（ADR-11 点名的第 ① 处偏差）：`options` 是 **V1 的成员名**，官方迁移指南
// 逐字说「Provider `options` are separated into `settings`, `headers`, and `body` according to their
// request role」，且禁止在 V2 条目里混入 V1 形态。于是 v11 往 options 里塞的 `api` / `compat`
// **在本期没有出口**（协议只有 openai 一种，wire 协议由 adapter 内置的官方 package 决定）。
type providerDoc struct {
	Name     string              `json:"name,omitempty"`
	Env      []string            `json:"env,omitempty"`
	Package  string              `json:"package,omitempty"`
	Settings map[string]any      `json:"settings,omitempty"`
	Models   map[string]modelDoc `json:"models,omitempty"`
}

// modelDoc opencode 的模型条目（`models.<id>`，key 就是条目的本地 id）。
//
// 用 map 而不是定形结构体：条目的字段面由**底座**定义（`name` / `disabled` / `modelID` / …），
// 平台本期只声明 `name`（`extra` 已在 ADR-7 删除，见 §载体差异的「未迁移字段」清单）。
type modelDoc map[string]any

// buildConfig 生成 opencode 的整份配置（纯函数）。
//
// **不写 `apiKey`**：密钥只以 `env: ["<逐供应商引用名>"]` **引用**，值由平台经子进程 env 注入。
// 这条是硬的：内联内容走环境变量，同机同用户 `ps eww` 可见。
func buildConfig(set resource.Set) ([]byte, error) {
	doc := configDoc{
		Schema:    ConfigSchema,
		Providers: map[string]providerDoc{},
		Ownership: map[string]any{"manager": "ark-mate", "generator_version": generatorVersion},
	}
	// provider 顺序由 map 决定不了 → 先收集并排序，保证同输入同字节（golden 逐字节的前提）。
	routes := make([]string, 0, len(set.Providers))
	for _, p := range set.Providers {
		routes = append(routes, p.ID)
	}
	sort.Strings(routes)
	byID := make(map[string]resource.Provider, len(set.Providers))
	for _, p := range set.Providers {
		byID[p.ID] = p
	}

	for _, routeID := range routes {
		p := byID[routeID]
		// `name` 取**路由 id**（= 供应商 id），与 v11 逐字相同。供应商的展示名 `Provider.Name`
		// 刻意不下发：它是平台侧的用户词，底座侧 `name` 是另一个字段面，改它会让等价性 golden 的
		// A 部分（未点名处）出现差异——那按纪律必须以 golden 为准。
		pd := providerDoc{Name: routeID, Package: ProviderPackage, Models: map[string]modelDoc{}}
		if p.Ref != "" {
			pd.Env = []string{p.Ref}
		}
		if p.BaseURL != "" {
			pd.Settings = map[string]any{"baseURL": p.BaseURL}
		}
		doc.Providers[routeID] = pd
	}

	// 默认模型：`Set.DefaultModel` 是**平台模型 id**，要在这里解析成上游 id 与它挂靠的 route
	// （端口两边各用各的词汇，不做隐式转换）。解析不到就**不下发**默认——比下发一个不存在的值好：
	// 底座在默认值不在可用集时会静默退到第一个可用模型（事实 #46），那种"看起来设了"更坏。
	defaultRoute, defaultModel := "", ""
	for _, m := range set.Models {
		pd, ok := doc.Providers[m.ProviderID]
		if !ok {
			continue
		}
		if pd.Models == nil {
			pd.Models = map[string]modelDoc{}
		}
		md := modelDoc{}
		if m.Name != "" {
			md["name"] = m.Name
		}
		pd.Models[m.ModelID] = md
		doc.Providers[m.ProviderID] = pd
		if set.DefaultModel != "" && m.ID == set.DefaultModel {
			defaultRoute, defaultModel = m.ProviderID, m.ModelID
		}
	}
	if defaultRoute != "" {
		// 短形式；底座在默认值不在可用集时会**静默退到第一个可用模型**（事实 #46），
		// 所以对账/生效回执必须如实报，而不能假定这个值一定生效。
		doc.Model = defaultRoute + "/" + defaultModel
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// selfCheck 自校验：把生成的字节用**同一族的解析器**读回来。
//
// 不能偷懒成"检查非空"：模板/转义 bug 只有解析器抓得到。校验只判"能不能被解析成预期形状"，
// 不判"底座认不认这些字段"（后者是对账与端到端闸门的事）。
func selfCheck(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("生成内容为空")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("不是合法 JSON: %w", err)
	}
	provs, ok := doc["providers"].(map[string]any)
	if !ok || len(provs) == 0 {
		return fmt.Errorf("缺少 providers（复数）——v1 的 `provider`/`enabled_providers` 在 v2 上是静默的未知字段")
	}
	for route, v := range provs {
		pd, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("providers.%s 不是对象", route)
		}
		if _, ok := pd["models"].(map[string]any); !ok {
			return fmt.Errorf("providers.%s 缺少 models", route)
		}
	}
	return nil
}

// ——— 落盘（同目录 temp + 原子 rename） ———

// writeFileAtomic 原子写一份文件：同目录临时文件 → 设置权限 → rename 覆盖。
//
// 同目录是硬要求（跨设备 rename 不是原子的）；权限在 rename **之前**设，避免出现"路径已可见但
// 权限还宽"的窗口。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // rename 成功后这里是 no-op
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// bytesEqual 逐字节比较（空切片与 nil 视为相等，语义上都是"没有内容"）。
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
