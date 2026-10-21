package archtest

import (
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// scanExempt 不参与层表映射的包（§3.2.2 注）。
var scanExempt = []string{
	"internal/testutil",
	"internal/archtest",
}

// ——— 生产包扫描器（v3.2 刀 11：从已删的 v3.1 层表文件里留下）———
//
// 只做一件事：列出 backend/internal 与 backend/pkg 下的**生产**包及其 import。
// 层表映射由各自的表负责（v3.2 用 `v32Resolve`），扫描器不认识层号。

// pkgInfo 一个生产包的扫描结果。
type pkgInfo struct {
	dir     string
	rel     string
	imports []string
}

// scanProductionPackages 扫描 backend/internal 与 backend/pkg 下的全部生产包（跳过 _test.go）。
func scanProductionPackages(t *testing.T) []pkgInfo {
	t.Helper()
	root := backendRoot(t)
	var out []pkgInfo
	for _, top := range []string{"internal", "pkg"} {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		for _, dir := range goPackagesUnder(t, base) {
			relPath := filepath.ToSlash(rel(root, dir))
			pkg, err := build.ImportDir(dir, 0)
			if err != nil {
				t.Fatalf("解析包 %s 失败: %v", relPath, err)
			}
			// 只看生产 import：build.ImportDir 的 pkg.Imports 不含 _test.go。
			out = append(out, pkgInfo{dir: dir, rel: relPath, imports: pkg.Imports})
		}
	}
	if len(out) == 0 {
		t.Fatalf("层表守卫失效：internal/ 与 pkg/ 下扫到 0 个包")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// importPathOf 把 import 字符串去掉 module 前缀（非本仓 import 返回空）。
func importPathOf(imp string) string {
	if !strings.HasPrefix(imp, modulePrefix) {
		return ""
	}
	return strings.TrimPrefix(imp, modulePrefix)
}

// isScanExempt 判断相对路径是否落在豁免面（含其子包）。
func isScanExempt(relPath string) bool {
	for _, e := range scanExempt {
		if relPath == e || strings.HasPrefix(relPath, e+"/") {
			return true
		}
	}
	return false
}

func TestRuntimeDoesNotImportRuntimeAdapters(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "runtime")
	if !hasGoFiles(dir) {
		if true {
			t.Fatalf("internal/runtime 不存在（P2 尚未完成？）")
		}
		return
	}
	for _, imp := range importedByProductionPackage(t, dir) {
		if strings.HasPrefix(imp, modulePrefix+"internal/adapters/outbound/dsh") {
			t.Errorf("adapter 边界：internal/runtime 不得 import %s", imp)
		}
	}
}

// TestOnlyAssemblyImportsRuntimeAdapters 只有 internal/bootstrap/assembly 可以 import runtime adapter。
func TestOnlyAssemblyImportsRuntimeAdapters(t *testing.T) {
	root := backendRoot(t)
	pkgs := goPackagesUnder(t, filepath.Join(root, "internal"))
	var hits []string
	for _, dir := range pkgs {
		relPath := filepath.ToSlash(rel(root, dir))
		if relPath == "internal/bootstrap/assembly" || isScanExempt(relPath) {
			continue
		}
		// 只看**生产** import：测试夹具直接用真实 adapter 是合法的（v3.1 §3.2.1 规则 5 管的是
		// 生产代码的依赖方向；把测试包也算进来会让"装配边唯一"退化成"谁都不许写测试夹具"）。
		for _, imp := range importedByProductionPackage(t, dir) {
			if strings.HasPrefix(imp, modulePrefix+"internal/adapters/outbound/dsh") {
				hits = append(hits, relPath+" → "+imp)
			}
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("层表守卫失效：internal/ 下扫到 0 个包")
	}
	for _, h := range hits {
		if true {
			t.Errorf("adapter 装配边：只有 internal/bootstrap/assembly 可以 import infra/dsh；违规 %s", h)
		} else {
			t.Logf("P0 报告（adapter 装配边）：%s", h)
		}
	}
}

// TestAdapterStaysOutsideAppCore adapter 不得 import 应用内核。
func TestAdapterStaysOutsideAppCore(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "adapters", "outbound", "dsh")
	if !hasGoFiles(dir) {
		return // P2 之前不存在
	}
	forbidden := []string{
		"internal/bootstrap/app", "internal/adapters/inbound/cli/cmd", "internal/bootstrap/assembly", "internal/adapters/inbound/http/router", "internal/delivery/controller",
		"internal/app", "internal/app/run", "internal/drivers/registry", "internal/adapters/outbound/sqlite/store",
		"internal/adapters/outbound/sqlite/dao", "internal/adapters/outbound/sqlite/model", "internal/domain", "internal/drivers/mechanism", "internal/drivers/gateway",
		"internal/adapters/outbound/runtimeconn",
	}
	for _, imp := range importedByPackage(t, dir) {
		relImp := importPathOf(imp)
		for _, bad := range forbidden {
			if relImp == bad || strings.HasPrefix(relImp, bad+"/") {
				t.Errorf("adapter 边界：internal/adapters/outbound/dsh 不得 import %s", imp)
			}
		}
	}
}
