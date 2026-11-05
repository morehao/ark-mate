package sessionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/automation"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/service/svcautomation"
)

// automationRig 一个装了自动化目录的 Service（M4-3 的写面 + 注册表都在它上面测）。
func automationRig(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if n := svc.Planes().Automation.LoadAutomationDefinitions(dir); n != 0 {
		t.Fatalf("空目录应装载 0 条，got %d", n)
	}
	return svc, dir
}

func writeAutomationFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

// autoSvc 取自动化面的**用例层**（P4a 正文第九刀起 REST 面在 `svcautomation`，不再由 Service 提供）。
//
// 测试经它与"真 Service + 真域平面"驱动——这层的实现搬走了，但夹具链一个字不用改：
// 平面还是那个平面，用例只是换了个所有者。
func autoSvc(svc *Service) svcautomation.AutomationSvc {
	return svcautomation.New(svc.planes.Automation)
}

// mustListAutomations 列出注册表（端口签名带 error，测试里不关心 err）。
func (s *Service) mustListAutomations(t *testing.T) []dtoautomation.AutomationResp {
	t.Helper()
	items, err := autoSvc(s).ListAutomations()
	if err != nil {
		t.Fatalf("ListAutomations: %v", err)
	}
	return items
}

// dailyPatch 一条 daily 定义的完整 patch（控制器从 DTO 拼出来的形态）。
func dailyPatch(id, hour string) map[string]any {
	return map[string]any{
		"id":      id,
		"name":    "每日巡检",
		"enabled": true,
		"schedule": map[string]any{
			"kind": "daily", "timezone": "Asia/Shanghai", "hour": 9, "minute": 0,
		},
		"target": map[string]any{
			"cwd": "/tmp/ws", "prompt": "跑一遍依赖巡检", "delivery": "new_session",
		},
	}
}

func TestAutomationCreateUpdateDelete(t *testing.T) {
	svc, dir := automationRig(t)
	path := filepath.Join(dir, "daily.json")

	v, err := svc.Planes().Automation.WriteAutomation(context.Background(), "daily", dailyPatch("daily", "9"), true)
	if err != nil {
		t.Fatalf("新建: %v", err)
	}
	if v.ID != "daily" || v.State != automation.StateActive || v.NextDueAt == 0 {
		t.Fatalf("新建视图不符: %+v", v)
	}
	if v.Schedule.Kind != automation.KindDaily || v.Schedule.Hour == nil || *v.Schedule.Hour != 9 {
		t.Fatalf("调度不符: %+v", v.Schedule)
	}
	if got := readDef(t, path); got["id"] != "daily" || got["name"] != "每日巡检" {
		t.Fatalf("落盘内容不符: %+v", got)
	}

	// id 已存在 → 40901，绝不覆盖（先改内容再试，确认原文件没被动）
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "daily", dailyPatch("daily", "10"), true); errCode(err) != "automation_exists" ||
		numericCode(err) != 40901 {
		t.Fatalf("重复新建应 40901 automation_exists，got %v（code=%d）", err, numericCode(err))
	}
	if got := readDef(t, path); got["schedule"].(map[string]any)["hour"].(float64) != 9 {
		t.Fatalf("被拒的重复新建不该动文件: %+v", got)
	}

	// 只改 name：schedule/target 原样保留（PUT 的"只改出现过的键"）
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{"name": "改名了"}, false); err != nil {
		t.Fatalf("局部更新: %v", err)
	}
	got := readDef(t, path)
	if got["name"] != "改名了" || got["schedule"].(map[string]any)["hour"].(float64) != 9 ||
		got["target"].(map[string]any)["cwd"] != "/tmp/ws" {
		t.Fatalf("局部更新应保留未提及的键: %+v", got)
	}

	// 不存在的 id → 40401（平台不会"顺便新建"）
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "nope", map[string]any{"name": "x"}, false); errCode(err) != "automation_not_found" ||
		numericCode(err) != 40401 {
		t.Fatalf("更新不存在的应 40401 automation_not_found，got %v（code=%d）", err, numericCode(err))
	}
	if _, statErr := os.Stat(filepath.Join(dir, "nope.json")); !os.IsNotExist(statErr) {
		t.Fatal("更新不存在的 id 不该建出文件")
	}

	// 删除幂等
	if removed, err := svc.Planes().Automation.DeleteAutomationByID(context.Background(), "daily"); err != nil || !removed {
		t.Fatalf("删除: %v removed=%v", err, removed)
	}
	if removed, err := svc.Planes().Automation.DeleteAutomationByID(context.Background(), "daily"); err != nil || removed {
		t.Fatalf("重复删除应 removed=false: %v %v", err, removed)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("删除后文件应消失")
	}
	if _, ok := svc.Planes().Automation.AutomationByID("daily"); ok {
		t.Fatal("删除后注册表应摘除条目")
	}
}

