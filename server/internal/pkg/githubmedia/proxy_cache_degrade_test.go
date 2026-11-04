package githubmedia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// newCapacityTestService 起一个容量上限极小的媒体面服务，上游固定回 size
// 字节的 png。返回服务与上游 URL 前缀。
func newCapacityTestService(t *testing.T, capacity int64, size int) (*ProxyService, string) {
	t.Helper()
	payload := bytes.Repeat([]byte("x"), size)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(upstream.Close)
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:   defaultCacheSubdir,
		cacheTTL:      time.Hour,
		capacityLimit: capacity,
		client:        upstream.Client(),
		allowURL:      func(u *url.URL) bool { return u.Host == parsed.Host },
	})
	return svc, upstream.URL
}

// 容量闸长在写入点：水位会越过上限时不缓存、返回 ErrCacheFull，且**不删
// 任何已落盘条目**——容量机制不再删文件，TTL 内命中的图片不会因容量变破
// 图（INT-172 R4 方向审计把「扫描期按最旧淘汰」换成了这条准入闸）。
func TestFetchAndCacheRejectsWhenCapacityExhausted(t *testing.T) {
	svc, upstream := newCapacityTestService(t, 40, 32)
	ctx := context.Background()

	first, err := svc.FetchAndCache(ctx, upstream+"/a.png")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// 32 已落盘，再要 32 会越过 40。
	if _, err := svc.FetchAndCache(ctx, upstream+"/b.png"); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("second fetch err = %v, want ErrCacheFull", err)
	}

	// 已落盘条目必须原样在盘上，且仍可命中。
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("first entry stat = %v, want 保留（准入闸不删已落盘条目）", err)
	}
	if _, ok, err := svc.GetCached(upstream + "/a.png"); err != nil || !ok {
		t.Fatalf("GetCached = ok:%v err:%v, want 命中", ok, err)
	}
}

// 预留计数覆盖并发下载：两次预留各自通过后合并水位仍受上限约束，释放后可
// 再用——否则一个高并发页面就能把目录推过上限（P2-b 的「并发下载不设限」）。
func TestReserveCacheBytesCountsConcurrentReservations(t *testing.T) {
	svc, _ := newCapacityTestService(t, 100, 1)

	first, err := svc.reserveCacheBytes(60)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if _, err := svc.reserveCacheBytes(60); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("concurrent reserve err = %v, want ErrCacheFull（60+60 > 100）", err)
	}
	first.release()
	if _, err := svc.reserveCacheBytes(60); err != nil {
		t.Fatalf("reserve after release: %v, want 成功（退还后额度回到 100）", err)
	}
}

// 过期扫描删掉条目后要立刻校准水位：不校准，被删字节会一直占着额度，缓存
// 明明空着却一直拒绝新字节。
func TestSweepExpiredEntriesRecalibratesCapacity(t *testing.T) {
	svc, upstream := newCapacityTestService(t, 40, 32)
	ctx := context.Background()

	if _, err := svc.FetchAndCache(ctx, upstream+"/a.png"); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := svc.FetchAndCache(ctx, upstream+"/b.png"); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("second fetch err = %v, want ErrCacheFull", err)
	}

	// 把 a 改成已过期，扫描回收后水位应回到 0。
	metaPath := svc.metaPath(upstream+"/a.png", "")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	entry.CachedAt = time.Now().UTC().Add(-2 * time.Hour)
	entry.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	expired, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(metaPath, expired, 0o640); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	svc.SweepExpiredEntries()

	if _, err := svc.FetchAndCache(ctx, upstream+"/b.png"); err != nil {
		t.Fatalf("fetch after sweep err = %v, want 成功（回收后额度已释放）", err)
	}
}

