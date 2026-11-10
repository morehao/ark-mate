package assembly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/service/svcmaterial"
	"github.com/morehao/ark-mate/internal/testutil"
)

// 产物抽取口径：**只认 present**（与 dsh 语义对齐）。write/edit 写出的文件是「产出」而非
// 「交付物」，不再登记为产物、也不再自动收录进资料库。本文件用 present 驱动全部登记链路。

// presentInfo 造一条 present 工具事件（files 用给定路径）。
func presentInfo(id string, paths ...string) protocol.ToolCallInfo {
	files := make([]any, 0, len(paths))
	for _, p := range paths {
		files = append(files, map[string]any{"path": p})
	}
	return protocol.ToolCallInfo{
		ID: id, Title: "present", Status: protocol.ToolStatusCompleted,
		Input: map[string]any{"files": files},
	}
}

// newExtractRig 造一个真库 + 真附件字节存储 + 真 artifactSvc 的抽取器装置（进程内集成）。
func newExtractRig(t *testing.T) (*artifactExtractor, string, func(ctx context.Context) (int, error)) {
	t.Helper()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	if err := attachmentstore.Migrate(st.DB()); err != nil {
		t.Fatalf("filestore.Migrate: %v", err)
	}
	attachStore, err := attachmentstore.New(st.DB(), attachmentstore.Options{
		BaseDir:        filepath.Join(t.TempDir(), "attachments"),
		MaxUploadBytes: 20 << 20,
	})
	if err != nil {
		t.Fatalf("attachmentstore.New: %v", err)
	}
	facts := Facts{Store: st, AttachStore: attachStore}
	artSvc := facts.buildArtifactSvc()
	// 自动收录需要资料库用例（生产装配根走 facts.buildMaterialSvc；这里只需要 ImportArtifact 用到的
	// NodeStore + ArtifactReader 两个端口，上传/字节读取可以给 nil）。
	matSvc := svcmaterial.New(st, nil, nil, nil, materialArtifactReader{art: artSvc})
	x := newArtifactExtractor(st, attachStore, artSvc, matSvc)
	if x == nil {
		t.Fatalf("抽取器不应为 nil（store/attach/artifact 都有）")
	}
	// 返回一个「等异步落盘完成、取产物数」的轮询器。
	count := func(ctx context.Context) (int, error) {
		resp, err := artSvc.ListBySession(ctx, "ses1")
		if err != nil {
			return 0, err
		}
		if resp == nil {
			return 0, nil
		}
		return len(resp.Artifacts), nil
	}
	return x.(*artifactExtractor), st.DB().Name(), count
}

