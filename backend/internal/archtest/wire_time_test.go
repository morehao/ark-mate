package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— 时间口径守卫：DTO 里的"时刻"字段必须是 Unix 毫秒 ———
//
// 约定（docs/agents/time.md）：REST/WS **返回**的瞬时一律 Unix 毫秒 `int64`，前端按需格式化。
// 挡的是"新加一个 `_at` 字段时顺手写了 string"——它不会让任何测试变红，只会让前端多一处
// `new Date(iso)` 的解析，最后两种格式在同一个响应里并存。
//
// 判据：
//   - 扫描 `internal/dto/*/*.go` 的字段（剥行注释后按 struct 字段正则取）；
//   - Go 字段名以 `At`/`Since` 结尾，或就叫 `TS`/`Ts` 的，类型必须是 `int64` 或 `*int64`；
//   - 唯一登记的非时间同名键是 `EffectiveAt`（枚举 now|next_session|next_launch）；
//   - fail-closed：扫不到 DTO 文件、或一条"时刻"都没命中，都判红（正则/目录漂移会静默空转）。
//
// 不扫的东西（刻意的边界）：
//   - `internal/core/automation` 的 `Schedule.At` / `RunRecord.ScheduledFor`：那是**定义文件**
//     的 JSON 形状（RFC3339），接口侧另建 `RunRecordResp` / 保持 `schedule.at` 字符串；
//   - `internal/domain/session/transcript.go` 的 `ts`/`created_at`：文件载体；
//   - `model.LayoutTS` / `persistence` 的时间列：落库契约。
func TestDTOInstantFieldsAreUnixMillis(t *testing.T) {
	root := backendRoot(t)
	dtoRoot := filepath.Join(root, "internal", "dto")
	files, err := filepath.Glob(filepath.Join(dtoRoot, "*", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("扫不到 internal/dto/*/*.go: %v（fail-closed）", err)
	}

	// 同名但不是"时刻"的字段：键 = `<相对 dto 的包>/<文件名>:<字段名>`。双向检查（见下）。
	nonInstant := map[string]string{
		"dtomodel/model_list.go:EffectiveAt": "枚举（now | next_session | next_launch），不是时刻",
	}
	used := map[string]bool{}

	fieldRe := regexp.MustCompile("(?m)^\\s*(\\w+)\\s+([\\w\\[\\]\\*]+)\\s+`[^`]*json:\"([^\",]+)")

	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		rel, rerr := filepath.Rel(dtoRoot, path)
		if rerr != nil {
			t.Fatalf("相对路径 %s: %v", path, rerr)
		}
		rel = filepath.ToSlash(rel)
		src := stripLineComments(readFileOrFatal(t, path))
		for _, m := range fieldRe.FindAllStringSubmatch(src, -1) {
			name, typ, tag := m[1], m[2], m[3]
			if !isInstantFieldName(name, tag) {
				continue
			}
			key := rel + ":" + name
			if reason, ok := nonInstant[key]; ok {
				used[key] = true
				if typ != "string" {
					t.Fatalf("%s 的 %s 已不是 string（%s）：登记的理由（%s）不再成立，把它从 nonInstant 删掉",
						rel, name, typ, reason)
				}
				continue
			}
			checked++
			if typ != "int64" && typ != "*int64" {
				t.Fatalf("%s 的 %s 是 %s：对外的时刻字段必须是 int64 毫秒（见 docs/agents/time.md）；"+
					"若是枚举/时长，请在 nonInstant 里逐条登记理由", rel, name, typ)
			}
		}
	}

	if checked == 0 {
		t.Fatalf("一条时刻字段都没命中（fail-closed）：字段正则或 DTO 布局已漂移，守卫会静默放行")
	}
	for key, reason := range nonInstant {
		if !used[key] {
			t.Fatalf("nonInstant 登记 %q（%s）在 DTO 里不再命中：白名单该删", key, reason)
		}
	}
}

func isInstantFieldName(name, tag string) bool {
	if strings.HasSuffix(name, "At") || strings.HasSuffix(name, "Since") {
		return true
	}
	if name == "TS" || name == "Ts" {
		return true
	}
	return tag == "ts" || tag == "since"
}
