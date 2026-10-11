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
// 不是判据。declaration 原地（`pkg/runtime/dsh/dsh.go`、`pkg/runtime/opencode/*.go`）写的是
// 结构体字面量字段（`Images: false`），不含 `.Caps.Images` 这个取值表达式。
var staticCapsImagesAllowedFiles = map[string]string{
	"pkg/runtime/adapters.go": "把 adapter 的静态基线声明汇入 Spec（值拷贝，不做任何判据）",
}

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
	root := filepath.Join(backend, "internal")
	pkgRoot := filepath.Join(backend, "pkg")

	var offenders []string
	consumer := ""
	scanOne := func(path string) {
		base := filepath.ToSlash(path)
		rel := base
		if i := strings.Index(base, "/backend/"); i >= 0 {
			rel = base[i+len("/backend/"):]
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s: %v", path, err)
		}
		src := stripLineComments(string(raw))
		if !strings.Contains(src, ".Caps.Images") {
			return
		}
		if _, ok := staticCapsImagesAllowedFiles[rel]; ok {
			return
		}
		offenders = append(offenders, rel)
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
	walkGo(root)
	walkGo(pkgRoot)

	if len(offenders) > 0 {
		t.Fatalf("静态能力基线 `Caps.Images` 出现了新的读法：%v。\n"+
			"它是**保守基线**而不是事实——真值只有连接期 initialize 的 `promptCapabilities.image` 知道"+
			"（ADR-14）。要做判据请读 `acl.Capabilities.Images`（连接期事实）；"+
			"确实只是声明汇入的话，把它加进 staticCapsImagesAllowedFiles 并写明理由", offenders)
	}

	// 正向：连接期事实必须有消费者（否则"用连接期事实"是空话）。
	for _, rel := range []string{"internal/manager/model_catalog.go", "internal/manager/connection.go"} {
		raw, err := os.ReadFile(filepath.Join(backend, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("读 %s: %v（改名前请同步本守卫）", rel, err)
		}
		if strings.Contains(string(raw), "Capabilities().Images") {
			consumer = rel
			break
		}
	}
	if consumer == "" {
		t.Fatal("找不到连接期 `Capabilities().Images` 的消费者：ADR-14 要求「本路由能不能收图」" +
			"以连接期事实为准；把它删掉等于让门禁悄悄退回静态基线（那会给用户一个错误结论）")
	}
}
