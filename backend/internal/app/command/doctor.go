package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
	doctorfacts "github.com/morehao/ark-mate/internal/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/version"
	"github.com/morehao/ark-mate/pkg/config"
)

// 本文件是 `mate doctor`（M4-5）：**只读体检**——不装配 App、不监听、不写 MATE_HOME
// 以外的任何路径。它回答的是"这份部署现在到底装成什么样"，以及"它自己说自己是什么"。
//
// K2 起它多读两样东西（**仍然只读**）：`mate.db` 以 SQLite `mode=ro` 打开（不跑迁移，
// 见 `store.OpenReadOnly`），以及托管凭据文档 `<mate home>/credentials.json` 的权限与形状
// （只 stat/读，不建文件、不改权限）。这两处刻意写下来，因为它们看起来像"什么都没写"而实际
// 会落笔——`mode=ro` 仍可能让 SQLite 重建 `-shm`/`-wal` 两个共享内存面文件；探针也只落在
// MATE_HOME 之内。
//
// ## 为什么要有这条命令（而不是"启动失败时看日志"）
//
// 桌面态的失败大多不是崩溃，而是**看起来起来了但用不了**：资产是旧的那一份、载荷装错平台、
// 原生插件的 ABI 与 Electron 对不上、数据目录只读、端口被别的进程占着。这些在启动期要么静默
// （`/meta` 照发一份自洽的值），要么只表现为运行期某个具体功能报错——而用户手里的现场往往
// 已经不可复现（改过文件、换过包）。所以判据必须是"拿**盘上的实际内容**复算，再与各处声明比"，
// 这正是本命令与 `make verify/release-json` 同一条纪律（见 design §6.3 的完整性一行：
// "打包期逐文件生成清单 + CI 复算；全量复算由 `mate doctor --verify-payload` 承担"）。
//
// ## 声明 vs 事实（本命令的分工）
//
//	事实（自己算）      资产树的逐文件摘要、载荷树的逐文件摘要与合成摘要、HOME 可写性、端口可绑定性
//	声明（别人给的）     release.json（发布契约）、payload.json（载荷清单）、壳交来的 `--payloads`
//	判据                两者**逐项相符**；不符即退出码 1（"无法校验"记警告，不判失败）
//
// 最后一条是刻意的：非打包态（dev、只有 API 的部署）根本没有 release.json，把"没有可比对象"
// 判成失败会让这条命令在开发态永远红，人就会开始忽略它——那比没有这条命令更坏。

// doctorStatus 一条检查的结论。三档的语义固定：
//
//	ok    事实与声明相符（或该检查没有可比对象但自身健康）
//	warn  不阻塞、但值得说出来（"无法校验"、端口已被占用、开发构建跳过契约比对）
//	fail  事实与声明不符 —— 只有它影响退出码
type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

// doctorCheck 一条检查。字段序即 `--json` 的键序（确定性纪律 C9）。
type doctorCheck struct {
	Name   string       `json:"name"`
	Status doctorStatus `json:"status"`
	Detail string       `json:"detail"`
}

// doctorReport `mate doctor --json` 的输出形状。
//
// 为什么把 failed / warnings / passed 也放进响应：机器消费方（CI、诊断包、将来的面板）不该
// 自己数一遍 `checks`——"怎么算失败"必须由产生结论的那一侧给出，否则同一份报告在两边可能
// 被判成两种结果（warn 算不算问题？这里定死：不算）。
type doctorReport struct {
	Version       string        `json:"version"`
	APIContract   string        `json:"apiContract"`
	Home          string        `json:"home"`
	Addr          string        `json:"addr"`
	VerifyPayload bool          `json:"verifyPayload"`
	Checks        []doctorCheck `json:"checks"`
	// Providers provider / 模型清单段的机器可读事实（K2；键名稳定）。
	//
	// 它与人话的 `checks` 并存而不是互替：`checks[]` 回答"这一项过没过"，这一段回答
	// "事实到底是几"。结构体定义在 manager（事实的构造函数也在那里），键名即它的字段序。
	Providers doctorfacts.ProviderDoctorFacts `json:"providers"`
	Failed    int                             `json:"failed"`
	Warnings  int                             `json:"warnings"`
	Passed    bool                            `json:"passed"`
}

// payloadManifestDoc 是随包载荷清单 `payload.json` 的读取形状（M3-1 的产出，M4-5 的复算对象）。
//
// 字段集与 `scripts/lib/payload.mjs` 的写入口径一致；这里**只读不写**，也不改清单格式——
// 新增字段属于载荷契约变更（计划 §12 的偏离处理第 2 条），本命令没有那个权限。
type payloadManifestDoc struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Platform string   `json:"platform"`
	Arch     string   `json:"arch"`
	NodeABI  string   `json:"nodeAbi"`
	Entry    string   `json:"entry"`
	Digest   string   `json:"digest"`
	Files    int      `json:"files"`
	Bytes    int64    `json:"bytes"`
	FileList []string `json:"fileList"`
	// FileListBase fileList 的相对基准（载荷根下的哪一层；`""` = 载荷根本身）。
	//
	// 用**指针**是为了区分"清单显式写了空基准"与"M4-5 之前装配的清单根本没这个字段"：
	// 前者可以直接用载荷根，后者只能靠闭合集合的唯一命中判定（见 resolveFileListBase）。
	// 两者都当成 `""` 处理会让"字段缺失"这件事实被静默吞掉——而它恰恰是判据强度的分界。
	FileListBase *string `json:"fileListBase"`
}

// payloadOnDisk 一份已装配的载荷（清单 + 它所在的目录）。
type payloadOnDisk struct {
	Dir      string
	Manifest payloadManifestDoc
}

