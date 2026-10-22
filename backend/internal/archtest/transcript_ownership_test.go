package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ——— P3b-2b「transcript 归会话域」守卫 ———
//
// transcript（会话 wire JSONL 旁路）在 P3b-2b 之前是**跨层**的：recorder 本体在
// `internal/sessionruntime`，域只经一个临时端口 `TranscriptSink` 往它写一行；装载、检索、handoff
// 摘要又各自散在 manager 的几个文件里。搬完之后它们全在 `internal/domain/session`，
// 端口的唯一存在理由（"另外那半还在 manager"）也随之消失。
//
// 这种"半搬"的失效方式是**静默**的，而且会重演两次：
//
//	① 谁在别处又需要"往 transcript 写一行"，就会顺手在那边再放一个 recorder/glob——
//	   两份实现立刻分叉（一份写了 header、另一份没写；一份按会话分组装载、另一份按文件名），
//	   而两边都能编译、测试也大多绿（本仓 2026-10-14 那条「dist 重建了、GET / 还是旧产物」
//	   就是这类"两条路径都自洽"的家族）；
//	② 更常见的是**装配漏接**：`SetTranscriptDir` 忘了调（或调在 `NewService` 之前），
//	   recorder 恒 nil ⇒ transcript 一行不写，而进程启动正常、会话正常、日志干净——只有用户
//	   有一天发现"重启后会话不见了"才现形（这正是 §3.1.46 的第三种错）。
//
// 判据分两半，都是静态可判、fail-closed：
//
//	TestTranscriptStateLivesInSessionPlane —— 域的边界：recorder 的类型与 wire schema 字面量
//	   只能在 `internal/domain/session` 出现，**manager 的生产文件里一个都不能有**；
//	TestTranscriptWiringIsAttached     —— 装配面：`SetTranscriptDir` 的两个消费方
//	   （serve / demo）都真的调了它，而且调用点必须在 `NewService` **之后**（早一行 = 域还没造出来）。
//
// 反证（实跑，见计划 §3.1.47）：把 recorder 的 `wireTranscriptSchema` 常量复制到
// `internal/sessionruntime` 的一个新生产文件里 → 红；把 `internal/bootstrap/app/app.go` 里的
// `a.svc.SetTranscriptDir(cfg.TranscriptDir)` 删掉 → 红。

// transcriptOwnerDir 是 transcript 面**唯一**允许的归属包（相对仓库根的斜杠路径）。
const transcriptOwnerDir = "internal/domain/session"

// transcriptOwnerLiterals 是"transcript 面的内部形状"——出现即意味着有人在那处重新实现了它。
//
// 每一个都是**格式身份**（不是文件名）：wire schema 名写错一个字符，写出去的文件就读不回来；
// 而 `.jsonl` 是文件命名约定——它是 recorder、`mate transcripts prune`、`mate migrate`、
// `mate doctor` 与 `serve` 的空库提示共同依赖的那一件事，**只有 recorder 那一份**不许在
// 别处重新拼（装配与 CLI 命令按目录扫文件是允许的，见下面的 scanFaces）。
var transcriptOwnerLiterals = []string{
	"wire-transcript",    // wire schema 名（v2 与 v1 两代都在这个名字里）
	"transcriptRecorder", // recorder 类型名：两份 recorder 就是两份实现
}

// transcriptNonOwnerRoots 是"**绝不能**认识 transcript 内部形状"的包（生产文件；测试文件
// 需要按文件名造夹具，`_test.go` 一律跳过）。
//
// 为什么点这两个：`internal/sessionruntime` 是拆分中的实现层、`internal/service/svc*` 是用例端口层
// ——两处都只该经会话域的动作面碰 transcript。CLI（`internal/bootstrap/cli` 命令树 +
// `internal/bootstrap/cliops` 命令实现）与装配根（`internal/bootstrap/assembly`）**刻意不在
// 名单里**：它们的职责就是"按目录扫文件"（`mate migrate` /
// `transcripts prune` / `doctor` / serve 的空库提示），ban 了它们等于让守卫与产品行为对打。
var transcriptNonOwnerRoots = []string{
	filepath.Join("internal", "sessionruntime"),
	filepath.Join("internal", "service"),
}

