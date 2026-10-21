package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/credential"
)

// fakeEnv 让"继承环境"这一层可测：真 os.Setenv 会让用例互相影响（并行与子进程都不安全）。
func fakeEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
	// os.WriteFile 受 umask 影响，显式 chmod 保证权限断言测的是本包而不是 umask。
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod 失败：%v", err)
	}
}

// TestLayeringOrder 是 v20 的核心判据：四层顺序必须是
// 继承环境 > 托管文档 > <cwd>/.env > <home>/.env，且撤掉上层后**逐层**回落。
//
// 反证：把托管文档提到最前（或把 dotenv 提到托管之前）→ 本用例红。
func TestLayeringOrder(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "proj")
	home := filepath.Join(dir, "home")
	docPath := filepath.Join(home, "credentials.json")
	writeFile(t, docPath, `{"providers/a": "from-managed"}`, 0o600)
	writeFile(t, filepath.Join(cwd, ".env"), "DSH_API_KEY=from-cwd\n", 0o600)
	writeFile(t, filepath.Join(home, ".env"), "DSH_API_KEY=from-home\n", 0o600)

	doc, err := Open(docPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	req := Request{Ref: "providers/a", EnvKeys: []string{"DSH_API_KEY"}}

	cases := []struct {
		name string
		env  map[string]string
		want string
		src  credential.Source
	}{
		{"四层都在 → 继承环境最高", map[string]string{"DSH_API_KEY": "from-env"}, "from-env", credential.SourceEnv},
		{"无继承环境 → 托管文档", nil, "from-managed", credential.SourceManaged},
	}
	for _, tc := range cases {
		l := New(doc, cwd, home)
		l.Env = fakeEnv(tc.env)
		got := l.Resolve(req)
		if !got.Found || got.Value != tc.want || got.Source != tc.src {
			t.Fatalf("%s：得到 value=%q source=%q found=%v problems=%v，期望 %q/%q",
				tc.name, got.Value, got.Source, got.Found, got.Problems, tc.want, tc.src)
		}
	}

	// 撤掉托管文档里的那条 → 落到「项目级 .env」；再撤掉项目级 → 落到「用户级 .env」。
	writeFile(t, docPath, `{"providers/other": "x"}`, 0o600)
	l := New(doc, cwd, home)
	l.Env = fakeEnv(nil)
	if got := l.Resolve(req); !got.Found || got.Value != "from-cwd" || got.Source != credential.SourceDotenvCwd {
		t.Fatalf("撤掉托管文档后应落到 dotenv_cwd，得到 %+v", got)
	}
	if err := os.Remove(filepath.Join(cwd, ".env")); err != nil {
		t.Fatal(err)
	}
	if got := l.Resolve(req); !got.Found || got.Value != "from-home" || got.Source != credential.SourceDotenvHome {
		t.Fatalf("再撤掉项目级后应落到 dotenv_home，得到 %+v", got)
	}
	if err := os.Remove(filepath.Join(home, ".env")); err != nil {
		t.Fatal(err)
	}
	if got := l.Resolve(req); got.Found || len(got.Problems) != 0 {
		t.Fatalf("四层全无时应是 found=false 且无 problems，得到 %+v", got)
	}

	// 空串不算命中：把空值当"已设置"会让底座拿着空密钥去换 401，而错报在很远的地方。
	writeFile(t, filepath.Join(home, ".env"), "DSH_API_KEY=\n", 0o600)
	l = New(doc, cwd, home)
	l.Env = fakeEnv(map[string]string{"DSH_API_KEY": "  "})
	if got := l.Resolve(req); got.Found {
		t.Fatalf("空/空白值不得算命中，得到 %+v", got)
	}
}