// releaseContractDoc `release.json` 的读取形状（唯一发布契约，M4-1/§0.3）。
//
// 刻意**不**校验字段集与顺序：那是 `verify-release-json.mjs` 的职责（逐字比 RELEASE_FIELDS）。
// 本命令只取用它需要比对的几个值——两处都做同一件事只会让"谁是裁判"变模糊。
type releaseContractDoc struct {
	AppVersion  string `json:"appVersion"`
	CoreVersion string `json:"coreVersion"`
	WebBuildID  string `json:"webBuildId"`
	WebDigest   string `json:"webDigest"`
	APIContract string `json:"apiContract"`
	Electron    string `json:"electron"`
	Node        string `json:"node"`
	NodeABI     string `json:"nodeAbi"`
	Payloads    []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Digest  string `json:"digest"`
	} `json:"payloads"`
	Signing   string `json:"signing"`
	Notarized bool   `json:"notarized"`
	Channel   string `json:"channel"`
	MacUpdate string `json:"macUpdate"`
}

// doctorOptions 一条 doctor 调用的全部输入（解析后的值，不是 flag 集合）。
type doctorOptions struct {
	Home          string
	WebDir        string
	Addr          string
	PayloadsDir   string
	PayloadsWire  []ctrsystem.PayloadInfo
	PayloadsGiven bool
	ReleaseJSON   string
	VerifyPayload bool
	// ProviderDir provider 档案目录（K2：档案的**文件侧**载体与旧机制遗留物锚点的来源）。
	ProviderDir string
	// DBPath 库文件路径（K2：providers/models 两张表的读取对象）。
	DBPath string
	// StoreMode 解析后的持久层模式（sqlite|memory）：memory 形态下 DB 不是 provider 档案的
	// 事实源，K2 那一段据此如实记"不适用"，而不是报 0 条（那会假绿）。
	StoreMode string
	// TranscriptDir 会话 wire transcript 落盘目录（R3：v15 起旧形状的文件名要被看见）。
	TranscriptDir string
	// KnownRuntimeIDs **在册**的 runtime id 全集（v18 存量体检的判据输入）：全部内置适配 ∪
	// `runtimes.json` 的远程端点。**为什么由这里给**：判据必须落在注册表与配置这两处事实源上，
	// 而"哪些底座在册"是装配知识——采集侧（`manager`）连一个底座名都不许有（见
	// `manager/retired_runtime.go` 的文件头）。空集合 = 判据失效，那一段会响亮地说出来。
	KnownRuntimeIDs []string
	// ProjectionDirs 解析后的投影覆盖（`<runtime>:<kind>` → 目录）：用来认出"配给不在册底座的
	// 覆盖"（键形状合法、所以解析不报错，但它永远不会生效）。
	ProjectionDirs map[string]string
	// RebuildReadModels `--rebuild-read-models`：体检**之前**用 events 重建全部会话的读模型
	// （v21–v23）。默认 false = 只读体检一个字节都不写；true 是用户显式要求的写动作。
	RebuildReadModels bool
}

