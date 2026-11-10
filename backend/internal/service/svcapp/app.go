// Package svcapp 是「应用定义」（一期 A+Slot）的用例层（REST `/apps` 六接口的业务面）。
//
// 与 svccapability 的专家面同构：控制器只认本包声明的窄接口，本包持有持久化端口（store 适配），
// 把领域校验（`capability.ValidateApp`）+ 白名单在册判断 + 落库（apps + app_members + 审计）折在
// 一处。控制器因此拿不到 store/domain，写面纪律只有一条路。
//
// 三条硬规矩（docs/agents/layout.md）：本包只依赖更内层（core/model/dto），不 import 其它
// `service/svc*`（层守卫 deny `internal/service`）。
package svcapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/appslot"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/definition"
	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// Store 是应用定义的持久化窄端口（由装配侧用 `*store.Store` 适配）。
//
// 只暴露读面 + 写面 CAS：与 expert 的 `Store` 同一条判据——写入交给 store 事务，本层不判并发。
// `expectedRev` 对 apps 而言是 `definition_revisions.seq`（apps 无 revision 列，见 store/app.go）。
type Store interface {
	ListAppDefinitions(ctx context.Context) (model.AppEntityList, error)
	GetAppDefinition(ctx context.Context, id string) (*model.AppEntity, bool, error)
	ListAppMembers(ctx context.Context, appID string) (model.AppMemberEntityList, error)
	AppRevision(ctx context.Context, id string) (int64, error)
	UpsertAppDefinition(ctx context.Context, id string, row *model.AppEntity, members model.AppMemberEntityList, expectedRev int64, create bool, actor string) (bool, error)
	DeleteAppDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error)
	SetAppEnabledDefinition(ctx context.Context, id string, enabled bool, expectedRev int64, actor string) (bool, error)
}

// AppSvc 是应用定义用例端口（REST `/apps` 六接口）。
type AppSvc interface {
	// ListApps 列出全部 app（含禁用；列表不返回 settings_schema 正文，只带 has_page 派生位）。
	ListApps(ctx context.Context) ([]dtoapp.AppResp, error)
	// GetApp 读一条 app 详情（含 settings_schema 全量）；不存在 → 40401 app_not_found。
	GetApp(ctx context.Context, id string) (*dtoapp.AppResp, error)
	// WriteApp 新建/覆盖一条 app。create=true 时 id 取自 body（必填），id 已存在 → app_exists(40901)；
	// 否则 id 取自 pathID，基于当前 rev 做 CAS，不中 → definition_changed(40901)。
	WriteApp(ctx context.Context, pathID string, req dtoapp.AppWriteReq, create bool) (*dtoapp.AppWriteResp, error)
	// DeleteApp 删除一条 app（幂等：不存在 → deleted:false 且无错）。
	DeleteApp(ctx context.Context, id string) (*dtoapp.AppDeleteResp, error)
	// SetAppEnabled 启停一条 app（禁用只影响新选择，不影响已建会话）。
	SetAppEnabled(ctx context.Context, id string, enabled bool) (*dtoapp.AppToggleResp, error)
}

// appSvc 是 AppSvc 的实现：只持持久化端口。
type appSvc struct {
	store Store
}

// NewApp 构造应用定义用例（装配根建一次，控制器只收接口）。
func NewApp(store Store) AppSvc { return &appSvc{store: store} }

// Locator 应用定义给人看的定位串（DB 载体，无文件路径）。
func Locator(id string) string { return "db:app/" + id }

// ListApps 列出全部 app（含禁用；不含 settings_schema 正文）。
func (s *appSvc) ListApps(ctx context.Context) ([]dtoapp.AppResp, error) {
	if s.store == nil {
		return []dtoapp.AppResp{}, nil
	}
	rows, err := s.store.ListAppDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]dtoapp.AppResp, 0, len(rows))
	for i := range rows {
		resp, perr := s.appRespFromRow(ctx, &rows[i], false)
		if perr != nil {
			return nil, perr
		}
		out = append(out, *resp)
	}
	return out, nil
}