// TestResolveStopsAtUnreadableLayer 钉住"一层读不了就停，不用更低的层"。
//
// 这是本包最要紧的一条语义：读不了意味着"这一层有没有那把钥匙"**未知**，继续往下走会把
// "配置坏了"静默变成"换了把钥匙"——用户以为用的是托管的那把，实际生效的是某份陈旧 .env。
func TestResolveStopsAtUnreadableLayer(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "proj")
	home := filepath.Join(dir, "home")
	docPath := filepath.Join(home, "credentials.json")
	writeFile(t, filepath.Join(cwd, ".env"), "DSH_API_KEY=from-cwd\n", 0o600)
	// 权限过宽的托管文档：能读、但必须 fail-loud。
	writeFile(t, docPath, `{"providers/a": "from-managed"}`, 0o644)

	doc, err := Open(docPath)
	if err != nil {
		t.Fatal(err)
	}
	l := New(doc, cwd, home)
	l.Env = fakeEnv(nil)
	got := l.Resolve(Request{Ref: "providers/a", EnvKeys: []string{"DSH_API_KEY"}})
	if got.Found {
		t.Fatalf("托管文档读不了时不得回落到 .env，得到 %+v", got)
	}
	if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "0600") {
		t.Fatalf("必须如实报出问题且指路 chmod 600，得到 %v", got.Problems)
	}

	// 项目级 .env 坏掉 → 也不许用用户级那把。
	writeFile(t, docPath, `{}`, 0o600)
	writeFile(t, filepath.Join(home, ".env"), "DSH_API_KEY=from-home\n", 0o600)
	writeFile(t, filepath.Join(cwd, ".env"), "这不是 KEY=VALUE\n", 0o600)
	l2 := New(doc, cwd, home)
	l2.Env = fakeEnv(nil)
	got2 := l2.Resolve(Request{Ref: "providers/a", EnvKeys: []string{"DSH_API_KEY"}})
	if got2.Found || len(got2.Problems) != 1 {
		t.Fatalf("项目级 .env 解析失败时应停下并如实报问题，得到 %+v", got2)
	}
}

// TestManagedDocIsStrictMapping 钉住托管文档是**严格 `引用 → 字符串` 映射**、刻意不是 dotenv。
// 形状坏掉时必须一眼看出，而不是让半吊子解析器把 `{"a":1}` 当成"没有密钥"。
func TestManagedDocIsStrictMapping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	doc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// 文件不存在 = 空映射（新机器是正常状态），不是错误。
	values, err := doc.Load()
	if err != nil || len(values) != 0 {
		t.Fatalf("不存在的文档应得到空映射与 nil 错误，得到 %v/%v", values, err)
	}

	bad := map[string]string{
		"数组":        `["a"]`,
		"标量":        `"a"`,
		"null":      `null`,
		"值不是字符串":    `{"providers/a": 1}`,
		"嵌套对象":      `{"providers/a": {"k":"v"}}`,
		"dotenv 形态": `providers/a=secret`,
		"键含空白":      `{"providers a": "secret"}`,
	}
	for name, content := range bad {
		writeFile(t, path, content, 0o600)
		if _, err := doc.Load(); err == nil {
			t.Fatalf("%s（%s）必须被拒绝", name, content)
		}
	}

	writeFile(t, path, "  \n", 0o600)
	if values, err := doc.Load(); err != nil || len(values) != 0 {
		t.Fatalf("空文件应得到空映射，得到 %v/%v", values, err)
	}

	writeFile(t, path, `{"providers/a": "secret", "store:default/gh": "tok"}`, 0o600)
	values, err = doc.Load()
	if err != nil {
		t.Fatalf("正常映射应可读：%v", err)
	}
	if values["providers/a"] != "secret" || values["store:default/gh"] != "tok" {
		t.Fatalf("读回的映射不对：%v", values)
	}
}

// TestManagedDocPermissionFailLoud 钉住两侧权限语义：**读** fail-loud，**写** 以 0600 原子重写并治好。
func TestManagedDocPermissionFailLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	doc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `{"providers/a": "secret"}`, 0o644)
	if _, err := doc.Load(); err == nil {
		t.Fatal("0644 的密钥文档必须读失败（同机其他用户可读）")
	} else if !strings.Contains(err.Error(), "0600") {
		t.Fatalf("错误文案要给出可执行的出路（chmod 600）：%v", err)
	}

	// Set 是读-改-写：读失败即失败，绝不用"读不到就当空的"覆盖掉用户其余的密钥。
	if err := doc.Set("providers/b", "v"); err == nil {
		t.Fatal("底层文档不可读时 Set 必须失败（否则会把其余密钥一起抹掉）")
	}
	// Save 直接写整份 → 顺带把权限治回 0600。
	if err := doc.Save(map[string]string{"providers/a": "secret"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("Save 之后权限应为 0600，得到 %04o", perm)
	}
	if _, err := doc.Load(); err != nil {
		t.Fatalf("治好权限后应可读：%v", err)
	}
}

// TestSaveIsAtomicAndPrivate 钉住写入形态：0600、无临时文件残留、键排序稳定（可 diff）。
func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "credentials.json")
	doc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{"store:z/b": "2", "providers/a": "1"}
	if err := doc.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("权限应为 0600，得到 %04o", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".credentials-") {
			t.Fatalf("留下临时文件：%s", e.Name())
		}
	}
	// 本包自己建的目录必须是私有的（0700）：文件再严，同级目录列表也能泄露"有哪些引用"。
	// 注意判据只能落在**本包创建的**那一层上——`t.TempDir()` 的父目录权限是测试框架的事。
	dfi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dfi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("Save 新建的目录权限过宽：%04o", dfi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 键排序稳定：`providers/a` 先于 `store:z/b`。
	if strings.Index(string(b), "providers/a") > strings.Index(string(b), "store:z/b") {
		t.Fatalf("键未按稳定顺序输出（同一份映射两次写出的字节不同）：%s", b)
	}
	values, err := doc.Load()
	if err != nil || len(values) != 2 || values["providers/a"] != "1" {
		t.Fatalf("round-trip 不对：%v/%v", values, err)
	}
	// 非法键直接拒绝（写坏键比写不进去更难查）。
	if err := doc.Save(map[string]string{"has space": "v"}); err == nil {
		t.Fatal("非法引用键必须被拒绝")
	}
}