// 截图面不经过容量准入：它是持久资产面，回收只走行账保留策略（INT-184），
// 上界由张数与探测预算给出。给它一个形同虚设的上限也必须照常落盘。
func TestReviewFaceSkipsCapacityAdmission(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 64))
	}))
	defer upstream.Close()
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:   "review-screenshot-cache",
		cacheTTL:      time.Hour,
		capacityLimit: 1, // 形同虚设：截图面不吃这道闸
		client:        upstream.Client(),
		allowURL:      allowPublicImageHost,
		requireHTTPS:  true,
		imagesOnly:    true,
	})

	cached, err := svc.FetchAndCache(context.Background(), upstream.URL+"/s.png")
	if err != nil {
		t.Fatalf("review face fetch: %v, want 成功（不吃容量准入）", err)
	}
	if _, err := os.Stat(cached.Path); err != nil {
		t.Fatalf("cached path stat = %v, want 落盘", err)
	}
}

// 目录扫描读失败（权限、EIO、只读挂载）不能当成「占用为 0」写进水位：那会
// 把水位清零，准入闸随后按空目录再放行整整一个上限的字节，与本 PR 声明的
// 「磁盘有界」正相反。
func TestRecalibrateCacheUsageKeepsWatermarkWhenScanFails(t *testing.T) {
	svc, _ := newCapacityTestService(t, 100, 1)
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	held, err := svc.reserveCacheBytes(60)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	held.settle(60)

	if err := os.Chmod(svc.cacheDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(svc.cacheDir, 0o750) })
	if _, readErr := os.ReadDir(svc.cacheDir); readErr == nil {
		t.Skip("目录仍可读（以 root 运行？），无法模拟扫描失败")
	}

	svc.recalibrateCacheUsage()

	// 水位仍是 60：再要 60 会越过 100。若校准把水位清零，这里会成功。
	if _, err := svc.reserveCacheBytes(60); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("reserve after failed scan err = %v, want ErrCacheFull（水位未被清零）", err)
	}
}

// 校准在锁外扫目录：扫描期间落盘的字节不能被陈旧快照抹掉。快照之后完成
// 的 settle 叠加到快照上，水位等于真实占用（而不是只剩快照那份，也不是
// 只剩新落盘那份——后者会让重启前的存量整段不计入，磁盘可到上限的两倍）。
func TestRecalibrateCacheUsageAddsSettlesRacingTheScan(t *testing.T) {
	svc, upstream := newCapacityTestService(t, 100, 32)

	svc.applyCalibration(func() (int64, bool) {
		// 扫描读到空目录（快照 0 字节），随后一次下载落盘 32 字节并结算。
		if _, err := svc.FetchAndCache(context.Background(), upstream+"/race.png"); err != nil {
			t.Fatalf("fetch during scan: %v", err)
		}
		return 0, true
	})

	// 水位应为 32（= 快照 0 + 扫描期结算 32 = 盘上实际字节）。
	if _, err := svc.reserveCacheBytes(68); err != nil {
		t.Fatalf("reserve 68: %v, want 成功（32+68 = 100 ≤ 上限）", err)
	}
	if _, err := svc.reserveCacheBytes(1); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("reserve 1 err = %v, want ErrCacheFull（水位已到 100）", err)
	}
}

// 首次校准失败时不能按「水位 0」放行：那一刻账上只有本进程启动后的落盘
// 量，磁盘上重启前的存量整段不在账上，按它放行能让目录写到上限的两倍。
// 失败的校准也不随每条写请求重扫目录——退避期内一律直通。
func TestCapacityUncalibratedRefusesCachingAndThrottlesRetry(t *testing.T) {
	svc, _ := newCapacityTestService(t, 100, 1)
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.Chmod(svc.cacheDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			_ = os.Chmod(svc.cacheDir, 0o750)
		}
	}
	t.Cleanup(restore)
	if _, readErr := os.ReadDir(svc.cacheDir); readErr == nil {
		t.Skip("目录仍可读（以 root 运行？），无法模拟扫描失败")
	}

	if _, err := svc.reserveCacheBytes(1); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("reserve err = %v, want ErrCacheFull（未校准不放行）", err)
	}

	// 权限恢复后立刻再试：仍在退避期，不重扫、继续直通。
	restore()
	if _, err := svc.reserveCacheBytes(1); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("reserve in backoff err = %v, want ErrCacheFull（退避期内不重扫）", err)
	}

	// 退避到期后重新校准并放行。
	svc.capMu.Lock()
	svc.capNextCalibrateAt = time.Time{}
	svc.capMu.Unlock()
	held, err := svc.reserveCacheBytes(1)
	if err != nil {
		t.Fatalf("reserve after backoff: %v, want 成功（校准成功）", err)
	}
	held.release()
}

