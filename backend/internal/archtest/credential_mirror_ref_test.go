package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProviderMirrorRefFormatIsPinned 托管镜像键的**格式**必须有唯一实现，且钉在凭据机制里。
//
// 为什么值得一条守卫：`providers/<id>` 不是一个内部常量，是**持久化格式**——它写进
// `<MATE_HOME>/credentials.json` 的键，也回写进 `providers.json` 的 `key_ref`。把它改掉
// （或换个地方再拼一次）的后果是**静默**的：已有引用全部指向不存在的键，取值落空，
// 表现为"密钥没配"；而 `pkg/credentials` 的分层顺序、provider 域的档案校验**都不会红**。
//
// 判据三段（都 fail-closed：找不到即失败，避免改名/搬迁后守卫静默空转——这正是 P0 里
// 反复吃过的那类假绿）：
//  1. `ProviderMirrorRefPrefix` 全树**恰好声明一次**；
//  2. 那处声明在 `internal/mechanism/credential/`，取值**逐字**是 `providers/`；
//  3. 全树只有**一个**拼接点（`ProviderMirrorRefPrefix +`），即没有人在别处手拼这个键。
//
// 刻意**不**断言 `"providers/"` 这个字面量全仓唯一：`pkg/config` 的旧目录告警里有同名串，
// 那是指目录不是指键，判进来只会逼着后续加白名单。
func TestProviderMirrorRefFormatIsPinned(t *testing.T) {
	const decl = "ProviderMirrorRefPrefix"
	root := backendRoot(t)
	roots := []string{"internal", "pkg", "cmd"}

	var declFiles []string
	concatSites := 0
	for _, root2 := range roots {
		if _, err := os.Stat(filepath.Join(root, root2)); err != nil {
			continue // 该根不存在（例如没有 cmd/）不算失败
		}
		for _, f := range goFilesUnder(t, filepath.Join(root, root2)) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("读 %s: %v", f, err)
			}
			code := stripLineComments(string(src))
			if regexp.MustCompile(`(?m)^\s*(?:const\s+)?` + decl + `\s*=`).MatchString(code) {
				declFiles = append(declFiles, f)
			}
			concatSites += strings.Count(code, decl+" +")
		}
	}
	if len(declFiles) != 1 {
		t.Fatalf("%s 必须全树恰好声明一次（实际 %d 处：%v）——它是持久化格式，两份声明迟早在某一侧漂移",
			decl, len(declFiles), declFiles)
	}
	got := declFiles[0]
	if !strings.Contains(filepath.ToSlash(got), "internal/mechanism/credential/") {
		t.Fatalf("镜像键格式必须由凭据机制拥有，实际声明在 %s（provider 域拥有它会让「改镜像键」看起来像改 provider）", got)
	}
	src, _ := os.ReadFile(got)
	if !strings.Contains(stripLineComments(string(src)), decl+` = "providers/"`) {
		t.Fatalf("%s 的取值必须逐字是 %q —— 改它等于让所有已有引用指向不存在的键，失效是静默的", decl, "providers/")
	}
	if concatSites != 1 {
		t.Fatalf("镜像键只允许一个拼接点（%s + id），实际 %d 处——第二处就是第二份实现", decl, concatSites)
	}
}

// TestCredentialFaceHasNoTransitionalForwards 凭据面的**八条一行转发**不许回潮（P4a 正文第五十一刀）。
//
// `internal/sessionruntime/credentials.go` 整个删除（77 行）：`SetCredentials` / `SetCredentialCwd` /
// `credentialCwd` / `credentialDoc` / `SyncCredentialMirror` / `providerEnvKeyNames` /
// `mirrorSet` / `mirrorDelete` 全部改成调用点直连——装配期（`app.go` / `gateway/source.go`）走
// **事实出口** `CredentialStore()`，包内两处走 `s.creds` / `s.providerPlane`。
//
// **为什么这条判据值得单独存在**：这一族的失效方式与本仓其它转发层同形（一行转发的正确性完全
// 等价于被转发的那一行，所以它写错与否都不会让任何测试变红），但它还多一种——**同名符号跨包**
// 会让"没人用"看起来像"有人用"：`internal/domain/provider` 里**也有** `credentialDoc` /
// `mirrorSet` / `mirrorDelete` 三个同名私有方法（那是域自己的镜像写口，不是这里的转发），
// 于是那三条转发在本刀的 `TestManagerHasNoDeadPrivateSymbols`（按**名字**数引用）眼里是"活的"。
// 判据两段、fail-closed：
//
//	① manager 生产目录里零命中那八个名字的转发声明；
//	② 装配点必须走事实出口（`a.svc.Planes().Credentials`），否则"实现搬到哪一层"重新不可见。
func TestCredentialFaceHasNoTransitionalForwards(t *testing.T) {
	root := backendRoot(t)
	mgr := dirProductionBody(t, filepath.Join(root, "internal", "sessionruntime"))
	if len(mgr) < 1000 {
		t.Fatalf("`internal/sessionruntime` 只扫到 %d 字节——扫描面解析错了？（fail-closed）", len(mgr))
	}
	for _, dead := range []string{
		"func (s *Service) SetCredentials(",
		"func (s *Service) SetCredentialCwd(",
		"func (s *Service) credentialCwd(",
		"func (s *Service) credentialDoc(",
		"func (s *Service) SyncCredentialMirror(",
		"func (s *Service) providerEnvKeyNames(",
		"func (s *Service) mirrorSet(",
		"func (s *Service) mirrorDelete(",
		"func (s *Service) resolveProviderKey(",
	} {
		if strings.Contains(mgr, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：凭据面的过渡转发已随 P4a 正文第五十一刀清零"+
				"（装配走 `CredentialStore()`，包内走 `s.creds` / `s.providerPlane.KeyFor`）。", dead)
		}
	}
	// 事实出口必须在（少了它，装配点只能再造一份转发）。
	svcSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "sessionruntime", "service.go")))
	// P4b：`CredentialStore()` 也删了——事实出口收敛成 `Planes()`（一份实例，一条路径）。
	if !strings.Contains(svcSrc, "func (s *Service) Planes() *registry.Planes") {
		t.Error("`internal/sessionruntime/service.go` 里找不到 `Planes()` 事实出口：装配期没有它就只能" +
			"再写一层转发，而那正是本刀删掉的东西。")
	}
	// P5a 起凭据面由 `registry.Planes.Credentials` 拥有，装配动作在 app/assembly.go。
	appSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "assembly.go")))
	for _, want := range []string{
		"a.planes.Credentials.SetCwd(",
		"a.planes.Credentials.SetLayered(",
		"a.planes.Credentials.SyncCredentialMirror(",
	} {
		if !strings.Contains(appSrc, want) {
			t.Errorf("`internal/app/assembly.go` 里找不到 `%s`：装配期的凭据登记必须直连事实出口。"+
				"漏了会让这一条链悄悄退回某个中间层。", want)
		}
	}
	if gwSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "gateway", "source.go"))); !strings.Contains(gwSrc, "s.planes.Credentials.SetLayered(") {
		t.Error("`internal/gateway/source.go` 里找不到 `s.planes.Credentials.SetLayered(`：" +
			"网关装配必须直连事实（P5a 起是 registry.Planes，不再是 manager 转发）。")
	}
}
