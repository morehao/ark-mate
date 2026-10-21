package assembly

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/config"
)

// TestSequenceStartStopSymmetric 钉住两条最容易静默漂移的形状：
//
//	① 启动按声明顺序、关闭按**逆序**（关闭顺序与启动不对称的表现是"退出时偶发丢数据"，
//	   而任何单测都不会因此变红）；
//	② 只关闭**真的启动过**的步骤（启动失败回滚之后，未启动的 Stop 不得被调用——否则
//	   你会去关一个还没造出来的东西，第二个错误把第一个错误的现场埋掉）。
func TestSequenceStartStopSymmetric(t *testing.T) {
	var order []string
	step := func(name string) Step {
		return Step{
			Name: name,
			Start: func(context.Context) error {
				order = append(order, "start:"+name)
				return nil
			},
			Stop: func(context.Context) error {
				order = append(order, "stop:"+name)
				return nil
			},
		}
	}
	seq := NewSequence(step("a"), step("b"), step("c"))
	if err := seq.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := join(order); got != "start:a,start:b,start:c" {
		t.Fatalf("启动顺序不是声明顺序: %s", got)
	}
	order = nil
	if errs := seq.Stop(context.Background()); len(errs) != 0 {
		t.Fatalf("Stop 报错: %v", errs)
	}
	if got := join(order); got != "stop:c,stop:b,stop:a" {
		t.Fatalf("关闭顺序不是启动的逆序: %s", got)
	}
	// 幂等：第二次 Stop 不得重复关任何东西（关闭动作本身也应当幂等，这是双保险）。
	order = nil
	_ = seq.Stop(context.Background())
	if len(order) != 0 {
		t.Fatalf("第二次 Stop 又执行了关闭步骤: %v", order)
	}
}

// TestSequenceCriticalFailureRollsBack 钉住 critical 的语义：失败终止启动，并把**已启动**的
// 步骤按逆序回滚。改造前 `Init` 中途失败不会关闭已开的库句柄——那是一条真实的资源泄漏。
func TestSequenceCriticalFailureRollsBack(t *testing.T) {
	var order []string
	boom := errors.New("坏了")
	seq := NewSequence(
		Step{Name: "opened", Start: func(context.Context) error {
			order = append(order, "start:opened")
			return nil
		}, Stop: func(context.Context) error {
			order = append(order, "stop:opened")
			return nil
		}},
		Step{Name: "critical", Critical: true, Start: func(context.Context) error {
			order = append(order, "start:critical")
			return boom
		}},
		Step{Name: "never", Start: func(context.Context) error {
			order = append(order, "start:never")
			return nil
		}, Stop: func(context.Context) error {
			order = append(order, "stop:never")
			return nil
		}},
	)
	err := seq.Start(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("critical 失败应被返回（且用 %%w 保留原错误）: %v", err)
	}
	if got := join(order); got != "start:opened,start:critical,stop:opened" {
		t.Fatalf("回滚形状不对（未启动的步骤不得被关闭）: %s", got)
	}
}

// TestSequenceNonCriticalContinues 钉住"只该 WARN 的步骤"：非 critical 失败必须继续启动。
// 这一条与 critical 那条是一对——只钉一侧，判据就只剩"总是回滚"或"从不回滚"两种错法之一。
func TestSequenceNonCriticalContinues(t *testing.T) {
	var order []string
	seq := NewSequence(
		Step{Name: "warn", Start: func(context.Context) error {
			order = append(order, "start:warn")
			return errors.New("可选目录缺失")
		}},
		Step{Name: "next", Critical: true, Start: func(context.Context) error {
			order = append(order, "start:next")
			return nil
		}},
	)
	if err := seq.Start(context.Background()); err != nil {
		t.Fatalf("非 critical 失败不该终止启动: %v", err)
	}
	if got := join(order); got != "start:warn,start:next" {
		t.Fatalf("非 critical 失败后必须继续: %s", got)
	}
}

// TestSequenceTimeoutIsReported 步骤自己不观察 ctx 时（例如 `store.Open`），超时无法中断它，
// 但必须在返回后如实报出来——"超了还当成功继续"是这一层最坏的假绿。
func TestSequenceTimeoutIsReported(t *testing.T) {
	seq := NewSequence(Step{
		Name: "slow", Critical: true, Timeout: 5 * time.Millisecond,
		Start: func(context.Context) error {
			time.Sleep(20 * time.Millisecond)
			return nil
		},
	})
	err := seq.Start(context.Background())
	if !IsTimeout(err) {
		t.Fatalf("超时应被报成 DeadlineExceeded，实际: %v", err)
	}
}