// seedSessionWithCwd 插一条 cwd 指向 tmp 的会话。
func seedSessionWithCwd(t *testing.T, st interface{}, sessionID, cwd string) {
	t.Helper()
	// st 是 *store.Store
	type upsertable interface {
		UpsertSession(ctx context.Context, sess model.SessionEntity) error
	}
	u, ok := st.(upsertable)
	if !ok {
		t.Fatalf("st 不满足 UpsertSession")
	}
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	if err := u.UpsertSession(context.Background(), model.SessionEntity{
		ID: sessionID, Cwd: cwd, ModelID: "m1", RuntimeID: "dsh",
		RuntimeSessionID: "rt_native", Title: "t", Status: "idle",
		Origin: "manual", CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
}

func waitArtifactCount(t *testing.T, count func(ctx context.Context) (int, error), want int) {
	t.Helper()
	ctx := context.Background()
	deadline := time.After(3 * time.Second)
	for {
		n, err := count(ctx)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("等产物落库超时: got %d want %d", n, want)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// TestArtifactExtractor_PresentEndToEnd：present 声明 → 工作区文件 → filestore 快照 → artifacts 行。
func TestArtifactExtractor_PresentEndToEnd(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	// 造工作区文件（模拟 present 声明的成品内容）。
	if err := os.WriteFile(filepath.Join(cwd, "hello.txt"), []byte("hello artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "hello.txt"))
	waitArtifactCount(t, count, 1)

	resp, err := x.artifact.ListBySession(context.Background(), "ses1")
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Artifacts[0]
	if a.Origin != "tool_present" || a.Kind != "file" || a.Title != "hello.txt" {
		t.Errorf("产物元数据不符: origin=%q kind=%q title=%q", a.Origin, a.Kind, a.Title)
	}
	if a.FileID == "" {
		t.Errorf("file 类产物应有 file_id（快照已进 filestore）")
	}
	if a.SizeBytes != int64(len("hello artifact")) {
		t.Errorf("size=%d 期望 %d", a.SizeBytes, len("hello artifact"))
	}
	if a.ContentSha256 == "" {
		t.Errorf("应有快照哈希")
	}
	assertAutoImported(t, x, a.ID)
}

// TestArtifactExtractor_ScratchExcluded：present 指向过程产物目录（`.scratch/`）的文件**不登记**，
// 也不进资料库——即便模型误把 scratch 内脚本/中间文件 present。过程文件 ≠ 交付物。
func TestArtifactExtractor_ScratchExcluded(t *testing.T) {
	x, _, _ := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	scratchDir := filepath.Join(cwd, ".scratch", "ses1")
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratchDir, "build.py"), []byte("print(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1",
		presentInfo("tc1", filepath.Join(".scratch", "ses1", "build.py")))

	time.Sleep(50 * time.Millisecond) // 给异步落盘窗口，确认**零**产物
	resp, err := x.artifact.ListBySession(context.Background(), "ses1")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Artifacts) != 0 {
		t.Fatalf("scratch 内文件不应登记为产物，得到 %d 条", len(resp.Artifacts))
	}
}

// TestArtifactExtractor_WriteEditNotRegistered 钉住核心修复：write/edit 写出的文件**不再**登记为
// 产物、**不再**自动进资料库（过程脚本 gen.py 等不会被当成品）。
func TestArtifactExtractor_WriteEditNotRegistered(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	// 模拟模型先写中间脚本（write），再 edit。
	script := filepath.Join(cwd, "gen.py")
	if err := os.WriteFile(script, []byte("print('build pptx')"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", protocol.ToolCallInfo{
		ID: "tc1", Title: "write", Status: protocol.ToolStatusCompleted,
		Input: map[string]any{"file_path": "gen.py", "content": "print('build pptx')"},
	})
	x.ExtractToolWrite(context.Background(), "ses1", "run1", protocol.ToolCallInfo{
		ID: "tc2", Title: "edit", Status: protocol.ToolStatusCompleted,
		Input: map[string]any{"file_path": "gen.py", "old_string": "a", "new_string": "b"},
	})
	time.Sleep(120 * time.Millisecond)
	if n, _ := count(context.Background()); n != 0 {
		t.Fatalf("write/edit 不应登记为产物，实际 %d 行", n)
	}
	// 资料库也不应有任何自动收录节点。
	resp, err := x.material.ListNodesByCategory(context.Background(), "", "artifact", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Nodes) != 0 {
		t.Fatalf("write/edit 不应自动进资料库，实际 %d 个节点", len(resp.Nodes))
	}
}

// TestArtifactExtractor_PresentMultiFile：present 一次声明多文件，逐个登记。
func TestArtifactExtractor_PresentMultiFile(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	for _, n := range []string{"report.pptx", "data.xlsx"} {
		if err := os.WriteFile(filepath.Join(cwd, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "report.pptx", "data.xlsx"))
	waitArtifactCount(t, count, 2)
}

// TestArtifactExtractor_PresentAbsolutePathInsideCwd（真机回归）：present 给的是绝对路径。
func TestArtifactExtractor_PresentAbsolutePathInsideCwd(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	name := "杭州天气-2026-11-04.md"
	body := "# 杭州天气简报\n"
	if err := os.WriteFile(filepath.Join(cwd, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1",
		presentInfo("tc1", filepath.Join(cwd, name)))
	waitArtifactCount(t, count, 1)

	resp, err := x.artifact.ListBySession(context.Background(), "ses1")
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Artifacts[0]
	if a.Title != name {
		t.Errorf("title=%q 期望 %q", a.Title, name)
	}
	// path 落的是**相对工作区**的展示路径（dirty 判定靠它回定位原文件）。
	if a.Path != name {
		t.Errorf("path=%q 期望相对路径 %q", a.Path, name)
	}
	if a.SizeBytes != int64(len(body)) {
		t.Errorf("size=%d 期望 %d", a.SizeBytes, len(body))
	}
	got, err := x.artifact.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dirty {
		t.Errorf("未改动不应 dirty")
	}
	assertAutoImported(t, x, a.ID)
}

// TestArtifactExtractor_PresentAbsoluteOutsideCwdSkipped：present 绝对路径落在工作区外 → 跳过。
func TestArtifactExtractor_PresentAbsoluteOutsideCwdSkipped(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	outside := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", outside))
	time.Sleep(80 * time.Millisecond)
	if n, _ := count(context.Background()); n != 0 {
		t.Fatalf("工作区外的绝对路径不应建产物行，实际 %d", n)
	}
}

// TestArtifactExtractor_NonPresentSkipped：非 present 工具不产生产物。
func TestArtifactExtractor_NonPresentSkipped(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	_ = os.WriteFile(filepath.Join(cwd, "x.txt"), []byte("x"), 0o644)
	x.ExtractToolWrite(context.Background(), "ses1", "run1", protocol.ToolCallInfo{
		ID: "tc1", Title: "read", Status: protocol.ToolStatusCompleted,
		Input: map[string]any{"file_path": "x.txt"},
	})
	time.Sleep(80 * time.Millisecond)
	if n, _ := count(context.Background()); n != 0 {
		t.Fatalf("read 工具不应产生产物，实际 %d", n)
	}
}

// TestArtifactExtractor_PresentEscapeSkipped：路径越界（../）不产生产物。
func TestArtifactExtractor_PresentEscapeSkipped(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "../../etc/passwd"))
	time.Sleep(80 * time.Millisecond)
	if n, _ := count(context.Background()); n != 0 {
		t.Fatalf("越界路径不应产生产物，实际 %d", n)
	}
}

// TestArtifactExtractor_PresentTooLargeSkipped：超阈值跳过不建行（R2 已拍板）。
func TestArtifactExtractor_PresentTooLargeSkipped(t *testing.T) {
	x, _, count := newExtractRig(t)
	x.maxBytes = 8 // 把阈值调到很小
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	_ = os.WriteFile(filepath.Join(cwd, "big.txt"), []byte("this is longer than 8 bytes"), 0o644)
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "big.txt"))
	time.Sleep(80 * time.Millisecond)
	if n, _ := count(context.Background()); n != 0 {
		t.Fatalf("超阈值文件不应建产物行，实际 %d", n)
	}
}

// TestArtifactExtractor_PresentMissingFileSkipped：present 声明的文件不存在（已被删）不建行。
func TestArtifactExtractor_PresentMissingFileSkipped(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "ghost.txt"))
	time.Sleep(80 * time.Millisecond)
	if n, _ := count(context.Background()); n != 0 {
		t.Fatalf("文件不存在不应建产物行，实际 %d", n)
	}
}

