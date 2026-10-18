package runtime

import (
	"context"
	"reflect"
	"testing"

	pkgidentity "github.com/morehao/ark-mate/internal/identity"
	"github.com/morehao/ark-mate/internal/resource"
)

// ——— 启动面多值参数（`--patch`）与身份端口表的静态判据 ———
//
// `replaceArgs` 有一个"看起来对"的坏形态：只摘**第一次**出现的 flag。剩下的重复参数在底座侧
// 是"叠加两层"——旧层仍压在上面，表现为"改了但像是没改"，而平台侧每次交付都报 applied。

func TestReplaceArgsReplacesAllOccurrences(t *testing.T) {
	args := []string{"--profile", "acp", "--patch", "/old/a.yml", "--patch", "/old/b.yml"}
	got := replaceArgs(args, "--patch", []string{"/m.yml", "/i.yml"})
	want := []string{"--profile", "acp", "--patch", "/m.yml", "--patch", "/i.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replaceArgs 结果不符：\n got=%v\nwant=%v", got, want)
	}

	// 空 values = 摘掉**所有**出现（不是摘一条）。
	got = replaceArgs(got, "--patch", nil)
	if want := []string{"--profile", "acp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("空 values 应摘掉全部出现：got=%v want=%v", got, want)
	}
}

func TestReplaceArgsHandlesEqualsForm(t *testing.T) {
	args := []string{"--patch=/old.yml", "--profile", "acp"}
	got := replaceArgs(args, "--patch", []string{"/new.yml"})
	want := []string{"--profile", "acp", "--patch", "/new.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("`--flag=value` 形态未被摘除：got=%v want=%v", got, want)
	}
}

// fakeIdentityAdapter 记录收到的 Spec（身份端口表的登记/取用判据）。
type fakeIdentityAdapter struct {
	got   []pkgidentity.Spec
	out   resource.Outcome
	calls int
}

func (f *fakeIdentityAdapter) DeliverIdentity(_ context.Context, spec pkgidentity.Spec) (resource.Outcome, error) {
	f.calls++
	f.got = append(f.got, spec)
	return f.out, nil
}

// TestDeliverySetArgsReplacesLaunchArgs 端口 `SetArgs` 落到 `Spec.Launch.Args`：
// 取值按传入顺序、旧的出现全部摘掉（这是模型清单与身份共存的机器保证）。
func TestDeliverySetArgsReplacesLaunchArgs(t *testing.T) {
	r := NewRegistry(Spec{ID: "x", Launch: LaunchSpec{Command: "x", Args: []string{"--profile", "acp"}}})
	d := r.DeliveryFor("x")
	if err := d.SetArgs("--patch", []string{"/m.yml", "/i.yml"}); err != nil {
		t.Fatalf("SetArgs: %v", err)
	}
	spec, _ := r.Get("x")
	want := []string{"--profile", "acp", "--patch", "/m.yml", "--patch", "/i.yml"}
	if !reflect.DeepEqual(spec.Launch.Args, want) {
		t.Fatalf("启动参数不符：got=%v want=%v", spec.Launch.Args, want)
	}
	// 单值 `SetArg` 在多值之下仍然成立（等价于一个元素的 `SetArgs`）。
	if err := d.SetArg("--patch", "/only.yml"); err != nil {
		t.Fatalf("SetArg: %v", err)
	}
	spec, _ = r.Get("x")
	if want := []string{"--profile", "acp", "--patch", "/only.yml"}; !reflect.DeepEqual(spec.Launch.Args, want) {
		t.Fatalf("SetArg 应覆盖同一 flag 的全部取值：got=%v want=%v", spec.Launch.Args, want)
	}
}

// TestIdentityTableRoundTrip 身份端口表：登记后能取到，未登记 = 如实 false。
func TestIdentityTableRoundTrip(t *testing.T) {
	r := NewRegistry(Spec{ID: "x"})
	if _, ok := r.Identity("x"); ok {
		t.Fatal("未登记时不应取到身份实现")
	}
	fake := &fakeIdentityAdapter{}
	r.RegisterIdentity("x", fake)
	got, ok := r.Identity("x")
	if !ok {
		t.Fatal("登记后应能取到身份实现")
	}
	if _, same := got.(*fakeIdentityAdapter); !same {
		t.Fatal("取回的不是登记的那个实现")
	}
}
