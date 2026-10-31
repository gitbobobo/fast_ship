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

// seedCacheMeta 落一条未过期的缓存 meta，返回 meta 路径。contentType 给
// 条目声明的 Content-Type（历史缓存复核测试用它塞老准入口径写进去的值）。
func seedCacheMeta(t *testing.T, svc *ProxyService, normalized, contentType string) string {
	t.Helper()
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	entry := cacheEntry{
		URL:         normalized,
		ContentType: contentType,
		Size:        5,
		CachedAt:    time.Now().UTC(),
		ExpiresAt:   time.Now().Add(time.Hour).UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	metaPath := svc.metaPath(normalized, "")
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
	metaPath := seedCacheMeta(t, svc, normalized, "image/png")
	if err := os.Chmod(metaPath, 0o000); err != nil {
		t.Fatalf("chmod meta: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(metaPath, 0o640) })
	if err := os.WriteFile(svc.dataPath(normalized, ""), []byte("png"), 0o640); err != nil {
		t.Fatalf("write data: %v", err)
	}

	cached, ok, err := svc.getCachedNormalized(normalized, "")
	if ok || err != nil || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss 不报错（降级重下）", cached, ok, err)
	}
	if _, statErr := os.Stat(metaPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("meta stat = %v, want 已被降级清理", statErr)
	}
	if _, statErr := os.Stat(svc.dataPath(normalized, "")); !errors.Is(statErr, os.ErrNotExist) {
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
	seedCacheMeta(t, svc, normalized, "image/png")
	// 指向自身的符号链接：os.Stat 跟随链接即 ELOOP（非 ErrNotExist）。
	dataPath := svc.dataPath(normalized, "")
	if err := os.Symlink(dataPath, dataPath); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}

	cached, ok, err := svc.getCachedNormalized(normalized, "")
	if ok || err != nil || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss 不报错（降级重下）", cached, ok, err)
	}
	if _, statErr := os.Stat(svc.metaPath(normalized, "")); !errors.Is(statErr, os.ErrNotExist) {
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
	metaPath := seedCacheMeta(t, svc, normalized, "image/png")
	if err := os.Chmod(metaPath, 0o000); err != nil {
		t.Fatalf("chmod meta: %v", err)
	}
	// 目录去掉写权限：Remove 必然失败。
	if err := os.Chmod(svc.cacheDir, 0o550); err != nil {
		t.Fatalf("chmod cache dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(svc.cacheDir, 0o750) })

	if _, _, err := svc.getCachedNormalized(normalized, ""); !errors.Is(err, ErrLocalCache) {
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

// INT-171 缓存命中复核：旧准入口径（收 svg）写进缓存的历史条目，命中时按
// 当前规则重新校验 ContentType，不过关就删 meta+data 按 miss 走重下；位图
// 条目不受复核影响，仍然命中。
func TestGetCachedRevalidatesContentTypeGate(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL: func(u *url.URL) bool { return true },
	})

	// 历史 svg 条目：命中时被淘汰，meta 与 data 都被清掉。
	svgURL, err := svc.normalize("https://user-images.githubusercontent.com/legacy.svg")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	seedCacheMeta(t, svc, svgURL, "image/svg+xml")
	if err := os.WriteFile(svc.dataPath(svgURL, ""), []byte("<svg/>"), 0o640); err != nil {
		t.Fatalf("write data: %v", err)
	}

	cached, ok, err := svc.GetCached(svgURL)
	if ok || err != nil || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss 不报错（复核不过关淘汰）", cached, ok, err)
	}
	if _, statErr := os.Stat(svc.metaPath(svgURL, "")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("svg meta stat = %v, want 已被淘汰清理", statErr)
	}
	if _, statErr := os.Stat(svc.dataPath(svgURL, "")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("svg data stat = %v, want 已被淘汰清理", statErr)
	}

	// 位图条目：复核过关，照常命中。
	pngURL, err := svc.normalize("https://user-images.githubusercontent.com/ok.png")
	if err != nil {
		t.Fatalf("normalize png: %v", err)
	}
	seedCacheMeta(t, svc, pngURL, "image/png")
	if err := os.WriteFile(svc.dataPath(pngURL, ""), []byte("png"), 0o640); err != nil {
		t.Fatalf("write png data: %v", err)
	}

	cached, ok, err = svc.GetCached(pngURL)
	if err != nil || !ok || cached == nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want 命中", cached, ok, err)
	}
	if cached.Metadata.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", cached.Metadata.ContentType)
	}
}
