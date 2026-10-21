package router_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— automation 面的控制器契约（M4-5）———
//
// 走**真实路由装配**（router.New）：路径、方法与中间件形态都与线上一致，
// 断言的是"每个端点的错误码"这条 Gate M4 验收项。
//
// P4a 正文第九刀起这组用例跑在**真自动化域平面 + 真库**上（`Ports.AutomationPlane`）：
// 此前它替换的是 `AutomationSvc` 替身，靠替身返回的假错误去验码；现在"对外的码"由
// `svcautomation`（用例层）从**真领域动作**里算出来，所以验的是真链路——
// `HTTP → ctrautomation → svcautomation → domain/automation`。
//
// 两个**真平面造不出来**的码（载体 CAS 冲突、裸错误兜底 50000）随之搬到控制器自己的用例里
// （`internal/delivery/controller/ctrautomation/automation_test.go`，那里用接口替身），因为那两条验的是
// **控制器的错误出口**而不是领域行为。

// autoHostDouble 宿主端口替身：建会话返回一个假 id，其余什么也不做。
//
// 平面只要求"把内容送进哪条通道"，所以替身不需要任何会话域知识。
type autoHostDouble struct{}

func (autoHostDouble) CreateSession(_ context.Context, _ automation.HostSessionReq) (string, error) {
	return "ses_stub", nil
}
func (autoHostDouble) CancelRun(_ context.Context, _ string) error { return nil }
func (autoHostDouble) SessionRunning(_ string) bool                { return false }
func (autoHostDouble) EmitRun(_ context.Context, _ string, _ int64, _ map[string]any) {
}

// autoPlane 造一个**带库载体**的自动化域平面（真表、真写面）。
func autoPlane(t *testing.T) *automation.Plane {
	t.Helper()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	plane := automation.NewPlane()
	plane.SetDefinitionFiles(registry.AutomationDefinitionFiles{})
	plane.SetStore(st)
	plane.SetHost(autoHostDouble{})
	return plane
}

// autoEngine 只把 automation 那一格换成**真平面**（其余端口仍由 stubSvc 提供）。
func autoEngine(t *testing.T, plane *automation.Plane) *gin.Engine {
	deps := stubDeps(t, &stubSvc{})
	deps.automationPlane = plane
	return newRestEngine(deps)
}

// autoDef 一条**能过闸门**的最小定义（真写面会校验 schedule 与 target.prompt——
// 这两条闸门正是"替身验不出来"的那部分，所以夹具必须给合法的）。
func autoDef() map[string]any {
	return map[string]any{
		"enabled":  true,
		"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
		"target":   map[string]any{"prompt": "跑一下", "cwd": "/tmp"},
	}
}

// seedAutomation 经真写面落一条定义（夹具与被测路径同源，不另造一套装载）。
func seedAutomation(t *testing.T, plane *automation.Plane, id string, patch map[string]any) {
	t.Helper()
	def := autoDef()
	for k, v := range patch {
		def[k] = v
	}
	if _, err := plane.WriteAutomation(context.Background(), id, def, true); err != nil {
		t.Fatalf("seed automation %s: %v", id, err)
	}
}

