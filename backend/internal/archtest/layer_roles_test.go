package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ——— 顶层目录角色判据（P0，2026-10-21）———
//
// 口径：`.dsh/docs/specs/2026-10-21-backend-layer-roles-design.md` §2.2 的角色卡。
// 层号仍由 `layers_v32_test.go` 的 `v32Layers` 判定；本文件管的是**语义角色**：
// 同样落在合法方向的边上，也可能是不该存在的边（`core → infra` 就是典型——
// L1 同带允许，但"最内层的纯词汇"认识"外部协议"是角色错误）。
// 两层判据彼此不替代：层表管方向，本文件管角色。
//
// 四条判据：
//
//	G1 TestCoreStaysFreeOfInfra        core 不得 import infra/*
//	G2 TestDomainAvoidsInfraProtocol   domain 不得 import infra 的协议/进程/句柄子包（已清零=硬判据）
//	G3 TestForeignSDKStaysInInfra      第三方协议与驱动 SDK 只许出现在 infra（显式登记例外）
//	G4 TestTopLevelDirsHaveRoles       每个顶层目录必须在角色表里登记，且其包落在声明的层带
//
// G1/G2 现在是**双向登记的过渡态**：现状确有违规（见下表），先把它们逐条登记，
// 既挡"新增一条"（转红），也挡"改完了没删登记"（转红）。表清空之日即判据转硬之日，
// 这一步由设计文档 §3 的 P1/P3 完成，不需要再改本文件的结构。

// ——— G1：core 不得 import infra ———

// coreInfraImports 是**过渡期登记表**：`core` 下仍在 import `infra/*` 的包（键=core 子包相对路径，
// 值=它 import 的 infra 子包与原因）。**P1 已把它清零**：三个包原先只为取通用错误分类学
// （`Error`/`Class*`/`NewError`/`As`）而 import `infra/acp`，那套词汇现已归 `core/failure`。
// 表结构保留为空——它现在是**硬判据**：`core` 下任何一个包 import `infra/*` 即红。
var coreInfraImports = map[string]string{}

func TestCoreStaysFreeOfInfra(t *testing.T) {
	pkgs := scanProductionPackages(t)
	got := map[string]string{} // core 子包 → 违规的 infra import
	scanned := 0
	for _, p := range pkgs {
		if !strings.HasPrefix(p.rel, "internal/core") {
			continue
		}
		scanned++
		for _, imp := range p.imports {
			rel := importPathOf(imp)
			if rel == "" || !strings.HasPrefix(rel, "internal/infra") {
				continue
			}
			got[p.rel] = rel
		}
	}
	if scanned == 0 {
		t.Fatal("internal/core 下扫到 0 个生产包：判据失锚（空转）")
	}

	for rel, why := range got {
		if _, ok := coreInfraImports[rel]; ok {
			continue
		}
		t.Errorf("%s import 了 %s：`core` 是跨域借用的**纯词汇**（值对象 / 错误码 / 事件总线 / 身份常量），"+
			"不认识 `infra` 的任何实现。需要什么就往 `core` 下沉纯值，或把该逻辑挪到调用方。\n"+
			"（若这是过渡态：登记进 coreInfraImports 并写明原因——但登记是欠账，不是许可。）", rel, why)
	}
	for rel, why := range coreInfraImports {
		if _, ok := got[rel]; !ok {
			t.Errorf("coreInfraImports 登记了 %s（%s），但它现在已不 import infra：请删掉这条登记"+
				"（双向判据：降到 0 的例外不删也判红）。", rel, why)
		}
	}
}

// ——— G2：domain 不得 import infra 的协议/进程/句柄子包 ———

// domainInfraAllow 是 domain 唯一允许依赖的 infra 子包：日志出口是横切关注点，
// 让它走"每层自造一个 logger 端口"只会换来七份实现。
var domainInfraAllow = []string{"internal/infra/logging"}

