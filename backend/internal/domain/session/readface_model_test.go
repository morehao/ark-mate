package session

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// modelListStore 只实现 `ListModelDefinitions` 的存储替身（其余方法由嵌入的 nil 接口兜住）：
// `fillModelDisplay` 只该碰这一条读法，碰别的就说明它越界了。
type modelListStore struct {
	Store
	rows model.ModelEntityList
	err  error
}

func (s modelListStore) ListModelDefinitions(context.Context) (model.ModelEntityList, error) {
	return s.rows, s.err
}

// panicModelListStore 一旦被查询就 panic：用来证明"没有模型引用时不该读清单"。
type panicModelListStore struct{ Store }

func (panicModelListStore) ListModelDefinitions(context.Context) (model.ModelEntityList, error) {
	panic("没有模型引用的列表不该查模型清单")
}

// TestFillModelDisplayResolvesNames 会话行带的是平台模型行 id，浮层要显示的是**名字**：
//   - `models.name` 有值 → 用它；
//   - `models.name` 为空 → 回落上游模型名（与设置面 modelLabel 同口径）；
//   - 行已删除 / 未引用 → 留空（前端回落「已设置」，不摊 `mdl_…`）。
func TestFillModelDisplayResolvesNames(t *testing.T) {
	pl := NewPlane()
	pl.SetStore(modelListStore{rows: model.ModelEntityList{
		{ID: "mdl_a", Model: "gpt-4o", Name: "GPT-4o"},
		{ID: "mdl_b", Model: "deepseek-chat", Name: "  "},
	}})

	items := []SessionItem{
		{ID: "s1", Model: "mdl_a"},
		{ID: "s2", Model: "mdl_b"},
		{ID: "s3", Model: "mdl_deleted"},
		{ID: "s4"},
	}
	pl.fillModelDisplay(items)

	want := []string{"GPT-4o", "deepseek-chat", "", ""}
	for i, w := range want {
		if items[i].ModelName != w {
			t.Fatalf("第 %d 行 ModelName=%q，want %q", i, items[i].ModelName, w)
		}
	}
}

// TestFillModelDisplaySkipsWhenNoModelRef 没有模型引用时不查清单（列表刷新是高频路径），
// 内存模式（st == nil）也不得 panic。
func TestFillModelDisplaySkipsWhenNoModelRef(t *testing.T) {
	pl := NewPlane()
	pl.SetStore(panicModelListStore{})
	pl.fillModelDisplay([]SessionItem{{ID: "s1"}, {ID: "s2"}})

	mem := NewPlane()
	mem.fillModelDisplay([]SessionItem{{ID: "s1", Model: "mdl_a"}})
}