// GetApp 读一条 app 详情（含 settings_schema 全量）。
func (s *appSvc) GetApp(ctx context.Context, id string) (*dtoapp.AppResp, error) {
	if s.store == nil {
		return nil, definition.ErrCapabilityUnavailable()
	}
	row, ok, err := s.store.GetAppDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFoundErr(id)
	}
	return s.appRespFromRow(ctx, row, true)
}

// WriteApp 新建/覆盖一条 app。
func (s *appSvc) WriteApp(ctx context.Context, pathID string, req dtoapp.AppWriteReq, create bool) (*dtoapp.AppWriteResp, error) {
	if s.store == nil {
		return nil, definition.ErrCapabilityUnavailable()
	}
	app, err := s.buildApp(ctx, pathID, req, create)
	if err != nil {
		return nil, err
	}

	// 落库前校验：先词法/上限（ValidateApp），再白名单在册（写面职责，ValidateApp 不做）。
	if err := capability.ValidateApp(app); err != nil {
		return nil, invalidErr(app.ID, err)
	}
	if err := checkWhitelist(app); err != nil {
		return nil, err
	}

	var expectedRev int64
	if create {
		if _, ok, gerr := s.store.GetAppDefinition(ctx, app.ID); gerr != nil {
			return nil, gerr
		} else if ok {
			return nil, existsErr(app.ID)
		}
	} else {
		if _, ok, gerr := s.store.GetAppDefinition(ctx, app.ID); gerr != nil {
			return nil, gerr
		} else if !ok {
			return nil, notFoundErr(app.ID)
		}
		rev, gerr := s.store.AppRevision(ctx, app.ID)
		if gerr != nil {
			return nil, gerr
		}
		expectedRev = rev
	}

	row, members, err := encodeApp(app)
	if err != nil {
		return nil, err
	}
	applied, err := s.store.UpsertAppDefinition(ctx, app.ID, row, members, expectedRev, create, definition.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !applied {
		// CAS 没中（并发写抢先 / 行已不存在）：翻成 definition_changed(40901)，绝不 last-write-wins。
		return nil, definition.DefinitionChangedErr(Locator(app.ID))
	}
	rev, err := s.store.AppRevision(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	return &dtoapp.AppWriteResp{ID: app.ID, Path: Locator(app.ID), Created: create, Revision: rev}, nil
}

// DeleteApp 删除一条 app（幂等）。
func (s *appSvc) DeleteApp(ctx context.Context, id string) (*dtoapp.AppDeleteResp, error) {
	if s.store == nil {
		return nil, definition.ErrCapabilityUnavailable()
	}
	if !definition.ValidDefinitionID(id) {
		return &dtoapp.AppDeleteResp{ID: id}, nil // 非法 id 不可能存在 → 幂等 no-op
	}
	_, ok, err := s.store.GetAppDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &dtoapp.AppDeleteResp{ID: id}, nil
	}
	rev, err := s.store.AppRevision(ctx, id)
	if err != nil {
		return nil, err
	}
	deleted, err := s.store.DeleteAppDefinition(ctx, id, rev, definition.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, definition.DefinitionChangedErr(Locator(id))
	}
	return &dtoapp.AppDeleteResp{ID: id, Deleted: true}, nil
}

// SetAppEnabled 启停一条 app。
func (s *appSvc) SetAppEnabled(ctx context.Context, id string, enabled bool) (*dtoapp.AppToggleResp, error) {
	if s.store == nil {
		return nil, definition.ErrCapabilityUnavailable()
	}
	_, ok, err := s.store.GetAppDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFoundErr(id)
	}
	rev, err := s.store.AppRevision(ctx, id)
	if err != nil {
		return nil, err
	}
	applied, err := s.store.SetAppEnabledDefinition(ctx, id, enabled, rev, definition.DefinitionWriteActor)
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, definition.DefinitionChangedErr(Locator(id))
	}
	newRev, err := s.store.AppRevision(ctx, id)
	if err != nil {
		return nil, err
	}
	return &dtoapp.AppToggleResp{ID: id, Enabled: enabled, Revision: newRev}, nil
}

// ——— 内部：构建 / 编解码 / 白名单 ———

