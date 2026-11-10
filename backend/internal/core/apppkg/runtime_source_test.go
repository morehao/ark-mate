package apppkg

import (
	"testing"

	"github.com/morehao/ark-mate/internal/core/appslot"
)

// ——— WP-C2：manifest.provides.impls → appslot.ImplSource 对接 ———

func mkPkg(t *testing.T, id string, impls ...ManifestImpl) *Package {
	t.Helper()
	return &Package{Manifest: &Manifest{
		ID:       id,
		Provides: ManifestProvides{Impls: impls},
	}}
}

// TestRuntimeImplSpecsMapsPackageID 摊平后每个 spec 带 PackageID（= 包 id），并按 impl_id 升序。
func TestRuntimeImplSpecsMapsPackageID(t *testing.T) {
	pkgs := []*Package{
		mkPkg(t, "pkg-b", ManifestImpl{ImplID: "b.two", Kind: "slot"}),
		mkPkg(t, "pkg-a", ManifestImpl{ImplID: "a.one", Kind: "slot"}, ManifestImpl{ImplID: "a.two", Kind: "entity"}),
	}
	specs := RuntimeImplSpecs(pkgs)
	if len(specs) != 3 {
		t.Fatalf("应有 3 条 impl 声明，got %d: %+v", len(specs), specs)
	}
	if specs[0].ImplID != "a.one" || specs[1].ImplID != "a.two" || specs[2].ImplID != "b.two" {
		t.Fatalf("应按 impl_id 升序，got %+v", specs)
	}
	for _, sp := range specs {
		want := "pkg-a"
		if sp.ImplID == "b.two" {
			want = "pkg-b"
		}
		if sp.PackageID != want {
			t.Errorf("impl %q 的 PackageID 应为 %q，got %q", sp.ImplID, want, sp.PackageID)
		}
	}
}

// TestCombinedImplSourceHasRuntimeAndCompiled compiled ∪ runtime：已装包清单里在册的 impl 判 Has；
// runtime 未声明的 id 不在册（即便像 impl）。
func TestCombinedImplSourceHasRuntimeAndCompiled(t *testing.T) {
	// 造一个编译期来源：直接用一个空 apppkg 集，再验证 runtime 里声明的 impl 在册。
	pkgs := []*Package{
		mkPkg(t, "vendor-x", ManifestImpl{ImplID: "vendorx.sync", Kind: "slot"}),
	}
	combined := CombinedImplSource(pkgs)
	if !combined.Has("vendorx.sync") {
		t.Errorf("已装包 manifest 声明的 impl 应 Has=true")
	}
	if combined.Has("vendorx.notdeclared") {
		t.Errorf("未声明的 impl 不应在册（白名单不退化）")
	}
	// 运行期 impl 在 C3 前不可执行：Has 真、Impl 假。
	if _, ok := combined.Impl("vendorx.sync"); ok {
		t.Errorf("运行期 impl 在 C3 host 就绪前不应有可执行函数")
	}
}

// TestCombinedImplSourceEmpty 空包集 → 合法（只回落到编译期），不 panic。
func TestCombinedImplSourceEmpty(t *testing.T) {
	combined := CombinedImplSource(nil)
	if combined == nil {
		t.Fatalf("空包集应返回非 nil 来源")
	}
	// 空集不 panic 且不在册任意 id。
	if combined.Has("anything") {
		t.Errorf("空来源不应在册任何 id")
	}
	// 确认类型实现 appslot.ImplSource（编译期检查）。
	var _ appslot.ImplSource = combined
}
