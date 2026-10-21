package githubmedia

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

// 缓存条目不可读时的降级：清得掉按 miss 走重下，清不掉才上报 ErrLocalCache。

// seedCacheMeta 落一条未过期的缓存 meta，返回 meta 路径。
func seedCacheMeta(t *testing.T, svc *ProxyService, normalized string) string {
	t.Helper()
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	entry := cacheEntry{
		URL:         normalized,
		ContentType: "image/png",
		Size:        5,
		CachedAt:    time.Now().UTC(),
		ExpiresAt:   time.Now().Add(time.Hour).UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	metaPath := svc.metaPath(normalized)
	if err := os.WriteFile(metaPath, data, 0o640); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	return metaPath
}

// meta 读失败（权限，非 ErrNotExist）：降级 best-effort 删除 meta+data，删除
// 成功按 miss 返回（走重下），文件确实被清掉。
func TestGetCachedDegradesUnreadableMetaToMiss(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:  "review-screenshot-cache",
		allowURL:     allowPublicImageHost,
		requireHTTPS: true,
	})
	normalized, err := svc.normalize("https://uiv.example.com/f/x.png")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	metaPath := seedCacheMeta(t, svc, normalized)
	if err := os.Chmod(metaPath, 0o000); err != nil {
		t.Fatalf("chmod meta: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(metaPath, 0o640) })
	if err := os.WriteFile(svc.dataPath(normalized), []byte("png"), 0o640); err != nil {
		t.Fatalf("write data: %v", err)
	}

	cached, ok, err := svc.getCachedNormalized(normalized)
	if ok || err != nil || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss 不报错（降级重下）", cached, ok, err)
	}
	if _, statErr := os.Stat(metaPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("meta stat = %v, want 已被降级清理", statErr)
	}
	if _, statErr := os.Stat(svc.dataPath(normalized)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("data stat = %v, want 已被降级清理", statErr)
	}
}

// meta 正常但 data 不可 Stat（符号链接环，非 ErrNotExist 的失败）：同样降级
// 为 miss 并清掉残留 meta。
func TestGetCachedDegradesUnstatableDataToMiss(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:  "review-screenshot-cache",
		allowURL:     allowPublicImageHost,
		requireHTTPS: true,
	})
	normalized, err := svc.normalize("https://uiv.example.com/f/y.png")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	seedCacheMeta(t, svc, normalized)
	// 指向自身的符号链接：os.Stat 跟随链接即 ELOOP（非 ErrNotExist）。
	dataPath := svc.dataPath(normalized)
	if err := os.Symlink(dataPath, dataPath); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}

	cached, ok, err := svc.getCachedNormalized(normalized)
	if ok || err != nil || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss 不报错（降级重下）", cached, ok, err)
	}
	if _, statErr := os.Stat(svc.metaPath(normalized)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("meta stat = %v, want 已被降级清理", statErr)
	}
}

// 清理都做不了（缓存目录不可写=目录级故障）：以 ErrLocalCache 上报，不再静
// 默——静默的降级失败会在每次请求重复出现且无法自愈。
func TestGetCachedReportsLocalCacheErrWhenDiscardFails(t *testing.T) {
	root := t.TempDir()
	svc := newService(root, serviceConfig{
		cacheSubdir:  "review-screenshot-cache",
		allowURL:     allowPublicImageHost,
		requireHTTPS: true,
	})
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	normalized, err := svc.normalize("https://uiv.example.com/f/z.png")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	metaPath := seedCacheMeta(t, svc, normalized)
	if err := os.Chmod(metaPath, 0o000); err != nil {
		t.Fatalf("chmod meta: %v", err)
	}
	// 目录去掉写权限：Remove 必然失败。
	if err := os.Chmod(svc.cacheDir, 0o550); err != nil {
		t.Fatalf("chmod cache dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(svc.cacheDir, 0o750) })

	if _, _, err := svc.getCachedNormalized(normalized); !errors.Is(err, ErrLocalCache) {
		t.Fatalf("err = %v, want ErrLocalCache（降级也失败=目录级故障）", err)
	}
}

// image/svg（不带 +xml 的非标准写法）与 image/svg+xml 一样拒：视觉审查只吃
// 位图，svg 变体不能因为写法不标准就漏拒。
func TestScreenshotFaceRejectsSVGContentTypeVariants(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg")
		_, _ = w.Write([]byte("<svg/>"))
	}))
	defer upstream.Close()

	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir: "review-screenshot-cache",
		allowURL:    func(u *url.URL) bool { return u.Host == parsed.Host },
		imagesOnly:  true,
		client:      upstream.Client(),
	})
	if _, err := svc.FetchAndCache(t.Context(), upstream.URL+"/icon.svg"); !errors.Is(err, ErrUnsupportedUpstreamMedia) {
		t.Fatalf("err = %v, want ErrUnsupportedUpstreamMedia（image/svg 变体同拒）", err)
	}
}
