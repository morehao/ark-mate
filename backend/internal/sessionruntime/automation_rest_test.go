package sessionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
)

// ——— M4-5：端口形态（DTO ↔ 领域）的回归 ———
//
// 这些用例走的是**控制器真正调用的那一层**（`dtoautomation` 的原文 JSON 对象进来，
// `AutomationResp` 出去），而不是只走 mock 的 REST 测试：`rawPatch` 的 RawMessage 保留、
// id 的类型判定、拒绝时零字节落盘，任何一处走偏都只在真实路径上暴露。

// rawReq 把 map 编成写面 DTO（原文 JSON 对象）。
func rawReq(t *testing.T, body map[string]any) dtoautomation.AutomationWriteReq {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var req dtoautomation.AutomationWriteReq
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return req
}

// TestAutomationPortWriteFaceCreate DTO 写面：新建成功、id 缺失/类型不符 40000、重复 40901。
func TestAutomationPortWriteFaceCreate(t *testing.T) {
	svc, dir := automationRig(t)
	ctx := context.Background()

	// 缺 id → 40000（这是**请求形态**问题，不是定义内容问题）
	if _, err := autoSvc(svc).CreateAutomation(ctx, rawReq(t, map[string]any{"name": "x"})); numericCode(err) != 40000 {
		t.Fatalf("缺 id 应 40000，got %v（%d）", err, numericCode(err))
	}
	// id 类型不符 → 40000（而不是当成"没传 id"让脏值落盘）
	if _, err := autoSvc(svc).CreateAutomation(ctx, rawReq(t, map[string]any{"id": 7})); numericCode(err) != 40000 {
		t.Fatalf("id 非字符串应 40000，got %v（%d）", err, numericCode(err))
	}
	// 正常新建：文件落盘、id 归一、未知键保留
	resp, err := autoSvc(svc).CreateAutomation(ctx, rawReq(t, map[string]any{
		"id": "daily", "name": "巡检", "enabled": true,
		"schedule":  map[string]any{"kind": "daily", "timezone": "Asia/Shanghai", "hour": 9, "minute": 0},
		"target":    map[string]any{"cwd": "/tmp/ws", "prompt": "x"},
		"my_custom": map[string]any{"note": "用户自己的字段"},
	}))
	if err != nil {
		t.Fatalf("新建应成功: %v", err)
	}
	if resp.ID != "daily" || resp.Schedule.Kind != "daily" {
		t.Fatalf("回执应带定义: %+v", resp)
	}
	got := fileOnDisk(t, filepath.Join(dir, "daily.json"))
	if got["id"] != "daily" {
		t.Fatalf("id 应落盘为字符串: %#v", got["id"])
	}
	if _, ok := got["my_custom"]; !ok {
		t.Fatalf("未知键必须原样保留: %#v", got)
	}
	// 重复新建 → 40901
	if _, err := autoSvc(svc).CreateAutomation(ctx, rawReq(t, map[string]any{"id": "daily"})); numericCode(err) != 40901 {
		t.Fatalf("重复新建应 40901，got %v（%d）", err, numericCode(err))
	}
	// 取单条 / 不存在
	if _, err := autoSvc(svc).GetAutomation("daily"); err != nil {
		t.Fatalf("GetAutomation: %v", err)
	}
	if _, err := autoSvc(svc).GetAutomation("nope"); numericCode(err) != 40401 {
		t.Fatalf("未知 id 应 40401，got %v（%d）", err, numericCode(err))
	}
}