// doctor 子命令入口。退出码：0 = 没有失败项；1 = 有失败项；2 = 用法/配置错误。
func doctor(args []string) {
	fset := flag.NewFlagSet("doctor", flag.ExitOnError)
	home := fset.String("home", "", "数据目录（同 MATE_HOME；默认读配置解析结果）")
	webDir := fset.String("web-dir", "", "前端资产目录（绝对路径，须含 index.html；同 MATE_WEB_DIR）")
	addr := fset.String("addr", "", "端口占用检查目标（默认读配置解析结果，通常 127.0.0.1:10001）")
	payloads := fset.String("payloads", "",
		"桌面壳交来的已解析载荷清单（JSON 数组，与 serve 的 --payloads 同款）；"+
			"给出后会与 --payloads-dir 里的 payload.json 逐项比对（不符即失败）")
	payloadsDir := fset.String("payloads-dir", "",
		"随包载荷根目录（打包态是 <Resources>/payloads，内含 <name>/<version>/payload.json）；"+
			"载荷清单、入口存在性与 nodeAbi 的判据都取自它")
	releaseJSON := fset.String("release-json", "",
		"发布契约 release.json 路径（默认取核心二进制同目录那一份——打包态就是 <Resources>/release.json；"+
			"读不到记警告，不判失败：非打包态本来就没有它）")
	verifyPayload := fset.Bool("verify-payload", false,
		"对 --payloads-dir 下的每份载荷做**全量复算**（逐个文件 sha256 + 合成摘要 + 文件数/字节数，"+
			"与 payload.json 逐项比对；几百 MB 需要数秒）。需要 --payloads-dir")
	asJSON := fset.Bool("json", false, "以 JSON 输出（键序固定，供 CI/诊断包消费）")
	rebuildReadModels := fset.Bool("rebuild-read-models", false,
		"**显式 opt-in 的写动作**：用 events 重建全部会话的读模型（runs/messages）后再体检。"+
			"默认路径仍然只读——它只在体检报出 read-models 漂移之后当出路用")
	_ = fset.Parse(args)

	cfg, _ := mustConfig()
	opts := doctorOptions{
		Home:          cfg.HomeDir,
		WebDir:        cfg.WebDir,
		Addr:          cfg.Addr,
		PayloadsDir:   strings.TrimSpace(*payloadsDir),
		ReleaseJSON:   strings.TrimSpace(*releaseJSON),
		VerifyPayload: *verifyPayload,
		ProviderDir:   cfg.ProviderDir,
		DBPath:        cfg.DBPath,
		StoreMode:     cfg.ResolvedStoreMode(),
		TranscriptDir: cfg.TranscriptDir,
		// v18 存量体检的判据输入：**全部**内置适配（不是产品装配的那一个——与逐底座采事实同口径：
		// 构建期钉在哪一个上，不代表磁盘上没有别的底座的遗留）∪ `runtimes.json` 的远程端点
		// （它们是这台机器上被声明为可用的 id，留着它们的目录不算遗留）。
		KnownRuntimeIDs: knownRuntimeIDs(cfg),
		ProjectionDirs:  cfg.ProjectionDirs,

		RebuildReadModels: *rebuildReadModels,
	}
	// CLI flag 优先级最高（与 serve 同一条规则：默认 < config.json < env < MATE_* < flag）。
	if *home != "" {
		opts.Home = *home
	}
	if *webDir != "" {
		opts.WebDir = *webDir
	}
	if *addr != "" {
		opts.Addr = *addr
	}
	if *payloads != "" {
		ps, err := parsePayloads(*payloads)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(2)
		}
		opts.PayloadsWire = ps
		opts.PayloadsGiven = true
	}
	// 全量复算没有载荷目录就无从谈起——这是"调用方参数给错了"，不是体检结论（退出码 2）。
	if opts.VerifyPayload && opts.PayloadsDir == "" {
		fmt.Fprintln(os.Stderr, "--verify-payload 需要同时给 --payloads-dir（全量复算的对象是盘上的载荷树）")
		os.Exit(2)
	}

	report := runDoctor(opts)
	if *asJSON {
		// json.Marshal 对结构体按字段序输出；不手拼字符串（与 versionOutput 同一条纪律）。
		b, err := json.Marshal(report)
		if err != nil {
			fmt.Fprintf(os.Stderr, "体检报告序列化失败: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(string(b))
	} else {
		fmt.Print(renderDoctorReport(report))
	}
	if report.Failed > 0 {
		os.Exit(1)
	}
}

// runDoctor 跑完所有检查并汇总。纯装配（IO 都在各自 check 里），便于单测注入临时目录。
func runDoctor(opts doctorOptions) doctorReport {
	checks := []doctorCheck{
		checkHomeWritable(opts.Home),
		checkPortBindable(opts.Addr),
	}

	// 资产：与 serve 走**同一个**解析器（ResolveAssets）——doctor 不该有自己的一套口径，
	// 否则"体检说没问题、启动却拒绝起服"（或反过来）就成了最难查的一类不一致。
	assets, assetsErr := router.ResolveAssets(opts.WebDir)
	if assetsErr != nil {
		checks = append(checks, doctorCheck{"assets", doctorFail, assetsErr.Error()})
	} else {
		checks = append(checks, doctorCheck{"assets", doctorOK,
			fmt.Sprintf("dir=%s digest=%s buildId=%s files=%d",
				orDash(assets.Path), orDash(assets.Digest), orDash(assets.CurrentBuildID()), len(assets.Digests))})
	}

	// 发布契约（声明面）：打包态才有；读不到记警告。
	contract, contractPath, contractLoaded := loadReleaseContract(opts.ReleaseJSON)
	if !contractLoaded {
		checks = append(checks, doctorCheck{"release", doctorWarn,
			fmt.Sprintf("未找到 release.json（%s）：跳过与发布契约的逐项比对（非打包态属正常）", contractPath)})
	}

	// 载荷清单（盘上）。
	manifests, manifestErr := scanPayloadManifests(opts.PayloadsDir)
	manifestsOK := opts.PayloadsDir != "" && manifestErr == nil
	switch {
	case opts.PayloadsDir == "":
		checks = append(checks, doctorCheck{"payloads", doctorWarn,
			"未给 --payloads-dir：跳过随包载荷的清单与复算（非桌面态属正常）"})
	case manifestErr != nil:
		checks = append(checks, doctorCheck{"payloads", doctorFail, manifestErr.Error()})
	default:
		names := make([]string, 0, len(manifests))
		for _, m := range manifests {
			names = append(names, m.Manifest.Name+"@"+m.Manifest.Version)
		}
		checks = append(checks, doctorCheck{"payloads", doctorOK,
			fmt.Sprintf("%d 份随包载荷：%s", len(manifests), strings.Join(names, "、"))})
		checks = append(checks, checkPayloadManifests(manifests, contract, contractLoaded, opts.VerifyPayload)...)
	}

	// 壳交来的清单（`--payloads`）与盘上事实的对照：这是"`/meta` 说的与包里实际装的是否一份东西"。
	//
	// 载荷根读不出来时**不做对照**（否则会多报一条"壳交来的清单与盘上不一致"，而真实原因在
	// 上一条 `payloads` 检查里）——同一件事报两次、且第二次指错方向，是诊断输出最忌讳的形态。
	if opts.PayloadsGiven {
		if opts.PayloadsDir != "" && !manifestsOK {
			checks = append(checks, doctorCheck{"payloads (wire)", doctorWarn,
				"载荷根读不出来（见上一条 payloads），跳过与盘上事实的对照"})
		} else {
			checks = append(checks, checkWirePayloads(opts.PayloadsWire, manifests, opts.PayloadsDir))
		}
	}

	// 契约逐项比对（核心版本、契约代数、资产指纹、载荷清单）。
	if contractLoaded {
		checks = append(checks, checkContract(contract, assets, assetsErr == nil, manifests, manifestsOK)...)
	}

	// provider（供应商凭据）与模型清单（K2）：全部只读，事实由 manager 构造（本文件只排版）。
	providers, providerChecks := checkProviderSection(opts)
	checks = append(checks, providerChecks...)

	// 读模型重建（`--rebuild-read-models`，显式 opt-in 的写动作）：**排在体检之前**，
	// 这样报出来的 `read-models` 是"修完之后"的状态（否则用户会先看到一条红、再看到修好了，
	// 而两次结论对同一件事）。
	if opts.RebuildReadModels {
		checks = append(checks, checkReadModelRebuild(opts)...)
	}

	// 读模型漂移（v21–v23）：`messages`/`runs` 是 `events` 的投影（J7），投影错了不会报错——
	// 所以它需要一条**可判定**的体检项，而不是"看起来对不对"。默认路径仍然只读。
	checks = append(checks, checkReadModels(opts)...)

	// transcript 目录的**形状体检**（R3）：v15 起平台会话 id 形如 `ses_<26 位 ULID>`，而目录里
	// 旧形状的文件（裸 uuid）在装载期会被**跳过 + WARN**——那件事发生在启动日志里，而 doctor
	// 要让它变成"随时可查的体检项"。
	checks = append(checks, checkTranscriptShape(opts.TranscriptDir))

	// 存量体检（v18）：删掉一个适配不会让磁盘与库里的存量消失。逐底座采事实的那两段都按
	// **注册表**枚举，适配一删那个底座就整段消失（报告一切正常、盘上东西一个没少），所以这里
	// 改判"**在不在册**"。
	checks = append(checks, checkRetiredSection(opts)...)

	failed, warnings := 0, 0
	for _, c := range checks {
		switch c.Status {
		case doctorFail:
			failed++
		case doctorWarn:
			warnings++
		}
	}
	return doctorReport{
		Version:       version.Version,
		APIContract:   version.APIContract,
		Home:          opts.Home,
		Addr:          opts.Addr,
		VerifyPayload: opts.VerifyPayload,
		Checks:        checks,
		Providers:     providers,
		Failed:        failed,
		Warnings:      warnings,
		Passed:        failed == 0,
	}
}

// renderDoctorReport 人话报告（纯函数：单测直接断言文本，不重定向 stdout）。
//
// 图标三档与状态一一对应；末尾一行给结论——**"通过"也要把警告数写出来**，
// 否则"0 项失败"会让人以为一切都已校验过（warn 的常见含义正是"这一项没校验成"）。
func renderDoctorReport(r doctorReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "==> mate doctor（核心 %s / 契约 %s）\n", r.Version, r.APIContract)
	// 人话这一路把**默认数据目录**渲染成 `~/…`（v19）：`~` 可粘贴，而绝对机器路径会随截图与
	// 诊断包外流。`--json` 刻意**不**换（那一路是给诊断包/CI 消费的，要的是机器事实：
	// 逐字可比的绝对路径）。两半的口径各由一条测试钉住（见 doctor_test 的显示名用例）。
	fmt.Fprintf(&b, "    数据目录 %s\n    检查地址 %s\n", orDash(config.DisplayHome(r.Home)), orDash(r.Addr))
	if r.VerifyPayload {
		b.WriteString("    载荷全量复算：已开启\n")
	}
	for _, c := range r.Checks {
		icon := "✅"
		switch c.Status {
		case doctorWarn:
			icon = "⚠️"
		case doctorFail:
			icon = "❌"
		}
		fmt.Fprintf(&b, "  %s %s — %s\n", icon, c.Name, config.DisplayHome(c.Detail))
	}
	if r.Passed {
		fmt.Fprintf(&b, "\n==> 通过：0 项失败，%d 项警告\n", r.Warnings)
	} else {
		fmt.Fprintf(&b, "\n==> 失败：%d 项不符（另有 %d 项警告）\n", r.Failed, r.Warnings)
	}
	return b.String()
}