// domainInfraImports 过渡期登记表：域包 → 它 import 的 infra 子包（原因见设计文档 §1.4）。
//
// **已清零（2026-10-22，W3）**：最后一条 `domain/mcp → infra/protocol/mcp` 随 A2 端口化消失
// ——域改声明 `LiveClient` 窄端口，`infra/protocol/mcp` 的结果值搬进 `core/protocol`，
// 实现体在装配面（`drivers/registry`）注入。表结构保留为空，它现在是**硬判据**：
// `domain` 下任何一个包 import `infra/*`（`infra/logging` 除外）即红。
//
// 收口沿革：P1 归位错误分类学（`core/failure`）→ P3-a/P3-b 载荷换 `core/protocol` 平台值
// → P3-c 连接与会话句柄换 `protocol.Conn`/`protocol.Session` → A2 MCP 客户端端口化。
var domainInfraImports = map[string]string{}

func TestDomainAvoidsInfraProtocol(t *testing.T) {
	pkgs := scanProductionPackages(t)
	got := map[string]string{}
	scanned := 0
	for _, p := range pkgs {
		if !strings.HasPrefix(p.rel, "internal/domain") {
			continue
		}
		scanned++
		var hits []string
		for _, imp := range p.imports {
			rel := importPathOf(imp)
			if rel == "" || !strings.HasPrefix(rel, "internal/infra") {
				continue
			}
			if isUnderAny(rel, domainInfraAllow) {
				continue
			}
			hits = append(hits, rel)
		}
		if len(hits) > 0 {
			sort.Strings(hits)
			got[p.rel] = strings.Join(hits, "、")
		}
	}
	if scanned == 0 {
		t.Fatal("internal/domain 下扫到 0 个生产包：判据失锚（空转）")
	}

	for rel, why := range got {
		if _, ok := domainInfraImports[rel]; ok {
			continue
		}
		t.Errorf("%s import 了 %s：`domain` 是**业务规则与状态**，不认识对方协议、不 spawn 进程、"+
			"不开数据库句柄。协议载荷请走平台值（`runtimeconn` 负责翻译），宿主能力放到 `infra`。"+
			"\n（允许的唯一例外是 `infra/logging`：日志是横切关注点。）", rel, why)
	}
	for rel, why := range domainInfraImports {
		if _, ok := got[rel]; !ok {
			t.Errorf("domainInfraImports 登记了 %s（%s），但它现在已不 import infra 协议包：请删掉这条登记"+
				"（双向判据）。", rel, why)
		}
	}
}

// ——— G3：第三方协议/驱动 SDK 只许在 infra ———

// foreignSDK 一条"外部世界"依赖：sdk 是 import 前缀，allow 是允许出现它的顶层目录。
// 判据是**今天零越界**——它不是过渡态，任何一层想用协议 SDK 都必须同时改这张表，
// 而改表要在 review 里给出理由。
var foreignSDKs = []struct {
	sdk   string
	allow []string
}{
	{"github.com/coder/acp-go-sdk", []string{"internal/infra"}},
	{"github.com/coder/websocket", []string{"internal/infra", "internal/delivery"}},
	{"github.com/gin-gonic/gin", []string{"internal/delivery"}},
	{"gorm.io/gorm", []string{"internal/infra", "internal/persistence"}},
	{"database/sql", []string{"internal/infra", "internal/persistence"}},
}

func TestForeignSDKStaysInInfra(t *testing.T) {
	pkgs := scanProductionPackages(t)
	seen := make(map[string]bool, len(foreignSDKs))
	for _, s := range foreignSDKs {
		for _, p := range pkgs {
			for _, imp := range p.imports {
				if imp != s.sdk && !strings.HasPrefix(imp, s.sdk+"/") {
					continue
				}
				seen[s.sdk] = true
				if !isUnderAny(p.rel, s.allow) {
					t.Errorf("%s import 了 %s：外部协议与驱动 SDK 只许出现在 %s。"+
						"它意味着「这层在跟外面说话」，而那是 `infra` 的角色；"+
						"业务层要用的是平台值，不是对方协议的线类型。",
						p.rel, imp, strings.Join(s.allow, " 或 "))
				}
			}
		}
		if !seen[s.sdk] {
			t.Fatalf("全仓找不到任何 %s 的 import：这条判据失去了检查对象"+
				"（依赖改名/换库时请同步 foreignSDKs，fail-closed）", s.sdk)
		}
	}
}

