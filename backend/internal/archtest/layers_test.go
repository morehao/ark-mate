package archtest

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePrefix = "github.com/morehao/ark-mate/"

// backendRoot 返回 backend/ 目录（本包位于 backend/internal/archtest）。
func backendRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析 backend 根目录失败: %v", err)
	}
	return root
}

// hasGoFiles 判断目录下是否直接存在 .go 文件。
// 用于跳过没有 Go 文件的目录（历史上 `pkg/web/dist` 就是这样一个 embed 宿主目录，
// 2026-10-10 随 `//go:embed` 一起删除）。
func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// importedByPackage 解析某目录下所有 .go 文件（含测试）的 import 列表。
// 用 build.ImportDir 而非正则：能正确处理分组、别名与多行 import。
func importedByPackage(t *testing.T, dir string) []string {
	t.Helper()
	pkg, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("解析包 %s 失败: %v", dir, err)
	}
	var all []string
	all = append(all, pkg.Imports...)
	all = append(all, pkg.TestImports...)
	all = append(all, pkg.XTestImports...)
	return all
}

// subdirs 返回 parent 下的一级子目录（跳过 testdata 与隐藏目录）。
func subdirs(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("读取目录 %s 失败: %v", parent, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "testdata" {
			continue
		}
		out = append(out, filepath.Join(parent, e.Name()))
	}
	return out
}

// TestPkgMustNotImportInternal 锁定 pkg/ 与 internal/ 的单向依赖：
// pkg/ 是可复用基础设施层（对齐 ark-iam 的 backend/pkg），只能依赖标准库、第三方库
// 与 pkg/ 内部；一旦反向 import internal/，pkg 就不再可复用，将来抽取独立 module
// 或复用到别的应用会立刻失败。
func TestPkgMustNotImportInternal(t *testing.T) {
	root := backendRoot(t)
	// pkg/a/b/c 这类嵌套包也要覆盖：逐层下钻到叶子。
	var walk func(dir string)
	walk = func(dir string) {
		if !hasGoFiles(dir) {
			return
		}
		for _, imp := range importedByPackage(t, dir) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				t.Errorf("分层违规：%s 不应 import %s（pkg 层必须与 internal 应用层解耦）",
					rel(root, dir), imp)
			}
		}
		for _, sub := range subdirs(t, dir) {
			walk(sub)
		}
	}
	for _, dir := range subdirs(t, filepath.Join(root, "pkg")) {
		walk(dir)
	}
}

// TestTransportFreeLayersMustNotImportAppLayers 锁定传输无关的底层包不得反向依赖
// 应用装配层：事件总线（internal/eventbus）与持久层（internal/store）都被上层广泛
// 引用，一旦它们 import manager/server 就会形成环，并把"可被任意宿主复用"的能力
// 绑死在当前装配方式上。
func TestTransportFreeLayersMustNotImportAppLayers(t *testing.T) {
	root := backendRoot(t)
	forbidden := []string{
		modulePrefix + "internal/sessionruntime",
		modulePrefix + "internal/server",
		modulePrefix + "internal/app",
	}
	for _, name := range []string{"eventbus", "store"} {
		dir := filepath.Join(root, "internal", name)
		for _, imp := range importedByPackage(t, dir) {
			for _, bad := range forbidden {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("分层违规：internal/%s 不应 import %s", name, imp)
				}
			}
		}
	}
}

// TestPortsAndDTOsMustNotImportTransport 锁定"传输层 → 端口/DTO"的单向依赖。
//
// 历史形态（本次重构修掉的层反向依赖）：internal/sessionruntime 直接 import
// internal/server/handler，把 handler.AgentResp 等传输层 DTO 当作自己的返回类型。
// 后果是领域层每加一个字段都要改传输层文件，且依赖方向与调用方向相反。
//
// 现在的边界：
//   - internal/dto/dto*  → 纯数据，谁都能依赖它，它不依赖任何应用层
//   - internal/service/svc* → 只依赖 dto，是控制器与实现之间的端口
//   - 传输层（server/handler）依赖端口；实现层（manager）实现端口
func TestPortsAndDTOsMustNotImportTransport(t *testing.T) {
	root := backendRoot(t)
	forbidden := []string{
		modulePrefix + "internal/server",
		modulePrefix + "internal/sessionruntime",
		modulePrefix + "internal/app",
	}
	var walk func(dir string)
	walk = func(dir string) {
		if !hasGoFiles(dir) {
			return
		}
		for _, imp := range importedByPackage(t, dir) {
			for _, bad := range forbidden {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("层反向依赖：%s 不应 import %s（端口/DTO 层必须与传输层、实现层解耦）",
						rel(root, dir), imp)
				}
			}
		}
		for _, sub := range subdirs(t, dir) {
			walk(sub)
		}
	}
	for _, top := range []string{"dto", "service"} {
		for _, dir := range subdirs(t, filepath.Join(root, "internal", top)) {
			walk(dir)
		}
	}
}