// checkHomeWritable 数据目录可写性：建目录 → 写探针 → **读回来** → 删掉。
//
// 为什么要读回来：只写不读会被"只读挂载上恰好能创建"（某些网络文件系统的写缓存）与
// "写成功了但内容落不下去"两类情况骗过；读回来是这笔账里最便宜的一次实证。
// 探针名带 pid：并发/中断留下的残file不会互相误伤，且一眼能看出是谁的。
func checkHomeWritable(home string) doctorCheck {
	if strings.TrimSpace(home) == "" {
		return doctorCheck{"home", doctorFail, "数据目录为空（配置没解析出 MATE_HOME / 默认 ~/.ark-mate）"}
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return doctorCheck{"home", doctorFail, fmt.Sprintf("%s 不可建/不可进入：%v", home, err)}
	}
	probe := filepath.Join(home, fmt.Sprintf(".mate-doctor-%d", os.Getpid()))
	if err := os.WriteFile(probe, []byte("mate-doctor"), 0o600); err != nil {
		return doctorCheck{"home", doctorFail, fmt.Sprintf("%s 不可写：%v", home, err)}
	}
	defer func() { _ = os.Remove(probe) }()
	if b, err := os.ReadFile(probe); err != nil || string(b) != "mate-doctor" {
		return doctorCheck{"home", doctorFail, fmt.Sprintf("%s 写入后读不回（挂载只读或写入未落盘）：%v", home, err)}
	}
	return doctorCheck{"home", doctorOK, fmt.Sprintf("%s 可写（探针已清理）", home)}
}

// checkPortBindable 端口占用检查：**尝试绑定再立刻释放**。
//
// 为什么"被占用"只记警告，不判失败：桌面态的常态就是"壳与核心正在跑"，这时端口当然被占。
// 判失败会让这条命令在**最需要它的时候**（应用正在出问题、用户开着它跑体检）永远红。
// 所以这里只如实区分两种占用：本平台核心已在响应（`/health` 通），与别的进程占着——
// 前者是人话解释，后者才是要动手处理的事，两者都不该被吞掉。
//
// TOCTOU 说明：绑定后立刻释放，检查与真正监听之间当然有窗口。体检不是互斥锁；
// 真正的判据是 serve 自己的监听失败（它会给明确错误）。
func checkPortBindable(addr string) doctorCheck {
	if strings.TrimSpace(addr) == "" {
		return doctorCheck{"port", doctorWarn, "未配置监听地址，跳过端口检查"}
	}
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		_ = ln.Close()
		return doctorCheck{"port", doctorOK, fmt.Sprintf("%s 可绑定（当前空闲）", addr)}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return doctorCheck{"port", doctorFail, fmt.Sprintf("%s 无法绑定：%v", addr, err)}
	}
	if healthReachable(addr) {
		return doctorCheck{"port", doctorWarn,
			fmt.Sprintf("%s 已被占用，且 /v1/mate/health 有响应——本平台核心很可能已在运行", addr)}
	}
	return doctorCheck{"port", doctorWarn,
		fmt.Sprintf("%s 已被占用，但 /v1/mate/health 无响应——占用者是别的进程", addr)}
}

