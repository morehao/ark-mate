package sessionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	defn "github.com/morehao/ark-mate/internal/core/definition"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— 写面夹具（M6-5：一套断言跑在**两种载体**上） ———
//
// M6 的对外承诺是"写面换载体不换语义"，而这句话只有在**同一批输入**分别走文件与 DB 都被检查时
// 才算被验证过。所以这一组的夹具是载体无关的：`sqlite`（DB 行，生产形态）与 `memory`（文件，C9）。
//
// 不把两套断言各抄一份的理由是"分叉看不见"：各写一遍时，某个错误分支迟早只在一种载体上被覆盖，
// 而分叉的表现是"某个模式下能覆盖别人的写入"——静默的数据丢失。
type writeFace struct {
	name string
	svc  *Service
	// dir 文件载体的定义目录；DB 载体下是一个**没被装载、也没被读写**的空目录
	// （顺带证明 sqlite 模式的写面不需要能力目录）。
	dir string
	// st 仅 DB 载体非 nil（直接读回行 / 制造写失败用）。
	st *store.Store
}

// writeFaces 返回两种载体的夹具（subtest 名字就是载体名）。
func writeFaces(t *testing.T, specs ...runtime.Spec) []*writeFace {
	t.Helper()
	return []*writeFace{
		writeFaceDB(t, specs...),
		writeFaceFile(t, specs...),
	}
}

// writeFaceDB sqlite 载体：真库（testutil 建并迁移到最新 schema）→ EnableStore → 从 store 装载。
func writeFaceDB(t *testing.T, specs ...runtime.Spec) *writeFace {
	t.Helper()
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	svc := NewService(runtime.NewRegistry(specs...), eventbus.New())
	svc.EnableStore(st)
	// 与生产装配同源：装载走 store 分支，导入/导出目录给一个空目录。
	svc.Planes().LoadCapabilityDefinitionsFromStore(ctx, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	return &writeFace{name: "sqlite", svc: svc, dir: t.TempDir(), st: st}
}

// writeFaceFile memory 载体：空目录 → LoadCapabilityDefinitions（文件装载）。
func writeFaceFile(t *testing.T, specs ...runtime.Spec) *writeFace {
	t.Helper()
	dir := t.TempDir()
	svc := NewService(runtime.NewRegistry(specs...), eventbus.New())
	svc.Planes().LoadCapabilityDefinitions(dir)
	return &writeFace{name: "memory", svc: svc, dir: dir}
}

// sub 把能力种类映射成目录名（文件载体的目录名 = DB 载体的写面分区名）。
func subOf(kind string) string {
	if kind == model.DefinitionKindMcp {
		return defn.CapSubMcps
	}
	return defn.CapSubSkills
}

// expectPath 载体无关地给出写面响应里 `path` 的期望值（人类可读的定位串）。
func (w *writeFace) expectPath(t *testing.T, kind, id string) string {
	t.Helper()
	if w.st != nil {
		return "db:" + kind + "/" + id
	}
	return filepath.Join(w.dir, subOf(kind), id+".json")
}

// defPath 文件载体下的定义文件路径（DB 载体下无意义，仅给"文件不该存在"类断言用）。
func (w *writeFace) defPath(kind, id string) string {
	return filepath.Join(w.dir, subOf(kind), id+".json")
}

// readDef 载体无关地读回一条定义（DB：读行的 body；文件：读盘）。
func (w *writeFace) readDef(t *testing.T, kind, id string) map[string]any {
	t.Helper()
	var raw string
	if w.st != nil {
		ctx := context.Background()
		if kind == model.DefinitionKindMcp {
			row, found, err := w.st.GetMcpServerDefinition(ctx, id)
			if err != nil || !found {
				t.Fatalf("读 DB 里的 mcp %s: found=%v err=%v", id, found, err)
			}
			raw = row.Body
		} else {
			row, found, err := w.st.GetSkillDefinition(ctx, id)
			if err != nil || !found {
				t.Fatalf("读 DB 里的 skill %s: found=%v err=%v", id, found, err)
			}
			raw = row.Body
		}
	} else {
		b, err := os.ReadFile(w.defPath(kind, id))
		if err != nil {
			t.Fatalf("读 %s: %v", w.defPath(kind, id), err)
		}
		raw = string(b)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("解析 %s/%s 的定义: %v（原文 %s）", kind, id, err, raw)
	}
	return m
}

// hasDef 载体无关地判断定义是否还在（"被拒的定义不得留下任何痕迹"用它）。
func (w *writeFace) hasDef(t *testing.T, kind, id string) bool {
	t.Helper()
	if w.st != nil {
		ctx := context.Background()
		if kind == model.DefinitionKindMcp {
			_, found, err := w.st.GetMcpServerDefinition(ctx, id)
			if err != nil {
				t.Fatalf("查 mcp %s: %v", id, err)
			}
			return found
		}
		_, found, err := w.st.GetSkillDefinition(ctx, id)
		if err != nil {
			t.Fatalf("查 skill %s: %v", id, err)
		}
		return found
	}
	_, err := os.Stat(w.defPath(kind, id))
	return err == nil
}

// countDefs 载体里该种类的定义条数（键名不对时"目录里空不空"不能代替"载体里有没有"）。
func (w *writeFace) countDefs(t *testing.T, kind string) int {
	t.Helper()
	if w.st != nil {
		ctx := context.Background()
		if kind == model.DefinitionKindMcp {
			list, err := w.st.ListMcpServerDefinitions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return len(list)
		}
		list, err := w.st.ListSkillDefinitions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	entries, err := os.ReadDir(filepath.Join(w.dir, subOf(kind)))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n
}

// seed 预置一条**带未知键**的定义（模拟"用户自己写的字段"）：两种载体都必须原样保留它。
func (w *writeFace) seed(t *testing.T, kind, id, raw string) {
	t.Helper()
	ctx := context.Background()
	if w.st != nil {
		if kind == model.DefinitionKindMcp {
			if _, err := w.st.UpsertMcpServerDefinition(ctx, id, raw, 0, "test"); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := w.st.UpsertSkillDefinition(ctx, id, raw, 0, "test"); err != nil {
				t.Fatal(err)
			}
		}
		w.svc.Planes().LoadCapabilityDefinitionsFromStore(ctx, w.dir)
		return
	}
	path := w.defPath(kind, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := w.svc.Planes().LoadCapabilityDefinitions(w.dir); n == 0 {
		t.Fatalf("装载应至少读到 1 条 %s", kind)
	}
}

// failWrites 制造一次写失败（探测"写失败必须回滚内存"）。
//
//	文件载体：把 `skills` 建成**文件**（不是目录）→ MkdirAll 直接失败；
//	DB 载体：关掉库 → 事务开不起来。
func (w *writeFace) failWrites(t *testing.T) {
	t.Helper()
	if w.st != nil {
		if err := w.st.Close(); err != nil {
			t.Fatalf("关库: %v", err)
		}
		return
	}
	skillsDir := filepath.Join(w.dir, defn.CapSubSkills)
	_ = os.RemoveAll(skillsDir)
	if err := os.WriteFile(skillsDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
}
