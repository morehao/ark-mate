package router_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
)

// TestProviderWriteFaceREST A1：四条 provider 端点走**真实装配路径**
// （`manager.NewService` + `router.New`，含 v1 分组与默认中间件），逐条断言错误码。
//
// 为什么不只用 manager 的进程内调用：契约守卫只保证"路由注册了、README 写了"，
// 而"控制器把码映射对了、请求体绑定对了、路径参数取对了"只有走一遍引擎才看得见。
func TestProviderWriteFaceREST(t *testing.T) {
	e, _ := newEngineWithService(t)

	// POST → 新建成功，密钥只以 has_key 呈现。
	env, _ := do(t, e, "POST", "/v1/mate/providers", `{"id":"p1","protocol":"openai","base_url":"https://api.deepseek.com/v1","api_key":"sk-secret","note":"主账号"}`)
	if env.Code != 0 {
		t.Fatalf("POST /providers 应成功: %+v", env)
	}
	var created dtomodel.ProviderResp
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("解析新建响应: %v", err)
	}
	if created.ID != "p1" || created.Protocol != "openai" || created.BaseURL == "" || !created.HasKey || created.Note != "主账号" {
		t.Fatalf("新建响应不符: %+v", created)
	}
	if strings.Contains(string(env.Data), "sk-secret") {
		t.Fatalf("响应里出现了明文密钥: %s", env.Data)
	}

	// POST 再建同 id → 40901 provider_exists。
	env, _ = do(t, e, "POST", "/v1/mate/providers", `{"id":"p1","protocol":"openai"}`)
	if env.Code != 40901 || !strings.Contains(env.Msg, "已存在") {
		t.Fatalf("重复新建应 40901（provider_exists，文案说明已存在）: %+v", env)
	}

	// POST id 非法 → 40001 invalid_provider_id。
	env, _ = do(t, e, "POST", "/v1/mate/providers", `{"id":"../escape","protocol":"openai"}`)
	if env.Code != 40001 || !strings.Contains(env.Msg, "非法") {
		t.Fatalf("非法 id 应 40001（invalid_provider_id，文案说明规则）: %+v", env)
	}

	// GET 列表：带引用面与问题清单。
	env, _ = do(t, e, "GET", "/v1/mate/providers", "")
	if env.Code != 0 {
		t.Fatalf("GET /providers 应成功: %+v", env)
	}
	var list []dtomodel.ProviderResp
	if err := json.Unmarshal(env.Data, &list); err != nil || len(list) != 1 {
		t.Fatalf("列表面不符: n=%d err=%v body=%s", len(list), err, env.Data)
	}
	if list[0].Refs.Models == nil || list[0].Problems == nil {
		t.Fatalf("refs/problems 必须是数组（空也要是 []，前端不必判 null）: %+v", list[0])
	}

	// PATCH 不存在 → 40401 provider_not_found（v11 收紧：不再 upsert）。
	env, _ = do(t, e, "PATCH", "/v1/mate/providers/nope", `{"protocol":"openai"}`)
	if env.Code != 40401 || !strings.Contains(env.Msg, "不存在") {
		t.Fatalf("PATCH 不存在应 40401（provider_not_found，文案指出用 POST）: %+v", env)
	}

	// PATCH 成功：改端点 + 清密钥。
	env, _ = do(t, e, "PATCH", "/v1/mate/providers/p1", `{"base_url":"https://api.openai.com/v1","api_key":""}`)
	if env.Code != 0 {
		t.Fatalf("PATCH /providers/p1 应成功: %+v", env)
	}
	var patched dtomodel.ProviderResp
	_ = json.Unmarshal(env.Data, &patched)
	if patched.BaseURL != "https://api.openai.com/v1" || patched.HasKey {
		t.Fatalf("PATCH 后形态不符: %+v", patched)
	}

	// DELETE → deleted:true；重复 DELETE → deleted:false 且 code=0（幂等）。
	env, _ = do(t, e, "DELETE", "/v1/mate/providers/p1", "")
	if env.Code != 0 {
		t.Fatalf("DELETE 应成功: %+v", env)
	}
	var del dtomodel.ProviderDeleteResp
	_ = json.Unmarshal(env.Data, &del)
	if !del.Deleted {
		t.Fatalf("首次删除应 deleted:true: %+v", del)
	}
	env, _ = do(t, e, "DELETE", "/v1/mate/providers/p1", "")
	if env.Code != 0 {
		t.Fatalf("重复删除必须幂等（code=0）: %+v", env)
	}
	_ = json.Unmarshal(env.Data, &del)
	if del.Deleted {
		t.Fatalf("重复删除应 deleted:false: %+v", del)
	}
}
