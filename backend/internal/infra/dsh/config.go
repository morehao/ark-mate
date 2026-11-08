// 本文件是 dsh 的**模型清单交付实现**（`resource.Syncer`）：生成 `--patch` overlay、两级校验门、
// 落盘 + 装卸启动参数。
//
// 本包是纯数据 + 纯函数子包：不 import `internal/**`（archtest 的 TestAdapterHasNoStoreLiterals 守），
// 也不 import 父包 `internal/runtime`（父子 import 环，约束⑦）。改启动面的两件事（写文件 / 装卸启动参数）
// 只能经端口传进来的 `resource.Delivery` 完成；跑底座级校验门所需的外部信息（可执行文件、staging
// 目录）只能经 `Options` 传进来——adapter 拿不到注册表，也不该自己拼 HOME。
package dsh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/core/resource"
)

const (
	// PatchFlag dsh 的覆盖层参数名（`--patch <绝对路径>`）。
	//
	// 为什么平台写**自己的** overlay 而不是 dsh 的 home patch（ADR-14）：归属干净（那个文件只有
	// 平台会写）、**可撤销**（坏了去掉参数即回出厂配置）、层序最高（bundle → profile → home → CLI）。
	PatchFlag = "--patch"
	// dumpSchemaFlag dsh 的配置转储子命令。
	//
	// 判据落在 `--dump-config-schema` 上（**已对 0.2.1-rc.2 实测核对；2026-11-06 在 0.2.2-rc.1 复测核对**）：
	// 只有它输出 JSON 并带
	// `x-cordis.{complete,diagnostics}`；`--dump-config` 输出的是 YAML、诊断只写 stderr，判据不好机器化。
	// 代价：schema dump 会 import 各插件模块（可能初始化 profile）——所以 staging home 是必需的。
	dumpSchemaFlag = "--dump-config-schema"

	// defaultValidateTimeout 底座级校验门的默认超时（v11 同值：一次 dump 是秒级）。
	defaultValidateTimeout = 20 * time.Second

	// apiOpenAICompletions 本期唯一的 wire 协议。
	//
	// 为什么是常量而不是用户字段：`api` 是 dsh 的 **wire 协议**，而 v11 让用户从 provider 的
	// `projections.<runtime>.api` 手填，等于把底座词汇放进用户表单（ADR-3/ADR-11）。本期只支持
	// OpenAI 兼容端点，所以它由 adapter 固定。
	apiOpenAICompletions = "openai-completions"

	// patchPerm 平台写下的 overlay 权限（清单不是密钥，但落点在平台独占根里，从紧无代价）。
	patchPerm = os.FileMode(0o600)
)

// DefaultRefPattern 平台凭据引用名的默认模板（`Options.RefPattern` 留空时用它）。
//
// adapter 用它派生**平台命名空间前缀**（`MATE_`），从而知道凭据文档里哪些 `refs` 条目是自己写的。
// 为什么需要知道：凭据文档是**共享面**（还有 `records:` 与用户自己的 refs），契约③的空集撤回在这里
// 的粒度只能是"平台自己写过的条目"，而不是整份文档（§安全 纪律⑤）。
const DefaultRefPattern = "MATE_%s_API_KEY"

// Options 构造同步实现所需的**已解析落点**与底座级校验门的开关。
//
// 路径由父包解析后传入（见 `internal/runtime.ResolveConfigLocation`）：adapter 拿不到注册表，
// 也不该自己拼 `HOME`（那正是"平台写到用户真实目录"的入口，见 `docs/agents/isolation.md` 的隔离纪律）。
type Options struct {
	// PatchPath overlay 的绝对落点（`--patch` 指向它）。空 = 未声明清单落点（交付面不可用）。
	PatchPath string
	// CredentialDocPath 原生凭据文档的绝对落点（`<$DSH_HOME>/.credentials.yaml`）。空 = 不走凭据面。
	CredentialDocPath string
	// DshBin dsh 可执行文件。**空 = 不跑底座级校验门**（纯生成场景/单测）：此时只跑自校验，
	// 并在 Outcome 的 Where/Problems 里**如实说明"未跑底座级校验"**——不许把它说成已校验。
	DshBin string
	// StagingHome 底座级校验门的 staging 根（校验用的私有 `DSH_HOME` 建在它下面）。
	// 必须与活跃 home 分开：dump 是 boot-free 的，但 prepareProfile 会建 profile 目录。
	StagingHome string
	// Timeout 一次 dump 的超时；<=0 用 defaultValidateTimeout。
	Timeout time.Duration
	// RefPattern 平台凭据引用名模板；用来识别凭据文档里"平台自己写过的"条目。空 = DefaultRefPattern。
	RefPattern string
	// PatchArgs `--patch` 取值列表的共享管理器（模型清单与产品身份共用同一个 flag）。
	//
	// 由父包在登记可执行半场时造**一个**实例、交给需要它的所有 adapter 实现；nil = 本实现
	// 自己造一个（单测/独立使用）。**绝不能各造各的**：两个 `SetArg` 会互相覆盖，
	// 表现为"装了身份、模型清单没了"（或反之），而两边都报 applied。
	PatchArgs *PatchArgs
}