// TestTranscriptStateLivesInSessionPlane transcript 的 recorder 类型与 wire schema 字面量
// 只许出现在 `internal/domain/session`；manager 与 svc* 的生产文件里零命中。
//
// fail-closed 两侧：扫描面为空 → 失败（目录搬走/改名后这条守卫会静默空转）；
// 归属包本身扫不到 schema 字面量 → 也失败（说明常量被改名或搬走了，判据失去对象）。
func TestTranscriptStateLivesInSessionPlane(t *testing.T) {
	root := backendRoot(t)

	// ① 归属包必须真的有这些字面量（否则判据挂空）。
	ownerDir := filepath.Join(root, filepath.FromSlash(transcriptOwnerDir))
	ownerFiles := productionGoFiles(t, ownerDir)
	if len(ownerFiles) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：transcript 归属守卫失去了归属方（目录改名/搬走了？）",
			transcriptOwnerDir)
	}
	ownerHits := 0
	for _, path := range ownerFiles {
		ownerHits += countLiterals(t, path, transcriptOwnerLiterals)
	}
	if ownerHits == 0 {
		t.Fatalf("%s 下找不到任何 transcript 内部形状字面量（%v）：常量被改名/搬走了？"+
			"这条守卫的判据就挂在它们身上，失锚等于空转", transcriptOwnerDir, transcriptOwnerLiterals)
	}

	// ② 非归属包零命中。
	scanned := 0
	for _, sub := range transcriptNonOwnerRoots {
		dir := filepath.Join(root, sub)
		files := productionGoFiles(t, dir)
		if len(files) == 0 {
			t.Fatalf("扫描面 %s 一个生产 .go 文件都没有：守卫静默空转（目录改名/搬走了？）", sub)
		}
		scanned += len(files)
		for _, path := range files {
			for _, lit := range transcriptOwnerLiterals {
				if strings.Contains(readFileOrFatal(t, path), lit) {
					t.Errorf("%s 里出现 transcript 内部形状 %q：transcript（recorder 类型 + wire schema）"+
						"归 %s 唯一所有（P3b-2b）。在别处再实现一份的后果是**两份实现静默分叉**"+
						"（header 写不写、装载按会话还是按文件），而两边都编译得过；"+
						"要写一行请调会话域的 `Plane.BeginTranscript` / 让它回读。"+
						"\n\t（`internal/bootstrap/cliops` 的 CLI 与 `internal/bootstrap/assembly` 的装配按目录扫文件是有意保留的）",
						rel(root, path), lit, transcriptOwnerDir)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("transcript 归属守卫没有扫到任何生产文件")
	}
}

// TestTranscriptWiringIsAttached transcript 的**装配链**必须真的接上：两个入口
// （`serve` 与 `demo`）都得把 transcript 目录交给会话域，且调用点在 `NewService` 之后。
//
// 为什么单钉装配而不是行为：`SetTranscriptDir` 忘调的后果是**完全静默**的（进程正常、会话
// 正常、日志干净，只是重启后会话不见了），而 `internal/bootstrap/app` 那几条用例只跑启动与查询，
// 不会因为"transcript 一行没写"而红——§3.1.46 的第三种错（"编译通过 ≠ 装配接上"）在这里
// 有第二次现形的机会。判据是调用点字面量：这一层没有运行时信号，只有"这一行还在不在"。
//
// fail-closed：两个文件里任何一个找不到调用点即失败（改名/搬迁让它失锚）。
func TestTranscriptWiringIsAttached(t *testing.T) {
	root := backendRoot(t)
	cases := []struct {
		file string // 相对 backend/ 的路径
		why  string
	}{
		{"internal/bootstrap/assembly/assembly.go", "serve：sqlite/memory 分流里 memory 那一支把目录交给会话域"},
		{"internal/bootstrap/maintenance/demo.go", "demo：演示会话也落 transcript（wire 事实源）"},
	}
	for _, c := range cases {
		path := filepath.Join(root, filepath.FromSlash(c.file))
		src := readFileOrFatal(t, path)
		if !strings.Contains(src, "SetTranscriptDir(") {
			t.Errorf("%s 里找不到 `SetTranscriptDir(`：%s。\n"+
				"装配漏接的表现是**静默**的——recorder 恒 nil、transcript 一行不写，"+
				"而进程启动、会话、日志全部正常，只有重启后「会话不见了」才现形。", c.file, c.why)
		}
		// 调用点必须在 NewService 之后：早一行时会话域还没造出来（`svc` 是 nil），
		// 而这个错在编译期无法发现（方法挂在 *Service 上，nil 接收者调用要等运行期）。
		newIdx := strings.Index(src, "NewService(")
		setIdx := strings.Index(src, "SetTranscriptDir(")
		if newIdx >= 0 && setIdx >= 0 && setIdx < newIdx {
			t.Errorf("%s 里 `SetTranscriptDir(` 出现在 `NewService(` **之前**：%s。\n"+
				"会话域还没造出来（recorder 无处可挂），而这个错编译期看不出来。", c.file, c.why)
		}
	}
}

// productionGoFiles 列出 dir 下的生产 `.go` 文件（递归；**跳过 `_test.go`** 与 testdata）。
//
// 跳过测试文件是刻意的：manager 的历史用例要按 `<会话>.jsonl` 造夹具并逐字节核对格式
// （`redact_publish_test.go` 那几条是**守卫本身**的一半——它们验的是"落盘的确实是脱敏后的
// 载荷"），把它们算进来只会逼守卫加白名单。
func productionGoFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, path := range goFilesUnder(t, dir) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		out = append(out, path)
	}
	return out
}

// countLiterals 数一个文件（**不剥注释**）里各个字面量的出现次数。
//
// 刻意不剥注释：这两个字面量本身就是"这份文件的形状"，出现在注释里也是同一个信号
// （而 `scanForLiterals` 那一族用剥注释是因为它们的禁词会在注释里被**解释**）。
func countLiterals(t *testing.T, path string, literals []string) int {
	t.Helper()
	src := readFileOrFatal(t, path)
	n := 0
	for _, lit := range literals {
		n += strings.Count(src, lit)
	}
	return n
}
