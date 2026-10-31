package githubmedia

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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

// meta 读失败（权限，非 ErrNotExist）：按 miss 返回，读路径不删文件。
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
	if _, statErr := os.Stat(metaPath); statErr != nil {
		t.Fatalf("meta stat = %v, want 文件仍在（读路径不删）", statErr)
	}
	if _, statErr := os.Stat(svc.dataPath(normalized, "")); statErr != nil {
		t.Fatalf("data stat = %v, want 文件仍在（读路径不删）", statErr)
	}
}

// meta 正常但 data 不可 Stat（符号链接环，非 ErrNotExist 的失败）：按 miss
// 返回，读路径不删文件。
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
	if _, statErr := os.Stat(svc.metaPath(normalized, "")); statErr != nil {
		t.Fatalf("meta stat = %v, want 文件仍在（读路径不删）", statErr)
	}
}

// 清理都做不了（缓存目录不可写=目录级故障）：以 ErrLocalCache 上报，不再静
// 默——静默的降级失败会在每次请求重复出现且无法自愈。
// meta 读失败（权限，非 ErrNotExist）降级为 miss：读路径不删文件（删除会与
// 同 key 写盘竞态），下次读取仍 miss、重下后按同一 key 覆盖自愈。
func TestGetCachedDegradesUnreadableMetaToMissWithoutRemoval(t *testing.T) {
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

	cached, ok, err := svc.GetCached(normalized)
	if ok || err != nil || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss（读失败降级为 miss）", cached, ok, err)
	}
	if _, statErr := os.Stat(metaPath); statErr != nil {
		t.Fatalf("meta stat = %v, want 文件仍在（读路径不删）", statErr)
	}
}

