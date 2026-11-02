package githubmedia

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

	svc.SweepExpiredEntries()

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
		t.Fatalf("broken.json stat = %v, want 保留（mtime 新，写侧中间态不猜）", err)
	}

	// 隔离名 .gone 的回收：超龄（>2×TTL）清、新鲜留，两种后缀各一——与隔离
	// 判据同一阈值，1×~2×TTL 区间的写侧新文件不会被另一轮扫描误回收。
	staleGone := filepath.Join(svc.cacheDir, "stale.bin.gone")
	if err := os.WriteFile(staleGone, []byte("old"), 0o640); err != nil {
		t.Fatalf("write stale gone: %v", err)
	}
	staleTime := time.Now().Add(-3 * defaultCacheTTL)
	if err := os.Chtimes(staleGone, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes stale gone: %v", err)
	}
	freshGone := filepath.Join(svc.cacheDir, "fresh.json.gone")
	if err := os.WriteFile(freshGone, []byte("{}"), 0o640); err != nil {
		t.Fatalf("write fresh gone: %v", err)
	}

	svc.sweepExpiredEntries()

	if _, err := os.Stat(staleGone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale gone stat = %v, want 已回收", err)
	}
	if _, err := os.Stat(freshGone); err != nil {
		t.Fatalf("fresh gone stat = %v, want 保留（mtime 未超 2×TTL）", err)
	}

	// 损坏 meta 超 2×TTL：按孤儿口径回收（meta 与同名 .bin 一起删）——写盘
	// 中断/磁盘满留下的截断 meta 不再连带 256MB 的 .bin 永久占盘。
	staleBroken := filepath.Join(svc.cacheDir, "stale-broken.json")
	if err := os.WriteFile(staleBroken, []byte("{truncated"), 0o640); err != nil {
		t.Fatalf("write stale broken: %v", err)
	}
	old := time.Now().Add(-3 * defaultCacheTTL)
	if err := os.Chtimes(staleBroken, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	staleBin := filepath.Join(svc.cacheDir, "stale-broken.bin")
	if err := os.WriteFile(staleBin, []byte("big"), 0o640); err != nil {
		t.Fatalf("write stale broken bin: %v", err)
	}
	// URL 不可得的损坏 meta 依赖孤儿判据：.bin 的 mtime 也要超 2×TTL 才回收
	//（.bin 新鲜意味着可能是写侧刚 rename 的新 data，不能删）。
	if err := os.Chtimes(staleBin, old, old); err != nil {
		t.Fatalf("chtimes bin: %v", err)
	}

	svc.sweepExpiredEntries()

	if _, err := os.Stat(staleBroken); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale broken stat = %v, want 已回收", err)
	}
	if _, err := os.Stat(staleBin); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale broken bin stat = %v, want 已回收", err)
	}
}

