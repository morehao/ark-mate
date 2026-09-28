package router_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrsystem"
	"github.com/morehao/ark-forge/internal/router"
)

// recomputeDigest 是 `webDigest` 的**独立第二实现**（照 meta.go/web.go 注释里的算法重写一遍）：
// 逐文件 sha256 → 按相对路径排序 → `rel\x00hex\n` 拼接 → sha256 → `sha256:<hex>`。
//
// 特意不复用生产代码里的 `compositeDigest`：用一个函数验证它自己等于没验证。
// 这条对应 M2-3 的验收——"`webDigest` 与实际产物逐字节可复算"。
func recomputeDigest(t *testing.T, root string) string {
	t.Helper()
	type ent struct{ rel, sum string }
	var ents []ent
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := sha256.Sum256(b)
		ents = append(ents, ent{filepath.ToSlash(rel), hex.EncodeToString(s[:])})
		return nil
	})
	if err != nil {
		t.Fatalf("走查产物失败：%v", err)
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].rel < ents[j].rel })
	h := sha256.New()
	for _, e := range ents {
		fmt.Fprintf(h, "%s\x00%s\n", e.rel, e.sum)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// TestMetaWebDigestIsRecomputable `/meta` 的资产三件套与实际产物一致，且摘要可被独立复算。
func TestMetaWebDigestIsRecomputable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)

	assets, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	if want := recomputeDigest(t, dir); assets.Digest != want {
		t.Fatalf("资产层的合成摘要与独立复算不符：\n  实际 %s\n  复算 %s", assets.Digest, want)
	}

	// 端到端：经真实装配取 /meta，三件套必须与解析结果一致。
	eng := router.New(router.Ports{}, nil, router.Options{Web: assets})
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/forge/meta", nil))
	if w.Code != 200 {
		t.Fatalf("GET /meta HTTP %d：%s", w.Code, w.Body.String())
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			WebSource string  `json:"webSource"`
			WebPath   *string `json:"webPath"`
			WebDigest *string `json:"webDigest"`
			Payloads  []any   `json:"payloads"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("信封解析失败：%v", err)
	}
	if env.Data.WebSource != string(router.AssetSourceDir) {
		t.Errorf("webSource = %q，期望 dir", env.Data.WebSource)
	}
	if env.Data.WebPath == nil || *env.Data.WebPath != dir {
		t.Errorf("webPath = %v，期望 %q", env.Data.WebPath, dir)
	}
	if env.Data.WebDigest == nil || *env.Data.WebDigest != recomputeDigest(t, dir) {
		t.Errorf("webDigest = %v，期望可复算的 %s", env.Data.WebDigest, recomputeDigest(t, dir))
	}
	if env.Data.Payloads == nil {
		t.Error("payloads 必须是空数组而不是 null")
	}
}

// TestMetaHomeAndContract 诊断面的另外两项（装配层提供的值原样透出）。
func TestMetaHomeAndContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)
	assets, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatal(err)
	}
	eng := router.New(router.Ports{}, nil, router.Options{
		Web: assets,
		Meta: func() ctrsystem.MetaSource {
			return ctrsystem.MetaSource{CoreVersion: "9.9.9", APIContract: "v1", Home: "/tmp/forge-home"}
		},
	})
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/forge/meta", nil))
	body := w.Body.String()
	for _, want := range []string{`"coreVersion":"9.9.9"`, `"apiContract":"v1"`, `"home":"/tmp/forge-home"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/meta 应含 %s：%s", want, body)
		}
	}
}
