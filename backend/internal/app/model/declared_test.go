package model

import (
	"context"
	"errors"
	"testing"

	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/adapters/outbound/dsh"

	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/platform/resource"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/drivers/mechanism/credential"
	"github.com/morehao/ark-mate/internal/contract/dtomodel"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/golib/gerror"
)

// ——— 平台模型清单**写面**的判据（P4a 正文第五刀从 `manager/model_list_test.go` 搬来） ———
//
// 这几条用例原先是 `*Service` 的方法测试，夹具还带着"stub ACP + 两个合成底座 + 隔离 env"
// （那时的写面要摊默认值到各 runtime、要借 agent 读目录）。搬进本包之后它们只依赖三件事：
// **清单载体**（真库）、**供应商域平面**（真平面 + 一条真档案）、**过渡端口**（下边的 `stubHost`）。
// 这一层不认识 ACP、不认识执行面——夹具的形态就是这条边界最直白的证据。

// codeOf 把任意 error 归一成对外业务码（与信封里的 code 同源：控制器出口走的就是 FromError）。
func codeOf(err error) int {
	if err == nil {
		return 0
	}
	var ge gerror.Error
	if errors.As(code.FromError(err), &ge) {
		return ge.Code
	}
	return 0
}

// errCode 取对外业务码的**符号名**（`invalid_param`/`model_exists`…）。
func errCode(err error) string {
	if re, ok := code.RuntimeErrorOf(err); ok {
		return re.Code
	}
	return ""
}

// stubHost 过渡端口（`model.Host`）的测试替身。
//
// 三件事各自对应一半真实实现，这里只留判据要看的形态：
//
//	ModelRow      —— 真读库 + 真投影（写面的响应断言要看到真列值，不能是"恒回一个空壳"）
//	ModelEffect   —— 记下 `default_touched`，给一个可断言的固定回执
//	TrySyncModels —— 数调用次数，并按需回一句给用户看的话
type stubHost struct {
	st    *store.Store
	syncs int
	note  string
}