// TestAutomationPortWriteFaceUpdate 覆盖写的三种键语义 + 保留键拒绝。
func TestAutomationPortWriteFaceUpdate(t *testing.T) {
	svc, dir := automationRig(t)
	ctx := context.Background()
	if _, err := autoSvc(svc).CreateAutomation(ctx, rawReq(t, map[string]any{
		"id": "daily", "name": "旧名", "enabled": true,
		"schedule": map[string]any{"kind": "daily", "timezone": "Asia/Shanghai", "hour": 9, "minute": 0},
		"target":   map[string]any{"cwd": "/tmp/ws", "prompt": "x"},
	})); err != nil {
		t.Fatalf("准备: %v", err)
	}
	// 只改 name：其余键（schedule/target）必须原样保留
	if _, err := autoSvc(svc).UpdateAutomation(ctx, "daily", rawReq(t, map[string]any{"name": "新名"})); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	got := fileOnDisk(t, filepath.Join(dir, "daily.json"))
	if got["name"] != "新名" {
		t.Fatalf("name 未更新: %#v", got)
	}
	if sch, ok := got["schedule"].(map[string]any); !ok || sch["kind"] != "daily" {
		t.Fatalf("未出现的键必须保留: %#v", got["schedule"])
	}
	// null 删键
	if _, err := autoSvc(svc).UpdateAutomation(ctx, "daily", rawReq(t, map[string]any{"name": nil})); err != nil {
		t.Fatalf("null 删键: %v", err)
	}
	if _, ok := fileOnDisk(t, filepath.Join(dir, "daily.json"))["name"]; ok {
		t.Fatal("null 应删掉该键")
	}
	// 平台独占键 → 40001，且**不落盘**
	before, err := os.ReadFile(filepath.Join(dir, "daily.json"))
	if err != nil {
		t.Fatalf("读前: %v", err)
	}
	if _, err := autoSvc(svc).UpdateAutomation(ctx, "daily", rawReq(t, map[string]any{
		"last_fired_at": "2030-01-01T00:00:00Z",
	})); numericCode(err) != 40001 || !strings.Contains(err.Error(), "平台") {
		t.Fatalf("写保留键应 40001，got %v（%d）", err, numericCode(err))
	}
	after, err := os.ReadFile(filepath.Join(dir, "daily.json"))
	if err != nil {
		t.Fatalf("读后: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("被拒的写面不该改动文件")
	}
	if _, ok := autoSvc(svc).GetAutomation("daily"); ok != nil {
		t.Fatal("daily 应仍在注册表里")
	}
	// 删不存在的 → 幂等 deleted:false
	del, err := autoSvc(svc).DeleteAutomation(ctx, "nope")
	if err != nil || del.Deleted {
		t.Fatalf("删不存在应 deleted:false 且无错: %+v %v", del, err)
	}
	// 删存在的
	del, err = autoSvc(svc).DeleteAutomation(ctx, "daily")
	if err != nil || !del.Deleted {
		t.Fatalf("删除应 deleted:true: %+v %v", del, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "daily.json")); !os.IsNotExist(err) {
		t.Fatal("文件应被删除")
	}
}

// TestAutomationPortPreview 预览：**不落盘、不建会话、也不要求定义已存在**。
func TestAutomationPortPreview(t *testing.T) {
	svc, dir := automationRig(t)
	req := dtoautomation.AutomationPreviewReq{
		// days_of_week 是 int（0=周日，与 time.Weekday 对齐）——写面允许重复/乱序，装载时归一
		Schedule: json.RawMessage(`{"kind":"weekly","timezone":"Asia/Shanghai","hour":9,"minute":0,"days_of_week":[3,1,3]}`),
		Count:    3,
	}
	resp, err := autoSvc(svc).PreviewAutomation(req)
	if err != nil {
		t.Fatalf("预览应成功: %v", err)
	}
	if len(resp.Occurrences) != 3 {
		t.Fatalf("应给 3 次: %+v", resp)
	}
	for _, o := range resp.Occurrences {
		if o.Wall == "" || o.Timezone != "Asia/Shanghai" || !strings.HasSuffix(o.At, "Z") {
			t.Fatalf("每项都要有本地墙钟与时区（UTC 供机器、墙钟供人）: %+v", o)
		}
	}
	// 预览不动磁盘、不动注册表
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("预览不该落盘: %v", entries)
	}
	if items := svc.mustListAutomations(t); len(items) != 0 {
		t.Fatalf("预览不该进注册表: %+v", items)
	}
	// 缺 schedule / 非法 from / 超上限
	if _, err := autoSvc(svc).PreviewAutomation(dtoautomation.AutomationPreviewReq{}); numericCode(err) != 40000 {
		t.Fatalf("缺 schedule 应 40000，got %v", err)
	}
	if _, err := autoSvc(svc).PreviewAutomation(dtoautomation.AutomationPreviewReq{
		Schedule: json.RawMessage(`{"kind":"daily","timezone":"UTC","hour":9,"minute":0}`), From: "昨天",
	}); numericCode(err) != 40000 {
		t.Fatalf("非法 from 应 40000，got %v", err)
	}
	if _, err := autoSvc(svc).PreviewAutomation(dtoautomation.AutomationPreviewReq{
		Schedule: json.RawMessage(`{"kind":"daily","timezone":"UTC","hour":9,"minute":0}`), Count: 999,
	}); err == nil {
		t.Fatal("count 超上限应报错（预览是给人看的，不是导出工具）")
	}
}

