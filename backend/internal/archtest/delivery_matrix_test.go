package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/object/objdelivery"
)

// ——— 第 22 条守卫：交付矩阵完整性（每个资源 kind 都必须有交付通道声明） ———
//
// 为什么需要它：平台管着好几种资源（provider 凭据 / 模型清单 / skill / mcp / agent /
// automation），而它们在**底座侧生效的方式互不替代**——密钥走子进程 env、会话级模型走会话
// 配置项、模型清单整份写配置文件、skill 物化到读根、MCP 注入会话能力集。这张「资源 → 交付
// 通道 + 生效时刻」的表是静态事实表（`pkg/object/objdelivery`），**通道按资源种类固定**。
//
// 它挡的是一种**静默缺口**：新增一种资源（比如再加一类可投给底座的定义）却忘了给它声明交付
// 通道。那种缺口不会让任何东西报错——写面照常成功、界面照常显示「已保存」，而用户永远等不到
// 它生效；排查时也会被读成"底座不认"或"投放写错了"。所以判据必须独立于实现：本测试从
// `internal/model/enum.go` 的 `DefinitionKind*` 常量（「平台拥有哪些资源种类」的唯一事实源）
// 读出全部 kind，逐个断言 `objdelivery.For` 查得到声明；并断言**未知 kind 必须返回 false**，
// 把这个查询本身也钉死（一条永远返回 true 的假实现不能蒙混过去）。
//
// 故意的红：往 `pkg/object/objdelivery` 的表里删掉任意一个 kind 的声明 →
// `TestDeliveryMatrixCoversKinds` 必须红（`missingDeliveryKinds` 的负例分支就是它的自证）。

// capabilityKindConstants 从 internal/model/enum.go 读出 `DefinitionKind*` 常量（常量名 → 取值）。
//
// 用 go/parser 而不是正则：常量块、多行声明、注释里的同名文字都不该被误读。读不到任何
// `DefinitionKind*` 常量（文件被改名/常量被整体搬走）时直接用 t.Fatalf 判红——守卫的前提
// 消失时必须 **fail-closed**，否则它会变成一条永远绿的装饰。
func capabilityKindConstants(t *testing.T) map[string]string {
	t.Helper()
	root := backendRoot(t)
	path := filepath.Join(root, "internal", "model", "enum.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败（交付矩阵守卫的事实源读不到）: %v", rel(root, path), err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "DefinitionKind") {
					continue
				}
				if i >= len(vs.Values) {
					t.Errorf("%s: %s 没有显式取值（常量块里省略了取值）——守卫读不出它，"+
						"请写成显式字符串字面量", rel(root, path), name.Name)
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s: %s 的取值不是字符串字面量——守卫读不出来",
						rel(root, path), name.Name)
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: %s 的取值无法解析: %v", rel(root, path), name.Name, err)
					continue
				}
				out[name.Name] = v
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s 里没有 DefinitionKind* 常量：交付矩阵守卫的事实源已消失，"+
			"守卫必须随之更新而不能默认放行", rel(root, path))
	}
	return out
}

// missingDeliveryKinds 返回「没有交付声明的 kind」的违规描述（空 = 全部合规）。
//
// 抽成纯函数是为了能对**合成**输入自测：直接跑真实常量的话，"删掉一条声明会不会红"无法验证，
// 而一条不会红的守卫等于没有守卫（与 `capabilityScopeViolations` 同一理由）。
func missingDeliveryKinds(kinds map[string]string) []string {
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		kind := kinds[name]
		d, ok := objdelivery.For(kind)
		if !ok {
			out = append(out, name+"=\""+kind+"\" 在 pkg/object/objdelivery 里没有交付声明："+
				"新增资源种类时必须同时声明它走哪条通道、什么时候生效")
			continue
		}
		if d.Channel != objdelivery.ChannelNone && !objdelivery.IsDeliveryChannel(d.Channel) {
			out = append(out, name+"=\""+kind+"\" 声明的通道 \""+d.Channel+"\" 不在交付通道词表内"+
				"（也不是 \""+objdelivery.ChannelNone+"\"）")
		}
		if d.Note == "" {
			out = append(out, name+"=\""+kind+"\" 的交付声明没有可执行的人话 Note")
		}
	}
	return out
}

// TestDeliveryMatrixCoversKinds 真实 kind 集合必须全部有声明，且守卫本身有牙齿
// （合成一个未声明的 kind 必须被抓到）。
func TestDeliveryMatrixCoversKinds(t *testing.T) {
	kinds := capabilityKindConstants(t)

	// 1) 正向：平台拥有的每一种资源都必须查得到交付声明。
	if bad := missingDeliveryKinds(kinds); len(bad) > 0 {
		t.Fatalf("交付矩阵有缺口（新增资源种类却漏了交付通道声明）：\n%s", joinLines(bad))
	}
	// 2) 负例（**故意的红**）：删掉任意一条声明都必须被这条路径抓到。
	if bad := missingDeliveryKinds(map[string]string{"DefinitionKindX": "__undeclared_kind__"}); len(bad) == 0 {
		t.Fatal("未声明的 kind 应被判违规（守卫没牙齿）")
	}
	// 3) 未知 kind 必须返回 false：调用方据此把缺口报出来，而不是拿零值 Delivery 渲染出一条
	//    看起来合法的空通道。
	if d, ok := objdelivery.For("__undeclared_kind__"); ok {
		t.Fatalf("For 对未声明的 kind 返回了声明（%+v）：它必须是 false", d)
	}
	// 4) 表与索引自洽：Kinds() 报出的每个 kind 都要能查到（抓大小写/拼写漂移）。
	for _, kind := range objdelivery.Kinds() {
		if _, ok := objdelivery.For(kind); !ok {
			t.Errorf("Kinds() 报出 %q 但 For 查不到（表与索引漂移）", kind)
		}
	}
	// 5) M6 的具体承诺：模型清单走 T5（写配置文件、进程级生效）。
	if d, ok := objdelivery.For(objdelivery.KindModel); !ok {
		t.Error("model 必须有交付声明")
	} else if d.Channel != objdelivery.ChannelConfigWrite || d.Effect != objdelivery.EffectNextProcess {
		t.Errorf("model 的交付形态应为 %s/%s，得到 %s/%s",
			objdelivery.ChannelConfigWrite, objdelivery.EffectNextProcess, d.Channel, d.Effect)
	}
}
