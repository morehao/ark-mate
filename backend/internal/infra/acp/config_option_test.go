package acl

import (
	"context"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ——— 会话配置项（模型目录面的唯一来源）在 acl 层的端到端单测 ———

// cfgCat/cfgStr 测试小工具（构造 SDK 指针字段）。
func cfgCat(c acpsdk.SessionConfigOptionCategory) *acpsdk.SessionConfigOptionCategory { return &c }
func cfgStr(s string) *string                                                         { return &s }

// modelOptionGrouped 构造 dsh 形态：分组的下拉，值为不透明串（JSON.stringify([provider,model])）。
func modelOptionGrouped(current string) acpsdk.SessionConfigOption {
	groups := acpsdk.SessionConfigSelectOptionsGrouped{
		{Group: "deepseek", Name: "DeepSeek", Options: []acpsdk.SessionConfigSelectOption{
			{Value: `["deepseek","deepseek-chat"]`, Name: "DeepSeek Chat"},
			{Value: `["deepseek","deepseek-reasoner"]`, Name: "DeepSeek Reasoner"},
		}},
		{Group: "anthropic", Name: "Anthropic", Options: []acpsdk.SessionConfigSelectOption{
			{Value: `["anthropic","claude-sonnet"]`, Name: "Claude Sonnet"},
		}},
	}
	return acpsdk.SessionConfigOption{Select: &acpsdk.SessionConfigOptionSelect{
		Id: "model", Name: "Model", Category: cfgCat(acpsdk.SessionConfigOptionCategoryModel),
		CurrentValue: acpsdk.SessionConfigValueId(current), Options: acpsdk.SessionConfigSelectOptions{Grouped: &groups},
		Type: "select",
	}}
}

// modelOptionFlat 构造扁平形态：扁平下拉，值为 "provider/model"（历史实测里某底座如此下发）。
func modelOptionFlat(current string) acpsdk.SessionConfigOption {
	flat := acpsdk.SessionConfigSelectOptionsUngrouped{
		{Value: "test/test-model", Name: "Test/Test Model"},
		{Value: "test/other", Name: "Test/Other"},
	}
	return acpsdk.SessionConfigOption{Select: &acpsdk.SessionConfigOptionSelect{
		Id: "model", Name: "Model", Category: cfgCat(acpsdk.SessionConfigOptionCategoryModel),
		CurrentValue: acpsdk.SessionConfigValueId(current), Options: acpsdk.SessionConfigSelectOptions{Ungrouped: &flat},
		Type: "select",
	}}
}

// thoughtLevelOption 构造一个**非模型**配置项（category=thought_level，id 与模型项不同名）。
// 平台已不提供「思考档位」选择面（只原样投影运行时状态），此处仅作为「第二个选项」样本，
// 用于验证多选项归一化与 set 响应的**全量**语义。
func thoughtLevelOption(id, current string, values ...string) acpsdk.SessionConfigOption {
	flat := acpsdk.SessionConfigSelectOptionsUngrouped{}
	for _, v := range values {
		flat = append(flat, acpsdk.SessionConfigSelectOption{Value: acpsdk.SessionConfigValueId(v), Name: v})
	}
	return acpsdk.SessionConfigOption{Select: &acpsdk.SessionConfigOptionSelect{
		Id: acpsdk.SessionConfigId(id), Name: "Reasoning effort",
		Category:     cfgCat(acpsdk.SessionConfigOptionCategoryThoughtLevel),
		CurrentValue: acpsdk.SessionConfigValueId(current),
		Options:      acpsdk.SessionConfigSelectOptions{Ungrouped: &flat},
		Type:         "select",
	}}
}

// TestNewSessionCapturesConfigOptions session/new 响应的 configOptions 被采集并归一化
// （分组展平、保留 Group/GroupName、值原样保留）。
func TestNewSessionCapturesConfigOptions(t *testing.T) {
	agent := &fakeAgent{
		init:       fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), nil),
		newCfgOpts: []acpsdk.SessionConfigOption{modelOptionGrouped(`["deepseek","deepseek-reasoner"]`), thoughtLevelOption("reasoning_effort", "", "low", "high")},
	}
	cl := dialFake(t, agent, ClientOptions{})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	opts := sess.ConfigOptions()
	if len(opts) != 2 {
		t.Fatalf("配置项数量不符: %+v", opts)
	}
	m, ok := FindConfigOption(opts, CategoryModel)
	if !ok {
		t.Fatalf("未按 category 找到模型项: %+v", opts)
	}
	if m.ID != "model" || m.Type != OptionTypeSelect || !m.Grouped {
		t.Fatalf("模型项投影不符: %+v", m)
	}
	if len(m.Values) != 3 {
		t.Fatalf("分组应展平为 3 个值: %+v", m.Values)
	}
	if m.Values[0].Group != "deepseek" || m.Values[0].GroupName != "DeepSeek" {
		t.Fatalf("分组信息应保留: %+v", m.Values[0])
	}
	// 值不透明：原样保留，不做任何解析/拼接。
	if got := CurrentConfigValue(opts, CategoryModel); got != `["deepseek","deepseek-reasoner"]` {
		t.Fatalf("当前值应原样保留: %q", got)
	}
	if !HasConfigValue(opts, CategoryModel, `["anthropic","claude-sonnet"]`) {
		t.Fatal("分组内的值应可命中")
	}
	if HasConfigValue(opts, CategoryModel, "gpt-4") {
		t.Fatal("未广告的值不应命中")
	}
	// 非模型选项按 category 命中（id 与模型项不同名）——平台按 category 取值的证据。
	if _, ok := FindConfigOption(opts, CategoryThoughtLevel); !ok {
		t.Fatalf("未按 category 找到第二个选项: %+v", opts)
	}
}