// TestAutomationPortRunsAndRunNow 运行史与手动触发的端口形态。
func TestAutomationPortRunsAndRunNow(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "")
	// 运行史：空史也是合法应答（limit 回带）
	runs, err := autoSvc(r.svc).ListAutomationRuns("daily")
	if err != nil || runs.Limit == 0 || runs.ID != "daily" {
		t.Fatalf("运行史: %+v %v", runs, err)
	}
	if _, err := autoSvc(r.svc).ListAutomationRuns("nope"); numericCode(err) != 40401 {
		t.Fatalf("未知 id 的运行史应 40401，got %v", err)
	}
	// 定义坏了 → 40001（不假装没有运行史）。注意 LoadAutomationDefinitions 的返回值是
	// "装载成功的条数"，坏定义**在册但不算成功**——所以这里断言的是注册表里有它。
	writeAutomationFile(t, filepath.Join(r.dir, "daily.json"), "{不是 JSON")
	planesOf(r.svc).Automation.LoadAutomationDefinitions(r.dir)
	if _, ok := planesOf(r.svc).Automation.AutomationByID("daily"); !ok {
		t.Fatal("坏定义也必须留在列表里（带 error），否则用户只看到它凭空消失")
	}
	if _, err := autoSvc(r.svc).ListAutomationRuns("daily"); numericCode(err) != 40001 {
		t.Fatalf("坏定义的运行史应 40001，got %v（%d）", err, numericCode(err))
	}
	// 手动触发：claimed 回带会话 id
	r.writeDaily(t, "daily", "")
	planesOf(r.svc).Automation.LoadAutomationDefinitions(r.dir)
	out, err := autoSvc(r.svc).RunAutomationNow(context.Background(), "daily")
	if err != nil || out.Status != "claimed" || out.SessionID == "" || out.Trigger != "manual" {
		t.Fatalf("手动触发应 claimed 且带会话: %+v %v", out, err)
	}
	// 会话在跑 → 再触发一次是 overlap（手动不等于可以并发）
	out2, err := autoSvc(r.svc).RunAutomationNow(context.Background(), "daily")
	if err != nil || out2.Status != "overlap" {
		t.Fatalf("并发手动触发应 overlap: %+v %v", out2, err)
	}
	// 停用后拒绝
	if _, err := planesOf(r.svc).Automation.WriteAutomation(context.Background(), "daily", map[string]any{"enabled": false}, false); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if _, err := autoSvc(r.svc).RunAutomationNow(context.Background(), "daily"); numericCode(err) != 40001 {
		t.Fatalf("停用后手动触发应 40001，got %v（%d）", err, numericCode(err))
	}
	// 运行史里能看到刚才这几笔（claimed → overlap，手动触发不推进游标故 claim 仍算一次）
	got, err := autoSvc(r.svc).ListAutomationRuns("daily")
	if err != nil || len(got.Runs) < 2 {
		t.Fatalf("运行史应留痕: %+v %v", got, err)
	}
}

// fileOnDisk 读一个定义文件成 map（断言"盘上到底写了什么"）。
func fileOnDisk(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	return out
}
