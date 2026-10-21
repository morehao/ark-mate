package archtest

// 本文件钉住 **开发循环的阶段表**（T3）：`frontend/scripts/dev-stages.json` 是那张表唯一的事实源，
// `dev-loop.mjs` 读它执行，这里读它校验。
//
// 为什么需要机器判据：阶段表的每一条错法都不会让别的测试变红——
//
//	① 少写一个阶段（或表被整个删掉）：`dev-loop.mjs` 照跑，只是那个 watcher 再也不起；
//	② critical 与超时/降级动作脱钩：挂了之后"是拖垮循环还是降级"变成读代码才知道的事；
//	③ 声明表与启动配方各说一套：表里有 `assets`、`recipes` 里没有 ⇒ 运行期才发现（且只在那一刻）；
//	④ 依赖指向后面的阶段：顺序即执行序，指后的依赖等于"这一步要在它依赖的东西之前跑"。
//
// 判据（全部 fail-closed：读不到文件即红，别让改名把这条守卫变成空转）：
//
//	A1 声明表自洽：名字唯一非空 / kind ∈ {once,daemon} / 每步有 timeoutMs>0 /
//	   非 critical 必写 degrade / dependsOn 指向表里更靠前的阶段；
//	A2 两侧同源：`dev-loop.mjs` 必须真的读 `dev-stages.json`（而不是自己另写一份）；
//	A3 声明 ↔ 配方**双向**相等：表里的每个阶段都有启动配方，配方也都登记在表里。
//
// 反证（实跑 3 条，均红）：把 `types` 的 `degrade` 清空；把 `serve` 的 `dependsOn` 改成
// `["serve"]`（指后/自指）；从 `recipes` 里删掉 `assets`。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// devStage 阶段声明的一项（只声明"怎么监督"，命令与参数在 dev-loop.mjs 的 recipes 里）。
type devStage struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Critical   bool     `json:"critical"`
	TimeoutMS  int64    `json:"timeoutMs"`
	DependsOn  []string `json:"dependsOn"`
	Degrade    string   `json:"degrade"`
	ReadyProbe string   `json:"readyProbe"`
}

// TestDevStageTableIsDeclared 见文件头。
func TestDevStageTableIsDeclared(t *testing.T) {
	root := frontendRoot(t)
	tablePath := filepath.Join(root, "scripts", "dev-stages.json")
	raw, err := os.ReadFile(tablePath)
	if err != nil {
		t.Fatalf("读不到 %s：%v（阶段表搬走了？这条守卫不能静默失效）", tablePath, err)
	}
	var table struct {
		Stages []devStage `json:"stages"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("解析 %s 失败: %v", tablePath, err)
	}
	if len(table.Stages) == 0 {
		t.Fatalf("%s 里一个阶段都没有：这张表就是开发循环的唯一事实源", tablePath)
	}

	// —— A1：声明自洽（与后端启动序列的 `assembly.ValidateSteps` 同一套规则）——
	index := make(map[string]int, len(table.Stages))
	for i, st := range table.Stages {
		if strings.TrimSpace(st.Name) == "" {
			t.Fatalf("第 %d 个阶段没有名字", i+1)
		}
		if j, dup := index[st.Name]; dup {
			t.Fatalf("阶段名 %q 重复（第 %d 与第 %d）", st.Name, j+1, i+1)
		}
		index[st.Name] = i
		if st.Kind != "once" && st.Kind != "daemon" {
			t.Errorf("阶段 %q 的 kind=%q 非法（once | daemon）", st.Name, st.Kind)
		}
		if st.TimeoutMS <= 0 {
			t.Errorf("阶段 %q 没有超时：一次构建挂住或一个 watcher 立刻死掉，都会让循环"+
				"停在看起来正常的假活状态（既没产物也没报错）", st.Name)
		}
		if !st.Critical && strings.TrimSpace(st.Degrade) == "" {
			t.Errorf("非 critical 阶段 %q 没有声明降级动作（degrade）：它挂了之后开发循环长什么样，"+
				"只该有一个答案", st.Name)
		}
		if st.ReadyProbe != "" && st.ReadyProbe != "http-health" {
			t.Errorf("阶段 %q 的 readyProbe=%q 非法（http-health）", st.Name, st.ReadyProbe)
		}
	}
	for i, st := range table.Stages {
		for _, dep := range st.DependsOn {
			j, ok := index[dep]
			if !ok {
				t.Errorf("阶段 %q 依赖 %q，但表里没有这一步", st.Name, dep)
				continue
			}
			if j >= i {
				t.Errorf("阶段 %q 依赖 %q，而后者排在它后面（顺序即执行序）", st.Name, dep)
			}
		}
	}

	// —— A2 / A3：两侧同源 + 声明 ↔ 配方双向相等 ——
	loopPath := filepath.Join(root, "scripts", "dev-loop.mjs")
	loopRaw, err := os.ReadFile(loopPath)
	if err != nil {
		t.Fatalf("读不到 %s：%v", loopPath, err)
	}
	code := stripTSComments(string(loopRaw))
	if !strings.Contains(code, "dev-stages.json") {
		t.Errorf("dev-loop.mjs 没有引用 dev-stages.json：开发循环在自己另写一份阶段表（两份就是两个真相）")
	}
	if !strings.Contains(code, "loadStageTable") {
		t.Errorf("dev-loop.mjs 没有调用 loadStageTable：阶段表读进来了但没校验，等于只声明不判据")
	}

	recipes := recipesBlock(t, code)
	recipeRe := regexp.MustCompile(`(?m)^\s{2}([A-Za-z_][A-Za-z0-9_]*)\s*:\s*\(`)
	declared := make(map[string]bool, len(table.Stages))
	for _, st := range table.Stages {
		declared[st.Name] = true
		if !recipeRe.MatchString(recipes) || !strings.Contains(recipes, "\n  "+st.Name+": (") {
			t.Errorf("阶段表声明了 %q，而 dev-loop.mjs 的 recipes 里没有它的启动配方"+
				"（运行到那一步才会发现，且只在那一次）", st.Name)
		}
	}
	for _, m := range recipeRe.FindAllStringSubmatch(recipes, -1) {
		if !declared[m[1]] {
			t.Errorf("dev-loop.mjs 有阶段 %q 的启动配方，但 dev-stages.json 里没有这条声明"+
				"（配方不会被监督：没有 critical/超时/降级分档）", m[1])
		}
	}
}

// recipesBlock 抠出 `const recipes = { … }` 的块（按第一个顶格 `}` 收尾）。
//
// 为什么按这个形状抠而不是解析 JS：这里只判"有没有 `name: (`"这一层，块内没有嵌套的顶格 `}`；
// 抠不到（块被改名/删掉）直接判失败——守卫宁可变红，也不要静默空转。
func recipesBlock(t *testing.T, code string) string {
	t.Helper()
	start := strings.Index(code, "const recipes = {")
	if start < 0 {
		t.Fatalf("dev-loop.mjs 里找不到 `const recipes = {`：配方表被改名/删掉了？")
	}
	rest := code[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("`const recipes = {` 块没有按预期收尾（顶格 `}`），本判据需要跟着改")
	}
	return rest[:end]
}