// TestAutomationRoutesHappyPath 8 条路由都能通，且写面语义真的落到了盘上。
//
// 断言从"替身收到过什么"改成"读回来是什么"：`PUT` 的"只改出现过的键"、`DELETE` 之后
// 真的取不到、手动触发真的建了会话——这三条以前靠替身标志位，替身一撤就没人验了。
func TestAutomationRoutesHappyPath(t *testing.T) {
	plane := autoPlane(t)
	r := autoEngine(t, plane)

	// 新建
	env, _ := do(t, r, "POST", "/v1/mate/automations",
		`{"id":"daily","name":"巡检","enabled":true,"schedule":{"kind":"daily","timezone":"UTC","hour":9,"minute":0},`+
			`"target":{"prompt":"跑一下","cwd":"/tmp"}}`)
	if env.Code != 0 || !strings.Contains(string(env.Data), `"daily"`) {
		t.Fatalf("新建应成功: %+v", env)
	}
	// 列表
	env, _ = do(t, r, "GET", "/v1/mate/automations", "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"巡检"`) {
		t.Fatalf("列表应含新建那条: %+v", env)
	}
	// 取单条
	env, _ = do(t, r, "GET", "/v1/mate/automations/daily", "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"巡检"`) {
		t.Fatalf("取单条应成功: %+v", env)
	}
	// 覆盖写：只改出现过的键（name 之外的字段必须原样保留）
	env, _ = do(t, r, "PUT", "/v1/mate/automations/daily", `{"name":"改名"}`)
	if env.Code != 0 || !strings.Contains(string(env.Data), `"改名"`) {
		t.Fatalf("覆盖写应成功: %+v", env)
	}
	env, _ = do(t, r, "GET", "/v1/mate/automations/daily", "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"改名"`) ||
		!strings.Contains(string(env.Data), `"UTC"`) {
		t.Fatalf("只改出现过的键（schedule 的时区应原样保留）: %+v", env)
	}
	// 运行史（没触发过也要是 `[]` 而不是 `null`）
	env, _ = do(t, r, "GET", "/v1/mate/automations/daily/runs", "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"runs":[]`) {
		t.Fatalf("运行史应成功且是空数组: %+v", env)
	}
	// 手动触发：真建了一个会话
	env, _ = do(t, r, "POST", "/v1/mate/automations/daily/run", "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"ses_stub"`) {
		t.Fatalf("手动触发应成功并回带会话 id: %+v", env)
	}
	// 预览（不落盘、不建会话）
	env, _ = do(t, r, "POST", "/v1/mate/automations/preview",
		`{"schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0}}`)
	if env.Code != 0 || !strings.Contains(string(env.Data), `"occurrences"`) {
		t.Fatalf("预览应成功: %+v", env)
	}
	// 删除后取不到
	env, _ = do(t, r, "DELETE", "/v1/mate/automations/daily", "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"deleted":true`) {
		t.Fatalf("删除应成功: %+v", env)
	}
	env, _ = do(t, r, "GET", "/v1/mate/automations/daily", "")
	if env.Code != 40401 {
		t.Fatalf("删除后应 40401: %+v", env)
	}
}

// TestAutomationPreviewIsNotAnID `preview` 必须走预览端点，而不是被 `:id` 吃掉。
//
// 这是注册顺序的真实风险：gin 会优先匹配静态段，但一旦有人把 preview 挪到 :id 之后，
// 表现是"预览神秘地报 40401 未知 automation preview"——所以用断言钉住。
func TestAutomationPreviewIsNotAnID(t *testing.T) {
	r := autoEngine(t, autoPlane(t))
	env, _ := do(t, r, "POST", "/v1/mate/automations/preview",
		`{"schedule":{"kind":"once","timezone":"UTC","at":"2099-01-01T00:00:00Z"}}`)
	if env.Code != 0 {
		t.Fatalf("preview 不该被当成 :id: %+v", env)
	}
	// 同一个词走 GET 就是一条 id（真平面里不存在 ⇒ 40401）：证明两条路由各自独立。
	env, _ = do(t, r, "GET", "/v1/mate/automations/preview", "")
	if env.Code != 40401 {
		t.Fatalf("GET /automations/preview 应是一条未知 id: %+v", env)
	}
}

// TestAutomationRoutesErrorCodes 每个端点的错误码：领域动作的拒绝必须原样落成对外码（不是 code:-1）。
//
// 40000 与 40001 的分界沿用能力写面已确立的那条线：**请求形态**（缺字段/类型不符/body 不是对象）
// 是 40000，**取值语义**（保留键、id 非法、定义不可执行、未配置目录、已停用）是 40001。
// 两者混用会让"我该改参数还是改内容"这个问题在客户端无从判断。
func TestAutomationRoutesErrorCodes(t *testing.T) {
	// 未配置载体：既没有库句柄、也没有定义目录 ⇒ 写面拒绝（同一个真平面的一套最小装配）。
	barePlane := automation.NewPlane()
	barePlane.SetDefinitionFiles(registry.AutomationDefinitionFiles{})

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		seed   func(t *testing.T, plane *automation.Plane)
		plane  *automation.Plane
		want   int
	}{
		{name: "取单条不存在", method: "GET", path: "/v1/mate/automations/nope", want: 40401},
		{name: "运行史不存在", method: "GET", path: "/v1/mate/automations/nope/runs", want: 40401},
		{name: "新建重复", method: "POST", path: "/v1/mate/automations", body: `{"id":"d"}`,
			seed: func(t *testing.T, p *automation.Plane) { seedAutomation(t, p, "d", map[string]any{}) },
			want: 40901},
		{name: "写平台独占键", method: "PUT", path: "/v1/mate/automations/d",
			body: `{"last_fired_at":"2026-01-01T00:00:00Z"}`,
			seed: func(t *testing.T, p *automation.Plane) { seedAutomation(t, p, "d", map[string]any{}) },
			want: 40001},
		{name: "定义非法", method: "PUT", path: "/v1/mate/automations/d",
			body: `{"schedule":{"kind":"daily"}}`,
			seed: func(t *testing.T, p *automation.Plane) { seedAutomation(t, p, "d", map[string]any{}) },
			want: 40001},
		{name: "未配置载体", method: "DELETE", path: "/v1/mate/automations/d", plane: barePlane, want: 40001},
		{name: "未配置载体（新建）", method: "POST", path: "/v1/mate/automations",
			body: `{"id":"d"}`, plane: barePlane, want: 40001},
		{name: "预览非法 schedule", method: "POST", path: "/v1/mate/automations/preview",
			body: `{"schedule":{"kind":"daily"}}`, want: 40001},
		{name: "手动触发已停用", method: "POST", path: "/v1/mate/automations/d/run",
			seed: func(t *testing.T, p *automation.Plane) {
				seedAutomation(t, p, "d", map[string]any{"enabled": false})
			},
			want: 40001},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plane := c.plane
			if plane == nil {
				plane = autoPlane(t)
			}
			if c.seed != nil {
				c.seed(t, plane)
			}
			r := autoEngine(t, plane)
			env, _ := do(t, r, c.method, c.path, c.body)
			if env.Code != c.want {
				t.Fatalf("期望码 %d，got %d（msg=%s）", c.want, env.Code, env.Msg)
			}
		})
	}
}

// TestAutomationBindErrorIsInvalidParam body 不是合法 JSON / 不是对象 → 40000（不是 50000）。
func TestAutomationBindErrorIsInvalidParam(t *testing.T) {
	r := autoEngine(t, autoPlane(t))
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/mate/automations", `[1,2,3]`},
		{"PUT", "/v1/mate/automations/d", `not json`},
		{"POST", "/v1/mate/automations/preview", `"x"`},
	} {
		env, _ := do(t, r, c.method, c.path, c.body)
		if env.Code != 40000 {
			t.Fatalf("%s %s body=%s 期望 40000，got %d", c.method, c.path, c.body, env.Code)
		}
		if !strings.Contains(env.Msg, "参数错误") {
			t.Fatalf("文案应指出是参数问题: %s", env.Msg)
		}
	}
}
