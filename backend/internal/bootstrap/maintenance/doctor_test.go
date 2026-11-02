package maintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/dto/dtosystem"
	"github.com/morehao/ark-mate/internal/router"
)

// writeFiles 在 root 下写一组文件（rel → 内容），自动建父目录。
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("建目录失败 %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("写文件失败 %s: %v", abs, err)
		}
	}
}

// payloadSpec 一份最小载荷的装配输入（`Files` 的键是**相对于 Base 的**相对路径）。
type payloadSpec struct {
	Root    string
	Name    string
	Version string
	// Base fileList 的相对基准（`""` = 载荷根；`node_modules` = 闭包那一层）。
	Base string
	// OmitBase 模拟 M4-5 之前装配的清单：**不写** fileListBase 字段。
	OmitBase bool
	Files    map[string]string
}

// makePayload 按载荷清单的**写入口径**装配一份最小载荷（M3-1 的形态：先复制文件、后写清单），
// 返回清单。清单本身落在载荷根——真实装配里它就在那里，而"它不参与复算"正是被测行为之一。
//
// 刻意在这里重写一遍"怎么算清单"而不是调用被测函数：用它验证它自己等于没验证
// （与 web_meta_test 的 recomputeDigest 同一条纪律）。
func makePayload(t *testing.T, spec payloadSpec) payloadManifestDoc {
	t.Helper()
	target := spec.Root
	if spec.Base != "" {
		target = filepath.Join(spec.Root, filepath.FromSlash(spec.Base))
	}
	writeFiles(t, target, spec.Files)
	digests := map[string]string{}
	var bytes int64
	var fileList []string
	for rel, content := range spec.Files {
		sum := sha256.Sum256([]byte(content))
		hexSum := hex.EncodeToString(sum[:])
		digests[rel] = hexSum
		fileList = append(fileList, hexSum+"  "+rel)
		bytes += int64(len(content))
	}
	// entry 一律**相对于载荷根**（与 prepare-runtime 的口径一致）：闭包型载荷的入口在那一层
	// 目录里，直接铺开的型就在载荷根——两者正好各覆盖一种形态。
	entry := "bin/tool"
	if spec.Base != "" {
		entry = spec.Base + "/bin/tool"
	}
	doc := payloadManifestDoc{
		Name:     spec.Name,
		Version:  spec.Version,
		Platform: "darwin",
		Arch:     "arm64",
		NodeABI:  "149",
		Entry:    entry,
		Digest:   router.CompositeDigest(digests),
		Files:    len(spec.Files),
		Bytes:    bytes,
		FileList: fileList,
	}
	if !spec.OmitBase {
		base := spec.Base
		doc.FileListBase = &base
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("清单序列化失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(spec.Root, "payload.json"), b, 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	return doc
}

// TestCompositeDigestMatchesJSAlgorithm 冻结"Go 侧载荷复算"与 JS 侧 `lib/payload.mjs` 的
// `compositeDigest` 是**同一个算法**。
//
// 期望值是用 JS 实现（node 跑 lib/payload.mjs）算出来的字节常量，不是 Go 自己算的——
// 两边各自算一个自洽的值正是这条要防的事（"门禁永远绿"比"永远红"更坏）。
func TestCompositeDigestMatchesJSAlgorithm(t *testing.T) {
	// JS：compositeDigest([{rel:"a.txt",digest:sha256("a\n")},{rel:"b/c.txt",digest:sha256("b\n")}])
	got := router.CompositeDigest(map[string]string{
		"a.txt":   "87428fc522803d31065e7bce3cf03fe475096631e5e07bbd7a0fde60c4cf25c7",
		"b/c.txt": "0263829989b6fd954f72baaf2fc64bc2e2f01d692d4de72986ea808f6e99813f",
	})
	const want = "sha256:f4b2a4585a7632e9c3940e7f328bd78b3f7addc30df816b394649aceaacef2f1"
	if got != want {
		t.Fatalf("合成摘要与 JS 实现不一致：got %s want %s", got, want)
	}
}

// TestVerifyPayloadTree 全量复算的正例与各类负例（缺 / 改 / 多 / 清单被改 / 基准判定）。
func TestVerifyPayloadTree(t *testing.T) {
	files := map[string]string{
		"bin/tool":              "#!/bin/sh\necho hi\n",
		"node_modules/a/p.json": "{}",
	}

	newTree := func(t *testing.T) (string, payloadManifestDoc) {
		t.Helper()
		dir := t.TempDir()
		return dir, makePayload(t, payloadSpec{Root: dir, Name: "dsh", Version: "1.0.0", Files: files})
	}

	t.Run("正例：逐字复算一致", func(t *testing.T) {
		dir, m := newTree(t)
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorOK {
			t.Fatalf("期望 ok，得到 %s：%s", got.Status, got.Detail)
		}
		if !strings.Contains(got.Detail, m.Digest) {
			t.Fatalf("报告里应带上合成摘要：%s", got.Detail)
		}
	})

	t.Run("清单文件本身不参与复算", func(t *testing.T) {
		dir, m := newTree(t)
		// 装配的真实顺序：先复制文件、后写清单。清单在载荷根里，但**不是载荷内容**。
		if err := os.WriteFile(filepath.Join(dir, "payload.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := verifyPayloadTree(dir, m); got.Status != doctorOK {
			t.Fatalf("payload.json 不该被计入载荷内容：%s", got.Detail)
		}
	})

	t.Run("内容变了", func(t *testing.T) {
		dir, m := newTree(t)
		if err := os.WriteFile(filepath.Join(dir, "bin", "tool"), []byte("tampered"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "内容变了 1") {
			t.Fatalf("期望判出内容变化，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("缺文件", func(t *testing.T) {
		dir, m := newTree(t)
		if err := os.Remove(filepath.Join(dir, "bin", "tool")); err != nil {
			t.Fatal(err)
		}
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "缺 1") {
			t.Fatalf("期望判出缺文件，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("多出文件", func(t *testing.T) {
		dir, m := newTree(t)
		writeFiles(t, dir, map[string]string{"extra.js": "// 塞进来的"})
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "多出 1") {
			t.Fatalf("期望判出多出文件，得到 %s：%s", got.Status, got.Detail)
		}
		if !strings.Contains(got.Detail, "extra.js") {
			t.Fatalf("差异里应点名文件：%s", got.Detail)
		}
	})

	t.Run("合成摘要对不上（逐文件都对）", func(t *testing.T) {
		dir, m := newTree(t)
		m.Digest = "sha256:" + strings.Repeat("0", 64)
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "合成摘要不符") {
			t.Fatalf("期望判出合成摘要不符，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("文件数与字节数对不上", func(t *testing.T) {
		dir, m := newTree(t)
		m.Files = 99
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "文件数/字节数") {
			t.Fatalf("期望判出计数不符，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("清单行不是 <hex>  <rel>", func(t *testing.T) {
		dir, m := newTree(t)
		m.FileList = []string{"没有两个空格的一行"}
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "不是 `<hex>  <rel>`") {
			t.Fatalf("期望判出清单格式错，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("载荷里出现非普通文件（软链）", func(t *testing.T) {
		dir, m := newTree(t)
		if err := os.Symlink(filepath.Join(dir, "bin", "tool"), filepath.Join(dir, "link")); err != nil {
			t.Skipf("本平台建不了软链：%v", err)
		}
		got := verifyPayloadTree(dir, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "非普通文件") {
			t.Fatalf("期望判出非普通文件，得到 %s：%s", got.Status, got.Detail)
		}
	})

	// —— fileList 的相对基准（M4-5 修掉的那个真问题）——
	//
	// dsh 的闭包落在 `node_modules/` 下，fileList 却是按复制源根记的；清单此前没有任何字段
	// 说明这件事。这几条把"基准从哪来、来源不明时怎么判"钉死。

	closureSpec := func(root string, omitBase bool) payloadSpec {
		return payloadSpec{
			Root: root, Name: "dsh", Version: "1.0.0", Base: "node_modules", OmitBase: omitBase,
			Files: map[string]string{"@scope/a/index.js": "module.exports = 1\n", ".package-map.json": "{}"},
		}
	}

	t.Run("基准记为 node_modules", func(t *testing.T) {
		root := t.TempDir()
		m := makePayload(t, closureSpec(root, false))
		got := verifyPayloadTree(root, m)
		if got.Status != doctorOK {
			t.Fatalf("期望 ok，得到 %s：%s", got.Status, got.Detail)
		}
		if !strings.Contains(got.Detail, "fileListBase=node_modules") {
			t.Fatalf("结论里应写明基准：%s", got.Detail)
		}
	})

	t.Run("旧清单没记基准：按样例唯一命中判定并注明", func(t *testing.T) {
		root := t.TempDir()
		m := makePayload(t, closureSpec(root, true))
		if m.FileListBase != nil {
			t.Fatal("构造失败：OmitBase 应让字段缺席")
		}
		got := verifyPayloadTree(root, m)
		if got.Status != doctorOK {
			t.Fatalf("期望 ok，得到 %s：%s", got.Status, got.Detail)
		}
		if !strings.Contains(got.Detail, "清单未记录 fileListBase") {
			t.Fatalf("结论必须说明基准是推断出来的：%s", got.Detail)
		}
	})

	t.Run("记录的基准在载荷里不存在：当场失败，不回落", func(t *testing.T) {
		root := t.TempDir()
		m := makePayload(t, closureSpec(root, false))
		bogus := "vendor"
		m.FileListBase = &bogus
		got := verifyPayloadTree(root, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "不是目录") {
			t.Fatalf("期望判出基准不存在，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("基准两个形态都能命中样例：判歧义而不是随便挑一个", func(t *testing.T) {
		root := t.TempDir()
		m := makePayload(t, payloadSpec{
			Root: root, Name: "dsh", Version: "1.0.0", OmitBase: true,
			Files: map[string]string{"dup.txt": "x"},
		})
		// 载荷根与 node_modules 下各放一份同名同内容的 dup.txt：两个候选都能命中。
		writeFiles(t, filepath.Join(root, "node_modules"), map[string]string{"dup.txt": "x"})
		got := verifyPayloadTree(root, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "有歧义") {
			t.Fatalf("期望判出歧义，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("基准之下没有样例：判无法判定并让人重新装配", func(t *testing.T) {
		root := t.TempDir()
		m := makePayload(t, payloadSpec{
			Root: root, Name: "dsh", Version: "1.0.0", OmitBase: true,
			Files: map[string]string{"bin/tool": "x"},
		})
		// 把文件挪到第三层形态（既不在根、也不在 node_modules）——闭合集合之外，不该被猜中。
		if err := os.MkdirAll(filepath.Join(root, "vendor"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "bin"), filepath.Join(root, "vendor", "bin")); err != nil {
			t.Fatal(err)
		}
		got := verifyPayloadTree(root, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "无法判定") {
			t.Fatalf("期望判出无法判定，得到 %s：%s", got.Status, got.Detail)
		}
	})

	t.Run("载荷根下有清单之外的条目", func(t *testing.T) {
		root := t.TempDir()
		m := makePayload(t, closureSpec(root, false))
		writeFiles(t, root, map[string]string{"payload.json.tmp-999": "半份清单的残留"})
		got := verifyPayloadTree(root, m)
		if got.Status != doctorFail || !strings.Contains(got.Detail, "载荷根下有清单之外的条目") {
			t.Fatalf("期望判出计划外条目，得到 %s：%s", got.Status, got.Detail)
		}
		if !strings.Contains(got.Detail, "payload.json.tmp-999") {
			t.Fatalf("差异里应点名条目：%s", got.Detail)
		}
	})
}

// TestScanPayloadManifests 载荷清单扫描：布局合规性（目录 ↔ 清单必须一致、清单必须完整）。
func TestScanPayloadManifests(t *testing.T) {
	writeManifest := func(t *testing.T, root, name, version string, doc payloadManifestDoc) {
		t.Helper()
		dir := filepath.Join(root, name, version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	full := payloadManifestDoc{
		Name: "dsh", Version: "1.0.0", NodeABI: "149",
		Entry: "bin/tool", Digest: "sha256:" + strings.Repeat("a", 64), FileList: []string{},
	}

	t.Run("正例：按 name 排序返回", func(t *testing.T) {
		root := t.TempDir()
		writeManifest(t, root, "rtx", "2.0.0", payloadManifestDoc{
			Name: "rtx", Version: "2.0.0", NodeABI: "149", Entry: "bin/rtx",
			Digest: "sha256:" + strings.Repeat("b", 64), FileList: []string{},
		})
		writeManifest(t, root, "dsh", "1.0.0", full)
		got, err := scanPayloadManifests(root)
		if err != nil {
			t.Fatalf("扫描失败：%v", err)
		}
		if len(got) != 2 || got[0].Manifest.Name != "dsh" || got[1].Manifest.Name != "rtx" {
			t.Fatalf("期望按 name 排序的两份载荷，得到 %+v", got)
		}
	})

	t.Run("目录与清单不一致", func(t *testing.T) {
		root := t.TempDir()
		writeManifest(t, root, "dsh", "9.9.9", full) // 目录 9.9.9，清单 1.0.0
		if _, err := scanPayloadManifests(root); err == nil || !strings.Contains(err.Error(), "与目录不一致") {
			t.Fatalf("期望判出目录/清单不一致，得到 %v", err)
		}
	})

	t.Run("清单字段为空", func(t *testing.T) {
		root := t.TempDir()
		broken := full
		broken.Entry = ""
		writeManifest(t, root, "dsh", "1.0.0", broken)
		if _, err := scanPayloadManifests(root); err == nil || !strings.Contains(err.Error(), "entry 为空") {
			t.Fatalf("期望判出 entry 为空，得到 %v", err)
		}
	})

	t.Run("空目录 / 非法 JSON / 缺清单", func(t *testing.T) {
		if _, err := scanPayloadManifests(t.TempDir()); err == nil || !strings.Contains(err.Error(), "没扫到") {
			t.Fatalf("空目录应报错，得到 %v", err)
		}
		root := t.TempDir()
		dir := filepath.Join(root, "dsh", "1.0.0")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload.json"), []byte("{不是 JSON"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := scanPayloadManifests(root); err == nil || !strings.Contains(err.Error(), "不是合法 JSON") {
			t.Fatalf("非法 JSON 应报错，得到 %v", err)
		}
		root2 := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root2, "dsh", "1.0.0"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := scanPayloadManifests(root2); err == nil || !strings.Contains(err.Error(), "没有可读的 payload.json") {
			t.Fatalf("缺清单应报错，得到 %v", err)
		}
	})

	t.Run("必须是绝对路径", func(t *testing.T) {
		if _, err := scanPayloadManifests("resources/payloads"); err == nil || !strings.Contains(err.Error(), "绝对路径") {
			t.Fatalf("相对路径应报错，得到 %v", err)
		}
	})
}

// TestCheckHomeWritable 数据目录可写性：正常目录过、非目录挡。
func TestCheckHomeWritable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mate-home")
	if got := checkHomeWritable(home); got.Status != doctorOK {
		t.Fatalf("期望 ok，得到 %s：%s", got.Status, got.Detail)
	}
	// 探针必须被清掉：体检不能在用户数据目录里留垃圾。
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("体检留下了残留文件：%+v", entries)
	}

	// 拿一个**普通文件**当数据目录：MkdirAll 必失败（跨平台稳定，不依赖 /dev/null 这类平台路径）。
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := checkHomeWritable(filepath.Join(file, "sub")); got.Status != doctorFail {
		t.Fatalf("期望 fail，得到 %s：%s", got.Status, got.Detail)
	}
	if got := checkHomeWritable("  "); got.Status != doctorFail {
		t.Fatalf("空目录应 fail，得到 %s", got.Status)
	}
}

// TestRenderDoctorReport 人话报告：图标三档 + 末尾必须把警告数说出来。
//
// 最后一条不是排版洁癖：只报"0 项失败"会让人以为一切都校验过了，而 warn 的常见含义
// 恰恰是"这一项**没校验成**"（无法校验 / 端口被占 / 开发构建跳过契约比对）。
func TestRenderDoctorReport(t *testing.T) {
	got := renderDoctorReport(doctorReport{
		Version:     "0.1.0",
		APIContract: "v1",
		Home:        "/tmp/mate",
		Addr:        "127.0.0.1:10001",
		Checks: []doctorCheck{
			{"home", doctorOK, "/tmp/mate 可写"},
			{"port", doctorWarn, "被占用"},
			{"assets", doctorFail, "没有可用的前端资产"},
		},
		Failed:   1,
		Warnings: 1,
		Passed:   false,
	})
	for _, want := range []string{"✅ home", "⚠️ port", "❌ assets", "0.1.0 / 契约 v1", "失败：1 项不符（另有 1 项警告）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("报告缺少 %q：\n%s", want, got)
		}
	}

	ok := renderDoctorReport(doctorReport{Version: "0.1.0", Passed: true, Warnings: 2})
	if !strings.Contains(ok, "通过：0 项失败，2 项警告") {
		t.Fatalf("通过行必须带警告数：\n%s", ok)
	}
}

// TestDoctorHomeDisplayForm（v19）钉显示名的**两半**（人话换、`--json` 不换）。
//
// 失效方式都是静默的：人话那半漏了 → 绝对机器路径继续随截图/问题单外流（没人会因此被拦下）；
// `--json` 那半跟着换 → 诊断包与 CI 拿到 `~/…`，在**另一个人的机器上**指向别处（原本逐字可比
// 的绝对路径没了）。所以两半各断言一次，且都要断言"另一种形态**不**出现"。
func TestDoctorHomeDisplayForm(t *testing.T) {
	// 默认数据目录：测试里清掉 MATE_HOME，让解析退回家目录默认值（DisplayHome 只换这一种）。
	t.Setenv("MATE_HOME", "")
	def := config.HomeDirFromEnv()
	if !filepath.IsAbs(def) {
		t.Skip("本机取不到家目录，显示名不适用")
	}
	report := doctorReport{
		Version:     "0.1.0",
		APIContract: "v1",
		Home:        def,
		Checks: []doctorCheck{
			{"home", doctorOK, filepath.Join(def, "mate.db") + " 可写"},
			{"备份布局", doctorWarn, "老布局遗留 1 个：" + filepath.Join(def, "mate.db.bak-v15-x")},
		},
		Passed: true,
	}

	human := renderDoctorReport(report)
	if !strings.Contains(human, "数据目录 ~/.ark-mate") {
		t.Fatalf("人话输出应把默认数据目录渲染成 `~/.ark-mate`：\n%s", human)
	}
	if !strings.Contains(human, "~/.ark-mate/mate.db 可写") || !strings.Contains(human, "~/.ark-mate/mate.db.bak-v15-x") {
		t.Fatalf("检查项里的路径也要换（含子目录与文件名）：\n%s", human)
	}
	if strings.Contains(human, def) {
		t.Fatalf("人话输出里不该再出现绝对数据目录 %q：\n%s", def, human)
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), def) {
		t.Fatalf("`--json` 必须保留绝对路径（诊断包/CI 要逐字可比）：%s", raw)
	}
	if strings.Contains(string(raw), "~/.ark-mate") {
		t.Fatalf("`--json` 不该出现人类显示形态：%s", raw)
	}
}

// TestDoctorReportJSONShape 冻结 `mate doctor --json` 的形状（键序 = 结构体字段序）。
//
// 与 `TestVersionOutput` 同一条纪律：机器消费方（CI、诊断包、将来的面板）读的是**字节**，
// 加字段/挪位置都会改变它——所以这里逐字断言，而不是"解析后看看有没有这个键"。
//
// K2 起多一个 `providers` 段：它把 provider / 模型清单的事实摆成机器可读的形状
// （`mate doctor` 的 `checks[]` 只回答"过没过"）。这里的 null 是**零值结构体**的产物
// （本用例直接构造 doctorReport，没走采集）；真实运行里那些列表恒为 `[]`
// （`CollectProviderDoctorFacts` 逐个初始化），机器消费方不必区分 null 与空。
func TestDoctorReportJSONShape(t *testing.T) {
	b, err := json.Marshal(doctorReport{
		Version:     "0.1.0",
		APIContract: "v1",
		Home:        "/tmp/mate",
		Addr:        "127.0.0.1:10001",
		Checks:      []doctorCheck{{"home", doctorOK, "可写"}},
		Passed:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// v20 起凭据面的字段换成：旧机制的**遗留物**（legacySecret*）+ 托管文档（credentials*）
	// + 库里那两份（providerKeysWithKey / mcpSecretRefs）与三方对账（missing* / orphanMcpSecrets）。
	const want = `{"version":"0.1.0","apiContract":"v1","home":"/tmp/mate","addr":"127.0.0.1:10001",` +
		`"verifyPayload":false,"checks":[{"name":"home","status":"ok","detail":"可写"}],` +
		`"providers":{"carrier":"","providerRows":0,"providerIds":null,"modelRows":0,` +
		`"modelsWithoutCapacity":null,"modelsPartialCapacity":null,` +
		`"runtimeConfigDigestWritten":false,"providersFile":false,"providersBackup":false,` +
		`"legacySecretDir":false,"legacySecretKeyFile":false,"legacySecretCount":0,` +
		`"credentialsExists":false,"credentialsPermOk":false,"credentialsRefs":0,` +
		`"providerKeysWithKey":null,"mcpSecretRefs":null,"missingInCredentials":null,` +
		`"missingInStore":null,"orphanMcpSecrets":null,` +
		`"noKeyProviders":null,"importMismatch":null,` +
		`"importMismatchBenignOnly":false,"runtimes":null,"missingBaseUrl":null,"sensitiveFiles":null},` +
		`"attachments":{"total":0,"pending":0,"linked":0,"orphan":0,"missing":0,"unreadable":0},` +
		`"failed":0,"warnings":0,"passed":true}`
	if string(b) != want {
		t.Fatalf("--json 形状变了：\n got %s\nwant %s", b, want)
	}
}

// TestRunDoctorEndToEnd 端到端装配：临时资产目录 + 临时载荷树 + 临时 HOME + `:0` 端口。
//
// 用 `127.0.0.1:0` 而不是默认端口：内核随便给一个必然空闲的端口，结论才是确定的
// （默认端口上"恰好有人在跑"会让这条测试在开发机上随机变红）。
func TestRunDoctorEndToEnd(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	web := filepath.Join(base, "web")
	writeFiles(t, web, map[string]string{"index.html": "<html></html>", "build-id.txt": "abcdef\n"})
	payloadsRoot := filepath.Join(base, "payloads")
	payloadDir := filepath.Join(payloadsRoot, "dsh", "1.0.0")
	manifest := makePayload(t, payloadSpec{
		Root: payloadDir, Name: "dsh", Version: "1.0.0", Base: "node_modules",
		Files: map[string]string{"bin/tool": "tool"},
	})

	opts := doctorOptions{
		Home: home, WebDir: web, Addr: "127.0.0.1:0",
		PayloadsDir: payloadsRoot, VerifyPayload: true,
		PayloadsWire:  ctrsystemPayloadInfo(manifest),
		PayloadsGiven: true,
	}
	report := runDoctor(opts)
	if report.Failed != 0 {
		t.Fatalf("期望 0 项失败，得到 %d：\n%s", report.Failed, renderDoctorReport(report))
	}
	if !report.Passed {
		t.Fatal("Passed 应与 Failed==0 一致")
	}
	// 「非打包态没有 release.json」必须是**警告**：判失败会让这条命令在开发态永远红。
	if report.Warnings == 0 {
		t.Fatalf("期望至少 1 项警告（缺 release.json）：\n%s", renderDoctorReport(report))
	}
	names := map[string]bool{}
	for _, c := range report.Checks {
		names[c.Name] = true
	}
	for _, want := range []string{"home", "port", "assets", "release", "payloads", "payloads (wire)", "payload dsh", "payload dsh 全量复算"} {
		if !names[want] {
			t.Fatalf("报告缺检查项 %q：\n%s", want, renderDoctorReport(report))
		}
	}

	// 负例：载荷被改一个字节 → 全量复算当场红（退出码由 Failed>0 决定）。
	if err := os.WriteFile(filepath.Join(payloadDir, "node_modules", "bin", "tool"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := runDoctor(opts)
	if bad.Failed == 0 {
		t.Fatalf("载荷被改后必须判失败：\n%s", renderDoctorReport(bad))
	}
	if !strings.Contains(renderDoctorReport(bad), "全量复算") {
		t.Fatal("失败项应指向全量复算")
	}
}

// ctrsystemPayloadInfo 把盘上清单投影成"壳交来的一条"（字段与 `/meta` 的 payloads[] 一致）。
func ctrsystemPayloadInfo(m payloadManifestDoc) []dtosystem.PayloadInfo {
	return []dtosystem.PayloadInfo{{Name: m.Name, Version: m.Version, Digest: m.Digest, Source: "bundled"}}
}