// newCapacityTestServiceWithHits 同 newCapacityTestService，额外暴露上游命中
// 计数，用来断言「容量满时不多打上游」。
func newCapacityTestServiceWithHits(t *testing.T, capacity int64, size int) (*ProxyService, string, *atomic.Int32) {
	t.Helper()
	payload := bytes.Repeat([]byte("x"), size)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(upstream.Close)
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:   defaultCacheSubdir,
		cacheTTL:      time.Hour,
		capacityLimit: capacity,
		client:        upstream.Client(),
		allowURL:      func(u *url.URL) bool { return u.Host == parsed.Host },
	})
	return svc, upstream.URL, &hits
}

// 请求中途才被准入闸拒时不能丢掉已经打开的上游响应：把它交给调用方流式回
// 包，上游只被请求一次。容量满不是偶发而是稳态，二次 GET 会让媒体代理对上
// 游的请求数与连接数翻倍，抬高上游限流与延迟。
func TestFetchAndCacheCacheFullReusesUpstreamResponse(t *testing.T) {
	svc, upstream, hits := newCapacityTestServiceWithHits(t, 100, 64)

	// 先占掉 60 字节水位（未满），随后一次 64 字节的下载放不下。
	held, err := svc.reserveCacheBytes(60)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	held.settle(60)

	_, err = svc.FetchAndCache(context.Background(), upstream+"/big.png")
	if !errors.Is(err, ErrCacheFull) {
		t.Fatalf("err = %v, want ErrCacheFull", err)
	}
	resp, ok := PassthroughResponse(err)
	if !ok || resp == nil {
		t.Fatalf("PassthroughResponse = %v, want 可复用的上游响应", ok)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}
	if len(body) != 64 {
		t.Fatalf("body len = %d, want 64（复用已打开的上游响应）", len(body))
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1（不得二次请求）", got)
	}
}

// 水位已满时连回源都不该发：预检直接拒绝，调用方走直通只打一次上游。
func TestFetchAndCacheCacheFullPrecheckSkipsUpstream(t *testing.T) {
	svc, upstream, hits := newCapacityTestServiceWithHits(t, 100, 64)

	held, err := svc.reserveCacheBytes(100)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	held.settle(100)

	_, err = svc.FetchAndCache(context.Background(), upstream+"/any.png")
	if !errors.Is(err, ErrCacheFull) {
		t.Fatalf("err = %v, want ErrCacheFull", err)
	}
	if _, ok := PassthroughResponse(err); ok {
		t.Fatal("PassthroughResponse 非空, want 空（预检拒绝没有上游响应可复用）")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("upstream hits = %d, want 0（满容量时预检直接拒绝，不回源）", got)
	}
}

// 按 Content-Length 判定超限时响应体还没读，可以整份交回调用方直通：重新
// ProxyPass 会把同一个超限附件再下一次上游。
func TestFetchAndCacheMediaTooLargeReusesUpstreamResponse(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 64)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:       defaultCacheSubdir,
		cacheTTL:          time.Hour,
		maxCacheEntrySize: 8, // 上游 64 字节必超
		client:            upstream.Client(),
		allowURL:          func(u *url.URL) bool { return u.Host == parsed.Host },
	})

	_, err = svc.FetchAndCache(context.Background(), upstream.URL+"/big.mp4")
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("err = %v, want ErrMediaTooLarge", err)
	}
	resp, ok := PassthroughResponse(err)
	if !ok || resp == nil {
		t.Fatalf("PassthroughResponse = %v, want 可复用的上游响应", ok)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}
	if len(body) != 64 {
		t.Fatalf("body len = %d, want 64", len(body))
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1（不得二次请求）", got)
	}
}