// buildApp 把写面原文对象合并进既有定义（覆盖写）或从零构建（新建），解成强类型 App。
//
// 语义同 expert 写面：PUT 只改 body 里出现的键（`null` 删键），未出现的保留原值；create 时 base 为空。
func (s *appSvc) buildApp(ctx context.Context, pathID string, req dtoapp.AppWriteReq, create bool) (*capability.App, error) {
	patch := definition.RawPatch(req)

	id := pathID
	bodyID, hasBodyID, err := definition.RawString(req, "id")
	if err != nil {
		return nil, err
	}
	if create {
		if !hasBodyID || strings.TrimSpace(bodyID) == "" {
			return nil, definition.InvalidParamErr("新建 app 必须带 id")
		}
		id = strings.TrimSpace(bodyID)
	} else {
		if hasBodyID && bodyID != pathID {
			return nil, definition.InvalidParamErr(fmt.Sprintf(
				"路径 id %q 与 body id %q 不一致（id 不可改：要换 id 就删掉重建）", pathID, bodyID))
		}
	}
	if !definition.ValidDefinitionID(id) {
		return nil, definition.InvalidDefinitionIDErr("app", id)
	}

	var baseDoc map[string]json.RawMessage
	if !create {
		row, ok, gerr := s.store.GetAppDefinition(ctx, id)
		if gerr != nil {
			return nil, gerr
		}
		if !ok {
			return nil, notFoundErr(id)
		}
		baseDoc, gerr = rowToDoc(row)
		if gerr != nil {
			return nil, gerr
		}
	} else {
		baseDoc = map[string]json.RawMessage{}
	}

	// 合并 patch（patch 值已是 json.RawMessage；nil = 删键）。
	merged := make(map[string]json.RawMessage, len(baseDoc)+len(patch))
	for k, v := range baseDoc {
		merged[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(merged, k)
			continue
		}
		raw, ok := v.(json.RawMessage)
		if !ok {
			b, merr := json.Marshal(v)
			if merr != nil {
				return nil, merr
			}
			raw = b
		}
		merged[k] = raw
	}
	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var app capability.App
	if err := json.Unmarshal(mergedBytes, &app); err != nil {
		return nil, definition.InvalidParamErr(fmt.Sprintf("app 定义无法解析：%v", err))
	}
	app.ID = id
	return &app, nil
}

// checkWhitelist 是写面的**白名单在册判断**（ValidateApp 只做词法/上限，不做这一层）：
//
//   - entities[].kind 必须编译期在 `appslot.KnownEntities` 里在册；
//   - slots[].impl_id 必须编译期在 `appslot.KnownImpls` 里在册。
//
// 不在册 = "引用了不存在的实体/实现"（数据只选 impl、不提供 impl，设计 §5.3），拒绝 40001 app_invalid。
func checkWhitelist(app *capability.App) error {
	knownEntities := appslot.KnownEntities()
	for i, e := range app.Entities {
		if _, ok := knownEntities[e.Kind]; !ok {
			return invalidErr(app.ID, fmt.Errorf("entities[%d].kind %q 不在实体注册表（appslot.KnownEntities）里在册", i, e.Kind))
		}
	}
	knownImpls := appslot.KnownImpls()
	for i, sl := range app.Slots {
		if _, ok := knownImpls[sl.ImplID]; !ok {
			return invalidErr(app.ID, fmt.Errorf("slots[%d].impl_id %q 不在实现白名单（appslot.KnownImpls）里在册", i, sl.ImplID))
		}
	}
	return nil
}

// encodeApp 把强类型 App 折成 apps 行 + 成员行集合。
//
// 成员行按 members（4 类）+ entities（member_kind=entity）重建；tool_domains **不是**成员
// （app_members.member_kind 只认 expert/automation/skill/mcp/entity，设计 §5.9），它们随 apps 行落库。
func encodeApp(app *capability.App) (*model.AppEntity, model.AppMemberEntityList, error) {
	tagsJSON, err := marshalJSONArray(app.Tags)
	if err != nil {
		return nil, nil, err
	}
	entitiesJSON, err := marshalJSONArray(app.Entities)
	if err != nil {
		return nil, nil, err
	}
	domainsJSON, err := marshalJSONArray(app.ToolDomains)
	if err != nil {
		return nil, nil, err
	}
	slotsJSON, err := marshalJSONArray(app.Slots)
	if err != nil {
		return nil, nil, err
	}
	pageJSON := ""
	if app.Page != nil {
		b, perr := json.Marshal(app.Page)
		if perr != nil {
			return nil, nil, perr
		}
		pageJSON = string(b)
	}
	enabled := 1
	if !app.IsEnabled() {
		enabled = 0
	}
	row := &model.AppEntity{
		ID:                 app.ID,
		Name:               app.Name,
		Description:        app.Description,
		Avatar:             app.Avatar,
		TagsJSON:           tagsJSON,
		EntitiesJSON:       entitiesJSON,
		ToolDomainsJSON:    domainsJSON,
		SlotsJSON:          slotsJSON,
		PageJSON:           pageJSON,
		SettingsSchemaJSON: app.SettingsSchema,
		APIVersion:         app.APIVersion,
		Enabled:            enabled,
	}
	return row, buildMemberRows(app), nil
}