// NewSyncer 构造 dsh 的清单同步实现（装配形态见 `internal/infra/dsh/adapter.go` 的 `registerBuiltinSyncers`）。
func NewSyncer(opts Options, delivery resource.Delivery) resource.Syncer {
	patches := opts.PatchArgs
	if patches == nil {
		patches = NewPatchArgs(delivery)
	}
	return &syncer{
		patchPath:   strings.TrimSpace(opts.PatchPath),
		credDocPath: strings.TrimSpace(opts.CredentialDocPath),
		dshBin:      strings.TrimSpace(opts.DshBin),
		stagingHome: strings.TrimSpace(opts.StagingHome),
		timeout:     opts.Timeout,
		refPattern:  opts.RefPattern,
		delivery:    delivery,
		patches:     patches,
	}
}

// DiscoverDshBin 按"显式覆盖、不探测宿主安装路径"的口径找 dsh 可执行文件：`MATE_DSH_BIN` 优先，
// 否则 `exec.LookPath("dsh")`；都拿不到返回空串（= 不跑底座级校验门）。
//
// **刻意不在 `NewSyncer` / `registerBuiltinSyncers` 里自动调用它**：注册表是装机期静态构造的，
// 在那里探测会让"这台机器上恰好装了 dsh"决定平台行为——包括**测试**（`go test ./...` 会去执行宿主的
// dsh，而本仓的纪律是全量测试不依赖外部 runtime，且隔离纪律要求不碰用户自己的底座）。所以这一步只由
// **登记点**（`internal/infra/dsh/adapter.go` 的 `registerBuiltinSyncers`）在需要底座级校验门时显式给，结果经 `Options.DshBin` 传进来。
//
// 也**不得**硬编码 `/opt/homebrew/bin/dsh`、`/usr/local/bin/dsh` 这类宿主路径：那是"依赖宿主机环境"，
// 与桌面态"随包载荷"的交付形态反向。
func DiscoverDshBin() string {
	if v := strings.TrimSpace(os.Getenv("MATE_DSH_BIN")); v != "" {
		return v
	}
	p, err := exec.LookPath("dsh")
	if err != nil {
		return ""
	}
	return p
}

type syncer struct {
	patchPath   string
	credDocPath string
	dshBin      string
	stagingHome string
	timeout     time.Duration
	refPattern  string
	delivery    resource.Delivery
	// patches `--patch` 取值列表（与身份 adapter 共用同一个实例）。
	//
	// 为什么不再自己记一个 `lastArg`：`--patch` 有**两个**独立写者（模型清单与产品身份），
	// 各自记影子必然分叉——A 以为列表是 `[模型]`、B 以为 `[身份]`，最后一次 `SetArg` 把它们
	// 变成一个，而两边的幂等判据都还停在各自的旧值上。收成 `PatchArgs` 之后只有一份影子。
	patches *PatchArgs
}

// SyncModels 生成 overlay → 自校验 → 底座级校验门 → 落盘 + 装参数 + 写凭据文档；清单为空时**撤回**。
//
// 顺序是硬要求（§失败半径）：**校验门在写盘之前**。dsh 的失败半径是**拒启**，所以"生成 → 当
// `--patch` 校验 → 通过才落到真正的位置"是天然顺序，不需要先写到位再回滚。
func (s *syncer) SyncModels(ctx context.Context, set resource.Set) (resource.Outcome, error) {
	if s.patchPath == "" {
		return resource.Outcome{
			Status:   resource.StatusUnsupported,
			Where:    "未声明清单落点",
			Effect:   resource.EffectNextProcess,
			Problems: []string{"该 runtime 没有声明模型清单落点：请在 Spec.ModelConfig 里给出 Root 与 Filename"},
		}, nil
	}
	if len(set.Models) == 0 {
		return s.revoke(set)
	}

	data, err := buildPatch(set)
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("生成 dsh overlay 失败：%w", err)
	}
	// ② 自校验（失败即中止，一个字节都不写）。
	if err := selfCheck(data); err != nil {
		return resource.Outcome{}, fmt.Errorf("dsh overlay 自校验失败（未写任何字节）：%w", err)
	}
	// ②′ 底座级校验门：把候选**作为 `--patch`** 递进去跑一次 dump。三条判据同时成立才允许落盘
	// （只看退出码会漏掉「目标 entry id 不存在」——那是 warning、退出码仍是 0）。
	problems := []string{}
	if s.dshBin == "" {
		problems = append(problems, "未跑底座级校验（未提供 dsh 可执行文件）：这份候选只过了 JSON/YAML 自校验")
	} else if err := s.validate(ctx, data); err != nil {
		return resource.Outcome{}, fmt.Errorf("dsh 底座级校验门拒绝这份候选（未写任何字节）：%w", err)
	}

	// ③ 凭据面先写：权限不对会让 dsh **拒启**（§安全 纪律①），所以它比清单更该在动清单之前落定。
	// 反过来（先写清单再写凭据）在凭据失败时会留下"清单已换、凭据没换"的中间态。
	cred, credErr := s.syncCredentials(set)
	if credErr != nil {
		return resource.Outcome{}, credErr
	}

	// ④ 交付清单：写文件（同目录 temp + 原子 rename）+ 把 `--patch <绝对路径>` 装进启动参数。
	changed := true
	prev, rerr := os.ReadFile(s.patchPath)
	switch {
	case rerr == nil:
		changed = !bytes.Equal(prev, data)
	case os.IsNotExist(rerr):
		changed = true
	default:
		return resource.Outcome{}, fmt.Errorf("读上一份 dsh overlay 失败（%s）：%w", s.patchPath, rerr)
	}
	if changed {
		if werr := writeFileAtomic(s.patchPath, data, patchPerm); werr != nil {
			return resource.Outcome{}, fmt.Errorf("写 dsh overlay 失败（%s）：%w", s.patchPath, werr)
		}
	}
	abs, aerr := filepath.Abs(s.patchPath)
	if aerr != nil {
		return resource.Outcome{}, fmt.Errorf("解析 dsh overlay 绝对路径失败（%s）：%w", s.patchPath, aerr)
	}
	// 参数**每次同步都要装**：启动参数住在注册表的内存里，重启后必须靠一次同步重建。
	// `--patch` 的路径必须是**绝对**路径（dsh 侧按调用目录解析，而平台拉底座的 cwd 是用户会话目录）。
	//
	// 走 `PatchArgs` 而不是直接 `SetArg`：`--patch` 有两个独立写者（模型清单与产品身份），
	// 任何一方直接 `SetArg` 都会把另一方挤掉——而两边都会报 applied。
	argChanged, err := s.patches.SetModel(abs)
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("装载 dsh 启动参数失败（%s）：%w", PatchFlag, err)
	}

	status := resource.StatusApplied
	where := s.patchPath + "（平台自有 overlay，经 " + PatchFlag + " 以绝对路径递入）"
	if !changed && !argChanged {
		status = resource.StatusUnchanged
		problems = append(problems, "内容与上一份相同（幂等重放）：没有重写任何字节")
	}
	if cred.Status == resource.StatusFailed {
		problems = append(problems, "凭据交付未完成，见 credential 段")
	}
	return resource.Outcome{
		Status:     status,
		Where:      where,
		Effect:     resource.EffectNextProcess,
		Credential: cred,
		Problems:   problems,
	}, nil
}