// ——— G4：每个顶层目录一个角色 ———

// topLevelRole 一个顶层目录的角色卡：一句话角色 + 它的生产包允许落在哪些层带。
// bands 为 nil 表示"不入层表"（测试支撑类）。
type topLevelRole struct {
	role  string
	bands []int
	// bandExceptions 角色带内的**具名例外**：包相对路径 → 它实际所在的带。
	//
	// 为什么需要它（2026-10-22，W3）：`infra` 收成单层带（L5）后仍有一个包必须停在 L1——
	// `infra/logging` 是**横切关注点**：每一层都要能拿 logger，而"每层自造一个 logger 端口"
	// 只会换来七份实现（G2 的白名单也据此只放它一个）。写成 {1,5} 会让"infra 到底在第几层"
	// 重新变成一个没人能回答的问题；写成"L5 + 具名例外"则让例外**可数、可审**：往 L1 再塞一个
	// infra 子包不会静默通过，而要在这里加一行并在 review 里说明。
	//
	// 双向：例外登记了但那个包已不存在/已不在那一带 → 红（清单不许腐烂）。
	bandExceptions map[string]int
}

// topLevelRoles 顶层目录角色表（与设计文档 §2.2 逐行对应）。
//
// 新增一个顶层目录 = 必须先在这里登记角色与允许的层带；没登记的目录当场红。
// 这条判据挡的是"新人随手建一个目录，然后把包塞进去，没人问它属于哪一类"——
// 104 个包的成本主要就来自这种没人裁定的边界。
var topLevelRoles = map[string]topLevelRole{
	"core":   {role: "跨域借用的纯词汇（值对象 / 错误码与分类 / 事件总线 / 身份常量）", bands: []int{1}},
	"domain": {role: "业务规则与状态（实体 / 状态机 / 域端口）", bands: []int{2}},
	// `infra` 现在是**单层带**（2026-10-22，W3）：最后一条把它撑成跨带的是 `infra/protocol/mcp`
	// 停在 L1（`domain/mcp` 直连 MCP 客户端）。A2 端口化把那份依赖换成 `core/protocol` 平台值
	// + `LiveClient` 窄端口之后，协议绑定一律是 L5。带号由 `{1, 5}` 改小成 `{5}` 是**收紧**：
	// 从此 `core`/`domain` 再往 `infra` 伸手会同时撞上 G1/G2 与这张卡。
	//
	// 唯一的具名例外是 `infra/logging`（L1，横切日志出口，见 bandExceptions 的说明）。
	"infra": {role: "跟外面说话的实现（协议客户端 / 进程 / DB 句柄 / 日志出口）", bands: []int{5},
		bandExceptions: map[string]int{"internal/infra/logging": 1}},
	"dto":            {role: "对外契约形状（REST / WS / CLI 共用同一份）", bands: []int{3}},
	"service":        {role: "用例编排（校验 / 错误码 / 响应装配）", bands: []int{4}},
	"persistence":    {role: "表形状（model）+ 机械原语（dao）+ 读面与事务（store）", bands: []int{0, 5}},
	"runtime":        {role: "底座契约、注册表与隔离投影", bands: []int{1}},
	"drivers":        {role: "接线：域端口 ↔ 适配体、连接面、导入导出编排", bands: []int{6}},
	"sessionruntime": {role: "运行期编排（会话执行 / 权限 / 断连语义）", bands: []int{7}},
	"delivery":       {role: "入站适配（HTTP / WS 参数解析与响应）", bands: []int{8, 9}},
	"bootstrap":      {role: "组合根与生命周期（装配 / CLI / 维护命令）", bands: []int{9}},
	"config":         {role: "进程配置（加载 / 默认值 / 校验）", bands: []int{6}},
	"version":        {role: "构建号", bands: []int{1}},
	"archtest":       {role: "架构判据（只读源码，不进产品二进制）"},
	"testutil":       {role: "测试支撑（夹具与桩）"},
}

