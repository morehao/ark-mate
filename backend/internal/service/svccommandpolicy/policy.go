// Package svccommandpolicy 是命令门禁（A2）的**用例层**：读规则、校验、落盘、立即生效。
//
// 它拥有：参数校验（交给域的 `LoadCommandRules`）、错误码出口、响应装配、原子落盘。
// 它**不拥有**：裁决本身（域：`interaction.GateCommand`）与"规则怎么被用"（执行面：
// `manager.Orchestrator`）——后者由装配点交一个 `Apply` 回调，本包不 import manager。
package svccommandpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/dto/dtocommandpolicy"
)

// PolicySvc 命令门禁读写面（实现 `CommandPolicySvc`）。
type PolicySvc interface {
	Get(ctx context.Context) (*dtocommandpolicy.PolicyResp, error)
	Put(ctx context.Context, req dtocommandpolicy.PolicyReq) (*dtocommandpolicy.PolicyResp, error)
}

// Policy 实现 PolicySvc。
type Policy struct {
	path  string
	apply func([]interaction.PrefixRule)
}

// New 造一个门禁用例。`path` 为空 = 未配置（读面 enabled=false，写面拒绝）；
// `apply` 为空 = 只落盘、不热生效（装配层没给执行面时的退化，测试用）。
func New(path string, apply func([]interaction.PrefixRule)) PolicySvc {
	return &Policy{path: path, apply: apply}
}

var _ PolicySvc = (*Policy)(nil)

// Get 读当前规则。文件不存在 → enabled=true + 空规则（"已配置但还没写规则"）。
func (p *Policy) Get(_ context.Context) (*dtocommandpolicy.PolicyResp, error) {
	if p.path == "" {
		return &dtocommandpolicy.PolicyResp{Enabled: false, Rules: []dtocommandpolicy.Rule{}}, nil
	}
	rules, err := interaction.LoadCommandPolicyFile(p.path)
	if err != nil {
		return nil, code.FromError(code.ServiceError("command_policy_unreadable", fmt.Sprintf("命令门禁规则文件读不出来 [command_policy_unreadable]: %v", err)))
	}
	return &dtocommandpolicy.PolicyResp{Enabled: true, Rules: toDTO(rules)}, nil
}

// Put 全量替换规则：校验 → 原子落盘 → 立即生效。
//
// 顺序刻意如此：**先校验、再写、最后生效**。反过来（先生效后写）会出现"这一秒门禁按新规则
// 拦人、磁盘上还是旧规则"，重启后行为悄悄回退；写失败与生效失败都回滚不了内存，所以
// 只允许"盘上是新的"才动内存。
func (p *Policy) Put(_ context.Context, req dtocommandpolicy.PolicyReq) (*dtocommandpolicy.PolicyResp, error) {
	if p.path == "" {
		return nil, code.FromError(code.RequestError("command_policy_disabled", "命令门禁未配置（MATE_COMMAND_POLICY 为空）[command_policy_disabled]"))
	}
	rules, err := interaction.LoadCommandRules(toDomain(req.Rules))
	if err != nil {
		return nil, code.FromError(code.RequestError("command_policy_invalid", fmt.Sprintf("命令门禁规则不合法 [command_policy_invalid]: %v", err)))
	}
	if err := writePolicyFile(p.path, rules); err != nil {
		return nil, code.FromError(code.ServiceError("command_policy_write", fmt.Sprintf("命令门禁规则写入失败 [command_policy_write]: %v", err)))
	}
	if p.apply != nil {
		p.apply(rules)
	}
	return &dtocommandpolicy.PolicyResp{Enabled: true, Rules: toDTO(rules)}, nil
}

// writePolicyFile 原子写（同目录临时文件 + rename），权限 0600（规则里可能有内网路径）。
func writePolicyFile(path string, rules []interaction.PrefixRule) error {
	body, err := json.MarshalIndent(interaction.CommandPolicyFile{Rules: rules}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".command_policy-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // rename 成功后这条是 no-op
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func toDomain(in []dtocommandpolicy.Rule) []interaction.PrefixRule {
	out := make([]interaction.PrefixRule, 0, len(in))
	for _, r := range in {
		out = append(out, interaction.PrefixRule{
			Name:            r.Name,
			Pattern:         r.Pattern,
			Decision:        interaction.CommandDecision(r.Decision),
			MatchSamples:    r.MatchSamples,
			NotMatchSamples: r.NotMatchSamples,
			HostExecutable:  r.HostExecutable,
		})
	}
	return out
}

func toDTO(in []interaction.PrefixRule) []dtocommandpolicy.Rule {
	out := make([]dtocommandpolicy.Rule, 0, len(in))
	for _, r := range in {
		out = append(out, dtocommandpolicy.Rule{
			Name:            r.Name,
			Pattern:         r.Pattern,
			Decision:        string(r.Decision),
			MatchSamples:    r.MatchSamples,
			NotMatchSamples: r.NotMatchSamples,
			HostExecutable:  r.HostExecutable,
		})
	}
	return out
}