// TestAutomationValidationZeroBytes 校验不过 → **零字节落盘** + 注册表不动（与能力写面同一纪律）。
func TestAutomationValidationZeroBytes(t *testing.T) {
	svc, dir := automationRig(t)
	path := filepath.Join(dir, "bad.json")

	cases := []struct {
		name  string
		patch map[string]any
		code  string
	}{
		{"时区未知", map[string]any{
			"id": "bad", "enabled": true,
			"schedule": map[string]any{"kind": "daily", "timezone": "Mars/Olympus", "hour": 9, "minute": 0},
			"target":   map[string]any{"prompt": "x"},
		}, "invalid_timezone"},
		{"缺 target.prompt", map[string]any{
			"id": "bad", "enabled": true,
			"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
			"target":   map[string]any{"cwd": "/tmp"},
		}, "invalid_param"},
		{"duration 超上限", map[string]any{
			"id": "bad", "enabled": true, "max_run_duration": "90m",
			"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
			"target":   map[string]any{"cwd": "/tmp", "prompt": "x", "delivery": "new_session"},
		}, "invalid_param"},
		{"路径 id 与 body id 不一致", map[string]any{
			"id": "other", "enabled": true,
			"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
			"target":   map[string]any{"cwd": "/tmp", "prompt": "x", "delivery": "new_session"},
		}, "invalid_param"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "bad", c.patch, true); errCode(err) != c.code {
				t.Fatalf("期望码 %s，got %v（%s）", c.code, err, errCode(err))
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("被拒的定义必须零字节落盘，但 %s 存在", path)
			}
			if _, ok := svc.Planes().Automation.AutomationByID("bad"); ok {
				t.Fatal("被拒的定义不该进注册表")
			}
		})
	}
	// 非法 id（目录穿越）也要拒
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "../escape", dailyPatch("../escape", "9"), true); errCode(err) != "invalid_param" {
		t.Fatalf("非法 id 应 40000 invalid_param，got %v", err)
	}
}

// TestAutomationPreservesUnknownKeys 未知键（用户手写的注释性字段）在用户写面与运行态写回之后都要保留。
func TestAutomationPreservesUnknownKeys(t *testing.T) {
	svc, dir := automationRig(t)
	path := filepath.Join(dir, "daily.json")
	writeAutomationFile(t, path, `{
	  "id": "daily", "name": "巡检", "enabled": true,
	  "schedule": {"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},
	  "target": {"cwd":"/tmp/ws","prompt":"跑一遍","delivery":"new_session"},
	  "_comment": "这是我手写的备注，平台不许动",
	  "owner_team": "infra"
	}`)
	if n := svc.Planes().Automation.LoadAutomationDefinitions(dir); n != 1 {
		t.Fatalf("期望装载 1 条，got %d", n)
	}
	// 用户写面：只改 name
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{"name": "巡检（新）"}, false); err != nil {
		t.Fatalf("更新: %v", err)
	}
	// 运行态写回
	fired := time.Now().UTC().Truncate(time.Second)
	if err := svc.Planes().Automation.SaveOverlay(context.Background(), "daily", map[string]any{
		"last_fired_at": fired.Format(time.RFC3339Nano),
		"last_status":   automation.RunCompleted,
		"state":         automation.StateActive,
	}, ""); err != nil {
		t.Fatalf("运行态写回: %v", err)
	}
	got := readDef(t, path)
	if got["_comment"] != "这是我手写的备注，平台不许动" || got["owner_team"] != "infra" {
		t.Fatalf("未知键被抹掉了: %+v", got)
	}
	if got["name"] != "巡检（新）" || got["last_status"] != automation.RunCompleted {
		t.Fatalf("写回内容不符: %+v", got)
	}
	if got["schedule"].(map[string]any)["hour"].(float64) != 9 {
		t.Fatalf("运行态写回不该动 schedule: %+v", got)
	}
}

