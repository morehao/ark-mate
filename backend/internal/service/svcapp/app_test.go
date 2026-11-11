package svcapp_test

// svcapp 用例层集成测试：走真实 SQLite（临时文件库）+ 真实 store，验证 REST 六接口的业务语义：
//   - 新建/读取/列表/覆盖/删除/启停；
//   - 成员行按 members + entities 重建（tool_domains 不落 app_members）；
//   - CAS 不中 → definition_changed(40901)；id 已存在 → app_exists(40901)；
//   - 白名单在册判断：entities[].kind / slots[].impl_id 不在册 → app_invalid(40001)。
//
// 测试文件用 `svcapp_test` 外部包并 import 真实 store：层守卫只扫**生产** import
// （`scanProductionPackages` 用 build.ImportDir，不含 _test.go），所以生产 svcapp 仍只管接口。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/core/appslot"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcapp"
	"github.com/morehao/golib/gerror"
	_ "github.com/morehao/golib/glog/driver/zap"
)

func newSvc(t *testing.T) (svcapp.AppSvc, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return svcapp.NewApp(st), st
}

// errCode 取错误的业务码（经 code.FromError 归一后）。
func errCode(err error) int {
	if err == nil {
		return 0
	}
	var ge gerror.Error
	if errors.As(code.FromError(err), &ge) {
		return ge.Code
	}
	return -1
}

// raw 把 JSON 文本包成写面原文对象的一个键值。
func req(kv map[string]string) dtoapp.AppWriteReq {
	out := dtoapp.AppWriteReq{}
	for k, v := range kv {
		out[k] = []byte(v)
	}
	return out
}

func TestAppCrudAndMembers(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()

	// 登记一个实体 + 一个实现，供白名单在册。
	appslot.RegisterEntity("wp3test_entity", appslot.EntitySpec{Table: "wp3test_tickets", IDPrefix: "tk_"})
	appslot.RegisterImpl("wp3test.impl", func(appslot.Ctx) error { return nil })

	// 新建（带 members + entities + slots + tool_domains）。
	wr, err := svc.WriteApp(ctx, "", req(map[string]string{
		"id":           `"wp3-test-app"`,
		"name":         `"测试应用"`,
		"members":      `{"experts":["e1","e2"],"skills":["s1"],"mcps":["m1"]}`,
		"entities":     `[{"kind":"wp3test_entity"}]`,
		"slots":        `[{"impl_id":"wp3test.impl"}]`,
		"tool_domains": `["foo","bar"]`,
		"page":         `{"nav_label":"入口","route":"/wp3","component":"Wp3Page"}`,
	}), true)
	if err != nil {
		t.Fatalf("新建失败：%v", err)
	}
	if !wr.Created || wr.ID != "wp3-test-app" || wr.Revision == 0 {
		t.Fatalf("新建结果异常：%+v", wr)
	}

	// 成员行：4 个成员（e1/e2/s1/m1）+ 1 个 entity = 5 行；tool_domains 不进 app_members。
	members, err := st.ListAppMembers(ctx, "wp3-test-app")
	if err != nil {
		t.Fatalf("读成员行失败：%v", err)
	}
	if len(members) != 5 {
		t.Fatalf("成员行数应为 5（e1,e2,s1,m1,entity），实得 %d：%+v", len(members), members)
	}

	// 详情：settings/page/members 都回带。
	got, err := svc.GetApp(ctx, "wp3-test-app")
	if err != nil {
		t.Fatalf("详情失败：%v", err)
	}
	if got.Name != "测试应用" || !got.HasPage || got.Page == nil || got.Page.Route != "/wp3" {
		t.Fatalf("详情投影异常：%+v", got)
	}
	if len(got.ToolDomains) != 2 || len(got.Entities) != 1 || len(got.Slots) != 1 {
		t.Fatalf("详情实体/域/槽异常：%+v", got)
	}

	// 列表：含该 app。
	list, err := svc.ListApps(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("列表异常：len=%d err=%v", len(list), err)
	}

	// 覆盖写：只改 name；成员整组重建（清空 members，只留 entity）。
	ur, err := svc.WriteApp(ctx, "wp3-test-app", req(map[string]string{
		"name":    `"改名后"`,
		"members": `null`,
	}), false)
	if err != nil {
		t.Fatalf("覆盖写失败：%v", err)
	}
	if ur.Revision <= wr.Revision {
		t.Fatalf("覆盖写后 revision 应增长：before=%d after=%d", wr.Revision, ur.Revision)
	}
	members2, _ := st.ListAppMembers(ctx, "wp3-test-app")
	if len(members2) != 1 {
		t.Fatalf("清空 members 后成员行应只剩 1 个 entity，实得 %d：%+v", len(members2), members2)
	}
	got2, _ := svc.GetApp(ctx, "wp3-test-app")
	if got2.Name != "改名后" || len(got2.Members.Experts) != 0 {
		t.Fatalf("覆盖写投影异常：%+v", got2)
	}

	// 启停。
	tr, err := svc.SetAppEnabled(ctx, "wp3-test-app", false)
	if err != nil || tr.Enabled {
		t.Fatalf("停用异常：%+v err=%v", tr, err)
	}
	got3, _ := svc.GetApp(ctx, "wp3-test-app")
	if got3.Enabled {
		t.Fatal("停用后详情应 Enabled=false")
	}

	// 删除（幂等）。
	dr, err := svc.DeleteApp(ctx, "wp3-test-app")
	if err != nil || !dr.Deleted {
		t.Fatalf("删除异常：%+v err=%v", dr, err)
	}
	dr2, err := svc.DeleteApp(ctx, "wp3-test-app")
	if err != nil || dr2.Deleted {
		t.Fatalf("重复删除应幂等 deleted=false：%+v err=%v", dr2, err)
	}
}

