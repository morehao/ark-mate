package commandpolicy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/contract/dtocommandpolicy"
)

func TestPolicyGetDisabled(t *testing.T) {
	svc := New("", nil)
	resp, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Enabled || len(resp.Rules) != 0 {
		t.Fatalf("未配置时应 enabled=false / 空规则，got %+v", resp)
	}
	if _, err := svc.Put(context.Background(), dtocommandpolicy.PolicyReq{
		Rules: []dtocommandpolicy.Rule{{Pattern: [][]string{{"rm"}}, Decision: "forbidden"}},
	}); err == nil {
		t.Fatal("未配置时写面必须拒绝（否则会写到一个没人会读的路径）")
	}
}

func TestPolicyPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "command_policy.json")
	var applied []interaction.PrefixRule
	svc := New(path, func(r []interaction.PrefixRule) { applied = r })

	// 文件不存在：已配置、空规则。
	resp, err := svc.Get(context.Background())
	if err != nil || !resp.Enabled || len(resp.Rules) != 0 {
		t.Fatalf("文件不存在时应 enabled=true / 空规则，got (%+v,%v)", resp, err)
	}

	req := dtocommandpolicy.PolicyReq{Rules: []dtocommandpolicy.Rule{
		{Name: "deny rm", Pattern: [][]string{{"rm"}}, Decision: "forbidden",
			MatchSamples: [][]string{{"rm", "-rf", "/"}}},
		{Name: "allow git status", Pattern: [][]string{{"git"}, {"status"}}, Decision: "allow"},
	}}
	if _, err := svc.Put(context.Background(), req); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("落盘成功后必须立即生效（apply 收到 %d 条）", len(applied))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("规则文件未落盘: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("规则文件权限 = %o，want 0600（规则里可能有内网路径）", info.Mode().Perm())
	}
	resp, err = svc.Get(context.Background())
	if err != nil || len(resp.Rules) != 2 || resp.Rules[0].Decision != "forbidden" {
		t.Fatalf("回读不符: %+v (err=%v)", resp, err)
	}

	// 非法规则：整表拒绝、**不落盘**（旧文件保持原样）、不生效。
	before, _ := os.ReadFile(path)
	if _, err := svc.Put(context.Background(), dtocommandpolicy.PolicyReq{Rules: []dtocommandpolicy.Rule{
		{Pattern: [][]string{{"ls"}}, Decision: "allow", MatchSamples: [][]string{{"rm"}}},
	}}); err == nil {
		t.Fatal("样例断言不通过必须拒绝")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("坏规则不得覆盖盘上的旧规则")
	}
}