// healthReachable 探一次健康检查（2s 上限：体检不该被一个挂死的端口拖住）。
func healthReachable(addr string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/v1/mate/health")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// loadReleaseContract 读发布契约。显式路径读不到 → 警告（调用方处理）；未给路径时
// 取**核心二进制同目录**那一份：打包态的布局是 `<Resources>/{mate-core,release.json}`，
// 所以"我是谁"由可执行文件的位置回答，而开发态那里本来就没有这个文件（不判失败）。
func loadReleaseContract(explicit string) (releaseContractDoc, string, bool) {
	path := explicit
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return releaseContractDoc{}, "(可执行文件路径不可得)", false
		}
		path = filepath.Join(filepath.Dir(exe), "release.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return releaseContractDoc{}, path, false
	}
	var doc releaseContractDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		// 解析失败不能静默当成"没有契约"：那是两种完全不同的处境（文件坏了 vs 本来就没有）。
		return releaseContractDoc{}, fmt.Sprintf("%s（解析失败：%v）", path, err), false
	}
	return doc, path, true
}

// scanPayloadManifests 扫描载荷根：`<root>/<name>/<version>/payload.json`（M3-1 的布局）。
//
// 与 JS 侧 `readPayloadManifests` 同一口径（目录名必须与清单里的 name/version 一致），
// 但**不要求**两份必备载荷齐全：doctor 是体检，缺哪份就报哪份（判据在 checkPayloadManifests 里）。
func scanPayloadManifests(root string) ([]payloadOnDisk, error) {
	if root == "" {
		return nil, nil
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("--payloads-dir 必须是绝对路径，得到 %q", root)
	}
	fi, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("--payloads-dir 不可用：%s（%v）", root, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("--payloads-dir 不是目录：%s", root)
	}
	var out []payloadOnDisk
	names, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("读载荷根失败：%v", err)
	}
	for _, nameEnt := range names {
		if !nameEnt.IsDir() {
			continue
		}
		nameDir := filepath.Join(root, nameEnt.Name())
		versions, err := os.ReadDir(nameDir)
		if err != nil {
			return nil, fmt.Errorf("读载荷目录失败 %s：%v", nameDir, err)
		}
		for _, verEnt := range versions {
			if !verEnt.IsDir() {
				continue
			}
			dir := filepath.Join(nameDir, verEnt.Name())
			manifestPath := filepath.Join(dir, "payload.json")
			b, err := os.ReadFile(manifestPath)
			if err != nil {
				return nil, fmt.Errorf("载荷目录 %s 里没有可读的 payload.json（%v）——装配没跑完？", dir, err)
			}
			var doc payloadManifestDoc
			if err := json.Unmarshal(b, &doc); err != nil {
				return nil, fmt.Errorf("%s 不是合法 JSON：%v", manifestPath, err)
			}
			if doc.Name != nameEnt.Name() || doc.Version != verEnt.Name() {
				return nil, fmt.Errorf("%s 的清单与目录不一致：目录是 %s/%s，清单写的是 %s/%s",
					manifestPath, nameEnt.Name(), verEnt.Name(), doc.Name, doc.Version)
			}
			for _, kv := range []struct{ k, v string }{
				{"name", doc.Name}, {"version", doc.Version}, {"digest", doc.Digest}, {"entry", doc.Entry},
			} {
				if strings.TrimSpace(kv.v) == "" {
					return nil, fmt.Errorf("%s 的 %s 为空——清单不完整（装配被中断过？）", manifestPath, kv.k)
				}
			}
			out = append(out, payloadOnDisk{Dir: dir, Manifest: doc})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 下没扫到任何 <name>/<version>/payload.json——先跑 `make desktop/payloads`", root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.Name < out[j].Manifest.Name })
	return out, nil
}

// checkPayloadManifests 逐份载荷检查：入口存在、nodeAbi 与契约一致，并按需全量复算。
//
// 入口（entry）存在性是这里最便宜也最值钱的一条：清单说"入口是这一个文件"，盘上没有它，
// 症状是运行期某次 spawn 失败——而那时早已离开体检现场。
func checkPayloadManifests(manifests []payloadOnDisk, contract releaseContractDoc, contractLoaded, verifyPayload bool) []doctorCheck {
	checks := make([]doctorCheck, 0, len(manifests)+1)
	for _, m := range manifests {
		entry := filepath.Join(m.Dir, filepath.FromSlash(m.Manifest.Entry))
		if fi, err := os.Stat(entry); err != nil || fi.IsDir() {
			checks = append(checks, doctorCheck{"payload " + m.Manifest.Name, doctorFail,
				fmt.Sprintf("入口不在：%s（清单 entry=%s，%v）", entry, m.Manifest.Entry, err)})
		} else {
			checks = append(checks, doctorCheck{"payload " + m.Manifest.Name, doctorOK,
				fmt.Sprintf("version=%s platform=%s/%s nodeAbi=%s files=%d bytes=%d digest=%s entry=存在",
					m.Manifest.Version, orDash(m.Manifest.Platform), orDash(m.Manifest.Arch),
					orDash(m.Manifest.NodeABI), m.Manifest.Files, m.Manifest.Bytes, m.Manifest.Digest)})
		}
		if !contractLoaded {
			// 没有发布契约时 ABI 没有可比对象，但**全量复算照样要跑**：它比的是"盘上文件 ↔ 载荷清单"，
			// 与 release.json 无关（早先把这两件事写在同一个 continue 后面，开发态就永远不跑复算了）。
			if verifyPayload {
				checks = append(checks, verifyPayloadTree(m.Dir, m.Manifest))
			}
			continue
		}
		if m.Manifest.NodeABI != "" && contract.NodeABI != "" && m.Manifest.NodeABI != contract.NodeABI {
			// ABI 不符不是"警告"：原生 .node 到运行期必然崩，且崩在很远的地方。
			checks = append(checks, doctorCheck{"payload " + m.Manifest.Name + " ABI", doctorFail,
				fmt.Sprintf("清单 nodeAbi=%s，发布契约 nodeAbi=%s（载荷是按另一个 Node ABI 装配的）",
					m.Manifest.NodeABI, contract.NodeABI)})
		}
		if verifyPayload {
			checks = append(checks, verifyPayloadTree(m.Dir, m.Manifest))
		}
	}
	return checks
}

// verifyPayloadTree 全量复算一份载荷：逐个文件 sha256 → 与清单 fileList 逐条比 → 合成摘要比对。
//
// 三类差异分开报（缺/改/多）而不是合成一句"不一致"：处置完全不同——缺文件是装配没跑完，
// 改了内容是盘上被动过（或装配期的 bug），多了东西是有人往载荷里塞了文件（**这正是
// 签名/完整性要抓的形态**）。
//
// **fileList 的相对基准**（M4-5 修掉的一个真问题）：fileList 是装配脚本按"复制源根"记的，
// 而不同载荷的落点形态不同（npm 闭包型铺在 `node_modules/` 下、直接铺开的型在载荷根），
// 所以这个基准必须由清单里的 `fileListBase` 说明。缺失时**不静默回落**：只在两个已知形态
// （载荷根 / `node_modules`）里按样例文件做"唯一命中"判定，并在结论里明说是推断的
// ——推断错了会当场表现为摘要不符，而不会是"悄悄按错的基准算出个自洽值"。
func verifyPayloadTree(dir string, m payloadManifestDoc) doctorCheck {
	name := "payload " + m.Name + " 全量复算"

	// 清单侧：`<hex>  <rel>`（两个空格，与 lib/payload.mjs 的写入口径一致）。
	want := make(map[string]string, len(m.FileList))
	for i, line := range m.FileList {
		idx := strings.Index(line, "  ")
		if idx <= 0 {
			return doctorCheck{name, doctorFail, fmt.Sprintf("清单第 %d 行不是 `<hex>  <rel>`：%q", i+1, line)}
		}
		want[line[idx+2:]] = line[:idx]
	}

	base, baseNote, baseErr := resolveFileListBase(dir, m, want)
	if baseErr != nil {
		return doctorCheck{name, doctorFail, baseErr.Error()}
	}
	walkRoot := dir
	if base != "" {
		walkRoot = filepath.Join(dir, filepath.FromSlash(base))
	}

	got := map[string]string{} // rel(POSIX) → hex
	var bytes int64
	walkErr := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(walkRoot, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// 清单是"复制出来的普通文件"的清单（copyTreeFiltered 逐文件 copy）：载荷里出现
		// 符号链接/设备文件就是形态被破坏（软链可以指向包外，等于把只读面变成可变面）。
		if !d.Type().IsRegular() {
			return fmt.Errorf("载荷里出现非普通文件：%s（%s）——装配期已去引用，这里不该有", rel, d.Type())
		}
		// 清单本身不是载荷内容的一部分（写清单发生在复制之后），复算时必须排除，
		// 否则每份载荷的摘要都会"永远对不上"。基准不是载荷根时它不在走查面里，无需判断。
		if base == "" && rel == "payload.json" {
			return nil
		}
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(b)
		got[rel] = hex.EncodeToString(sum[:])
		bytes += int64(len(b))
		return nil
	})
	if walkErr != nil {
		return doctorCheck{name, doctorFail, fmt.Sprintf("复算走查失败：%v", walkErr)}
	}

	// 载荷根下**清单覆盖不到**的条目：基准不是载荷根时，这些内容既不在摘要里、也不会被
	// 上面的差异判定发现（走查根本没到那一层）。它们是随包只读面上的"计划外内容"。
	stray, strayErr := strayPayloadEntries(dir, base)
	if strayErr != nil {
		return doctorCheck{name, doctorFail, fmt.Sprintf("读载荷根失败：%v", strayErr)}
	}

	var missing, changed, extra []string
	for rel, sum := range want {
		gotSum, ok := got[rel]
		switch {
		case !ok:
			missing = append(missing, rel)
		case gotSum != sum:
			changed = append(changed, rel)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			extra = append(extra, rel)
		}
	}
	sort.Strings(missing)
	sort.Strings(changed)
	sort.Strings(extra)
	if len(missing)+len(changed)+len(extra)+len(stray) > 0 {
		msg := fmt.Sprintf("与清单不符：缺 %d（%s）、内容变了 %d（%s）、多出 %d（%s）",
			len(missing), sampleList(missing), len(changed), sampleList(changed), len(extra), sampleList(extra))
		if len(stray) > 0 {
			msg += fmt.Sprintf("、载荷根下有清单之外的条目 %d（%s）", len(stray), sampleList(stray))
		}
		return doctorCheck{name, doctorFail, msg}
	}

	computed := router.CompositeDigest(got)
	if m.Files != len(got) || (m.Bytes != 0 && m.Bytes != bytes) {
		return doctorCheck{name, doctorFail, fmt.Sprintf(
			"文件数/字节数与清单不符：实测 %d 个文件 %d 字节，清单写 %d 个文件 %d 字节",
			len(got), bytes, m.Files, m.Bytes)}
	}
	if computed != m.Digest {
		return doctorCheck{name, doctorFail, fmt.Sprintf("合成摘要不符：实测 %s，清单写 %s", computed, m.Digest)}
	}
	return doctorCheck{name, doctorOK, fmt.Sprintf(
		"%d 个文件 %d 字节逐字复算一致，合成摘要 %s 与清单相同（fileListBase=%s%s）",
		len(got), bytes, computed, quoteBase(base), baseNote)}
}