// 目录过期扫描：删已过期条目（meta+data），未过期与损坏 meta 不动。
func TestSweepExpiredEntriesRemovesOnlyExpired(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL: func(u *url.URL) bool { return true },
	})
	now := time.Now().UTC()

	write := func(name string, entry map[string]any) {
		t.Helper()
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(svc.cacheDir, name), data, 0o640); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	write("expired.json", map[string]any{"url": "https://x.test/a.png", "content_type": "image/png", "cached_at": past, "expires_at": past})
	write("fresh.json", map[string]any{"url": "https://x.test/b.png", "content_type": "image/png", "cached_at": now, "expires_at": future})
	if err := os.WriteFile(filepath.Join(svc.cacheDir, "broken.json"), []byte("{not json"), 0o640); err != nil {
		t.Fatalf("write broken.json: %v", err)
	}
	for _, name := range []string{"expired", "fresh"} {
		if err := os.WriteFile(filepath.Join(svc.cacheDir, name+".bin"), []byte("data"), 0o640); err != nil {
			t.Fatalf("write %s bin: %v", name, err)
		}
	}

	svc.sweepExpiredEntries()

	if _, err := os.Stat(filepath.Join(svc.cacheDir, "expired.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired.json stat = %v, want 已删除", err)
	}
	if _, err := os.Stat(filepath.Join(svc.cacheDir, "expired.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired.bin stat = %v, want 已删除", err)
	}
	if _, err := os.Stat(filepath.Join(svc.cacheDir, "fresh.json")); err != nil {
		t.Fatalf("fresh.json stat = %v, want 保留", err)
	}
	if _, err := os.Stat(filepath.Join(svc.cacheDir, "fresh.bin")); err != nil {
		t.Fatalf("fresh.bin stat = %v, want 保留", err)
	}
	if _, err := os.Stat(filepath.Join(svc.cacheDir, "broken.json")); err != nil {
		t.Fatalf("broken.json stat = %v, want 保留（解析不了不猜）", err)
	}
}

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
// 当前规则重新校验 ContentType，不过关只按 miss 走重下，不在读路径删文件
// ——GetCached 不持下载锁，删 meta/data 会与正在写盘的同 key 下载竞态。
// 空类型、基础类型（剥参小写）为 octet-stream 的旧条目直接按 miss 重下：
// 它们的放行依赖扩展名，meta 里没有重定向后的最终 URL，读路径上无法与写
// 入侧对齐。带具体类型的条目仍然命中，返回的 ContentType 换成规范化类型
// （只改返回值，不重写 meta）。
func TestGetCachedRevalidatesContentTypeGate(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL: func(u *url.URL) bool { return true },
	})

	// 历史 svg 条目：命中时按 miss 淘汰重下，meta 与 data 留在原地（下一次
	// 成功下载按同一 key 覆盖，TTL 到期另有清理）。
	svgURL, err := svc.normalize("https://user-images.githubusercontent.com/legacy.svg")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	svgMeta := seedCacheMeta(t, svc, svgURL, "image/svg+xml")
	if err := os.WriteFile(svc.dataPath(svgURL, ""), []byte("<svg/>"), 0o640); err != nil {
		t.Fatalf("write data: %v", err)
	}

	cached, ok, err := svc.GetCached(svgURL)
	if ok || err != nil || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss 不报错（复核不过关按 miss 淘汰）", cached, ok, err)
	}
	if _, statErr := os.Stat(svgMeta); statErr != nil {
		t.Fatalf("svg meta stat = %v, want 文件仍在（读路径不删）", statErr)
	}
	if _, statErr := os.Stat(svc.dataPath(svgURL, "")); statErr != nil {
		t.Fatalf("svg data stat = %v, want 文件仍在（读路径不删）", statErr)
	}

	// 过期条目同样只按 miss：读路径的过期清理也退出，交给持下载锁的写路径
	// 按同一 key 覆盖退役。
	expiredURL, err := svc.normalize("https://user-images.githubusercontent.com/expired.png")
	if err != nil {
		t.Fatalf("normalize expired: %v", err)
	}
	expiredMeta := seedCacheMeta(t, svc, expiredURL, "image/png")
	expiredEntry := map[string]any{
		"url": expiredURL, "content_type": "image/png", "size": 3,
		"cached_at": time.Now().Add(-2 * time.Hour).UTC(), "expires_at": time.Now().Add(-time.Hour).UTC(),
	}
	expiredData, err := json.Marshal(expiredEntry)
	if err != nil {
		t.Fatalf("marshal expired entry: %v", err)
	}
	if err := os.WriteFile(expiredMeta, expiredData, 0o640); err != nil {
		t.Fatalf("write expired meta: %v", err)
	}
	if err := os.WriteFile(svc.dataPath(expiredURL, ""), []byte("png"), 0o640); err != nil {
		t.Fatalf("write expired data: %v", err)
	}
	cached, ok, err = svc.GetCached(expiredURL)
	if ok || err != nil || cached != nil {
		t.Fatalf("expired: cached = %+v ok = %v err = %v, want miss", cached, ok, err)
	}
	if _, statErr := os.Stat(expiredMeta); statErr != nil {
		t.Fatalf("expired meta stat = %v, want 文件仍在（读路径不删）", statErr)
	}
	if _, statErr := os.Stat(svc.dataPath(expiredURL, "")); statErr != nil {
		t.Fatalf("expired data stat = %v, want 文件仍在（读路径不删）", statErr)
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

	// 老缓存存的 octet-stream + .png URL：放行依赖扩展名，而扩展名要用重定向
	// 后的最终 URL 才算得对、meta 里没存——保守判 miss 走重下（下一次成功
	// 下载按当前口径写入规范化类型），不在读路径上猜。
	legacyURL, err := svc.normalize("https://user-images.githubusercontent.com/legacy.png")
	if err != nil {
		t.Fatalf("normalize legacy png: %v", err)
	}
	legacyMeta := seedCacheMeta(t, svc, legacyURL, "application/octet-stream")
	if err := os.WriteFile(svc.dataPath(legacyURL, ""), []byte("png"), 0o640); err != nil {
		t.Fatalf("write legacy data: %v", err)
	}

	cached, ok, err = svc.GetCached(legacyURL)
	if err != nil || ok || cached != nil {
		t.Fatalf("cached = %+v ok = %v err = %v, want miss（octet-stream 旧条目无法还原最终 URL，保守重下）", cached, ok, err)
	}
	// 带参数形态同样要拦：旧写入路径原样存上游值，剥参后才是 octet-stream。
	// 同 key seed 会覆盖第一个条目，分开断言各自的 miss；读路径不删文件，
	// meta 原样保留（用第二次 seed 的值核）。
	metaBytes, err := os.ReadFile(legacyMeta)
	if err != nil {
		t.Fatalf("read legacy meta: %v", err)
	}
	var entry cacheEntry
	if err := json.Unmarshal(metaBytes, &entry); err != nil {
		t.Fatalf("unmarshal legacy meta: %v", err)
	}
	if entry.ContentType != "application/octet-stream" {
		t.Fatalf("meta content type = %q, want application/octet-stream（meta 文件不重写）", entry.ContentType)
	}
	seedCacheMeta(t, svc, legacyURL, "application/octet-stream; charset=binary")
	cached, ok, err = svc.GetCached(legacyURL)
	if err != nil || ok || cached != nil {
		t.Fatalf("param form: cached = %+v ok = %v err = %v, want miss", cached, ok, err)
	}
	metaBytes, err = os.ReadFile(legacyMeta)
	if err != nil {
		t.Fatalf("read param meta: %v", err)
	}
	if err := json.Unmarshal(metaBytes, &entry); err != nil {
		t.Fatalf("unmarshal param meta: %v", err)
	}
	if entry.ContentType != "application/octet-stream; charset=binary" {
		t.Fatalf("param meta content type = %q, want 原样保留（meta 文件不重写）", entry.ContentType)
	}
}