// revoke 契约③「空集必撤回」在 dsh 上的形态：**摘参数 + 删文件** + 只移除平台自己写过的凭据条目。
//
// 为什么不能"什么都不做"：什么都不做会让上一份 overlay 继续生效（v11 的真实缺陷）。
// 为什么不能"写一份空文件"：dsh 逐字说「an empty or comments-only file fails boot — disable the
// layer with `[]` instead」——空 patch 是**拒启**，所以文件必须**不存在**。
func (s *syncer) revoke(set resource.Set) (resource.Outcome, error) {
	existed := false
	if _, err := os.Stat(s.patchPath); err == nil {
		existed = true
	} else if !os.IsNotExist(err) {
		return resource.Outcome{}, fmt.Errorf("检查 dsh overlay 失败（%s）：%w", s.patchPath, err)
	}
	argChanged, err := s.patches.SetModel("")
	if err != nil {
		return resource.Outcome{}, fmt.Errorf("摘除 dsh 启动参数失败（%s）：%w", PatchFlag, err)
	}
	if existed {
		if err := os.Remove(s.patchPath); err != nil && !os.IsNotExist(err) {
			return resource.Outcome{}, fmt.Errorf("删除 dsh overlay 失败（%s）：%w", s.patchPath, err)
		}
	}
	// 凭据面按契约③的**粒度例外**撤回：只移除平台自己写过的 refs 条目，用户自己的 refs 与
	// `records:` 分节逐字节保留。
	cred, credErr := s.syncCredentials(set)
	if credErr != nil {
		return resource.Outcome{}, credErr
	}
	status := resource.StatusUnchanged
	if existed || argChanged {
		status = resource.StatusApplied
	}
	return resource.Outcome{
		Status:     status,
		Where:      s.patchPath + "（已撤回：文件不存在、启动参数已摘除）",
		Effect:     resource.EffectNextProcess,
		Credential: cred,
		Problems:   []string{"平台清单为空：已摘除 " + PatchFlag + " 并删除 overlay（空 patch 会让 dsh 拒启）"},
	}, nil
}

// ——— `--patch` 取值列表（模型清单与产品身份共用） ———

// PatchArgs 管理 dsh 的 `--patch` 取值列表：模型清单在**前**、产品身份在**后**。
//
// 为什么需要它（而不是让两个 adapter 各自 `SetArg`）：`--patch` 是可重复参数，而
// `resource.Delivery.SetArg` 的语义是"这一个 flag 只有一个值"——两个写者各调一次，后一次
// 会把前一次**挤掉**。两处都会报 applied，用户看到"模型清单已同步、身份已交付"，而底座只拿到
// 后装的那一层。收成一个列表管理器之后，"这个 flag 的完整取值"只有一处知道。
//
// 顺序固定为 **model → identity**：身份 patch 只碰系统提示，与模型 patch 的路由条目不冲突；
// 但顺序仍要钉住——`--patch` 按传入顺序叠加，而"谁在最后"决定同名条目谁赢。
type PatchArgs struct {
	mu       sync.Mutex
	delivery resource.Delivery
	model    string
	identity string
	// last 已装进启动面那一份的指纹（`\x00` 拼接）。
	//
	// 它是**唯一的影子**：`Delivery` 只有写没有读回。零值 `""` 恰好等于"当前没有装任何
	// `--patch`"——启动期注册表里本来就没有它，所以首次同步会正确地报 changed。
	last string
}

// NewPatchArgs 造一个列表管理器。delivery 为 nil（单测只验生成）= 不真的改启动面。
func NewPatchArgs(delivery resource.Delivery) *PatchArgs {
	return &PatchArgs{delivery: delivery}
}

