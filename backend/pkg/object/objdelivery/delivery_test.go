package objdelivery

import "testing"

// TestForModelIsT5ConfigWrite 钉住「平台模型清单」这一条的交付形态：T5 `config_write` +
// `next_process`，以及它的 applicability 只可能是 written / untested。
//
// 它是 M6 的**具体承诺**（不是泛泛的"表里有 model"）：模型清单只有物化到配置文件才不是装饰，
// 而配置文件在**进程启动时**被读——所以生效时刻必须是 next_process，不许被写成 next_turn
// （那会让界面承诺"下一轮生效"，而底座其实要等重启）。
func TestForModelIsT5ConfigWrite(t *testing.T) {
	d, ok := For(KindModel)
	if !ok {
		t.Fatalf("For(%q) 必须有声明（平台拥有模型清单）", KindModel)
	}
	if d.Channel != ChannelConfigWrite {
		t.Errorf("model 的通道应为 %q，得到 %q", ChannelConfigWrite, d.Channel)
	}
	if d.Effect != EffectNextProcess {
		t.Errorf("model 的生效时刻应为 %q，得到 %q", EffectNextProcess, d.Effect)
	}
	if d.Note == "" {
		t.Error("model 的声明必须有可执行的人话 Note")
	}

	written := ConfigApplicability("opencode", "<MATE_HOME>/runtime/opencode/opencode.json", true)
	if written.Status != StatusWritten || !written.OK {
		t.Errorf("written=true 应得到 ok/written，得到 %+v", written)
	}
	untested := ConfigApplicability("deepseekharness", "<MATE_HOME>/runtime/dsh/mate-models.patch.yml", false)
	if untested.Status != StatusUntested || !untested.OK {
		t.Errorf("written=false 应得到 ok/untested，得到 %+v", untested)
	}
	if written.Reason == "" || untested.Reason == "" {
		t.Error("applicability 的 reason 必须可执行（说清写哪个文件 / 为什么还没写）")
	}
}

// TestForUnknownKindIsFalse 未知 kind 必须返回 false：调用方据此报警，而不是拿零值 Delivery
// 渲染出一条看起来合法的空通道。
func TestForUnknownKindIsFalse(t *testing.T) {
	if d, ok := For("__no_such_kind__"); ok {
		t.Fatalf("未声明的 kind 必须返回 false，得到 %+v", d)
	}
	if _, ok := For(""); ok {
		t.Fatal("空 kind 必须返回 false")
	}
}

// TestTableInvariants 表自身的完整性：通道词表内、生效时刻词表内、Note 非空、Kinds 与 For 一致。
func TestTableInvariants(t *testing.T) {
	for _, kind := range Kinds() {
		d, ok := For(kind)
		if !ok {
			t.Fatalf("Kinds() 报出 %q 但 For 查不到（表与索引漂移）", kind)
		}
		if d.Channel != ChannelNone && !IsDeliveryChannel(d.Channel) {
			t.Errorf("%s 的通道 %q 不在交付通道词表 %v 内（也不是 %q）", kind, d.Channel, Channels(), ChannelNone)
		}
		if !contains(Effects(), d.Effect) {
			t.Errorf("%s 的生效时刻 %q 不在词表 %v 内", kind, d.Effect, Effects())
		}
		if d.Note == "" {
			t.Errorf("%s 没有可执行的人话 Note", kind)
		}
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
