package modelcatalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestValid(t *testing.T) {
	if !Valid(ProviderOpenAI) || !Valid(ProviderDeepSeek) || !Valid(ProviderGroq) {
		t.Fatal("已知 provider 应 valid")
	}
	if Valid(ProviderType("internal")) || Valid(ProviderType("")) {
		t.Fatal("未知/空 provider 应 invalid")
	}
}

func TestSnapshotModels(t *testing.T) {
	m, err := snapshotModels(ProviderDeepSeek)
	if err != nil {
		t.Fatalf("deepseek 快照: %v", err)
	}
	if len(m) == 0 {
		t.Fatal("deepseek 快照应为非空")
	}
	if m[0].ID != "deepseek-v4-flash" || !m[0].Recommended {
		t.Fatalf("deepseek 快照首条不符: %+v", m[0])
	}
	if _, err := snapshotModels(ProviderType("nope")); err == nil {
		t.Fatal("未知 provider 快照应报错")
	}
}

// liveServer 起一个返回给定 JSON 的 httptest server，并把 endpoints 指向它。
func liveServer(t *testing.T, typ ProviderType, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	prev := endpoints[typ]
	endpoints[typ] = ts.URL
	t.Cleanup(func() { endpoints[typ] = prev })
	return ts
}

func TestLiveFetchSuccess(t *testing.T) {
	liveServer(t, ProviderDeepSeek, `{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner","name":"DeepSeek Reasoner"}]}`)
	got, err := liveFetch(context.Background(), ProviderDeepSeek, "sk-test")
	if err != nil {
		t.Fatalf("liveFetch: %v", err)
	}
	want := []Model{{ID: "deepseek-chat", Title: "deepseek-chat"}, {ID: "deepseek-reasoner", Title: "DeepSeek Reasoner"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("live 归一化不符:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLiveFetchHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	prev := endpoints[ProviderGroq]
	endpoints[ProviderGroq] = ts.URL
	t.Cleanup(func() { endpoints[ProviderGroq] = prev })
	if _, err := liveFetch(context.Background(), ProviderGroq, "sk-test"); err == nil {
		t.Fatal("上游 5xx 应报错")
	}
}

func TestResolveSnapshotFallback(t *testing.T) {
	dir := t.TempDir()
	models, src, err := Resolve(context.Background(), ProviderOpenAI, "", dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceSnapshot {
		t.Fatalf("无 live/缓存 应回快照: %s", src)
	}
	if len(models) == 0 || models[0].ID != "gpt-4o" {
		t.Fatalf("快照内容不符: %+v", models)
	}
}

func TestResolveLiveThenCache(t *testing.T) {
	dir := t.TempDir()
	liveServer(t, ProviderDeepSeek, `{"data":[{"id":"deepseek-chat","name":"DeepSeek Chat"}]}`)

	// 1) live 成功 → SourceLive + 写缓存
	models, src, err := Resolve(context.Background(), ProviderDeepSeek, "sk-test", dir)
	if err != nil {
		t.Fatalf("live Resolve: %v", err)
	}
	if src != SourceLive || len(models) != 1 || models[0].ID != "deepseek-chat" {
		t.Fatalf("live 结果不符: %s %+v", src, models)
	}
	if _, err := os.Stat(cachePath(ProviderDeepSeek, dir)); err != nil {
		t.Fatalf("live 成功后应写缓存: %v", err)
	}

	// 2) 无 live（apiKey 空）但缓存未过期 → SourceCache
	models, src, err = Resolve(context.Background(), ProviderDeepSeek, "", dir)
	if err != nil {
		t.Fatalf("cache Resolve: %v", err)
	}
	if src != SourceCache || len(models) != 1 {
		t.Fatalf("cache 结果不符: %s %+v", src, models)
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	_, _, err := Resolve(context.Background(), ProviderType("nope"), "sk", t.TempDir())
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("未知 provider 应 ErrUnknownProvider: %v", err)
	}
}

func TestReadWriteCacheExpiry(t *testing.T) {
	dir := t.TempDir()
	if err := writeCache(ProviderGroq, dir, []Model{{ID: "llama", Title: "Llama"}}); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	models, ok := readCache(ProviderGroq, dir)
	if !ok || len(models) != 1 || models[0].ID != "llama" {
		t.Fatalf("readCache 不符: %+v ok=%v", models, ok)
	}
	// 过期：把 mtime 拨回「TTL+1h」之前。
	old := cachePath(ProviderGroq, dir)
	past := time.Now().Add(-(cacheTTL + time.Hour))
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if _, ok := readCache(ProviderGroq, dir); ok {
		t.Fatal("过期缓存应不命中")
	}
}
