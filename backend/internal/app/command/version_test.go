package command

import (
	"encoding/json"
	"testing"

	"github.com/morehao/ark-mate/internal/version"
)

// TestVersionOutput 版本出口的字节级契约（M4-1）。
//
// 为什么钉到字节：`build-release-json.mjs` 用 `mate-core --version --json` 填 release.json 的
// `coreVersion`/`apiContract`，而 `make verify/release-json` 会拿同一份输出复核。任何"顺手加个
// 字段/换个换行"都会让两侧判据悄悄错位——版本出口是发布契约的输入，不是打印好看就行。
func TestVersionOutput(t *testing.T) {
	if got, want := versionOutput(false), version.Version+"\n"; got != want {
		t.Errorf("非 JSON 形态 = %q，期望 %q（只有一行版本号）", got, want)
	}

	got := versionOutput(true)
	if got[len(got)-1] != '\n' {
		t.Fatalf("JSON 形态必须以换行收尾（行式消费），得到 %q", got)
	}
	// 键序固定为 version → apiContract：json.Marshal 按结构体字段序输出，两侧逐字可比。
	wantJSON := `{"version":"` + version.Version + `","apiContract":"` + version.APIContract + `"}` + "\n"
	if got != wantJSON {
		t.Errorf("JSON 形态 = %q，期望 %q（键序与写法都是契约）", got, wantJSON)
	}

	var parsed struct {
		Version     string `json:"version"`
		APIContract string `json:"apiContract"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("JSON 形态解析失败（%v）：%q", err, got)
	}
	if parsed.Version != version.Version || parsed.APIContract != version.APIContract {
		t.Errorf("解析回来的值 = %+v，期望 %+v", parsed, versionInfo{version.Version, version.APIContract})
	}
	if parsed.APIContract == "" {
		t.Error("apiContract 不该为空——它是前端与核心的契约代数（设计 §6.7）")
	}
}