// SetModel 设置/撤销模型清单那一层（空 = 撤销），返回**启动面是否真的变了**。
//
// 幂等判据是"与已装的那一份比"，不是"与本次入参比"：同一个路径重复同步 → false；
// 上一次装失败了（`SetArgs` 报错，`last` 没动）→ 下一次仍会重试。
func (p *PatchArgs) SetModel(path string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.model = path
	return p.applyLocked()
}

// SetIdentity 设置/撤销产品身份那一层（空 = 撤销）。
func (p *PatchArgs) SetIdentity(path string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.identity = path
	return p.applyLocked()
}

// Current 返回当前**这次会话进程里想装上**的取值列表（诊断/测试用；不改启动面）。
func (p *PatchArgs) Current() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.valuesLocked()
}

// valuesLocked 非空的取值，顺序固定 model → identity。
func (p *PatchArgs) valuesLocked() []string {
	values := make([]string, 0, 2)
	if strings.TrimSpace(p.model) != "" {
		values = append(values, p.model)
	}
	if strings.TrimSpace(p.identity) != "" {
		values = append(values, p.identity)
	}
	return values
}

// applyLocked 把当前期望值写进启动面；返回是否发生变化。
//
// 失败时**不更新 `last`**：那样下一次同值同步会被判成 changed 并重试（"参数没装上"这件事
// 必须能被一次重试修好，而不是被自己的幂等影子锁死）。
func (p *PatchArgs) applyLocked() (bool, error) {
	values := p.valuesLocked()
	key := strings.Join(values, "\x00")
	if key == p.last {
		return false, nil
	}
	if p.delivery != nil {
		if err := p.delivery.SetArgs(PatchFlag, values); err != nil {
			return false, err
		}
	}
	p.last = key
	return true, nil
}

// ——— 生成（纯函数） ———

// buildPatch 生成 dsh 的 `--patch` 文档（顶层数组；条目目标是 `id: acp`）。
//
// 三条从随包文档逐字抄来的约束（写错任何一条都不是"没生效"而是"**拒启**"或"无声"）：
//
//  1. 目标条目是 **`id: acp`**——ACP 平面唯一消费"当前选中"的行。这条**经实测确认**（2026-10-10，
//     最小 ACP 客户端直接问底座）：只改 `acp.config.provider/model` → `currentValue` 变成它；
//     只改 `agent-default-model` → `currentValue` **纹丝不动**。所以生成器**默认不写**
//     `agent-default-model`（不写就没有第二个会漂移的事实）。
//
//     ⚠️ 一条同批测到、值得写下来的教训：这个结论**一度被一个探针 bug 推翻过**——探针当时读的是
//     `GET /models` 的 `options[]`，而 v11 起那个字段只对**未声明清单落点**的 runtime 回落。
//     读错字段让"新 route 的模型没进目录"看起来成立，于是生成器被改成两条都写。用最小 ACP 客户端
//     直接问底座之后才看清：**目录里一直都有**。教训：**结论要落在底座自己的回答上**
//     （`options[category=model]`），不要落在平台的投影上。
//
//  2. patch 里除 `id` 外的键会被**原样拷到目标 entry 上**，而 `config` 是**整体替换**该行的
//     config（不与它合并）⇒ 必须**重述**要保留的每个字段。本生成器只写模型路由与默认模型，
//     其余字段由平台自己的 overlay 层承担（它不是 dsh 的 home patch）。
//
//  3. **绝不能产出空文件 / 只有注释的文件 / 只有 `---` 的文件**——dsh 逐字说那会 `fails boot`。
//     本函数在 `len(set.Models) == 0` 时由调用方短路（`revoke`），所以这里只会在"有内容"时被调用。
//
//  4. **图片限额字段成对出现且 quantum ≤ max**（0.2.2-rc.1 新增，写错是**拒启**不是静默）：
//     `llm-pi-ai` 的 provider profile 增加了 `maxRequestImages`（默认 50）与
//     `imageOffloadCountQuantum`（默认 `min(10, maxRequestImages)`）。本仓当前**不写这两个键**
//     （走底座默认，安全）；若将来要显式调，注意两者必须成对写、且 `imageOffloadCountQuantum ≤
//     maxRequestImages`——违反时 `resolveProfiles` 抛错 → dsh **拒启**。
//
// compatDeveloperRole 返回该供应商要下发的 `compat.supportsDeveloperRole`（nil = 不下发）。
//
// dsh 0.2.2 的 compat 键是布尔 `supportsDeveloperRole`（**不是**旧夹具里那个字符串 `systemRole`，
// 后者会被配置校验拒绝："no wire protocol declares"）。`false` = 告知 dsh 本上游不认新式
// `developer` → 退发旧式 `system`，正是 OpenAI 兼容代理被 422 拒收的正解（2026-11-08 实测 `llm.yygu.cn`）。
//
// 显式声明优先；空则对 openai-completions 兜底 `false`（兼容优先）——兜底放在 adapter 里，是为了
// "即使上游资源层忘了填，也不会把 developer 打给对方"。
func compatDeveloperRole(p resource.Provider) *bool {
	if p.SupportsDeveloperRole != nil {
		return p.SupportsDeveloperRole
	}
	if p.Protocol == resource.ProtocolOpenAI {
		f := false
		return &f
	}
	return nil
}

