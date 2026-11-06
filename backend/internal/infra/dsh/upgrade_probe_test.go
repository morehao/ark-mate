package dsh

// 一次性升级探针（0.2.1-rc.2 → 0.2.2-rc.1）。
//
// 本文件**不是常驻单测**：默认跳过，只在显式 `DSH_UPGRADE_PROBE=1` 且指定 -run 时才跑。
// 它用**本包真实的** buildPatch / buildIdentityPatch 生成模型 patch 与身份 patch，然后把
// 随包载荷（0.2.2-rc.1）的 bin.js 当 `dsh` 递给 `--profile acp --patch <abs> --dump-config-schema`，
// 判据 = exit0 + x-cordis.complete=true + diagnostics 空。用来在版本升级后复验 A9 的校验门。
//
// 环境变量：
//   DSH_UPGRADE_PROBE=1        开启
//   DSH_PROBE_PAYLOAD=<abs>    载荷目录（含 node_modules/@deepseek-ai/dsh/lib/bin.js）
//   DSH_PROBE_TMP=<abs>        隔离 HOME/DSH_HOME 的工作根（默认仓库 .dsh/tmp）

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	pkgidentity "github.com/morehao/ark-mate/internal/core/identity"
	"github.com/morehao/ark-mate/internal/core/resource"
)

// buildProbeModelSet 造一份**平台会真实生成**的模型清单：一条自定义 route + 两条模型（一条声明容量、
// 一条不声明），默认模型指向其中一条。这样覆盖 buildPatch 里 provider 声明 + 容量 + input 三块路径。
func buildProbeModelSet() resource.Set {
	in := int64(1_000_000)
	out := int64(128_000)
	return resource.Set{
		Runtime: "dsh",
		Providers: []resource.Provider{
			{
				ID:       "prov-probe",
				Name:     "Probe Provider",
				Protocol: resource.ProtocolOpenAI,
				BaseURL:  "https://api.example.com/v1",
				Ref:      "MATE_PROBE_API_KEY",
			},
		},
		Models: []resource.Model{
			{
				ID: "probe-a", ProviderID: "prov-probe", Model: "probe-model-a", Name: "Probe A",
				InputModalities: []string{"text"},
				MaxInputTokens:  &in, MaxOutputTokens: &out,
			},
			{
				ID: "probe-b", ProviderID: "prov-probe", Model: "probe-model-b",
				InputModalities: []string{"text", "image"},
			},
		},
		DefaultModel: "probe-a",
	}
}

func TestUpgradeProbeModelPatchDump(t *testing.T) {
	runDumpProbe(t, "model", func() ([]byte, error) {
		return buildPatch(buildProbeModelSet())
	})
}

func TestUpgradeProbeIdentityPatchDump(t *testing.T) {
	runDumpProbe(t, "identity", func() ([]byte, error) {
		return buildIdentityPatch(pkgidentity.Spec{
			SystemPrompt: "你是 ark-mate 内置的产品身份探针文本，用于验证 0.2.2-rc.1 的 system-prompt patch 门。",
		})
	})
}

func runDumpProbe(t *testing.T, label string, gen func() ([]byte, error)) {
	if os.Getenv("DSH_UPGRADE_PROBE") != "1" {
		t.Skip("升级探针默认跳过：设 DSH_UPGRADE_PROBE=1 显式运行")
	}
	payload := os.Getenv("DSH_PROBE_PAYLOAD")
	if payload == "" {
		t.Fatal("DSH_PROBE_PAYLOAD 未设（应指向 0.2.2-rc.1 载荷目录）")
	}
	tmp := os.Getenv("DSH_PROBE_TMP")
	if tmp == "" {
		t.Fatal("DSH_PROBE_TMP 未设（隔离工作根）")
	}
	bin := filepath.Join(payload, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("载荷 bin.js 不存在：%s：%v", bin, err)
	}

	data, err := gen()
	if err != nil {
		t.Fatalf("生成 %s patch 失败：%v", label, err)
	}
	work := filepath.Join(tmp, "probe-"+label)
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(filepath.Join(work, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	cand := filepath.Join(work, label+".patch.yml")
	if err := os.WriteFile(cand, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// node 路径：与当前测试进程同一个 node（构建期不假设 PATH 上有 dsh，直接跑 bin.js）。
	node, err := exec.LookPath("node")
	if err != nil {
		// 退回用 go 运行时能拿到的常见位置
		node = "node"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, node, bin, "--profile", "acp", "--patch", cand, dumpSchemaFlag)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"HOME="+filepath.Join(work, "home"),
		"DSH_HOME="+filepath.Join(work, "home", "dsh"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	exit := 0
	if ee, ok := runErr.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if runErr != nil {
		t.Fatalf("跑 dump 失败：%v（stderr=%s）", runErr, stderr.String())
	}

	// 落证据文件，便于人工复核。
	_ = os.WriteFile(filepath.Join(work, "dump.stdout.json"), stdout.Bytes(), 0o600)
	_ = os.WriteFile(filepath.Join(work, "dump.stderr.txt"), stderr.Bytes(), 0o600)

	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("[%s] dump 输出不是 JSON（exit=%d，头部=%q，stderr=%q）", label, exit, firstLineProbe(stdout.String()), firstLineProbe(stderr.String()))
	}
	xc, _ := doc["x-cordis"].(map[string]any)
	if xc == nil {
		t.Fatalf("[%s] dump 无 x-cordis 段（exit=%d）", label, exit)
	}
	complete, _ := xc["complete"].(bool)
	var diags []any
	if d, ok := xc["diagnostics"].([]any); ok {
		diags = d
	}
	t.Logf("[%s] exit=%d complete=%v diagnostics=%v", label, exit, complete, diags)
	if exit != 0 {
		t.Fatalf("[%s] 退出码 %d != 0", label, exit)
	}
	if !complete {
		t.Fatalf("[%s] x-cordis.complete=false", label)
	}
	if len(diags) != 0 {
		t.Fatalf("[%s] diagnostics 非空：%v", label, diags)
	}
}

func firstLineProbe(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