// TestAutomationPatchScheduleRecomputesNextDue 改 schedule → next_due 立刻重算（M4-3 验收项）。
func TestAutomationPatchScheduleRecomputesNextDue(t *testing.T) {
	svc, _ := automationRig(t)
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "daily", dailyPatch("daily", "9"), true); err != nil {
		t.Fatalf("新建: %v", err)
	}
	before, _ := svc.Planes().Automation.AutomationByID("daily")
	// 改到 10:00：无论"现在"在几点，next_due 都必须变（09:00 与 10:00 是同一 now 下的两个不同时刻）
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{
		"schedule": map[string]any{"kind": "daily", "timezone": "Asia/Shanghai", "hour": 10, "minute": 30},
	}, false); err != nil {
		t.Fatalf("改 schedule: %v", err)
	}
	after, _ := svc.Planes().Automation.AutomationByID("daily")
	if before.NextDueAt == 0 || after.NextDueAt == 0 {
		t.Fatalf("next_due 不该为空: %d → %d", before.NextDueAt, after.NextDueAt)
	}
	if before.NextDueAt == after.NextDueAt {
		t.Fatalf("改 schedule 后 next_due 应重算：%d → %d", before.NextDueAt, after.NextDueAt)
	}
	if after.Schedule.Hour == nil || *after.Schedule.Hour != 10 || after.Schedule.Minute == nil || *after.Schedule.Minute != 30 {
		t.Fatalf("调度应已更新: %+v", after.Schedule)
	}
}

// TestAutomationOverlayAdvancesNextDue 运行态写回（last_fired_at）后 next_due 必须跟着推进：
// 否则下个 tick 会拿同一个 occurrence 再来一次（重复触发）。
func TestAutomationOverlayAdvancesNextDue(t *testing.T) {
	svc, _ := automationRig(t)
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "daily", dailyPatch("daily", "9"), true); err != nil {
		t.Fatalf("新建: %v", err)
	}
	first, _ := svc.Planes().Automation.AutomationByID("daily")
	if err := svc.Planes().Automation.SaveOverlay(context.Background(), "daily", map[string]any{
		// 定义文件/overlay 是 RFC3339 载体（不是接口时间戳），所以这里转回文本。
		"last_fired_at": time.UnixMilli(first.NextDueAt).UTC().Format(time.RFC3339Nano), // 认领并"跑完"了这一次
		"last_status":   automation.RunCompleted,
	}, ""); err != nil {
		t.Fatalf("运行态写回: %v", err)
	}
	second, _ := svc.Planes().Automation.AutomationByID("daily")
	if second.NextDueAt == first.NextDueAt {
		t.Fatalf("last_fired_at 写回后 next_due 必须推进：%d 仍是 %d", second.NextDueAt, first.NextDueAt)
	}
	f1 := time.UnixMilli(first.NextDueAt)
	f2 := time.UnixMilli(second.NextDueAt)
	if d := f2.Sub(f1); d != 24*time.Hour {
		t.Fatalf("daily 的推进应恰好一天，got %s", d)
	}
}