// PassthroughError 的契约是「调用方负责关闭并回包」，只对媒体面生效。截图面
// 的调用方是 service 层，它只把错误往上抛——把打开的响应体交给它，连接与缓
// 冲永远没人关（泄漏）。截图面必须拿到裸错误，body 由 fetchAndCache 自己关。
func TestReviewFaceMediaTooLargeReturnsPlainError(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 64)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()
	svc := newService(t.TempDir(), serviceConfig{
		cacheSubdir:       "review-screenshot-cache",
		cacheTTL:          time.Hour,
		maxCacheEntrySize: 8,
		client:            upstream.Client(),
		allowURL:          allowPublicImageHost,
		requireHTTPS:      true,
		imagesOnly:        true,
	})

	_, err := svc.FetchAndCache(context.Background(), upstream.URL+"/big.png")
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("err = %v, want ErrMediaTooLarge", err)
	}
	if resp, ok := PassthroughResponse(err); ok {
		_ = resp.Body.Close()
		t.Fatal("PassthroughResponse 非空, want 空（截图面不交接响应体，调用方不会关）")
	}
}

// 清扫之后校准失败要留待重试：清扫已经把文件删了，水位却还按删之前算——
// 不重试的话媒体面会因为「反正水位满了」长期退化成直通。
func TestCapacityRecalibrateRetriesAfterFailure(t *testing.T) {
	svc, _ := newCapacityTestService(t, 100, 1)
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	held, err := svc.reserveCacheBytes(60)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	held.settle(60)

	if err := os.Chmod(svc.cacheDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			_ = os.Chmod(svc.cacheDir, 0o750)
		}
	}
	t.Cleanup(restore)
	if _, readErr := os.ReadDir(svc.cacheDir); readErr == nil {
		t.Skip("目录仍可读（以 root 运行？），无法模拟扫描失败")
	}

	svc.recalibrateCacheUsage() // 失败：置 pending
	restore()
	// 模拟退避窗口过去（真实实现里由 capacityCalibrateRetryInterval 控制）。
	svc.capMu.Lock()
	svc.capNextCalibrateAt = time.Time{}
	svc.capMu.Unlock()

	// 校准重试成功：目录实际为空，水位回到 0，再预留 60 不该被拒。
	next, err := svc.reserveCacheBytes(60)
	if err != nil {
		t.Fatalf("reserve after retry: %v, want 成功（清扫后重新校准，水位已释放）", err)
	}
	next.release()
}

// .gone 是清扫为压 stat→remove 窗口 rename 出来的隔离名：进程在 rename 后、
// remove 前被杀时留在盘上，要等 2×TTL 才收。不计入容量口径，实际占用就会在
// 回收前超过声明的上限。
func TestCacheDirBytesCountsGoneIsolationFiles(t *testing.T) {
	svc, _ := newCapacityTestService(t, 100, 1)
	if err := os.MkdirAll(svc.cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"a.bin.gone", "b.json.gone"} {
		if err := os.WriteFile(filepath.Join(svc.cacheDir, name), bytes.Repeat([]byte("x"), 40), 0o640); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	svc.recalibrateCacheUsage()

	// 水位 80：再要 30 会越过 100。
	if _, err := svc.reserveCacheBytes(30); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("reserve err = %v, want ErrCacheFull（隔离残留计入容量）", err)
	}
}

// 清扫末尾的校准要拿请求侧的同一把单飞锁：否则请求侧可能读到「清扫删除之前」
// 的快照、在清扫校准之后才写回，把已删字节重新计进水位——刚释放的额度白丢，
// 媒体面在下次清扫前一直拒缓存。
func TestRecalibrateCacheUsageSerializesWithAdmissionCalibration(t *testing.T) {
	svc, _ := newCapacityTestService(t, 100, 1)

	svc.capCalMu.Lock() // 模拟请求侧正在校准
	done := make(chan struct{})
	go func() {
		svc.recalibrateCacheUsage()
		close(done)
	}()
	select {
	case <-done:
		svc.capCalMu.Unlock()
		t.Fatal("recalibrate 未等待单飞锁, want 阻塞（扫描+赋值与请求侧校准串行）")
	case <-time.After(100 * time.Millisecond):
	}
	svc.capCalMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("放锁后 recalibrate 仍未完成")
	}
}