// TestArtifactDirty_LazyOnGet（P2-d）：抽取产物 → 改工作区文件 → Get 返回 dirty=true 并回写；
// 改回原内容 → dirty=false。列表页不算 dirty（R6），Get 才惰性算。
func TestArtifactDirty_LazyOnGet(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	p := filepath.Join(cwd, "a.txt")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "a.txt"))
	waitArtifactCount(t, count, 1)
	// 取产物 id
	list, _ := x.artifact.ListBySession(context.Background(), "ses1")
	id := list.Artifacts[0].ID

	// 刚抽取，未改动 → Get dirty=false
	got, err := x.artifact.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dirty {
		t.Fatalf("未改动不应 dirty")
	}
	if got.Path != "a.txt" {
		t.Fatalf("产物应记录工作区相对路径 path=%q", got.Path)
	}

	// 改工作区文件 → Get dirty=true 且回写
	if err := os.WriteFile(p, []byte("v2-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ = x.artifact.Get(context.Background(), id)
	if !got.Dirty {
		t.Fatalf("改了工作区文件后应 dirty=true")
	}
	// 回写生效：查库里的 dirty 应为 1
	ent, _, _ := x.st.ArtifactByID(context.Background(), id)
	if ent.Dirty != 1 {
		t.Fatalf("dirty 应已回写为 1，实际 %d", ent.Dirty)
	}

	// 改回原内容 → Get dirty=false
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ = x.artifact.Get(context.Background(), id)
	if got.Dirty {
		t.Fatalf("改回原内容后应 dirty=false")
	}
}

// TestArtifactDirty_FileDeleted（R5）：工作区原文件被删 → dirty=true。
func TestArtifactDirty_FileDeleted(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	p := filepath.Join(cwd, "gone.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "gone.txt"))
	waitArtifactCount(t, count, 1)
	list, _ := x.artifact.ListBySession(context.Background(), "ses1")
	id := list.Artifacts[0].ID
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	got, _ := x.artifact.Get(context.Background(), id)
	if !got.Dirty {
		t.Fatalf("工作区原文件被删后应 dirty=true（R5）")
	}
}

// TestArtifactDirty_ListPageDoesNotCompute（R6）：列表页不算 dirty（返回存量 0，即使工作区已改）。
func TestArtifactDirty_ListPageDoesNotCompute(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	p := filepath.Join(cwd, "b.txt")
	if err := os.WriteFile(p, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "b.txt"))
	waitArtifactCount(t, count, 1)
	// 改工作区文件（若列表页算 dirty 就会变 true）
	if err := os.WriteFile(p, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, _ := x.artifact.ListBySession(context.Background(), "ses1")
	if list.Artifacts[0].Dirty {
		t.Fatalf("列表页不应计算 dirty（R6），应返回存量 false")
	}
}

// TestArtifactSnapshotBeforeAsync（C5/E-4）：ExtractToolWrite 同步返回后立刻覆盖工作区文件，
// 异步落盘仍应记「返回那一刻」的字节——证明捕获发生在同步段（否则会读到覆盖后的内容）。
func TestArtifactSnapshotBeforeAsync(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	p := filepath.Join(cwd, "race.txt")
	orig := []byte("ORIGINAL-CONTENT")
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	// 同步调用（内部：同步读字节 → 异步落盘）。
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "race.txt"))
	// 返回后立刻覆盖工作区文件（模拟 dsh 下一步写同名文件）。
	if err := os.WriteFile(p, []byte("LATER-CONTENT-LONGER-THAN-BEFORE"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitArtifactCount(t, count, 1)
	resp, _ := x.artifact.ListBySession(context.Background(), "ses1")
	got := resp.Artifacts[0]
	sum := sha256.Sum256(orig)
	wantHash := hex.EncodeToString(sum[:])
	if got.ContentSha256 != wantHash {
		t.Fatalf("快照应是返回那一刻的字节（C5）: got=%s want=%s（读到覆盖后内容说明捕获被延后了）",
			got.ContentSha256, wantHash)
	}
	if got.SizeBytes != int64(len(orig)) {
		t.Fatalf("size 应是原始内容长度: got=%d want=%d", got.SizeBytes, len(orig))
	}
}

// TestArtifactExtractor_AutoImportReusesNodePerPath：同一文件在不同轮次 present 两次（如先粗稿、
// 后定稿），产物两行，资料库只留**一个**节点且载体指向最新快照（一文件一节点，避免同名节点堆积）。
func TestArtifactExtractor_AutoImportReusesNodePerPath(t *testing.T) {
	x, _, count := newExtractRig(t)
	cwd := t.TempDir()
	seedSessionWithCwd(t, x.st, "ses1", cwd)
	p := filepath.Join(cwd, "note.md")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc1", "note.md"))
	waitArtifactCount(t, count, 1)

	// 同一文件再 present 一次（新内容）：产物多一行，节点不增加。
	if err := os.WriteFile(p, []byte("v2-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.ExtractToolWrite(context.Background(), "ses1", "run1", presentInfo("tc2", "note.md"))
	waitArtifactCount(t, count, 2)

	list, err := x.artifact.ListBySession(context.Background(), "ses1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Artifacts) != 2 {
		t.Fatalf("产物应两行（每次 present 各一行），实际 %d", len(list.Artifacts))
	}
	latest := list.Artifacts[0].ID // created_at 降序
	waitLibraryProvenance(t, x, latest)

	resp, err := x.material.ListNodesByCategory(context.Background(), "", "artifact", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("同一路径应只有一个资料库节点，实际 %d", len(resp.Nodes))
	}
	if resp.Nodes[0].ProvenanceID != latest {
		t.Fatalf("节点应指向最新快照 %q，实际 %q", latest, resp.Nodes[0].ProvenanceID)
	}
	if resp.Nodes[0].SizeBytes != int64(len("v2-longer")) {
		t.Fatalf("节点大小应换成最新快照，实际 %d", resp.Nodes[0].SizeBytes)
	}
}

// waitLibraryProvenance 轮询到资料库的那一个产物节点指向 want（异步收录）。
func waitLibraryProvenance(t *testing.T, x *artifactExtractor, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		resp, err := x.material.ListNodesByCategory(context.Background(), "", "artifact", 50, 0)
		if err == nil && len(resp.Nodes) == 1 && resp.Nodes[0].ProvenanceID == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("等资料库节点指向 %q 超时（当前 %+v）", want, resp)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// assertAutoImported（2026-11-04 拍板）断言产物登记后**自动**出现在资料库的产物分栏：
// 一条 category='artifact' 的节点，provenance_id 指回该产物。
//
// **必须轮询等待，不能即时断言**（2026-11-05 修 flaky）：`ExtractToolWrite` 的落盘是异步链
// ①上传 filestore → ②登记 artifact 行 → ③`material.ImportArtifact` 自动收录进资料库。
// `waitArtifactCount` 只等到 ②（产物行出现），③ 与 ② 之间还有一段窗口——在 `-race` / 高负载
// 下这段窗口会被拉长，即时读库就会读到「0 个节点」而误判失败（实测：`-race` 必现、全量并发偶现）。
// 产品行为本身正确（③ 最终一定执行），这纯是测试等待不足，故改成与 `waitArtifactCount` 同口径轮询。
func assertAutoImported(t *testing.T, x *artifactExtractor, artifactID string) {
	t.Helper()
	if x.material == nil {
		t.Fatal("抽取器没接资料库用例，无法自动收录")
	}
	ctx := context.Background()
	deadline := time.After(3 * time.Second)
	var lastCount int
	for {
		resp, err := x.material.ListNodesByCategory(ctx, "", "artifact", 50, 0)
		if err != nil {
			t.Fatalf("列出资料库产物节点: %v", err)
		}
		lastCount = len(resp.Nodes)
		if lastCount == 1 {
			n := resp.Nodes[0]
			if n.Category != "artifact" || n.ProvenanceID != artifactID {
				t.Fatalf("收录节点不符：category=%q provenance=%q want %q", n.Category, n.ProvenanceID, artifactID)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("等自动收录超时: 应自动收录 1 个产物节点，实际 %d", lastCount)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}