// URL 不可得的损坏 meta（0 字节/开头即截断，解析不出 url 字段）：闸键会是
// 空串、与写侧不同键无互斥——只有同名 .bin 的 mtime 也超 2×TTL（不可能是
// 写侧刚 rename 的新 data）才删；.bin 新鲜时保留。
func TestSweepExpiredEntriesURLUnavailableBrokenMeta(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL: func(u *url.URL) bool { return true },
	})
	old := time.Now().Add(-3 * defaultCacheTTL)

	// 0 字节 meta + 新鲜 .bin（写侧刚 rename 新 data、meta 尚未写的窗口态）：
	// 不删——删了会把刚发布的条目抹掉。
	emptyMeta := filepath.Join(svc.cacheDir, "fresh-empty.json")
	if err := os.WriteFile(emptyMeta, nil, 0o640); err != nil {
		t.Fatalf("write empty meta: %v", err)
	}
	freshBin := filepath.Join(svc.cacheDir, "fresh-empty.bin")
	if err := os.WriteFile(freshBin, []byte("new"), 0o640); err != nil {
		t.Fatalf("write fresh bin: %v", err)
	}

	// 0 字节 meta + 超龄 .bin（历史残留）：一起回收。
	staleMeta := filepath.Join(svc.cacheDir, "stale-empty.json")
	if err := os.WriteFile(staleMeta, nil, 0o640); err != nil {
		t.Fatalf("write stale meta: %v", err)
	}
	if err := os.Chtimes(staleMeta, old, old); err != nil {
		t.Fatalf("chtimes meta: %v", err)
	}
	staleBin := filepath.Join(svc.cacheDir, "stale-empty.bin")
	if err := os.WriteFile(staleBin, []byte("old"), 0o640); err != nil {
		t.Fatalf("write stale bin: %v", err)
	}
	if err := os.Chtimes(staleBin, old, old); err != nil {
		t.Fatalf("chtimes bin: %v", err)
	}

	svc.sweepExpiredEntries()

	if _, err := os.Stat(freshBin); err != nil {
		t.Fatalf("fresh bin stat = %v, want 保留（写侧刚发布，URL 不可得不硬删）", err)
	}
	if _, err := os.Stat(emptyMeta); err != nil {
		t.Fatalf("empty meta stat = %v, want 保留（同上）", err)
	}
	if _, err := os.Stat(staleBin); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale bin stat = %v, want 已回收", err)
	}
	if _, err := os.Stat(staleMeta); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale meta stat = %v, want 已回收", err)
	}

	// 无 .bin 的超龄空 meta：没有 data 可误删，按 meta 自身 mtime 回收。
	loneMeta := filepath.Join(svc.cacheDir, "lone-empty.json")
	if err := os.WriteFile(loneMeta, nil, 0o640); err != nil {
		t.Fatalf("write lone meta: %v", err)
	}
	if err := os.Chtimes(loneMeta, old, old); err != nil {
		t.Fatalf("chtimes lone meta: %v", err)
	}

	svc.sweepExpiredEntries()

	if _, err := os.Stat(loneMeta); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lone meta stat = %v, want 已回收（无 .bin，按 meta mtime）", err)
	}
}

// 扫描与写盘竞争：写侧持下载闸发布期间，扫描必须等待并复核——发布完成后
// 条目不再过期，扫描跳过；旧过期条目（写侧没来覆盖）才被删。
func TestSweepExpiredEntriesWaitsForWriterAndSkipsFreshEntry(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL: func(u *url.URL) bool { return true },
	})
	now := time.Now().UTC()
	normalized, err := svc.normalize("https://x.test/r.png")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	metaPath := filepath.Join(svc.cacheDir, "raced.json")
	writeMeta := func(expires time.Time) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"url": normalized, "content_type": "image/png", "cached_at": now, "expires_at": expires})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(metaPath, data, 0o640); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	// 旧过期 meta 落盘，模拟「扫描已读到它」。
	writeMeta(now.Add(-time.Hour))
	binPath := filepath.Join(svc.cacheDir, "raced.bin")
	if err := os.WriteFile(binPath, []byte("old"), 0o640); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	// 写侧持下载闸（发布中）：扫描必须等闸，不能越过它删文件。
	gate := svc.acquireDownloadLock(normalized)
	gate.sem <- struct{}{}

	sweepDone := make(chan struct{})
	go func() {
		svc.SweepExpiredEntries()
		close(sweepDone)
	}()

	// 证明扫描真的在等闸：闸被持有时它不能结束（也没有删任何文件）。
	time.Sleep(300 * time.Millisecond)
	select {
	case <-sweepDone:
		t.Fatal("sweep finished while writer held the lock — 没有在等闸")
	default:
	}
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta stat = %v, want 持闸期间未被删", err)
	}

	// 写侧发布新条目并释放闸：扫描随后拿到闸，锁内复核发现不再过期，跳过。
	writeMeta(now.Add(time.Hour))
	if err := os.WriteFile(binPath, []byte("new"), 0o640); err != nil {
		t.Fatalf("write new bin: %v", err)
	}
	svc.releaseDownloadLock(normalized, gate)

	select {
	case <-sweepDone:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep did not finish — 检查是否死锁在等闸")
	}

	// 发布后的新条目必须原样保留（扫描在锁内复核发现不再过期，跳过）。
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta stat = %v, want 保留（锁内复核发现不再过期）", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("bin stat = %v, want 保留（锁内复核发现不再过期）", err)
	}

	// 闸已释放：再次扫描，此时若把 meta 改回过期态，扫描应能正常删除。
	writeMeta(now.Add(-time.Hour))
	svc.SweepExpiredEntries()
	if _, err := os.Stat(metaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("meta stat = %v, want 已删除（闸释放后正常过期回收）", err)
	}
}