// TestDeleteIsReadModifyWrite 钉住 Delete 的"不存在即无操作"与"读失败即失败"。
func TestDeleteIsReadModifyWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	doc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Delete("providers/missing"); err != nil {
		t.Fatalf("删除不存在的引用应是无操作：%v", err)
	}
	if err := doc.Set("providers/a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set("providers/b", "2"); err != nil {
		t.Fatal(err)
	}
	if err := doc.Delete("providers/a"); err != nil {
		t.Fatal(err)
	}
	values, err := doc.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values["providers/a"]; ok || values["providers/b"] != "2" {
		t.Fatalf("删除后映射不对：%v", values)
	}
}

// TestDotenvSubset 钉住 dotenv 的**刻意很小的子集**（解析器越大，它自己的出错面越大）。
func TestDotenvSubset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, strings.Join([]string{
		"# 注释行",
		"",
		"  A=1  ",
		"export B=2",
		`C="x\ny"`,
		`D='lit"eral'`,
		"A=3",
	}, "\n")+"\n", 0o600)

	values, err := readDotenv(path)
	if err != nil {
		t.Fatalf("readDotenv: %v", err)
	}
	if values["A"] != "3" {
		t.Fatalf("同一个键出现多次应后者胜，得到 %q", values["A"])
	}
	if values["B"] != "2" {
		t.Fatalf("export 前缀应被剥掉，得到 %q", values["B"])
	}
	if values["C"] != "x\ny" {
		t.Fatalf("双引号内的 \\n 应展开，得到 %q", values["C"])
	}
	if values["D"] != `lit"eral` {
		t.Fatalf("单引号内不做转义，得到 %q", values["D"])
	}
}

// TestDotenvRejectsMalformed 钉住"不合法就报错"——不猜、不跳过。
func TestDotenvRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"缺等号":     "JUSTAWORD\n",
		"等号开头":    "=1\n",
		"键名非法":    "1A=1\n",
		"键名含连字符":  "A-B=1\n",
		"变量展开不支持": "A=${B}\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "sub", name, ".env")
			writeFile(t, path, content, 0o600)
			if _, err := readDotenv(path); err == nil {
				t.Fatalf("%q 必须被拒绝", content)
			}
		})
	}
	// 不存在的文件不是问题（绝大多数机器上没有 .env）。
	values, err := readDotenv(filepath.Join(dir, "nope.env"))
	if err != nil || values != nil {
		t.Fatalf("文件不存在应得到 (nil, nil)，得到 %v/%v", values, err)
	}
}

// TestOnlyManagedIsWritable 钉住"哪一层能被平台写"：只有托管文档。
// 面板要据此把继承来的密钥显示成只读——显示成可编辑会让用户改完发现不生效（下次启动又被顶掉）。
func TestOnlyManagedIsWritable(t *testing.T) {
	for _, s := range []credential.Source{credential.SourceEnv, credential.SourceDotenvCwd, credential.SourceDotenvHome} {
		if s.Writable() {
			t.Fatalf("%s 不该是只读层之外的可写层", s)
		}
	}
	if !credential.SourceManaged.Writable() {
		t.Fatal("托管文档必须是可写层")
	}
}

// TestNilSafety 钉住装配期可能没有持久层这件事：nil 的 Doc/Layered 不能 panic。
func TestNilSafety(t *testing.T) {
	var l *Layered
	if got := l.Resolve(Request{Ref: "providers/a"}); len(got.Problems) != 1 {
		t.Fatalf("nil Layered 应如实报问题而不是 panic：%+v", got)
	}
	var d *Doc
	if d.Path() != "" {
		t.Fatal("nil Doc 的 Path 应为空串")
	}
	if _, err := d.Load(); err == nil {
		t.Fatal("nil Doc 的 Load 应报错")
	}
	if err := d.Save(nil); err == nil {
		t.Fatal("nil Doc 的 Save 应报错")
	}
	if _, err := Open("   "); err == nil {
		t.Fatal("空路径必须被 Open 拒绝")
	}
}