// TestControllersMustNotImportImplementations 锁定控制器的依赖面：
// 控制器只允许依赖 internal/service/svc*（端口）与 internal/dto/dto*（数据），
// 不得 import internal/sessionruntime（实现）或 internal/router（自己所属的装配层）。
//
// 这条边界是 P2/P3 重构的核心收益：控制器与实现两边的任一侧改动，
// 只要端口不变就不会互相牵连，也让控制器可以脱离 manager 单测。
func TestControllersMustNotImportImplementations(t *testing.T) {
	root := backendRoot(t)
	forbidden := []string{
		modulePrefix + "internal/sessionruntime",
		modulePrefix + "internal/router",
		modulePrefix + "internal/server",
		modulePrefix + "internal/app",
	}
	for _, dir := range subdirs(t, filepath.Join(root, "internal", "controller")) {
		if !hasGoFiles(dir) {
			continue
		}
		for _, imp := range importedByPackage(t, dir) {
			for _, bad := range forbidden {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("控制器越界依赖：%s 不应 import %s（只允许依赖 service 端口与 dto）",
						rel(root, dir), imp)
				}
			}
		}
	}
}

// TestPersistenceLayersMustNotImportAppLayers 锁定持久层内部的单向依赖。
//
// 三层的职责与允许的依赖方向（单向，不得回指）：
//
//	pkg/dbclient（句柄装配） ← internal/dao（单表数据访问） ← internal/store（跨表事务门面）
//	                 ↑                    ↑
//	          internal/model（实体与列约定，谁都依赖它，它不依赖任何一层）
//
// 具体边界：
//   - internal/model 只描述"库长什么样"，不得 import dao/store/传输层——否则实体定义
//     会被数据访问或业务语义污染，schema 契约也就不再中立；
//   - internal/dao 不得 import store（会与 store→dao 形成环）与传输层；
//   - internal/store 是门面，可以 import dao/model/dbclient，但不得反向 import manager/app/router。
func TestPersistenceLayersMustNotImportAppLayers(t *testing.T) {
	root := backendRoot(t)
	transport := []string{
		modulePrefix + "internal/controller",
		modulePrefix + "internal/router",
		modulePrefix + "internal/dto",
		modulePrefix + "internal/service",
	}
	app := []string{
		modulePrefix + "internal/sessionruntime",
		modulePrefix + "internal/app",
	}

	cases := []struct {
		name      string
		dir       string
		forbidden []string
		reason    string
	}{
		{
			name:      "model 不得依赖数据访问与业务层",
			dir:       filepath.Join(root, "internal", "model"),
			forbidden: append(append([]string{modulePrefix + "internal/store", modulePrefix + "internal/dao"}, app...), transport...),
			reason:    "实体只描述库结构，必须保持中立",
		},
		{
			name:      "dao 不得依赖 store 与上层",
			dir:       filepath.Join(root, "internal", "dao"),
			forbidden: append(append([]string{modulePrefix + "internal/store"}, app...), transport...),
			reason:    "store 已依赖 dao，反向依赖即成环",
		},
		{
			name:      "store 不得依赖传输层与实现层",
			dir:       filepath.Join(root, "internal", "store"),
			forbidden: append(append([]string{}, app...), transport...),
			reason:    "持久层不得感知业务实现与传输形态",
		},
	}
	for _, tc := range cases {
		if !hasGoFiles(tc.dir) {
			t.Errorf("持久层目录 %s 不存在或无 Go 文件", rel(root, tc.dir))
			continue
		}
		for _, imp := range importedByPackage(t, tc.dir) {
			for _, bad := range tc.forbidden {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					t.Errorf("持久层越界依赖：%s 不应 import %s（%s）", rel(root, tc.dir), imp, tc.reason)
				}
			}
		}
	}
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}