// quoteBase 把空基准显示成 `载荷根`，避免报告里出现 `fileListBase=`。
func quoteBase(base string) string {
	if base == "" {
		return "载荷根"
	}
	return base
}

// resolveFileListBase 定出 fileList 的相对基准（载荷根下的哪一层）。
//
// 判据顺序（**明确失败优先于推断**）：
//  1. 清单记了 fileListBase → 用它；那一层不存在就当场失败（不回落——路径算错必须响亮）；
//  2. 清单没记（M4-5 之前装配的）→ 只在 {载荷根, node_modules} 两个已知形态里按样例文件
//     做唯一命中判定，并在结论里注明"清单未记录"；
//  3. 两个形态都对不上样例 → 失败并让人重新装配（不猜第三个位置）。
//
// 为什么样例只取前若干个：基准是**结构事实**（整层的归属），不是逐文件属性；用有限样例定出
// 基准后，整棵树的逐文件比对仍会执行——推断错了会以"缺 N / 多 N"的形式当场暴露。
func resolveFileListBase(dir string, m payloadManifestDoc, want map[string]string) (string, string, error) {
	if m.FileListBase != nil {
		clean := path.Clean(strings.TrimSpace(*m.FileListBase))
		if clean == "." || clean == "/" {
			clean = ""
		}
		abs := dir
		if clean != "" {
			abs = filepath.Join(dir, filepath.FromSlash(clean))
		}
		fi, err := os.Stat(abs)
		if err != nil || !fi.IsDir() {
			return "", "", fmt.Errorf("清单的 fileListBase=%q 在载荷里不是目录（%s，%v）：清单与载荷树不是同一次装配的",
				*m.FileListBase, abs, err)
		}
		return clean, "", nil
	}
	if len(want) == 0 {
		return "", "", nil
	}
	// 未记录：按样例做闭合集合的唯一命中判定。
	samples := make([]string, 0, 8)
	for rel := range want {
		samples = append(samples, rel)
	}
	sort.Strings(samples)
	if len(samples) > 8 {
		samples = samples[:8]
	}
	var hits []string
	for _, cand := range []string{"", "node_modules"} {
		ok := true
		for _, rel := range samples {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(cand), filepath.FromSlash(rel))); err != nil {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, cand)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], "（清单未记录 fileListBase，按样例唯一命中判定）", nil
	case 0:
		return "", "", fmt.Errorf("fileList 的相对基准无法判定：清单没记 fileListBase，"+
			"载荷根与 node_modules 下都对不上样例（%s）——重新装配载荷（make desktop/payloads）", sampleList(samples))
	default:
		return "", "", errors.New("fileList 的相对基准有歧义：载荷根与 node_modules 下都能命中样例，" +
			"而清单没记 fileListBase——重新装配载荷（make desktop/payloads）")
	}
}