// TestSetConfigOptionReturnsFullState set_config_option 的响应是**全量**状态：
// 返回值即新快照，且下发值原样上行（configId 取运行时自报的 id）。
func TestSetConfigOptionReturnsFullState(t *testing.T) {
	agent := &fakeAgent{
		init:       fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), nil),
		newCfgOpts: []acpsdk.SessionConfigOption{modelOptionFlat("test/test-model")},
		setCfgOpts: []acpsdk.SessionConfigOption{modelOptionFlat("test/other"), thoughtLevelOption("effort", "low", "low", "high")},
	}
	cl := dialFake(t, agent, ClientOptions{})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	got, err := sess.SetConfigOption(context.Background(), "model", "test/other")
	if err != nil {
		t.Fatalf("SetConfigOption: %v", err)
	}
	if CurrentConfigValue(got, CategoryModel) != "test/other" {
		t.Fatalf("返回状态应为全量且含新值: %+v", got)
	}
	if _, ok := FindConfigOption(got, CategoryThoughtLevel); !ok {
		t.Fatalf("返回状态应含全部选项（全量语义）: %+v", got)
	}
	// 快照被更新（后续读取无需再问运行时）。
	if CurrentConfigValue(sess.ConfigOptions(), CategoryModel) != "test/other" {
		t.Fatalf("会话快照未更新: %+v", sess.ConfigOptions())
	}
	if len(agent.setCfgCalls) != 1 {
		t.Fatalf("应发出一次 set: %+v", agent.setCfgCalls)
	}
	req := agent.setCfgCalls[0].ValueId
	if req == nil {
		t.Fatalf("取值变体应为 ValueId: %+v", agent.setCfgCalls[0])
	}
	if req.ConfigId != "model" || req.Value != "test/other" || string(req.SessionId) != sess.ID() {
		t.Fatalf("下发参数应原样透传: %+v", req)
	}
}