// buildMemberRows 按成员声明重建 app_members 行（owned 缺省 0=引用）。
func buildMemberRows(app *capability.App) model.AppMemberEntityList {
	var rows model.AppMemberEntityList
	add := func(kind string, ids ...[]string) {
		for _, list := range ids {
			for _, id := range list {
				if strings.TrimSpace(id) == "" {
					continue
				}
				rows = append(rows, model.AppMemberEntity{AppID: app.ID, MemberKind: kind, MemberID: id})
			}
		}
	}
	add(model.AppMemberKindExpert, app.Members.Experts)
	add(model.AppMemberKindAutomation, app.Members.Automations)
	add(model.AppMemberKindSkill, app.Members.Skills)
	add(model.AppMemberKindMcp, app.Members.Mcps)
	for _, e := range app.Entities {
		rows = append(rows, model.AppMemberEntity{AppID: app.ID, MemberKind: model.AppMemberKindEntity, MemberID: e.Kind})
	}
	return rows
}

// appRespFromRow 把一行 app（+成员行 +当前 rev）投影成对外形态。
//
// withSettings=false（列表）抹掉 settings_schema 正文；成员集合从 app_members 重建（读面真相是关联表）。
func (s *appSvc) appRespFromRow(ctx context.Context, row *model.AppEntity, withSettings bool) (*dtoapp.AppResp, error) {
	app, err := rowToApp(row)
	if err != nil {
		return nil, err
	}
	rev, err := s.store.AppRevision(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return appToResp(app, row, rev, withSettings), nil
}

// appToResp 强类型 App + 行（拿时间列）→ dtoapp.AppResp。
func appToResp(app *capability.App, row *model.AppEntity, rev int64, withSettings bool) *dtoapp.AppResp {
	resp := &dtoapp.AppResp{
		ID:          app.ID,
		Name:        app.Name,
		Description: app.Description,
		Avatar:      app.Avatar,
		Tags:        cloneStrings(app.Tags),
		Members: dtoapp.AppMembersResp{
			Experts:     cloneStrings(app.Members.Experts),
			Automations: cloneStrings(app.Members.Automations),
			Skills:      cloneStrings(app.Members.Skills),
			Mcps:        cloneStrings(app.Members.Mcps),
		},
		ToolDomains: cloneStrings(app.ToolDomains),
		APIVersion:  app.APIVersion,
		Enabled:     app.IsEnabled(),
		HasPage:     app.Page != nil,
		Revision:    rev,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	for _, e := range app.Entities {
		resp.Entities = append(resp.Entities, dtoapp.AppEntityResp{Kind: e.Kind, Table: e.Table, IDPrefix: e.IDPrefix})
	}
	for _, sl := range app.Slots {
		resp.Slots = append(resp.Slots, dtoapp.AppSlotResp{ImplID: sl.ImplID})
	}
	if app.Page != nil {
		resp.Page = &dtoapp.AppPageResp{NavLabel: app.Page.NavLabel, Route: app.Page.Route, Component: app.Page.Component}
	}
	if withSettings {
		resp.SettingsSchema = app.SettingsSchema
	}
	return resp
}

// ——— 内部：行 ↔ App / doc 编解码 ———

// rowToApp 把 apps 行反序列化成强类型 App。
func rowToApp(row *model.AppEntity) (*capability.App, error) {
	app := &capability.App{
		ID:             row.ID,
		Name:           row.Name,
		Description:    row.Description,
		Avatar:         row.Avatar,
		SettingsSchema: row.SettingsSchemaJSON,
		APIVersion:     row.APIVersion,
	}
	en := row.Enabled != 0
	app.Enabled = &en
	if err := unmarshalJSONField(row.TagsJSON, &app.Tags); err != nil {
		return nil, err
	}
	if err := unmarshalJSONField(row.EntitiesJSON, &app.Entities); err != nil {
		return nil, err
	}
	if err := unmarshalJSONField(row.ToolDomainsJSON, &app.ToolDomains); err != nil {
		return nil, err
	}
	if err := unmarshalJSONField(row.SlotsJSON, &app.Slots); err != nil {
		return nil, err
	}
	if strings.TrimSpace(row.PageJSON) != "" {
		var page capability.AppPage
		if err := json.Unmarshal([]byte(row.PageJSON), &page); err != nil {
			return nil, err
		}
		app.Page = &page
	}
	return app, nil
}

// rowToDoc 把 apps 行折回写面原文对象（覆盖写时做 patch 的 base）。
//
// 只回带已知键（未知键在 apps 表里没有存放位——这是类型化多列表与 body 表的区别，无法保留未知键）。
func rowToDoc(row *model.AppEntity) (map[string]json.RawMessage, error) {
	app, err := rowToApp(row)
	if err != nil {
		return nil, err
	}
	set := func(doc map[string]json.RawMessage, key string, v any) error {
		b, merr := json.Marshal(v)
		if merr != nil {
			return merr
		}
		doc[key] = b
		return nil
	}
	doc := map[string]json.RawMessage{}
	if err := set(doc, "id", app.ID); err != nil {
		return nil, err
	}
	if err := set(doc, "name", app.Name); err != nil {
		return nil, err
	}
	if app.Description != "" {
		_ = set(doc, "description", app.Description)
	}
	if app.Avatar != "" {
		_ = set(doc, "avatar", app.Avatar)
	}
	if len(app.Tags) > 0 {
		_ = set(doc, "tags", app.Tags)
	}
	if len(app.Entities) > 0 {
		_ = set(doc, "entities", app.Entities)
	}
	if len(app.ToolDomains) > 0 {
		_ = set(doc, "tool_domains", app.ToolDomains)
	}
	if len(app.Slots) > 0 {
		_ = set(doc, "slots", app.Slots)
	}
	if app.Page != nil {
		_ = set(doc, "page", app.Page)
	}
	if app.SettingsSchema != "" {
		_ = set(doc, "settings_schema", app.SettingsSchema)
	}
	if app.APIVersion != "" {
		_ = set(doc, "api_version", app.APIVersion)
	}
	en := app.IsEnabled()
	_ = set(doc, "enabled", en)
	return doc, nil
}

// ——— 小工具 ———

// marshalJSONArray 把切片序列化成 JSON 文本；nil 切片 → "[]"（与 DDL 默认一致）。
func marshalJSONArray(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	s := string(b)
	if s == "null" {
		return "[]", nil
	}
	return s, nil
}

// unmarshalJSONField 把 JSON 文本解进 dst；空串 / "[]" 都是良性空。
func unmarshalJSONField(s string, dst any) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), dst)
}

// cloneStrings 复制切片（nil 保持 nil，供 omitempty）。
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

// ——— 错误出口 ———

// existsErr 新建时 id 已占用（40901 app_exists：与当前状态冲突）。
func existsErr(id string) error {
	return failure.NewError(failure.ClassLimit, "app_exists",
		fmt.Sprintf("app %q 已存在：改它用 PUT /apps/%s，或换一个 id", id, id), nil)
}

// notFoundErr 目标 app 不存在（40401 app_not_found）。
func notFoundErr(id string) error {
	return failure.NewError(failure.ClassRequest, "app_not_found", fmt.Sprintf("未知 app %q", id), nil)
}

// invalidErr 定义被拒绝（40001 app_invalid）。
func invalidErr(id string, cause error) error {
	return failure.NewError(failure.ClassRequest, "app_invalid",
		fmt.Sprintf("app %q 的定义被拒绝：%v", id, cause), cause)
}