func (h *stubHost) ModelRow(ctx context.Context, id string) (*dtomodel.ModelResp, bool, error) {
	row, found, err := h.st.GetModelDefinition(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	return &dtomodel.ModelResp{
		ID: row.ID, ProviderID: row.ProviderID, Model: row.Model, Name: row.Name, Note: row.Note,
		Revision:        row.Revision,
		InputModalities: resource.ModalitiesFromColumn(row.InputModalities),
	}, true, nil
}

func (h *stubHost) ModelEffect(defaultTouched bool) *dtomodel.ModelEffect {
	eff := &dtomodel.ModelEffect{Effect: "none"}
	if defaultTouched {
		eff.Effect = "next_session"
	}
	return eff
}

func (h *stubHost) TrySyncModels(context.Context) string {
	h.syncs++
	return h.note
}

// declaredRig 一个带真库 + 真供应商平面的写面架子。
type declaredRig struct {
	st    *store.Store
	plane *provider.Plane
	host  *stubHost
	svc   DeclaredSvc
}

func newDeclaredRig(t *testing.T) *declaredRig {
	t.Helper()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	// 注册表与凭据机制必须与生产同源（`provider.NewPlane` 的注释：两份 = 两个事实源）。
	// 刻意不给密钥：带密钥的写面会落托管文档（那是供应商面的事），写面用例只需要"这条档案在"。
	reg := dsh.Registry()
	plane := provider.NewPlane(reg, registry.NewProviderSecrets(credential.New(reg)))
	plane.SetProviderDir(t.TempDir())
	host := &stubHost{st: st}
	return &declaredRig{st: st, plane: plane, host: host, svc: NewDeclared(st, plane, host)}
}

func (r *declaredRig) createProvider(t *testing.T, providerID string) {
	t.Helper()
	baseURL := "https://api.deepseek.com/v1"
	// 刻意**不给密钥**：密钥的落点是凭据面的事（带密钥的写面会落托管文档，需要一个可写落点），
	// 而写面只问"这条档案在不在"。
	if _, err := r.plane.CreateProvider(context.Background(), provider.CreateRequest{
		ID: providerID, Protocol: "openai", BaseURL: &baseURL,
	}); err != nil && errCode(err) != "provider_exists" {
		t.Fatalf("CreateProvider(%s): %v", providerID, err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestDeclaredWriteFailureFaces 写面的失败面，错误码逐条等于设计表。
func TestDeclaredWriteFailureFaces(t *testing.T) {
	ctx := context.Background()
	r := newDeclaredRig(t)
	r.createProvider(t, "p1")

	// ① 请求体里带 `runtime` → 40001（A2）：模型不再分底座，静默收下等于承认它有意义。
	_, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p1", Model: "m", Runtime: ptr("stub")})
	if errCode(err) != "invalid_param" || codeOf(err) != 40001 {
		t.Fatalf("带 runtime 应 40001 invalid_param，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}
	if _, err := r.svc.UpdateModel(ctx, "x", dtomodel.ModelPatchReq{Runtime: ptr("stub")}); errCode(err) != "invalid_param" {
		t.Fatalf("PATCH 带 runtime 也应被拒，got %q", errCode(err))
	}

	// ② provider 不存在 → 40401 provider_not_found。
	_, err = r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "nope", Model: "m"})
	if errCode(err) != "provider_not_found" || codeOf(err) != 40401 {
		t.Fatalf("provider 不存在应 40401 provider_not_found，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}

	// ③ 同 `(provider_id, model_id)` 重复 → 40901 model_exists。
	if _, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p1", Model: "v4-flash"}); err != nil {
		t.Fatalf("首建应成功: %v", err)
	}
	_, err = r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p1", Model: "v4-flash"})
	if errCode(err) != "model_exists" || codeOf(err) != 40901 {
		t.Fatalf("重复 (provider_id, model_id) 应 40901 model_exists，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}
	// 换 provider → 是另一条（同一模型 id 可以挂在两个端点下）。
	if _, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p2", Model: "v4-flash"}); errCode(err) != "provider_not_found" {
		t.Fatalf("p2 还没建，应先报 provider_not_found，got %q", errCode(err))
	}
	r.createProvider(t, "p2")
	if _, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p2", Model: "v4-flash"}); err != nil {
		t.Fatalf("换 provider 应可建: %v", err)
	}
	rows, err := r.st.ListModelDefinitions(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("换 provider 应可建（共 2 条）: n=%d err=%v", len(rows), err)
	}

	// ④ PATCH/DELETE 不存在的条目 → 40401 model_not_found / 幂等 false。
	if _, err := r.svc.UpdateModel(ctx, "no-such", dtomodel.ModelPatchReq{Name: ptr("x")}); errCode(err) != "model_not_found" {
		t.Fatalf("PATCH 不存在应 model_not_found，got %q", errCode(err))
	}
	if resp, err := r.svc.DeleteModel(ctx, "no-such"); err != nil || resp.Deleted {
		t.Fatalf("DELETE 不存在应幂等（deleted=false, code=0）: %+v err=%v", resp, err)
	}

	// ⑤ 提交即同步：**成功落笔**的每一次都触发（上面共 2 次成功建）。
	// 失败路径（重复 / provider 不存在 / 未知 id）在落笔之前就返回，不触发同步。
	if r.host.syncs != 2 {
		t.Fatalf("每次成功落笔都应触发同步: syncs=%d", r.host.syncs)
	}
}

// TestDefaultModelIsSingleAndDeleteClears 默认模型是**单一值**；删掉它连带清空（B4）。
//
// v11 的语义是"每 runtime 一条 + 删默认被 40501 拒绝"。v12 两条都变了：默认是**用户的偏好**
// （单一值），偏好跟着资源消失比拦住用户更合理——所以允许删，但**必须把清空说出来**
// （`default_cleared`），不静默。
func TestDefaultModelIsSingleAndDeleteClears(t *testing.T) {
	ctx := context.Background()
	r := newDeclaredRig(t)
	r.createProvider(t, "p1")
	a, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p1", Model: "m-a", IsDefault: ptr(true)})
	if err != nil {
		t.Fatalf("建默认条目: %v", err)
	}
	if a.Effect == nil || a.Effect.Effect != "next_session" {
		t.Fatalf("改默认应回 next_session: %+v", a.Effect)
	}
	if id, found, _ := r.st.DefaultModelID(ctx); !found || id != a.ID {
		t.Fatalf("首条设为默认后应是唯一默认: id=%q found=%v", id, found)
	}
	b, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p1", Model: "m-b", IsDefault: ptr(true)})
	if err != nil {
		t.Fatalf("建第二条默认条目: %v", err)
	}
	// 单一值：设 b 之后默认就是 b，而不是"每个底座各一条"。
	if id, _, _ := r.st.DefaultModelID(ctx); id != b.ID {
		t.Fatalf("默认应是最后设置的那条（单一值），实际 %q", id)
	}
	// 删默认**允许**，且如实回带 default_cleared。
	resp, err := r.svc.DeleteModel(ctx, b.ID)
	if err != nil || !resp.Deleted || !resp.DefaultCleared {
		t.Fatalf("删默认应成功并回 default_cleared=true: %+v err=%v", resp, err)
	}
	if _, found, _ := r.st.DefaultModelID(ctx); found {
		t.Fatal("删掉默认之后默认应被清空")
	}
	// 删一条**非**默认不报 default_cleared。
	resp, err = r.svc.DeleteModel(ctx, a.ID)
	if err != nil || !resp.Deleted || resp.DefaultCleared {
		t.Fatalf("删非默认不该报 default_cleared: %+v err=%v", resp, err)
	}
}

// TestDeclaredWriteFacesRequireSQLite memory 模式（无清单载体）→ 40501，
// 而不是"内存生效、盘上没留痕"。
func TestDeclaredWriteFacesRequireSQLite(t *testing.T) {
	ctx := context.Background()
	svc := NewDeclared(nil, nil, &stubHost{})
	if _, err := svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p", Model: "m"}); errCode(err) != "model_list_unavailable" || codeOf(err) != 40501 {
		t.Fatalf("memory 模式的清单写面应 40501 model_list_unavailable，got %q/%d（%v）", errCode(err), codeOf(err), err)
	}
	if _, err := svc.UpdateModel(ctx, "mdl_x", dtomodel.ModelPatchReq{Name: ptr("x")}); errCode(err) != "model_list_unavailable" {
		t.Fatalf("PATCH 也应 40501，got %q", errCode(err))
	}
	if _, err := svc.DeleteModel(ctx, "mdl_x"); errCode(err) != "model_list_unavailable" {
		t.Fatalf("DELETE 也应 40501，got %q", errCode(err))
	}
}

// TestUpdateModelTogglesDefaultBothWays PATCH 的两个方向都要生效（`is_default` 写的是 meta 的单一默认）。
//
// 这条来自一次**端到端探针**：删掉 dsh 的 overlay 之后想恢复，只删文件不够——平台侧仍声明着默认，
// T2 会继续把它下发、底座已经不认识了，于是每条新会话都失败；而"清掉默认"这个动作当时是**静默无效**的
// （写面只处理 `is_default:true`）。
func TestUpdateModelTogglesDefaultBothWays(t *testing.T) {
	ctx := context.Background()
	r := newDeclaredRig(t)
	r.createProvider(t, "p1")
	created, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{ProviderID: "p1", Model: "m1"})
	if err != nil {
		t.Fatalf("建条目: %v", err)
	}
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{IsDefault: ptr(true)}); err != nil {
		t.Fatalf("置默认: %v", err)
	}
	if id, found, _ := r.st.DefaultModelID(ctx); !found || id != created.ID {
		t.Fatalf("置默认后应指向这一行: id=%q found=%v", id, found)
	}
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{IsDefault: ptr(false)}); err != nil {
		t.Fatalf("清默认: %v", err)
	}
	if _, found, _ := r.st.DefaultModelID(ctx); found {
		t.Fatal("清默认后不该再有默认")
	}
	// 幂等：本来就不是默认 → 仍然成功。
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{IsDefault: ptr(false)}); err != nil {
		t.Fatalf("重复清默认应幂等: %v", err)
	}
	// 改正文的那一版仍走行 CAS（一行 + 一条审计同事务的纪律）：置默认/清默认不碰模型行，
	// 所以修一次正文就该看到 revision 前进。
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{Name: ptr("别名")}); err != nil {
		t.Fatalf("改正文: %v", err)
	}
	got, found, err := r.st.GetModelDefinition(ctx, created.ID)
	if err != nil || !found {
		t.Fatalf("读行: found=%v err=%v", found, err)
	}
	if got.Revision < 2 {
		t.Fatalf("改正文应推进一版 revision，实际 %d", got.Revision)
	}
	if got.Name != "别名" {
		t.Fatalf("改正文应落到 name 列: %+v", got)
	}
	// 输入模态与容量的归一（列化之后它们是列，不是自由 JSON）：未声明 → `text`；0 = 清除。
	if got.InputModalities != resource.ModalityText || got.MaxInputTokens != nil {
		t.Fatalf("默认落库形态应是 text + 无容量声明: %+v", got)
	}
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{
		InputModalities: &[]string{"image"}, MaxInputTokens: ptr(int64(8192)),
	}); err != nil {
		t.Fatalf("改模态与容量: %v", err)
	}
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{MaxInputTokens: ptr(int64(0))}); err != nil {
		t.Fatalf("清容量: %v", err)
	}
	got, _, _ = r.st.GetModelDefinition(ctx, created.ID)
	if got.MaxInputTokens != nil {
		t.Fatalf("0 = 清除这条声明: %+v", got)
	}
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{MaxInputTokens: ptr(int64(-1))}); errCode(err) != "invalid_param" {
		t.Fatalf("负数容量应被拒，got %q（%v）", errCode(err), err)
	}
	if _, err := r.svc.UpdateModel(ctx, created.ID, dtomodel.ModelPatchReq{InputModalities: &[]string{"audio"}}); errCode(err) != "modality_unsupported" {
		t.Fatalf("不认识的模态应被拒，got %q（%v）", errCode(err), err)
	}
}

// TestDeclaredResponseCarriesRowAndSyncNote 响应装配：真列值 + 同步那句话。
func TestDeclaredResponseCarriesRowAndSyncNote(t *testing.T) {
	ctx := context.Background()
	r := newDeclaredRig(t)
	r.createProvider(t, "p1")
	r.host.note = "已同步：下一次新会话生效"
	resp, err := r.svc.CreateModel(ctx, dtomodel.ModelCreateReq{
		ProviderID: "p1", Model: "m1", Name: ptr("主模型"), Note: ptr("备注"),
	})
	if err != nil {
		t.Fatalf("建条目: %v", err)
	}
	if resp.ID == "" || resp.Name != "主模型" || resp.Note != "备注" || resp.ProviderID != "p1" {
		t.Fatalf("响应应是这一行的真值: %+v", resp)
	}
	if resp.ID[:4] != "mdl_" {
		t.Fatalf("平台模型 id 应由本侧铸造: %q", resp.ID)
	}
	if resp.Effect == nil || resp.Effect.Note != "已同步：下一次新会话生效" {
		t.Fatalf("同步失败/成功的说明应挂到生效回执上: %+v", resp.Effect)
	}
}