func TestAppExistsAndNotFound(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()

	if _, err := svc.WriteApp(ctx, "", req(map[string]string{"id": `"dup"`, "name": `"A"`}), true); err != nil {
		t.Fatalf("首次新建失败：%v", err)
	}
	// 重复 id → 40901 app_exists。
	_, err := svc.WriteApp(ctx, "", req(map[string]string{"id": `"dup"`, "name": `"B"`}), true)
	if c := errCode(err); c != code.Conflict {
		t.Fatalf("重复 id 应为 40901(%d)，实得 %d（err=%v）", code.Conflict, c, err)
	}
	// 更新不存在 → 40401 app_not_found。
	_, err = svc.WriteApp(ctx, "nope", req(map[string]string{"name": `"C"`}), false)
	if c := errCode(err); c != code.NotFound {
		t.Fatalf("更新不存在应为 40401(%d)，实得 %d（err=%v）", code.NotFound, c, err)
	}
}

func TestAppWhitelistRejected(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()

	// 未知 entity kind → 40001 app_invalid。
	_, err := svc.WriteApp(ctx, "", req(map[string]string{
		"id":       `"bad-entity"`,
		"name":     `"坏实体"`,
		"entities": `[{"kind":"not_registered_kind"}]`,
	}), true)
	if c := errCode(err); c != code.InvalidRequest {
		t.Fatalf("未在册 entity 应为 40001(%d)，实得 %d（err=%v）", code.InvalidRequest, c, err)
	}

	// 未知 impl_id → 40001 app_invalid。
	_, err = svc.WriteApp(ctx, "", req(map[string]string{
		"id":    `"bad-slot"`,
		"name":  `"坏槽"`,
		"slots": `[{"impl_id":"not.registered"}]`,
	}), true)
	if c := errCode(err); c != code.InvalidRequest {
		t.Fatalf("未在册 impl_id 应为 40001(%d)，实得 %d（err=%v）", code.InvalidRequest, c, err)
	}
}

func TestAppCASConflict(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()

	if _, err := svc.WriteApp(ctx, "", req(map[string]string{"id": `"cas-app"`, "name": `"A"`}), true); err != nil {
		t.Fatalf("新建失败：%v", err)
	}
	rev, _ := st.AppRevision(ctx, "cas-app")

	// 模拟并发：直接以旧 rev 调 store 的 SetAppEnabled（用较小 rev 触发 CAS 不中）。
	// svc.SetAppEnabled 会先读当前 rev 再 CAS，所以这里直接打 store 层制造冲突。
	ok, err := st.SetAppEnabledDefinition(ctx, "cas-app", true, rev-1, "test")
	if err != nil {
		t.Fatalf("CAS 调用出错：%v", err)
	}
	if ok {
		t.Fatal("以过时 rev 写入应 CAS 不中（applied=false），但成功了")
	}
}

// TestAppSourceSelfOnCreateAndPreservedOnUpdate v41：POST /apps 新建 → source=self；
// PUT /apps/:id 覆盖写**不改** source（来源身份一经落定不可被写面覆盖改掉）。
func TestAppSourceSelfOnCreateAndPreservedOnUpdate(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()

	if _, err := svc.WriteApp(ctx, "", req(map[string]string{"id": `"src-app"`, "name": `"A"`}), true); err != nil {
		t.Fatalf("新建失败：%v", err)
	}
	// 新建后 source=self（读面）。
	got, err := svc.GetApp(ctx, "src-app")
	if err != nil {
		t.Fatalf("详情失败：%v", err)
	}
	if got.Source != svcapp.AppSourceSelf {
		t.Fatalf("新建 app 的 source 应为 %q，实得 %q", svcapp.AppSourceSelf, got.Source)
	}

	// 把行内 source 篡改成 market（模拟"从市场来的定义被 PUT 覆盖"场景），再走 PUT：
	// 覆盖写不得把 source 改回 self，也不得改动它。
	if err := st.DB().Exec(`UPDATE apps SET source='market' WHERE id='src-app'`).Error; err != nil {
		t.Fatalf("篡改 source: %v", err)
	}
	if _, err := svc.WriteApp(ctx, "src-app", req(map[string]string{"name": `"改名"`}), false); err != nil {
		t.Fatalf("覆盖写失败：%v", err)
	}
	got2, _ := svc.GetApp(ctx, "src-app")
	if got2.Source != svcapp.AppSourceMarket {
		t.Fatalf("PUT 不应改 source（应仍为 market），实得 %q", got2.Source)
	}
	if got2.Name != "改名" {
		t.Fatalf("PUT 应改 name，实得 %q", got2.Name)
	}
}