// strayPayloadEntries 列出载荷根下**清单覆盖不到**的直接子项（基准不是载荷根时）。
//
// 基准是载荷根本身（base == ""）时不需要这条：那时整棵树都在走查面里，多余文件会以"多出"出现。
func strayPayloadEntries(dir, base string) ([]string, error) {
	if base == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	first := strings.SplitN(base, "/", 2)[0]
	var stray []string
	for _, e := range entries {
		if e.Name() == "payload.json" || e.Name() == first {
			continue
		}
		stray = append(stray, e.Name())
	}
	return stray, nil
}

// checkWirePayloads 把壳交来的清单（`/meta` 的 payloads[] 来源）与盘上事实对照。
//
// 为什么这条单独存在：`/meta` 是**诊断面**，它的载荷字段是壳"说"的。壳说 bundled 而盘上
// 没有、壳说的 digest 与清单不同——这两种情况都会让诊断面从此说谎，而"诊断面说谎"正是
// 最难查的一类问题（人会照着它去查错方向）。所以这里判失败。
func checkWirePayloads(wire []ctrsystem.PayloadInfo, manifests []payloadOnDisk, payloadsDir string) doctorCheck {
	onDisk := make(map[string]payloadManifestDoc, len(manifests))
	for _, m := range manifests {
		onDisk[m.Manifest.Name] = m.Manifest
	}
	var problems []string
	summary := make([]string, 0, len(wire))
	for _, p := range wire {
		summary = append(summary, fmt.Sprintf("%s@%s(%s)", p.Name, orDash(p.Version), p.Source))
		if payloadsDir == "" {
			continue
		}
		m, ok := onDisk[p.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s 不在 --payloads-dir 里", p.Name))
			continue
		}
		if p.Version != "" && p.Version != m.Version {
			problems = append(problems, fmt.Sprintf("%s 版本：壳说 %s，盘上是 %s", p.Name, p.Version, m.Version))
		}
		if p.Digest != "" && p.Digest != m.Digest {
			problems = append(problems, fmt.Sprintf("%s 摘要：壳说 %s，盘上是 %s", p.Name, p.Digest, m.Digest))
		}
	}
	if payloadsDir == "" {
		return doctorCheck{"payloads (wire)", doctorWarn,
			fmt.Sprintf("壳交来 %d 条：%s；未给 --payloads-dir，无法与盘上事实对照", len(wire), strings.Join(summary, "、"))}
	}
	if len(problems) > 0 {
		return doctorCheck{"payloads (wire)", doctorFail,
			fmt.Sprintf("壳交来的清单与盘上不一致：%s", strings.Join(problems, "；"))}
	}
	return doctorCheck{"payloads (wire)", doctorOK,
		fmt.Sprintf("壳交来 %d 条与盘上清单逐项一致：%s", len(wire), strings.Join(summary, "、"))}
}