// TestSetConfigOptionErrorPassthrough 运行时拒绝（如 unknown model option）→ 原样上抛，快照不变。
func TestSetConfigOptionErrorPassthrough(t *testing.T) {
	agent := &fakeAgent{
		init:       fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), nil),
		newCfgOpts: []acpsdk.SessionConfigOption{modelOptionFlat("test/test-model")},
		setCfgErr:  errInvalidParams("unknown model option: nope"),
	}
	cl := dialFake(t, agent, ClientOptions{})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.SetConfigOption(context.Background(), "model", "nope"); err == nil {
		t.Fatal("运行时拒绝应回错误")
	}
	if CurrentConfigValue(sess.ConfigOptions(), CategoryModel) != "test/test-model" {
		t.Fatalf("失败不应改动快照: %+v", sess.ConfigOptions())
	}
}

// TestConfigOptionUpdatePush 运行时主动推送 config_option_update → 归一化事件 + 快照刷新。
func TestConfigOptionUpdatePush(t *testing.T) {
	updated := []acpsdk.SessionConfigOption{modelOptionFlat("test/other")}
	agent := &fakeAgent{
		init:       fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), nil),
		newCfgOpts: []acpsdk.SessionConfigOption{modelOptionFlat("test/test-model")},
		promptUpdates: []acpsdk.SessionUpdate{{ConfigOptionUpdate: &acpsdk.SessionConfigOptionUpdate{
			ConfigOptions: updated, SessionUpdate: "config_option_update",
		}}},
	}
	events := make(chan Event, 8)
	cl := dialFake(t, agent, ClientOptions{OnEvent: func(e Event) { events <- e }})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != KindConfigOptions {
			t.Fatalf("事件 kind 不符: %s", ev.Kind)
		}
		info, ok := ev.Payload.(ConfigOptionsInfo)
		if !ok {
			t.Fatalf("载荷类型不符: %+v", ev.Payload)
		}
		if info.SessionID != sess.ID() {
			t.Fatalf("载荷会话 id 不符: %+v", info)
		}
		if CurrentConfigValue(info.Options, CategoryModel) != "test/other" {
			t.Fatalf("推送载荷应含全量新状态: %+v", info.Options)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 config_options 事件")
	}

	// config_option_update 是**全量**语义 → 直接替换快照（不需要重新拉取）。
	if got := CurrentConfigValue(sess.ConfigOptions(), CategoryModel); got != "test/other" {
		t.Fatalf("快照应被推送刷新: %q", got)
	}
}

// TestResumeSessionCapturesConfigOptions resume 响应同样携带全量配置项。
func TestResumeSessionCapturesConfigOptions(t *testing.T) {
	agent := &fakeAgent{
		init:          fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(false, true, true), nil),
		resumeCfgOpts: []acpsdk.SessionConfigOption{modelOptionFlat("test/test-model")},
	}
	cl := dialFake(t, agent, ClientOptions{})
	sess, err := cl.ResumeSession(context.Background(), "ses_1", SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if CurrentConfigValue(sess.ConfigOptions(), CategoryModel) != "test/test-model" {
		t.Fatalf("resume 应采集配置项: %+v", sess.ConfigOptions())
	}
	// close 后会话从注册表摘除（避免 resume/取样泄漏）。
	if cl.Session(sess.ID()) == nil {
		t.Fatal("resume 后会话应登记")
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cl.Session(sess.ID()) != nil {
		t.Fatal("close 后应从注册表摘除")
	}
}

// TestListSessionsCwd 会话列举透传 cwd（空 = 全部），并把返回条目归一化为 SessionInfo。
func TestListSessionsCwd(t *testing.T) {
	agent := &fakeAgent{
		init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, false, true), nil),
		listResp: acpsdk.ListSessionsResponse{Sessions: []acpsdk.SessionInfo{
			{SessionId: "ses_1", Cwd: "/work", Title: cfgStr("t1")},
		}},
	}
	cl := dialFake(t, agent, ClientOptions{})
	items, err := cl.ListSessions(context.Background(), "/work")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(items) != 1 || items[0].ID != "ses_1" || items[0].Cwd != "/work" || items[0].Title != "t1" {
		t.Fatalf("会话条目不符: %+v", items)
	}
}

// errInvalidParams 构造一个 SDK 请求类错误（模拟运行时拒绝取值）。
func errInvalidParams(msg string) error {
	return &acpsdk.RequestError{Code: -32602, Message: msg}
}
