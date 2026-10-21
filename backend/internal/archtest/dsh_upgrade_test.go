package archtest

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/dsh"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— dsh 升级的「声明跟随 pin」守卫 ———
//
// 这一族挡的是**升级时最典型的静默缺口**：载荷 pin 改了，而适配器上的「已实测」声明还停在
// 旧版本。它不会让任何测试变红——平台照常启动、照常交付——只是把一条**未在新版本上验证过**的
// 结论继续当成事实使用（凭据优先级在 0.2.0→0.2.1 正好反转过，这一类过期声明的后果是
// 「面板说 A、底座实际用 B」）。
//
// 两条判据都 fail-closed：文件/字段解析不到即失败，不允许「扫不到就绿」。

// TestDshTestedVersionMatchesPayloadPin 载荷 pin 与凭据面的实测声明必须逐字同源。
//
// 唯一手写真源是 `frontend/apps/desktop/runtime/dsh/package.json` 的精确 pin；`TestedOnVersion`
// 必须等于它。升级时的正确顺序是「先置 false/空 → 探针 → 写回 true + 新版本」，所以：
//   - Tested=true  → TestedOnVersion == pin
//   - Tested=false → TestedOnVersion == ""（中间态也必须自洽）
func TestDshTestedVersionMatchesPayloadPin(t *testing.T) {
	pinPath := filepath.Join(frontendRoot(t), "apps", "desktop", "runtime", "dsh", "package.json")
	raw, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatalf("读 dsh 载荷 pin 失败（%s）：%v", pinPath, err)
	}
	var doc struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 dsh 载荷 pin 失败（%s）：%v", pinPath, err)
	}
	pin := strings.TrimSpace(doc.Dependencies["@deepseek-ai/dsh"])
	if pin == "" {
		t.Fatalf("%s 的 dependencies 里没有 @deepseek-ai/dsh：本判据没有对象（fail-closed）", pinPath)
	}
	if strings.ContainsAny(pin[:1], "^~><=*") {
		t.Fatalf("dsh 载荷 pin 必须是精确版本（不带 ^/~/>/</=/ *），得到 %q", pin)
	}

	var spec runtime.Spec
	found := false
	for _, s := range dsh.Specs() {
		if s.ID == dsh.ID {
			spec, found = s, true
			break
		}
	}
	if !found {
		t.Fatalf("AllBuiltinSpecs() 里没有 %q：本判据没有对象（fail-closed）", dsh.ID)
	}

	if spec.Credential.Tested {
		if spec.Credential.TestedOnVersion != pin {
			t.Fatalf("凭据面声明已实测（Tested=true），但 TestedOnVersion=%q 与载荷 pin=%q 不一致：\n"+
				"  升级载荷后必须重跑 H6 探针并把 TestedOnVersion 改成新 pin；\n"+
				"  未重测时先把 Tested 置 false、TestedOnVersion 置空。", spec.Credential.TestedOnVersion, pin)
		}
	} else if spec.Credential.TestedOnVersion != "" {
		t.Fatalf("凭据面 Tested=false 时 TestedOnVersion 必须为空（中间态自洽），得到 %q", spec.Credential.TestedOnVersion)
	}

	// 0.2.1 起 dsh 的模型目录按凭据过滤（apiKeyEnv 缺失 → listModels 为空）。平台据此在
	// applyModel 前给出可执行的缺密钥错误；这条声明一旦丢掉，用户会退回「invalidParams」。
	if !spec.ModelCatalogNeedsCredential {
		t.Fatal("dsh 必须声明 ModelCatalogNeedsCredential=true（0.2.1 起模型目录按凭据过滤）")
	}
}

// TestNoFromDefaultProfileFlag 生产代码不得使用 `--from-default-profile`。
//
// 0.2.1 起该参数从「目标已存在则报错」变成「**删除并替换**目标 profile」：它会抹掉
// `$DSH_HOME/profiles/<name>/` 下的 `cordis.patch.yml` 与已装插件。平台当前零调用点，
// 这条守卫防止后人拿它「重置 profile」——那是一次不可撤销的用户数据删除。
func TestNoFromDefaultProfileFlag(t *testing.T) {
	root := backendRoot(t)
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 守卫自身会写下这个字面量；测试夹具有需要时允许出现（见判据只扫生产代码）。
			if d.Name() == "archtest" || d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(stripGoComments(string(b)), "--from-default-profile") {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 backend 失败：%v", err)
	}
	if len(hits) > 0 {
		t.Fatalf("生产代码命中 --from-default-profile（0.2.1 起它会删除并替换目标 profile）：%s", strings.Join(hits, "、"))
	}
}
