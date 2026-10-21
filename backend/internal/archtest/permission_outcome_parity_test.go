package archtest

import (
	"testing"

	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
)

// TestEffectivePermissionOutcomeParity 「用户点了什么」→「agent 收到什么」这条规则只有一个语义，
// 但实现必然有两份：领域侧（`interaction.EffectivePermissionOutcome`，REST 回执用它）与协议侧
// （`acp.EffectivePermissionOutcome`，真正投递给底座时用它）。领域不能 import 协议库，协议库也
// 不该认识领域，所以两份实现不可能合成一份——只能逐格比对。
//
// 失效方式（为什么值得一条判据）：两份分叉不会让任何东西变红。用户点「本次允许」，界面回执说
// `selected allow_once`，而底座收到的是 `cancelled`——那一轮就此卡住或改走别的分支，而两侧各自的
// 单测都是绿的。反过来也一样：领域说取消、协议投了第一个选项，等于**静默放行**一次未被批准的调用。
//
// 判据是决策 × 选项的**全矩阵**（含空选项、越界 option_id、同时置两个标志的畸形决策），
// 逐格比对 (kind, optionID)。新增一个分支而只改一侧 → 红。
func TestEffectivePermissionOutcomeParity(t *testing.T) {
	domainOpts := []interaction.PermissionOption{
		{OptionID: "allow_once", Name: "本次允许", Kind: "allow_once"},
		{OptionID: "allow_always", Name: "始终允许", Kind: "allow_always"},
		{OptionID: "reject_once", Name: "本次拒绝", Kind: "reject_once"},
	}
	acpOpts := make([]acp.PermissionOption, 0, len(domainOpts))
	for _, o := range domainOpts {
		acpOpts = append(acpOpts, acp.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}

	decisions := []struct {
		name   string
		domain interaction.PermissionDecision
		wire   acp.PermissionDecision
	}{
		{"取消（零值）", interaction.PermissionDecision{}, acp.PermissionDecision{}},
		{"放行-不指定选项", interaction.PermissionDecision{Allowed: true}, acp.PermissionDecision{Allowed: true}},
		{"放行-指定合法选项", interaction.PermissionDecision{Allowed: true, OptionID: "allow_always"},
			acp.PermissionDecision{Allowed: true, OptionID: "allow_always"}},
		{"放行-指定越界选项", interaction.PermissionDecision{Allowed: true, OptionID: "bogus"},
			acp.PermissionDecision{Allowed: true, OptionID: "bogus"}},
		{"拒绝-指定合法选项", interaction.PermissionDecision{Rejected: true, OptionID: "reject_once"},
			acp.PermissionDecision{Rejected: true, OptionID: "reject_once"}},
		{"拒绝-指定越界选项", interaction.PermissionDecision{Rejected: true, OptionID: "bogus"},
			acp.PermissionDecision{Rejected: true, OptionID: "bogus"}},
		{"畸形-两个标志都置", interaction.PermissionDecision{Allowed: true, Rejected: true, OptionID: "allow_once"},
			acp.PermissionDecision{Allowed: true, Rejected: true, OptionID: "allow_once"}},
	}

	for _, d := range decisions {
		dKind, dOID := interaction.EffectivePermissionOutcome(d.domain, domainOpts)
		wKind, wOID := acp.EffectivePermissionOutcome(d.wire, acpOpts)
		if dKind != wKind || dOID != wOID {
			t.Errorf("%s：领域 (%q,%q) ≠ 协议 (%q,%q)——界面回执与投递给底座的结果分叉了",
				d.name, dKind, dOID, wKind, wOID)
		}
	}

	// 空选项是一支独立规则：放行也要降级成取消（没有可选项就没有"选中"）。
	for _, d := range decisions {
		dKind, dOID := interaction.EffectivePermissionOutcome(d.domain, nil)
		wKind, wOID := acp.EffectivePermissionOutcome(d.wire, nil)
		if dKind != wKind || dOID != wOID {
			t.Errorf("%s（无选项）：领域 (%q,%q) ≠ 协议 (%q,%q)", d.name, dKind, dOID, wKind, wOID)
		}
	}
}