// TestAutomationLoadBadDefinitions 坏定义留在列表里带 error（不静默消失），但不进 due 快照。
func TestAutomationLoadBadDefinitions(t *testing.T) {
	dir := t.TempDir()
	writeAutomationFile(t, filepath.Join(dir, "ok.json"), `{
	  "id":"ok","name":"好定义","enabled":true,
	  "schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},
	  "target":{"cwd":"/tmp/ws","prompt":"跑","delivery":"new_session"}
	}`)
	writeAutomationFile(t, filepath.Join(dir, "broken.json"), `{not json`)
	writeAutomationFile(t, filepath.Join(dir, "mismatch.json"), `{
	  "id":"other","enabled":true,
	  "schedule":{"kind":"daily","timezone":"UTC","hour":9,"minute":0},
	  "target":{"prompt":"跑"}
	}`)
	writeAutomationFile(t, filepath.Join(dir, "disabled.json"), `{
	  "id":"disabled","name":"关着的","enabled":false,
	  "schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},
	  "target":{"cwd":"/tmp/ws","prompt":"跑","delivery":"new_session"}
	}`)
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if n := svc.Planes().Automation.LoadAutomationDefinitions(dir); n != 2 {
		t.Fatalf("成功装载应为 2（ok/disabled），got %d", n)
	}
	items := svc.mustListAutomations(t)
	if len(items) != 4 {
		t.Fatalf("列表应含坏定义共 4 条（可见才排查得了），got %d", len(items))
	}
	byID := map[string]dtoautomation.AutomationResp{}
	for _, v := range items {
		byID[v.ID] = v
	}
	if byID["broken"].Error == "" || byID["mismatch"].Error == "" {
		t.Fatalf("坏定义必须带 error: %+v", byID)
	}
	if byID["ok"].Error != "" || byID["ok"].NextDueAt == 0 {
		t.Fatalf("好定义应无 error 且有 next_due: %+v", byID["ok"])
	}
	// due 快照：坏定义与 disabled 都不在
	due := svc.Planes().Automation.DueSnapshot(time.Now().Add(48 * time.Hour))
	for _, rec := range due {
		if rec.Def.ID == "broken" || rec.Def.ID == "mismatch" || rec.Def.ID == "disabled" {
			t.Fatalf("不该出现在 due 快照里: %s", rec.Def.ID)
		}
	}
}

// TestAutomationDueSnapshot C9-free 的调度事实：只 `enabled && active && 有 next_due && 已到期`，
// 且按 (next_due, id) 稳定排序。
func TestAutomationDueSnapshot(t *testing.T) {
	svc, dir := automationRig(t)
	mk := func(id string, body string) {
		writeAutomationFile(t, filepath.Join(dir, id+".json"), body)
	}
	// a1：昨天 09:00 跑过 → 现在已到期（recover）
	mk("a1", `{"id":"a1","enabled":true,
	  "schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},
	  "target":{"cwd":"/tmp","prompt":"p","delivery":"new_session"},
	  "last_fired_at":"2020-01-01T01:00:00Z"}`)
	// b2：未到期（未来的一次 once）
	mk("b2", `{"id":"b2","enabled":true,
	  "schedule":{"kind":"once","timezone":"UTC","at":"2099-01-01T00:00:00Z"},
	  "target":{"cwd":"/tmp","prompt":"p","delivery":"new_session"}}`)
	// c3：归档 → 不参与
	mk("c3", `{"id":"c3","enabled":true,"state":"archived",
	  "schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},
	  "target":{"cwd":"/tmp","prompt":"p","delivery":"new_session"}}`)
	// d4：once 已过期且从未跑 → expire（nextDue 为零，不排进 due）
	mk("d4", `{"id":"d4","enabled":true,
	  "schedule":{"kind":"once","timezone":"UTC","at":"2020-01-01T00:00:00Z"},
	  "target":{"cwd":"/tmp","prompt":"p","delivery":"new_session"}}`)
	if n := svc.Planes().Automation.LoadAutomationDefinitions(dir); n != 4 {
		t.Fatalf("装载应为 4，got %d", n)
	}
	due := svc.Planes().Automation.DueSnapshot(time.Now())
	if len(due) != 1 || due[0].Def.ID != "a1" {
		t.Fatalf("只有 a1 该到期，got %+v", due)
	}
	rec, ok := svc.Planes().Automation.RecByID("d4")
	if !ok || rec.Startup != automation.StartupExpire || !rec.NextDue.IsZero() {
		t.Fatalf("过期的 once 应记 expire 且无 next_due: %+v（startup=%s）", rec, rec.Startup)
	}
	// once 未到期 → wait（future）
	if rec, _ := svc.Planes().Automation.RecByID("b2"); rec.Startup != automation.StartupWait {
		t.Fatalf("未到期的 once 应记 wait，got %s", rec.Startup)
	}
	// daily + 陈旧的 last_fired_at → recover（ADR-9 的补跑判据）
	if rec, _ := svc.Planes().Automation.RecByID("a1"); rec.Startup != automation.StartupRecover {
		t.Fatalf("停机错过的 daily 应记 recover，got %s", rec.Startup)
	}
}

