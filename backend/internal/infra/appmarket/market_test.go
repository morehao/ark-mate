package appmarket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// validMarketJSON 是一份最小的合法清单。
const validMarketJSON = `{
  "version": 1,
  "apps": [
    {
      "id": "weather-query",
      "name": "天气查询",
      "icon": "weather",
      "summary": { "zh": "实时天气", "en": "Live weather" },
      "version": "1.0.0",
      "source": { "type": "dir", "dir": "apps/weather-query" },
      "tags": ["工具", "天气"]
    }
  ]
}`

func TestParseValid(t *testing.T) {
	f, err := Parse([]byte(validMarketJSON), nil)
	if err != nil {
		t.Fatalf("合法清单应解析成功，得到 %v", err)
	}
	if len(f.Apps) != 1 || f.Apps[0].ID != "weather-query" {
		t.Fatalf("解析结果不符：%+v", f)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	bad := `{"version":1,"apps":[{"id":"x","name":"X","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/x"},"bogus":1}]}`
	if _, err := Parse([]byte(bad), nil); err == nil {
		t.Fatal("未知字段应被拒绝（DisallowUnknownFields）")
	}
}

func TestParseRejectsBadID(t *testing.T) {
	bad := `{"version":1,"apps":[{"id":"Bad_ID","name":"X","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/x"}}]}`
	if _, err := Parse([]byte(bad), nil); err == nil {
		t.Fatal("非法 id（大写/下划线）应被拒绝")
	}
}

func TestParseRejectsMissingZH(t *testing.T) {
	bad := `{"version":1,"apps":[{"id":"x","name":"X","summary":{"en":"only en"},"version":"1.0.0","source":{"type":"dir","dir":"apps/x"}}]}`
	if _, err := Parse([]byte(bad), nil); err == nil {
		t.Fatal("缺 summary.zh 应被拒绝")
	}
}

func TestParseRejectsBadSourceType(t *testing.T) {
	bad := `{"version":1,"apps":[{"id":"x","name":"X","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"ftp","dir":"apps/x"}}]}`
	if _, err := Parse([]byte(bad), nil); err == nil {
		t.Fatal("非法 source.type 应被拒绝")
	}
}

func TestParseRejectsAbsoluteDir(t *testing.T) {
	bad := `{"version":1,"apps":[{"id":"x","name":"X","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"/etc"}}]}`
	if _, err := Parse([]byte(bad), nil); err == nil {
		t.Fatal("绝对 source.dir 应被拒绝")
	}
}

func TestParseRejectsDirOutOfSourceRoot(t *testing.T) {
	// 给了来源根，dir 解析后越界（`../etc` 跳出根）应被拒绝。
	bad := `{"version":1,"apps":[{"id":"x","name":"X","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"../etc"}}]}`
	if _, err := Parse([]byte(bad), []string{"/opt/ark-mate/apps"}); err == nil {
		t.Fatal("越出来源根的 source.dir 应被拒绝（防指向 /etc）")
	}
}

func TestParseAcceptsDirWithinSourceRoot(t *testing.T) {
	good := `{"version":1,"apps":[{"id":"x","name":"X","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/x"}}]}`
	if _, err := Parse([]byte(good), []string{"/opt/ark-mate"}); err != nil {
		t.Fatalf("来源根内的 source.dir 应通过，得到 %v", err)
	}
}

func TestParseRejectsDuplicateID(t *testing.T) {
	dup := `{"version":1,"apps":[
      {"id":"x","name":"X","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/x"}},
      {"id":"x","name":"Y","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/y"}}
    ]}`
	if _, err := Parse([]byte(dup), nil); err == nil {
		t.Fatal("重复 id 应被拒绝")
	}
}

// TestBuiltinAlwaysAvailable 断言内置 embed 恒可解析（离线兜底基线）。
func TestBuiltinAlwaysAvailable(t *testing.T) {
	m := New(Options{})
	// 强制走内置：off。
	t.Setenv(marketFileEnv, "off")
	listings, src, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("内置清单应恒可解析，得到 %v", err)
	}
	if src != SourceBuiltin {
		t.Fatalf("off 应只用内置，得到来源 %q", src)
	}
	if len(listings) != 3 {
		t.Fatalf("内置清单应含 3 条示例（weather-query/daily-digest/doc-reviewer），得到 %d", len(listings))
	}
}

// TestOffIgnoresRemote 断言 MATE_APP_MARKET=off 时即使配置了远端也不联网（只用内置）。
func TestOffIgnoresRemote(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(validMarketJSON))
	}))
	defer srv.Close()

	t.Setenv(marketFileEnv, srv.URL)
	// off 应覆盖 URL：不联网。
	t.Setenv(marketFileEnv, "off")
	m := New(Options{})
	_, src, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("off 模式下不应发起远端请求")
	}
	if src != SourceBuiltin {
		t.Fatalf("off 应返回内置来源，得到 %q", src)
	}
}