func join(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// ——— T3：启动步骤表的**声明判据**（每条都是一种"不会让别的测试变红"的错法） ———

// TestValidateStepsRejects 四条规则各自**真的会拦**：名字/阶段/critical 无超时/非 critical 无降级，
// 外加 DependsOn 的两种指错（指向不存在的、指向后面的）。
//
// 这些错法在改造前都是"注释里写得对、代码里悄悄错"：例如给了 `Critical: true` 却没给 Timeout，
// 那一步挂住时进程既不起来也不退出，而所有单测都是绿的。
func TestValidateStepsRejects(t *testing.T) {
	ok := func(name string) Step {
		return Step{Name: name, Phase: PhaseDomain, Critical: true, Timeout: time.Second}
	}
	opt := func(name string) Step {
		return Step{Name: name, Phase: PhaseDomain, Timeout: time.Second, Degrade: "降级成 X"}
	}
	cases := []struct {
		name  string
		steps []Step
		want  string
	}{
		{"合法表", []Step{ok("a"), opt("b"), ok("c")}, ""},
		{"空名字", []Step{{Name: " ", Phase: PhaseDomain, Critical: true, Timeout: time.Second}}, "没有名字"},
		{"重名", []Step{ok("a"), ok("a")}, "重复"},
		{"非法阶段", []Step{{Name: "a", Phase: Phase("某阶段"), Critical: true, Timeout: time.Second}}, "Phase"},
		{"critical 无超时", []Step{{Name: "a", Phase: PhaseDomain, Critical: true}}, "没有超时"},
		{"非 critical 无降级", []Step{{Name: "a", Phase: PhaseDomain, Timeout: time.Second}}, "没有声明降级动作"},
		{"依赖不存在", []Step{Step{Name: "a", Phase: PhaseDomain, Critical: true, Timeout: time.Second, DependsOn: []string{"ghost"}}}, "表里没有这一步"},
		{"依赖排在后面", []Step{
			Step{Name: "a", Phase: PhaseDomain, Critical: true, Timeout: time.Second, DependsOn: []string{"b"}},
			ok("b"),
		}, "排在它后面"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSteps(tc.steps)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("这张表应当合法，却被判错：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应当被拒（%s），实际通过", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误文案应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestStartupStepsTableIsValid 真实的那张表过同一份判据，并钉住三处**顺序敏感**的事实
// （改表的人最容易在"看起来都一样"的列表里把这几行挪位）。
func TestStartupStepsTableIsValid(t *testing.T) {
	as := &assembly{cfg: config.Default()}
	steps := as.steps()
	if err := ValidateSteps(steps); err != nil {
		t.Fatalf("启动步骤表不自洽：%v", err)
	}
	if len(steps) < 20 {
		t.Fatalf("步骤数 %d 明显偏少：表被删成了骨架？", len(steps))
	}
	idx := make(map[string]int, len(steps))
	for i, st := range steps {
		idx[st.Name] = i
	}
	must := func(name string) int {
		i, ok := idx[name]
		if !ok {
			t.Fatalf("表里少了步骤 %q（改名了？本判据跟着改）", name)
		}
		return i
	}
	if must("命令门禁") != 0 {
		t.Fatalf("命令门禁必须最先：规则文件非法要拒绝启动，不能被后面的步骤抢先")
	}
	if must("凭据面") > must("持久层打开") {
		t.Fatalf("凭据面必须在持久层打开之前（接线那一次同步就要读 provider 密钥）")
	}
	if must("就绪宣告") != len(steps)-1 {
		t.Fatalf("就绪宣告必须是最后一步（监听器绑好、路由就绪之后才谈得上\"起来了\"）")
	}
	// 就绪宣告依赖的两项必须真的在它前面（DependsOn 已由 ValidateSteps 判过，这里钉语义）。
	for _, dep := range []string{"路由", "监听绑定"} {
		if must(dep) > must("就绪宣告") {
			t.Fatalf("%s 必须排在就绪宣告之前", dep)
		}
	}
}

// TestReadyAnnounceIsAStep 就绪行由**序列**打，而不是 `App.Run` 里那三行：判据是"回调拿到了
// 监听器的实际地址"，且序列**没有**交出就绪宣告时不调（测试/非桌面形态）。
func TestReadyAnnounceIsAStep(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听器: %v", err)
	}
	defer func() { _ = ln.Close() }()

	var got net.Addr
	as := &assembly{
		cfg:  config.Default(),
		opts: Options{ReadyAnnounce: func(addr net.Addr) error { got = addr; return nil }},
		ln:   ln,
	}
	if err := as.startReadyAnnounce(context.Background()); err != nil {
		t.Fatalf("就绪宣告: %v", err)
	}
	if got == nil || got.String() != ln.Addr().String() {
		t.Fatalf("回调应拿到监听器的实际地址：got %v want %v", got, ln.Addr())
	}

	// 没有回调（测试/非桌面形态）→ 什么都不做，也不报错。
	as.opts.ReadyAnnounce = nil
	if err := as.startReadyAnnounce(context.Background()); err != nil {
		t.Fatalf("没有回调时不应报错：%v", err)
	}
}