// checkContract 用发布契约（唯一发布契约 release.json）逐项比对**本次进程的实际状态**。
//
// 三条判据的分工：
//   - coreVersion / apiContract：契约说的核心版本，就是**正在跑的这个二进制**报的版本——
//     这条抓"包里混进了另一个核心"（换过 mate-core、打包用了旧构建）；
//   - webBuildId / webDigest：契约说的资产，就是核心**此刻解析到的那份资产**——
//     这条抓"资产被换过/不是打包时那一份"（版本闸门 V8 的同源判据）；
//   - payloads[]：契约说的载荷，就是盘上清单里的那几份（与 checkPayloadManifests 的分工是
//     "契约 ↔ 清单"，那边是"清单 ↔ 文件"）。
//
// `dev` 核心跳过版本比对并**明说**（开发态的核心报 dev 是既有的既定口径，不是缺陷）：
// 把它判成失败会让 `go run ./cmd/mate doctor --release-json …` 在开发态永远红。
func checkContract(contract releaseContractDoc, assets router.AssetInfo, assetsOK bool, manifests []payloadOnDisk, manifestsOK bool) []doctorCheck {
	var checks []doctorCheck

	if version.Version == "dev" {
		checks = append(checks, doctorCheck{"contract 版本", doctorWarn,
			"本次核心是开发构建（版本 dev），跳过与发布契约的 coreVersion 比对"})
	} else if contract.CoreVersion != version.Version {
		checks = append(checks, doctorCheck{"contract 版本", doctorFail,
			fmt.Sprintf("契约 coreVersion=%s，本二进制报 %s（包里混进了另一个核心？）", contract.CoreVersion, version.Version)})
	} else {
		checks = append(checks, doctorCheck{"contract 版本", doctorOK,
			fmt.Sprintf("coreVersion=%s 与本二进制一致，apiContract=%s", contract.CoreVersion, contract.APIContract)})
	}
	if contract.APIContract != "" && contract.APIContract != version.APIContract {
		checks = append(checks, doctorCheck{"contract 契约代数", doctorFail,
			fmt.Sprintf("契约 apiContract=%s，本二进制报 %s", contract.APIContract, version.APIContract)})
	}

	if assetsOK {
		if contract.WebBuildID != assets.CurrentBuildID() {
			checks = append(checks, doctorCheck{"contract 资产构建 id", doctorFail,
				fmt.Sprintf("契约 webBuildId=%s，核心此刻读到的资产是 %s", contract.WebBuildID, orDash(assets.CurrentBuildID()))})
		} else if contract.WebDigest != "" && contract.WebDigest != assets.Digest {
			checks = append(checks, doctorCheck{"contract 资产摘要", doctorFail,
				fmt.Sprintf("契约 webDigest=%s，核心解析到的资产摘要=%s", contract.WebDigest, orDash(assets.Digest))})
		} else {
			checks = append(checks, doctorCheck{"contract 资产", doctorOK,
				fmt.Sprintf("webBuildId=%s / webDigest=%s 与核心实际服务的资产一致", contract.WebBuildID, orDash(assets.Digest))})
		}
	}

	if manifestsOK {
		want := make([]string, 0, len(contract.Payloads))
		for _, p := range contract.Payloads {
			want = append(want, fmt.Sprintf("%s@%s %s", p.Name, p.Version, p.Digest))
		}
		got := make([]string, 0, len(manifests))
		for _, m := range manifests {
			got = append(got, fmt.Sprintf("%s@%s %s", m.Manifest.Name, m.Manifest.Version, m.Manifest.Digest))
		}
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, " | ") != strings.Join(got, " | ") {
			checks = append(checks, doctorCheck{"contract 载荷清单", doctorFail,
				fmt.Sprintf("契约与盘上不一致：契约 [%s]，盘上 [%s]", strings.Join(want, "; "), strings.Join(got, "; "))})
		} else {
			checks = append(checks, doctorCheck{"contract 载荷清单", doctorOK,
				fmt.Sprintf("%d 份载荷的 name/version/digest 与契约逐项一致", len(got))})
		}
	}

	if contract.Electron != "" || contract.Node != "" {
		checks = append(checks, doctorCheck{"contract 运行时", doctorOK,
			fmt.Sprintf("electron=%s node=%s nodeAbi=%s signing=%s notarized=%t channel=%s macUpdate=%s",
				orDash(contract.Electron), orDash(contract.Node), orDash(contract.NodeABI),
				orDash(contract.Signing), contract.Notarized, orDash(contract.Channel), orDash(contract.MacUpdate))})
	}
	return checks
}

// orDash 空值显示成 `-`：报告里"字段是空的"和"字段是 -"不该长得一样。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// sampleList 把差异列表摘成一行（最多 5 条 + 总数）：报告要能一眼读完，
// 完整清单属于"跑一次单测/自己复算"的范畴。
func sampleList(items []string) string {
	const max = 5
	if len(items) == 0 {
		return "-"
	}
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s … 共 %d 条", strings.Join(items[:max], ", "), len(items))
}