// 无 meta 的孤儿 .bin 按 mtime 超过 2×TTL 清理：历史删除中断的残留不永久堆积。
func TestSweepExpiredEntriesRemovesStaleOrphanBins(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL: func(u *url.URL) bool { return true },
	})
	orphan := filepath.Join(svc.cacheDir, "orphan.bin")
	if err := os.WriteFile(orphan, []byte("data"), 0o640); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	stale := time.Now().Add(-3 * defaultCacheTTL)
	if err := os.Chtimes(orphan, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	fresh := filepath.Join(svc.cacheDir, "fresh-orphan.bin")
	if err := os.WriteFile(fresh, []byte("data"), 0o640); err != nil {
		t.Fatalf("write fresh orphan: %v", err)
	}

	svc.SweepExpiredEntries()

	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan stat = %v, want 已清理", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh orphan stat = %v, want 保留（mtime 未超 2×TTL）", err)
	}
}

// 中断下载留下的 media-*.tmp 同样按 mtime 超 2×TTL 回收（正在写盘的 mtime 新，
// 不会被误删）。
func TestSweepExpiredEntriesRemovesStaleTmpFiles(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL: func(u *url.URL) bool { return true },
	})
	stale := filepath.Join(svc.cacheDir, "media-123.tmp")
	if err := os.WriteFile(stale, []byte("partial"), 0o640); err != nil {
		t.Fatalf("write stale tmp: %v", err)
	}
	old := time.Now().Add(-3 * defaultCacheTTL)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	active := filepath.Join(svc.cacheDir, "media-456.tmp")
	if err := os.WriteFile(active, []byte("writing"), 0o640); err != nil {
		t.Fatalf("write active tmp: %v", err)
	}

	svc.SweepExpiredEntries()

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale tmp stat = %v, want 已清理", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active tmp stat = %v, want 保留（正在写盘）", err)
	}
}