// TestAutomationDirUnset 未配置目录 → 显式报错（不静默"内存生效、盘上没留痕"）。
func TestAutomationDirUnset(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "x", dailyPatch("x", "9"), true); errCode(err) != "automation_dir_unset" {
		t.Fatalf("期望 automation_dir_unset，got %v", err)
	}
	if _, err := svc.Planes().Automation.DeleteAutomationByID(context.Background(), "x"); errCode(err) != "automation_dir_unset" {
		t.Fatalf("删除同样要报 automation_dir_unset，got %v", err)
	}
}

// TestAutomationCreateWritesNormalizedSchedule 规范化发生在落盘之前（weekly 的 days_of_week 去重升序）。
func TestAutomationCreateWritesNormalizedSchedule(t *testing.T) {
	svc, dir := automationRig(t)
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "w", map[string]any{
		"id": "w", "enabled": true,
		"schedule": map[string]any{
			"kind": "weekly", "timezone": "UTC", "hour": 9, "minute": 0,
			"days_of_week": []any{3, 1, 3, 0},
		},
		"target": map[string]any{"cwd": "/tmp", "prompt": "p", "delivery": "new_session"},
	}, true); err != nil {
		t.Fatalf("新建: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "w.json"))
	var got automation.Automation
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析: %v", err)
	}
	want := []int{0, 1, 3}
	if len(got.Schedule.DaysOfWeek) != len(want) {
		t.Fatalf("days_of_week 应去重升序 %v，got %v", want, got.Schedule.DaysOfWeek)
	}
	for i := range want {
		if got.Schedule.DaysOfWeek[i] != want[i] {
			t.Fatalf("days_of_week 应去重升序 %v，got %v", want, got.Schedule.DaysOfWeek)
		}
	}
}

// TestAutomationDeliverySessionUnsetLoadsNeedsAttention v35 §3.4：老定义无 session_id 且缺省
// delivery=session → 装载期报 delivery_session_unset、留在列表里带 error（进 needs_attention），
// **不静默新建会话**、不参与调度。
func TestAutomationDeliverySessionUnsetLoadsNeedsAttention(t *testing.T) {
	svc, dir := automationRig(t)
	writeAutomationFile(t, filepath.Join(dir, "old.json"), `{
	  "id":"old","name":"老定义","enabled":true,
	  "schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},
	  "target":{"prompt":"跑"}
	}`)
	if n := svc.Planes().Automation.LoadAutomationDefinitions(dir); n != 0 {
		t.Fatalf("delivery=session 缺 session_id 不应装载成功，got %d", n)
	}
	items := svc.mustListAutomations(t)
	if len(items) != 1 || items[0].ID != "old" {
		t.Fatalf("坏定义应留在列表里：%+v", items)
	}
	if !strings.Contains(items[0].Error, "delivery_session_unset") &&
		!strings.Contains(items[0].Error, "session_id") {
		t.Fatalf("应报 delivery_session_unset（含 session_id 提示），got %q", items[0].Error)
	}
	// 不参与调度
	if due := svc.Planes().Automation.DueSnapshot(time.Now().Add(48 * time.Hour)); len(due) != 0 {
		t.Fatalf("坏定义不该被调度：%+v", due)
	}
}

// TestAutomationWriteFaceSessionMustExist v35 §3.0：写面 delivery=session 时校验会话存在
// （宿主端口返回不存在 → session_not_found）；delivery=new_session 不查会话、正常保存。
func TestAutomationWriteFaceSessionMustExist(t *testing.T) {
	svc, _ := automationRig(t)
	// delivery=session + 不存在的 session_id → 写面拒绝。
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "s2", map[string]any{
		"id": "s2", "enabled": true,
		"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
		"target":   map[string]any{"prompt": "p", "delivery": "session", "session_id": "sess_missing"},
	}, true); err == nil {
		t.Fatal("会话不存在应被写面拒绝")
	} else if got := errCode(err); got != "session_not_found" {
		t.Fatalf("应是 session_not_found，got %q（%v）", got, err)
	}
	// delivery=new_session 不查会话，正常保存。
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "n1", map[string]any{
		"id": "n1", "enabled": true,
		"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
		"target":   map[string]any{"prompt": "p", "delivery": "new_session", "cwd": "/tmp"},
	}, true); err != nil {
		t.Fatalf("new_session 应可保存: %v", err)
	}
}