// TestRemoteSucceeds 断言远端可达时用远端清单。
func TestRemoteSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(validMarketJSON))
	}))
	defer srv.Close()

	t.Setenv(marketFileEnv, srv.URL)
	m := New(Options{})
	listings, src, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceRemote {
		t.Fatalf("远端可达应返回 remote 来源，得到 %q", src)
	}
	if len(listings) != 1 || listings[0].ID != "weather-query" {
		t.Fatalf("远端清单内容不符：%+v", listings)
	}
}

// TestFallbackRemoteFailToCache 断言远端失败时退回磁盘缓存。
func TestFallbackRemoteFailToCache(t *testing.T) {
	// 预写磁盘缓存（内容与远端不同，用 id=from-cache 区分）。
	cacheDir := t.TempDir()
	cacheJSON := `{"version":1,"apps":[{"id":"from-cache","name":"缓存条","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/from-cache"}}]}`
	if err := os.WriteFile(filepath.Join(cacheDir, cacheFileName), []byte(cacheJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// 远端返回 500（失败）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv(marketFileEnv, srv.URL)
	m := New(Options{CacheDir: cacheDir})
	listings, src, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceCache {
		t.Fatalf("远端失败应退回磁盘缓存，得到来源 %q", src)
	}
	if len(listings) != 1 || listings[0].ID != "from-cache" {
		t.Fatalf("应返回磁盘缓存内容，得到 %+v", listings)
	}
}

// TestFallbackRemoteFailNoCacheToBuiltin 断言远端失败且无磁盘缓存时退回内置。
func TestFallbackRemoteFailNoCacheToBuiltin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv(marketFileEnv, srv.URL)
	m := New(Options{CacheDir: t.TempDir()}) // 空缓存目录
	listings, src, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceBuiltin {
		t.Fatalf("远端失败且无缓存应退回内置，得到来源 %q", src)
	}
	if len(listings) != 3 {
		t.Fatalf("应返回内置 3 条，得到 %d", len(listings))
	}
}

// TestRemoteWritesDiskCache 断言远端成功后写入磁盘缓存，供后续降级使用。
func TestRemoteWritesDiskCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(validMarketJSON))
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	t.Setenv(marketFileEnv, srv.URL)
	m := New(Options{CacheDir: cacheDir})
	if _, src, _ := m.List(context.Background()); src != SourceRemote {
		t.Fatalf("应走远端，得到 %q", src)
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, cacheFileName))
	if err != nil {
		t.Fatalf("远端成功后应写磁盘缓存：%v", err)
	}
	var f MarketFile
	if err := json.Unmarshal(data, &f); err != nil || len(f.Apps) != 1 {
		t.Fatalf("磁盘缓存内容不符：%v %+v", err, f)
	}
}

// TestRefreshForcesReload 断言 Refresh 清空内存缓存后，下一次 List 会重新获取（远端内容变化可见）。
func TestRefreshForcesReload(t *testing.T) {
	var payload = validMarketJSON
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	t.Setenv(marketFileEnv, srv.URL)
	m := New(Options{})
	first, _, _ := m.List(context.Background())
	if len(first) != 1 {
		t.Fatalf("首次应 1 条，得到 %d", len(first))
	}

	// 改远端内容为 2 条，未 Refresh 时仍命中内存缓存（1 条）。
	payload = `{"version":1,"apps":[
      {"id":"a","name":"A","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/a"}},
      {"id":"b","name":"B","summary":{"zh":"z"},"version":"1.0.0","source":{"type":"dir","dir":"apps/b"}}
    ]}`
	cached, _, _ := m.List(context.Background())
	if len(cached) != 1 {
		t.Fatalf("未刷新应命中内存缓存（1 条），得到 %d", len(cached))
	}

	// Refresh 后强制重拉，应见 2 条。
	m.Refresh()
	after, src, _ := m.List(context.Background())
	if len(after) != 2 {
		t.Fatalf("Refresh 后应见远端 2 条，得到 %d", len(after))
	}
	if src != SourceRemote {
		t.Fatalf("Refresh 后应走远端，得到 %q", src)
	}
}

// TestMemoryCacheHitWithinTTL 断言 TTL 内命中内存缓存（不重复联网）。
func TestMemoryCacheHitWithinTTL(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(validMarketJSON))
	}))
	defer srv.Close()

	t.Setenv(marketFileEnv, srv.URL)
	m := New(Options{})
	if _, _, _ = m.List(context.Background()); calls != 1 {
		t.Fatalf("首次应联网 1 次，得到 %d", calls)
	}
	if _, _, _ = m.List(context.Background()); calls != 1 {
		t.Fatalf("TTL 内应命中内存缓存、不再联网，得到 %d 次", calls)
	}
}