// meta 读失败（目录级故障）按限频 warn 记录：同 cacheDir 间隔内只记一次。
func TestLogMetaReadFailRateLimited(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := newService(t.TempDir(), serviceConfig{
		allowURL:     allowPublicImageHost,
		requireHTTPS: true,
	})
	svc.metaReadFailLogger = logger
	svc.metaReadFailAt = make(map[string]time.Time)

	metaPath := svc.metaPath(svc.metaPath("k", ""), "")
	svc.logMetaReadFail(metaPath, os.ErrPermission)
	svc.logMetaReadFail(metaPath, os.ErrPermission)
	if n := strings.Count(buf.String(), "缓存 meta 读取失败"); n != 1 {
		t.Fatalf("warn count = %d, want 1（限频）", n)
	}
	svc.metaReadFailAt[svc.cacheDir] = time.Now().Add(-2 * metaReadFailLogInterval)
	svc.logMetaReadFail(metaPath, os.ErrPermission)
	if n := strings.Count(buf.String(), "缓存 meta 读取失败"); n != 2 {
		t.Fatalf("warn count = %d, want 2（间隔过后再记）", n)
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

// 容量回收：.bin 合计超出 capacityLimit 时按 mtime 最旧淘汰到上限以内，
// 未过期的条目也会被淘汰（媒体面条目 miss 重下自愈，删了不丢数据）；
// meta 解析不出 URL 的 .bin 不参与容量淘汰（孤儿/损坏清理的辖区，绕过
// 写侧互斥硬删最坏抹掉并行发布中的新条目）。
func TestSweepExpiredEntriesEvictsOldestOverCapacity(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL:      func(u *url.URL) bool { return true },
		capacityLimit: 25,
	})
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	now := time.Now().UTC()
	writeEntry := func(url string, size int, age time.Duration) string {
		t.Helper()
		metaPath := seedCacheMeta(t, svc, url, "image/png")
		binPath := strings.TrimSuffix(metaPath, ".json") + ".bin"
		if err := os.WriteFile(binPath, bytes.Repeat([]byte("x"), size), 0o640); err != nil {
			t.Fatalf("write bin: %v", err)
		}
		mtime := now.Add(-age)
		if err := os.Chtimes(binPath, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		return binPath
	}
	oldBin := writeEntry("https://x.test/old.png", 10, 3*time.Hour)
	midBin := writeEntry("https://x.test/mid.png", 10, 2*time.Hour)
	freshBin := writeEntry("https://x.test/fresh.png", 10, 0)
	// meta 损坏的 .bin：发布最早，但 URL 不可得——容量淘汰不动手，它归
	// mtime 判据（孤儿/损坏清理，>2×TTL）管。
	corruptJSON := filepath.Join(svc.cacheDir, "corrupt.json")
	if err := os.WriteFile(corruptJSON, []byte("{broken"), 0o640); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}
	corruptBin := filepath.Join(svc.cacheDir, "corrupt.bin")
	if err := os.WriteFile(corruptBin, bytes.Repeat([]byte("x"), 10), 0o640); err != nil {
		t.Fatalf("write corrupt bin: %v", err)
	}
	stale := now.Add(-4 * time.Hour)
	if err := os.Chtimes(corruptBin, stale, stale); err != nil {
		t.Fatalf("chtimes corrupt bin: %v", err)
	}

	// 合计（只算可安全淘汰的）30 > 上限 25：corrupt 跳过且不进容量总量，
	// 淘汰 old 后 20 ≤ 25 即停——mid 与 fresh 保留。
	svc.sweepExpiredEntries()

	if _, err := os.Stat(oldBin); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old bin stat = %v, want 已淘汰", err)
	}
	oldMeta := strings.TrimSuffix(oldBin, ".bin") + ".json"
	if _, err := os.Stat(oldMeta); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s stat = %v, want 已淘汰", oldMeta, err)
	}
	for _, p := range []string{midBin, freshBin} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s 读不到, want 保留（最旧淘汰到上限内即停）", p)
		}
	}
	if _, err := os.Stat(corruptBin); err != nil {
		t.Fatal("corrupt bin 读不到, want 保留（URL 不可得不参与容量淘汰）")
	}
	if _, err := os.Stat(corruptJSON); err != nil {
		t.Fatal("corrupt meta 读不到, want 保留")
	}
}

// 容量口径只算可安全淘汰的字节：meta 缺失的孤儿 .bin（写侧 rename 后被杀、
// mtime 新，归孤儿判据管）不进容量总量——把它算进去，上限就成了不可满足的
// 约束，每轮扫描都会把可淘汰的存活条目删到底且永远填不平。
func TestSweepExpiredEntriesCapacityIgnoresUnreachableBytes(t *testing.T) {
	svc := newService(t.TempDir(), serviceConfig{
		allowURL:      func(u *url.URL) bool { return true },
		capacityLimit: 100,
	})
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	metaPath := seedCacheMeta(t, svc, "https://x.test/live.png", "image/png")
	liveBin := strings.TrimSuffix(metaPath, ".json") + ".bin"
	if err := os.WriteFile(liveBin, bytes.Repeat([]byte("x"), 10), 0o640); err != nil {
		t.Fatalf("write live bin: %v", err)
	}
	// 孤儿 .bin：无 meta，字节远超上限。
	orphanBin := filepath.Join(svc.cacheDir, "orphan.bin")
	if err := os.WriteFile(orphanBin, bytes.Repeat([]byte("x"), 200), 0o640); err != nil {
		t.Fatalf("write orphan bin: %v", err)
	}

	svc.sweepExpiredEntries()

	if _, err := os.Stat(liveBin); err != nil {
		t.Fatal("live bin 读不到, want 保留（可淘汰字节低于上限，孤儿字节不进容量口径）")
	}
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatal("live meta 读不到, want 保留")
	}
}
