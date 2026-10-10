package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/resource"
)

// fakeDelivery 记录 adapter 请求平台改的启动面（真实实现是注册表的 runtimeDelivery）。
type fakeDelivery struct {
	env     map[string]string
	args    map[string]string
	envCall int
}

func newFakeDelivery() *fakeDelivery {
	return &fakeDelivery{env: map[string]string{}, args: map[string]string{}}
}

func (f *fakeDelivery) SetEnv(key, value string) error {
	f.env[key] = value
	f.envCall++
	return nil
}

func (f *fakeDelivery) SetArg(flag, value string) error { f.args[flag] = value; return nil }

func testSet() resource.Set {
	return resource.Set{
		Runtime: ID,
		Providers: []resource.Provider{
			{ID: "gw", Name: "网关", Protocol: resource.ProtocolOpenAI, BaseURL: "https://gw.example.com/v1",
				APIKey: "sk-x", Ref: resource.RefFor(goldenRefPattern, "gw")},
		},
		Models: []resource.Model{
			{ID: "m1", ProviderID: "gw", ModelID: "gpt-4o-mini", Name: "小模型"},
			{ID: "m2", ProviderID: "gw", ModelID: "gpt-4o", Name: "大模型"},
		},
		DefaultModel: "m2",
	}
}

func TestSyncModelsWritesConfigAndInjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode", "opencode.json")
	d := newFakeDelivery()
	s := NewSyncer(Options{ConfigPath: path}, d)

	out, err := s.SyncModels(context.Background(), testSet())
	if err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if out.Status != resource.StatusApplied {
		t.Fatalf("首发应报 applied，实际 %q（problems=%v）", out.Status, out.Problems)
	}
	if out.Effect != resource.EffectNextProcess {
		t.Fatalf("清单的生效时刻应是 next_process，实际 %q", out.Effect)
	}
	if out.Credential.Channel != resource.ChannelEnv || !out.Credential.Tested {
		t.Fatalf("opencode 的凭据通道应是已实测的 env 通道，实际 %+v", out.Credential)
	}
	// 文件落盘 + 权限从紧。
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("配置文件未落盘：%v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("配置文件权限应为 0600，实际 %04o", perm)
	}
	b, _ := os.ReadFile(path)
	if got := d.env[EnvConfigContent]; got != string(b) {
		t.Fatalf("内联注入的内容应与文件逐字节相同（注入 %d 字节，文件 %d 字节）", len(got), len(b))
	}
	// 默认模型按平台 id 解析成 route/上游 id。
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "gw/gpt-4o" {
		t.Fatalf("默认模型应为 gw/gpt-4o（平台 id m2 → 上游 id），实际 %v", doc["model"])
	}
}

func TestSyncModelsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	d := newFakeDelivery()
	s := NewSyncer(Options{ConfigPath: path}, d)

	if _, err := s.SyncModels(context.Background(), testSet()); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	fi1, _ := os.Stat(path)

	out, err := s.SyncModels(context.Background(), testSet())
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != resource.StatusUnchanged {
		t.Fatalf("同一声明重复同步应报 unchanged（不重写字节），实际 %q", out.Status)
	}
	second, _ := os.ReadFile(path)
	fi2, _ := os.Stat(path)
	if string(first) != string(second) {
		t.Fatal("重复同步改动了文件字节")
	}
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("重复同步重写了文件（mtime 变了）——这会让代际键无谓换代")
	}
	// 注入仍然每次都要设（重启后注入面是空的，必须靠一次同步重建）。
	if d.envCall < 2 {
		t.Fatalf("注入应每次同步都设一次（同键替换），实际调用 %d 次", d.envCall)
	}
}

func TestSyncModelsEmptySetRevokes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	d := newFakeDelivery()
	s := NewSyncer(Options{ConfigPath: path}, d)

	if _, err := s.SyncModels(context.Background(), testSet()); err != nil {
		t.Fatal(err)
	}
	out, err := s.SyncModels(context.Background(), resource.Set{Runtime: ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != resource.StatusApplied {
		t.Fatalf("撤回已有交付应报 applied，实际 %q", out.Status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("空集必须删除配置文件，实际 err=%v", err)
	}
	if v, ok := d.env[EnvConfigContent]; !ok || v != "" {
		t.Fatalf("空集必须撤销内联注入（键必须存在且为空 = 撤销），实际 %q ok=%v", v, ok)
	}
	if out.Credential.Channel != resource.ChannelNone {
		t.Fatalf("空集不应声明任何凭据通道，实际 %+v", out.Credential)
	}
}

func TestSyncModelsEmptySetUnchangedWhenNothingToRevoke(t *testing.T) {
	dir := t.TempDir()
	d := newFakeDelivery()
	s := NewSyncer(Options{ConfigPath: filepath.Join(dir, "opencode.json")}, d)

	out, err := s.SyncModels(context.Background(), resource.Set{Runtime: ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != resource.StatusUnchanged {
		t.Fatalf("本来就没有交付过，空集应报 unchanged，实际 %q", out.Status)
	}
	if v, ok := d.env[EnvConfigContent]; !ok || v != "" {
		t.Fatalf("撤销注入必须显式调用 SetEnv(key, \"\")，实际 %q ok=%v", v, ok)
	}
}

func TestSelfCheckRejectsV1Shape(t *testing.T) {
	// v1 的形态（`provider` 单数）在 v2 上是静默的未知字段——自校验必须拒绝它，
	// 否则"生成器回退到 v1"这种事故一路静默。
	cases := map[string]string{
		"v1_key":    `{"provider":{"x":{}}}`,
		"no_models": `{"providers":{"x":{"name":"x"}}}`,
		"not_json":  `{`,
		"empty":     ``,
	}
	for name, data := range cases {
		if err := selfCheck([]byte(data)); err == nil {
			t.Errorf("%s：自校验应拒绝这份内容", name)
		}
	}
}

func TestGenerateOmitsOptionsSinceV1(t *testing.T) {
	b, err := buildConfig(testSet())
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"options"`, `"api"`, `"compat"`} {
		if strings.Contains(string(b), banned) {
			t.Errorf("产出里不得出现 %s（V1 的成员名，ADR-11 点名修正）", banned)
		}
	}
	if !strings.Contains(string(b), ProviderPackage) {
		t.Errorf("产出里应有官方原生 package %q", ProviderPackage)
	}
}