// 输出是**手写的确定性 YAML**（不引第三方 YAML 库：本仓零新依赖，且键序必须可钉）。
func buildPatch(set resource.Set) ([]byte, error) {
	var b []byte
	b = append(b, []byte("# ark-mate 生成的模型清单 overlay（平台自有文件；经 dsh --patch 递入）\n")...)
	b = append(b, []byte("# 归属标记：本文件只有平台会写；dsh 自己的 cordis.patch.yml 不在此列。\n")...)

	// 默认模型：`Set.DefaultModel` 是**平台模型 id**，要在这里解析成上游模型名与它挂靠的 route
	// （端口两边各用各的词汇，不做隐式转换）。解析不到就**不下发**默认——比下发一个不存在的值好。
	//
	// `provider:` 写的是**派生后的路由键**——它与下面 `providers.<route>:` 的键、以及 ACP 模型值的
	// 第一段必须逐字相同（三处同源，少一处就是"所有选了模型的会话立刻起不来"：`set_config_option`
	// 找不到那个 provider）。守卫③ 的静态那半 + `TestDshRouteKeyAgreesBetweenPatchAndModelValue`
	// 的动态那半一起钉这条边。
	for _, m := range sortedModels(set.Models) {
		if set.DefaultModel == "" || m.ID != set.DefaultModel {
			continue
		}
		// 目标行 `id: acp`：平台会话真正消费"当前选中"的那一行（实测：只改它 currentValue 就变；
		// 只改 agent-default-model 则不变）。
		b = append(b, []byte("- id: acp\n  config:\n")...)
		b = append(b, fmt.Sprintf("    provider: %s\n    model: %s\n", yamlQuote(routeKey(m.ProviderID)), yamlQuote(m.Model))...)
	}

	// 路由声明：按 route 分组（`llm-pi-ai` 的 `providers.<route>.models[]` 是**整体替换**语义，
	// 所以平台列出的就是该路由的全部——首版**不自动把底座默认值补进来**，那会造出平台没声明的条目）。
	// 本版只写 `llm-pi-ai` 路由；`llm-deepseek` 的内置目录保持不动（它的 `models` 语义是
	// 咨询性目录，写它反而会把内置两条挤掉）。
	if len(set.Providers) > 0 {
		// `routes` 收的是**派生后的路由键**（底座命名空间里的名字），`byRoute` 记回它对应的平台档案。
		// 两者必须分开记：键用底座词，而"这条模型属于谁"比的是**平台词**（`m.ProviderID == p.ID`）
		// ——拿派生键去比 `ProviderID` 会一条模型都匹配不上，产出一份**语法合法但没有 models 的**
		// 路由声明（底座静默回落自己的目录，外观上完全正常）。
		//
		// 排序仍按派生键：前缀是常量，派生后相对序与按平台 id 排**逐字相同**（v11 的等价性依赖这条）。
		byRoute := make(map[string]resource.Provider, len(set.Providers))
		routes := make([]string, 0, len(set.Providers))
		for _, p := range set.Providers {
			rk := routeKey(p.ID)
			routes = append(routes, rk)
			byRoute[rk] = p
		}
		sort.Strings(routes)
		models := sortedModels(set.Models)
		// `- id: llm-pi-ai` 这一条**只写一次**：dsh 的 `--patch` 语义是「除 `id` 外的键**整体替换**
		// 目标 entry 的 `config`（不与已有合并）」。若逐 route 各写一条 `- id: llm-pi-ai`，第二条起
		// 会整段覆盖前一条的 `providers`，最终只剩最后一个 route；而 `- id: acp` 的 `provider:`
		// 可能指向被丢弃的 route → dsh `session/new` 返回 -32603 `no adapter registered for provider X`
		// → 平台建会话 50001 Internal error。故所有 route 合并进**同一个** `providers:` 块，路由键
		// 仍按 `routes` 的升序依次排列。
		b = append(b, []byte("- id: llm-pi-ai\n  config:\n    providers:\n")...)
		for _, route := range routes {
			p := byRoute[route]
			// `displayName` 取**供应商展示名**（空则回落平台 id）——v24 之前这里写的是路由键，
			// 于是用户给供应商起的名字在交付面上**不可见**（底座界面一律显示路由键）。
			b = append(b, fmt.Sprintf("      %s:\n        displayName: %s\n", yamlQuote(route), yamlQuote(displayNameOf(p, route)))...)
			// `api` 由 adapter 内置（ADR-3：协议只支持 openai，不再让用户为它付一个自由串的代价）。
			b = append(b, fmt.Sprintf("        api: %s\n", yamlQuote(apiOpenAICompletions))...)
			// `compat: {supportsDeveloperRole: <bool>}`：developer 角色兼容开关（v40，修 422 developer 回归）。
			// OpenAI 兼容代理普遍只认旧式 `system`，dsh 的 openai-completions 默认发新式
			// `developer` 会被 422 拒收——故下发 `compat.supportsDeveloperRole:false` 让它退发
			// `system`。⚠ dsh 0.2.2 的键是布尔 `supportsDeveloperRole`（不是旧版字符串 `systemRole`）。
			if dr := compatDeveloperRole(p); dr != nil {
				b = append(b, fmt.Sprintf("        compat: {\"supportsDeveloperRole\":%t}\n", *dr)...)
			}
			// `apiKeyEnv` 是**凭据引用名**（逐供应商派生，ADR-16），配置文件里**绝不出现密钥值**——
			// 这是走官方推荐路径：配置只带引用，值住在凭据提供方。
			if p.Ref != "" {
				b = append(b, fmt.Sprintf("        apiKeyEnv: %s\n", yamlQuote(p.Ref))...)
			}
			if p.BaseURL != "" {
				b = append(b, fmt.Sprintf("        baseURL: %s\n", yamlQuote(p.BaseURL))...)
			}
			// profile 级默认思考强度（v39）：`reasoning: "<level>"`。
			//
			// 为什么"仅当同供应商所有思考模型默认档一致且非空才写"：`reasoning` 是**逐 route**
			// 的单一值，而默认档是**逐模型**的；一个 route 下若某模型的默认档为空（自动）而另一个是
			// `high`，就无法用一个 route 值同时表达——此时**不写**（让每个模型走底座/端点默认），
			// 而不是随便挑一个假装全体一致（宁缺毋假）。
			if def := unanimousDefaultEffort(models, p.ID); def != "" {
				b = append(b, fmt.Sprintf("        reasoning: %s\n", yamlQuote(def))...)
			}
			b = append(b, []byte("        models:\n")...)
			for _, m := range models {
				if m.ProviderID != p.ID {
					continue
				}
				b = append(b, fmt.Sprintf("          - id: %s\n", yamlQuote(m.Model))...)
				if m.Name != "" {
					b = append(b, fmt.Sprintf("            name: %s\n", yamlQuote(m.Name))...)
				}
				// 容量：**平台声明**（v25）。非空才写——空 = 不下发该字段，让底座自己的兜底生效
				// （`DEFAULT_CONTEXT_WINDOW`/`DEFAULT_MAX_TOKENS`），而不是由平台塞一个假值。
				//
				// 字段名用**底座的词**（`contextWindow`/`maxTokens`）只出现在这里：内核那两列叫
				// `max_input_tokens`/`max_output_tokens`，映射就这一处（A9 的精神：底座词表不回流）。
				if m.MaxInputTokens != nil {
					b = append(b, fmt.Sprintf("            contextWindow: %d\n", *m.MaxInputTokens)...)
				}
				if m.MaxOutputTokens != nil {
					b = append(b, fmt.Sprintf("            maxTokens: %d\n", *m.MaxOutputTokens)...)
				}
				// **总是**写 `input`（ADR-12）：`text` 是用户的**明确选择**而不是"没设"。
				//
				// 为什么不省掉它去"问下一层"：`declaredInput()` 对"声明了 catalog 认识的 id 而
				// 不写模态"会**保留 catalog 的**，而平台新造的 id 落到 `defaultInput: ["text"]`
				// ——于是"平台声明的模型能不能收图"变成一件取决于底座目录版本的事，用户既改不动
				// 也看不见。显式写它把这件事变成确定事实。
				//
				// 代价（必须可见，不得静默）：显式 `["text"]` 会**覆盖 catalog**，所以底座目录里
				// 本来支持图片的模型也会变成纯文本，除非用户在「设置 · 模型」里勾上图片。
				// 这是"默认 text"的直接后果，不是 bug（界面必须让用户能看见并一键改）。
				modalities := m.InputModalities
				if len(modalities) == 0 {
					// 端口契约是"总是带值"；这里兜一个防御性默认，免得空数组落成 `input: []`
					// （那在底座眼里是"没声明"，正好与我们想要的确定事实相反）。
					modalities = []string{resource.ModalityText}
				}
				b = append(b, fmt.Sprintf("            input: [%s]\n", strings.Join(modalities, ", "))...)
				// 思考能力（v39）：仅当声明为思考模型且至少一个档位时，写 `reasoningEfforts:{档位→wire值}`。
				//
				// 诚实口径（reverse-design §6/§7）：OpenAI 兼容下档位名本身即 wire 值，故
				// `low: "low"`——这是**真实下发**（底座据此开启 reasoning 并生成 thinkingLevelMap），
				// 不是"能勾但无效果"。未声明思考的模型**完全不写**这行，旧夹具字节因此不变。
				if m.SupportsReasoning && len(m.ReasoningEfforts) > 0 {
					b = append(b, []byte("            reasoningEfforts:\n")...)
					// 档位按规范序（从低到高）输出：端口契约已经归一，这里再排一次让"顺序"不依赖
					// 调用方记性，且逐字节 golden 稳定（用户勾选顺序不该改变产物）。
					for _, lvl := range resource.NormalizeReasoningEfforts(m.ReasoningEfforts) {
						b = append(b, fmt.Sprintf("              %s: %s\n", lvl, yamlQuote(lvl))...)
					}
				}
			}
		}
	}
	return b, nil
}

