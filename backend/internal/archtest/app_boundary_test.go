package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestProcessShellDoesNotImportAdapters 进程壳（`bootstrap/app`）**只认 `BuildResult` 的端口字段**。
//
// 阶段 0.6 的第二条判据。层表已经禁止 `app → store/dao/registry/runtimeconn/...`，但那张表
// 按**层号**判方向：`app`（L9）向 L5/L6 import 是"向内"，层号本身拦不住"进程壳直接摸适配器"
// 这类边——而那正是本次改造前的形态（`a.res.Planes.Mcp.AutoProbe`）。
//
// 判据按**适配器前缀**而不是层号列一遍：进程壳需要的只有 `assembly.BuildResult` 与
// 纯值（config / dto / core）。它拿到 `*store.Store`、`*registry.Planes`、`*runtimeconn.Pool`
// 中的任何一个，都说明端口收敛漏了一处。
//
// fail-closed：一个 import 都没解析到 → 失败（扫描面解析错时不能静默通过）。
func TestProcessShellDoesNotImportAdapters(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "bootstrap", "app")
	imports := importedByProductionPackage(t, dir)
	if len(imports) == 0 {
		t.Fatalf("`internal/bootstrap/app` 未解析到任何 import：扫描面失效，判据形同虚设")
	}
	forbidden := []string{
		"internal/persistence",
		"internal/infra",
		"internal/drivers",
		"internal/sessionruntime",
		"internal/app",
		"internal/domain",
		"internal/delivery",
	}
	for _, imp := range imports {
		if !strings.HasPrefix(imp, modulePrefix) {
			continue
		}
		rel := strings.TrimPrefix(imp, modulePrefix)
		for _, bad := range forbidden {
			if rel == bad || strings.HasPrefix(rel, bad+"/") {
				t.Errorf("`bootstrap/app` 直接 import 了 `%s`：进程壳只认 `assembly.BuildResult` 的"+
					"端口字段（Handler / Maintenance / Probe / Close）。\n"+
					"这类边不会让层表变红（向内），但会让「进程壳不认识适配器」这条不变量重新没有判据。", rel)
			}
		}
	}
}
