package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staticCapsImagesAllowedFiles 允许出现 `.Caps.Images` 的**唯一**地方：静态能力基线的汇入点。
//
// `pkg/runtime/adapters.go` 把各 adapter 自己声明的 `Caps` 值拷进 `Spec.Caps`——那是"声明"，
// 不是判据。declaration 原地（`pkg/runtime/<adapter>/*.go`，例如 `pkg/runtime/dsh/dsh.go`）写的是
// 结构体字面量字段（`Images: false`），不含 `.Caps.Images` 这个取值表达式。
var staticCapsImagesAllowedFiles = map[string]string{
	"pkg/runtime/adapters.go": "把 adapter 的静态基线声明汇入 Spec（值拷贝，不做任何判据）",
}

// connectionFactImages 是**连接期事实**的取值表达式（ACP initialize 广告的能力）。
//
// 判据按这个**符号**收集，不按文件路径：它今天写在 `manager` 包里定义 `promptImageSupported` 的那个
// 文件里，而那个文件会随分层重构搬到 `provider` 域包（设计 §3.4 文件去向表）。路径钉住的
// 判据在那之后只有两种下场——读不到那个文件而红得莫名其妙，或者被"顺手"改成"任一文件存在即可"
// 而在**判据层面**悄悄放宽。按符号扫整棵树则两种下场都没有。
const connectionFactImages = "Capabilities().Images"

// connectionFactImagesConsumers 是 `Capabilities().Images` 在 `internal/` + `pkg/` 的**生产**文件里
// 的**实测**命中数（2026-10-16 P0 实测：
// `grep -rn 'Capabilities()\.Images' internal pkg` 在生产文件里恰好 1 处 =
// `manager` 包里 `promptImageSupported` 的那一行 `v := rc.client.Capabilities().Images`）。
//
// 为什么是"恰好 N"而不是"至少一处"：N 就是这条判据的可观测面。0 处 = 连接期事实的消费者被删掉了
// ——门禁会悄悄退回静态基线，而那正是 ADR-14 要拦的东西；多出来的处 = 有人**又**复制了一份这个
// 取值表达式，那正是"静态基线 / 连接期事实"两条口径开始分叉的地方，值得有人看一眼。
// 合法新增消费者时把它改成实测值，并在提交信息里写明理由（这条常量本身就是那份理由的落点）。
const connectionFactImagesConsumers = 1

// TestStaticCapsImagesHasNoConsumer A11/ADR-14：静态能力基线 `Caps.Images` **不得**有新的读法。
//
// 为什么需要一条机器判据：`dsh.Caps.Images = false` 是"按源码声明的保守基线"，而真值取决于
// 附件仓与**当前路由**（桥在 initialize 里如实广告 `promptCapabilities.image`）。拿静态值当门禁
// = 把保守基线变成错误结论：用户会在一个其实能收图的 dsh 上被告知"不支持"。
//
// 它今天已无出口（`/runtimes` 与它的 `ListItems` 一起删掉了），但"没有出口"这件事**不会**
// 被编译期保证——下一个人写 `if spec.Caps.Images { … }` 照样能编译。这条守卫就是那个保证。
//
// 同时**正向**断言连接期事实真的被消费了（`pkg/acl` 解析 initialize 的 `Capabilities.Images`
// 必须在某处被读）：否则"用连接期事实"只是一句没人执行的话，而门禁会悄悄退回静态值。
func TestStaticCapsImagesHasNoConsumer(t *testing.T) {
	backend := backendRoot(t)

	var offenders []string
	var consumers []string
	scanned := 0
	scanOne := func(path string) {
		base := filepath.ToSlash(path)
		rel := base
		if i := strings.Index(base, "/backend/"); i >= 0 {
			rel = base[i+len("/backend/"):]
		}
		src := stripLineComments(readFileOrFatal(t, path))
		scanned++
		// ① 静态基线：只许出现在汇入点。
		if strings.Contains(src, ".Caps.Images") {
			if _, ok := staticCapsImagesAllowedFiles[rel]; !ok {
				offenders = append(offenders, rel)
			}
		}
		// ② 连接期事实：整棵树上按符号收集（路径无关），命中数下面按"恰好 N"判。
		if strings.Contains(src, connectionFactImages) {
			consumers = append(consumers, rel)
		}
	}
	walkGo := func(root string) {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// archtest 自身要写出被禁的字面量（本文件就是那条判据），跳过。
				if d.Name() == "archtest" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			scanOne(path)
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s: %v", root, err)
		}
	}
	walkGo(filepath.Join(backend, "internal"))
	walkGo(filepath.Join(backend, "pkg"))

	// fail-closed：一片 `.go` 都扫不到时，上面两条判据都会"照样绿"而不再看任何文件。
	if scanned == 0 {
		t.Fatal("`internal/` + `pkg/` 下一个生产 .go 文件都没扫到（fail-closed：判据失去对象，" +
			"空转的守卫比没有守卫更坏——它给出虚假的安心）")
	}

	if len(offenders) > 0 {
		t.Fatalf("静态能力基线 `Caps.Images` 出现了新的读法：%v。\n"+
			"它是**保守基线**而不是事实——真值只有连接期 initialize 的 `promptCapabilities.image` 知道"+
			"（ADR-14）。要做判据请读 `acl.Capabilities.Images`（连接期事实）；"+
			"确实只是声明汇入的话，把它加进 staticCapsImagesAllowedFiles 并写明理由", offenders)
	}

	if len(consumers) != connectionFactImagesConsumers {
		t.Fatalf("连接期事实 `%s` 在 `internal/` + `pkg/` 的生产文件里实测 **%d 处**（期望 %d）：%v。\n"+
			"ADR-14 要求「本路由能不能收图」以连接期事实为准，所以它必须有**确实被读**的落点；\n"+
			"0 处 = 消费者被删掉了（门禁会悄悄退回静态基线，给用户一个错误结论）；\n"+
			"多于 %d 处 = 又有人复制了一份这个取值表达式，请逐个核对后把 connectionFactImagesConsumers "+
			"改成实测值并写明理由（判据按符号扫整棵树，不按文件路径——它不随文件搬家失效）",
			connectionFactImages, len(consumers), connectionFactImagesConsumers, consumers,
			connectionFactImagesConsumers)
	}
}