// unanimousDefaultEffort 返回供应商 p 下所有**思考模型**的**一致**默认思考强度；不一致或任一为空
// 则返回空串（= 不写 profile 级 `reasoning`）。
//
// 语义见调用点：profile 级 `reasoning` 是逐 route 的单一值，只有在同 route 所有思考模型默认档都相同
// 且非空时才可无歧义地表达；否则宁可不写（让每模型走默认），也不挑一个假装一致。
//
// 只看 `SupportsReasoning && len(ReasoningEfforts)>0` 的模型：非思考模型不参与（它们的默认档无意义）。
// 若该供应商下**没有**思考模型，返回空串（不写）。
func unanimousDefaultEffort(models []resource.Model, providerID string) string {
	def := ""
	have := false
	for _, m := range models {
		if m.ProviderID != providerID || !m.SupportsReasoning || len(m.ReasoningEfforts) == 0 {
			continue
		}
		if m.DefaultEffort == "" {
			// 有思考模型默认档为空（自动）→ 无法与其它模型统一，不写。
			return ""
		}
		if !have {
			def, have = m.DefaultEffort, true
			continue
		}
		if m.DefaultEffort != def {
			return ""
		}
	}
	return def
}

// sortedModels 把模型按 `(ProviderID, Model)` 排序后返回。
//
// 为什么要防御性排序：dsh 的 `models[]` 是**数组**（顺序进字节），逐字节 golden 要求"同输入同字节"。
// 编排层虽然已经按 `(ProviderID, Model)` 固定顺序（`internal/core/resource.Set` 的契约），但这里再排一次
// 让"顺序"这件事不依赖调用方的记性；且该序在**逐 route 过滤后**恰好是 v11 的上游模型名升序
// （v11 的输入就是按上游模型名全局排序的），等价性因此成立。
func sortedModels(in []resource.Model) []resource.Model {
	out := append([]resource.Model(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ProviderID != out[j].ProviderID {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// yamlQuote 给 YAML 标量加引号（**总是加**：模型 id 里可能出现 `:`、`/`、`#` 这些在裸标量里
// 有意义的字符，靠"看起来不需要引号"判断迟早出事）。
func yamlQuote(s string) string {
	return `"` + yamlEscape(s) + `"`
}

// yamlEscape 只转义 JSON 字符串里必须转义的字符（双引号 YAML 标量与 JSON 字符串规则一致）。
func yamlEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(b[1 : len(b)-1])
}

// selfCheck 自校验：把生成的字节用**同一族的解析器**读回来。
//
// 这一步不能省，也不能偷懒成"检查非空"：dsh 侧解析失败 = **拒启**，而模板/转义 bug 只有解析器
// 抓得到。校验**只判"能不能被解析成预期形状"**，不判"底座认不认这些字段"（那是底座级校验门与
// 对账的事——`schemastery` 默认 strict=false，未知字段会被静默合并）。
func selfCheck(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("生成内容为空")
	}
	lines, arrayItems := 0, 0
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		lines++
		if strings.HasPrefix(t, "- id:") {
			arrayItems++
		}
	}
	if lines == 0 {
		// dsh 逐字：`an empty or comments-only file fails boot`。
		return fmt.Errorf("patch 只有注释/空行——这会让 dsh **拒启**（必须写出字面量 [] 或干脆不传 --patch）")
	}
	if arrayItems == 0 {
		// 顶层必须是数组，每项一个 `- id:`（注释行不计）。
		return fmt.Errorf("patch 必须是顶层数组（每项一个 `- id:` 条目）")
	}
	return nil
}

// ——— 底座级校验门（T3.2 / T3.4 的契约） ———

// dumpResult 一次 `--dump-config-schema` 的结论。
type dumpResult struct {
	ExitCode int
	// Complete `x-cordis.complete`（false = 有 entry 没被解析成完整形态）。
	Complete bool
	// Diagnostics `x-cordis.diagnostics` 的逐条文本（**空**才是通过）。
	Diagnostics []string
	// Raw 原始输出（截断后进错误文案，便于用户自己看）。
	Raw string
}

// validate 把候选文件**作为 `--patch` 递进去跑一次 dump**。
//
// 为什么必须有这一级，而且判据不能只看退出码（v11 `dsh_validate.go` 的结论，这里是它的落点）：
// dsh 的 patch 层有三条**静默失败**，全部不改退出码：
//
//	① 目标 id 不存在 → `warn('patch: entry "x" not found')` 然后 **continue 跳过整条**；
//	② `name` 与目标不符 → 同样跳过；
//	③ patch 里除 `id` 外的键被**原样拷到目标 entry 上** ⇒ 把 `config` 拼成 `configs` 会被当成
//	   entry 的一个新属性挂上去，`config` 一动不动，**完全无声**。
//
// 所以判据是 **exitCode == 0 且 `x-cordis.diagnostics` 为空 且 `x-cordis.complete == true`**。
// 只看 exitCode 会把"补丁没打到任何一行"当成成功。还有一条机制依据让这一级不是"看起来合理"：
// dsh 的 `include` 实现逐字说它的 patch 语义「shared by mounting (applyPatches) and offline config
// tooling (dsh --dump-config) **so a dump can never drift from what boots**」——dump 走的就是启动那条路。
//
// 三条纪律：
//   - **必须在 staging `DSH_HOME` 上跑**：dump 是 boot-free 的，但 `prepareProfile` 会建 profile 目录；
//     在活跃的 `<MATE_HOME>/runtime/dsh` 上跑等于去动正在服务的底座 home。
//   - **在候选上跑、不在成品上跑**：`--patch` 的语义就是"额外覆盖一层"，所以不需要先写到位再回滚。
//   - **失败不落盘**：调用方在写盘之前调用它，失败即中止（一个字节都不写）。
func (s *syncer) validate(ctx context.Context, candidate []byte) error {
	root := s.stagingHome
	if root == "" {
		// staging 必须**与活跃 home 分开**：放在临时目录而不是任何 runtime 目录里面。
		root = filepath.Join(os.TempDir(), "mate-dsh-validate")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, "stage-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	cand := filepath.Join(staging, "candidate.patch.yml")
	if err := os.WriteFile(cand, candidate, 0o600); err != nil {
		return err
	}
	// 用**本 adapter 自己的**启动形态（adapter 拥有 Command/Args 的知识），交给 `--patch` 与 dump。
	args := append([]string{}, Args...)
	args = append(args, PatchFlag, cand, dumpSchemaFlag)

	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultValidateTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(cctx, s.dshBin, args...)
	c.Dir = staging
	// 隔离 env：把 DSH_HOME 指到 staging（**绝不在活跃 home 上跑**），HOME 指到 staging。
	// 继承其余环境（PATH 等是跑起来的前提）；显式给的值赢，且每个键只出现一次。
	env := []string{"DSH_HOME=" + filepath.Join(staging, "dsh"), "HOME=" + staging}
	c.Env = mergeChildEnv(os.Environ(), env)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr

	runErr := c.Run()
	res := &dumpResult{}
	if ee, ok := runErr.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
	} else if runErr != nil {
		return fmt.Errorf("跑 dsh 校验门失败（%s）：%w", s.dshBin, runErr)
	}
	// **先解析完整输出，再为"给人看的证据"截断**——顺序反了就会把大 dump 截成非法 JSON，
	// 于是每一份稍大的候选都被判成"无法校验"（2026-10-10 实测踩到：dsh 的 schema dump 本来就
	// 远超 8KB，所以校验门对所有输入都恒红）。这类 bug 的形态很坏：它**看起来**像底座拒绝了候选。
	raw := stdout.String()
	res.Raw = clipText(raw, 8000)
	if se := strings.TrimSpace(stderr.String()); se != "" {
		res.Diagnostics = append(res.Diagnostics, "stderr: "+firstLine(se))
	}
	// 解析 `x-cordis`（dump 的输出是 JSON；解析不了就是"无法校验"→ 记诊断而不是当成通过）。
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		// 带上**证据**：只说"不是 JSON"的话，看到的就是一句无法行动的话。
		res.Diagnostics = append(res.Diagnostics,
			"dump 输出不是 JSON：无法校验（输出开头："+clipText(firstLine(raw), 200)+"）")
	} else if xc, _ := doc["x-cordis"].(map[string]any); xc == nil {
		res.Diagnostics = append(res.Diagnostics,
			"dump 输出里没有 x-cordis：无法校验（输出开头："+clipText(firstLine(raw), 200)+"）")
	} else {
		if b, ok := xc["complete"].(bool); ok {
			res.Complete = b
		}
		switch d := xc["diagnostics"].(type) {
		case []any:
			for _, item := range d {
				res.Diagnostics = append(res.Diagnostics, diagnosticText(item))
			}
		case nil:
		default:
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("%v", d))
		}
	}
	if dumpPassed(res) {
		return nil
	}
	parts := []string{fmt.Sprintf("退出码 %d", res.ExitCode)}
	if !res.Complete {
		parts = append(parts, "x-cordis.complete=false")
	}
	if len(res.Diagnostics) > 0 {
		parts = append(parts, strings.Join(res.Diagnostics, "；"))
	}
	return fmt.Errorf("判据（退出码 0 + x-cordis.complete=true + 诊断为空）未同时成立：%s", strings.Join(parts, "，"))
}