func TestTopLevelDirsHaveRoles(t *testing.T) {
	root := backendRoot(t)
	base := filepath.Join(root, "internal")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("读 %s 失败：%v（判据失锚）", base, err)
	}

	pkgs := scanProductionPackages(t)
	byTop := map[string][]string{} // 顶层目录 → 它的生产包
	for _, p := range pkgs {
		rest := strings.TrimPrefix(p.rel, "internal/")
		top := strings.SplitN(rest, "/", 2)[0]
		byTop[top] = append(byTop[top], p.rel)
	}
	if len(byTop) == 0 {
		t.Fatal("internal/ 下扫到 0 个生产包：判据失锚（空转）")
	}

	// ① 磁盘上的每个顶层目录都必须登记（含尚无生产包的空目录，防"先建目录后塞包"）。
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := topLevelRoles[e.Name()]; !ok {
			t.Errorf("internal/%s 没有角色登记：新增顶层目录必须先回答"+
				"「它会跟外面说话吗 / 它会改业务状态吗 / 它是被 ≥2 个域借用的纯词汇吗」，"+
				"并写进 topLevelRoles 与设计文档 §2.2。", e.Name())
		}
	}
	// ② 登记过的目录必须真的存在（防搬迁后留下幽灵条目）。
	for name := range topLevelRoles {
		if _, err := os.Stat(filepath.Join(base, name)); err != nil {
			t.Errorf("topLevelRoles 登记了 internal/%s，但目录不存在：请删掉这条登记"+
				"（双向判据，防清单腐烂）。", name)
		}
	}
	// ③ 每个生产包必须落在该目录声明的层带内（具名例外按例外那一带判）。
	checked := 0
	usedExceptions := map[string]bool{}
	for name, role := range topLevelRoles {
		for _, rel := range byTop[name] {
			_, layer, ok := v32Resolve(rel)
			if role.bands == nil {
				if ok {
					t.Errorf("%s 声明为「%s」（不入层表），但层表给了它 L%d：两侧口径打架。",
						name, role.role, layer.layer)
				}
				continue
			}
			if !ok {
				t.Errorf("%s 是「%s」，但层表里没有它的条目：先把它登记进 v32Layers。", rel, role.role)
				continue
			}
			checked++
			bands := role.bands
			if want, isExc := role.bandExceptions[rel]; isExc {
				usedExceptions[rel] = true
				bands = []int{want}
			}
			if !containsInt(bands, layer.layer) {
				t.Errorf("%s 落在 L%d，但 internal/%s 的角色「%s」只允许 %v —— "+
					"一个顶层目录跨带越多，新人越无法从目录名推出层号（`infra` 曾经同时是 L1 与 L5，"+
					"于是 core 与 domain 都有理由往里伸手）。", rel, layer.layer, name, role.role, role.bands)
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个包都没校验到层带：判据失锚（空转）")
	}
	// ⑤ 具名例外必须真的用上（双向：包被删/被搬到别的带 → 红，清单不许腐烂）。
	for name, role := range topLevelRoles {
		for rel, want := range role.bandExceptions {
			if !usedExceptions[rel] {
				t.Errorf("%s 的 bandExceptions 登记了 %s（L%d），但它已不在 internal/%s 的生产包里："+
					"请删掉这条例外（双向判据，防清单腐烂）。", name, rel, want, name)
			}
		}
	}
}

func isUnderAny(rel string, prefixes []string) bool {
	for _, p := range prefixes {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