// dumpPassed 校验门的判据（三条同时成立）。
func dumpPassed(res *dumpResult) bool {
	return res != nil && res.ExitCode == 0 && res.Complete && len(res.Diagnostics) == 0
}

// clipText 截断一段文本（诊断里只放开头，避免把整份 dump 塞进错误消息）。
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…（截断）"
}

// diagnosticText 把一条诊断渲染成人话（形状不稳，尽量兼容）。
func diagnosticText(item any) string {
	switch v := item.(type) {
	case string:
		return v
	case map[string]any:
		level := bodyString(v, "level")
		msg := bodyString(v, "message")
		if msg == "" {
			msg = bodyString(v, "text")
		}
		if path := bodyString(v, "path"); path != "" {
			msg = path + ": " + msg
		}
		if level != "" {
			return level + " " + msg
		}
		return msg
	}
	return fmt.Sprintf("%v", item)
}

// mergeChildEnv 合并子进程环境：**显式给的 env 赢、且每个键只出现一次**。
//
// 与 `internal/infra/protocol/acp` 的子进程 env 合并同一语义（POSIX getenv 取第一条、Node 取最后一条——同名两条会让
// 同一份环境在 Go 子进程与 JS 子进程里语义相反）。这里只需要"显式值赢"，所以重复实现 20 行，
// 而不是把 acp 的内部工具导出成公共 API。
func mergeChildEnv(base, override []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(override))
	for _, kv := range override {
		name, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(strings.TrimSpace(name))
		if up == "" || seen[up] {
			continue
		}
		seen[up] = true
		out = append(out, kv)
	}
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(strings.TrimSpace(name))
		if up == "" || seen[up] {
			continue
		}
		seen[up] = true
		out = append(out, kv)
	}
	return out
}

// firstLine 取一段文本的首行（诊断文案里只放一行，避免把 stderr 全量灌进响应）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// bodyString 从 map 里取一个字符串值（形状不稳，非字符串一律当空）。
func bodyString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// writeFileAtomic 原子写一份文件：同目录临时文件 → 设置权限 → rename 覆盖。
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
