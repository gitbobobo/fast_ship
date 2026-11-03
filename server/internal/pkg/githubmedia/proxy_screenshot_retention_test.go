package githubmedia

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// review 截图面的行账回收（INT-184）与命中续期（touchReviewEntry）的行为：
// 引用计数 + mtime 宽限判定、面护栏（媒体面 no-op）、孤儿 meta 回收、命中
// 刷新 mtime。

// seedReviewEntry 在 review 面缓存目录里落一个 .bin+.json 条目（PNG 魔数），
// 返回 .bin 路径。oldAge 非零时把两个文件的 mtime 拨回过去。
func seedReviewEntry(t *testing.T, svc *ProxyService, rawURL string, oldAge time.Duration) string {
	t.Helper()
	binPath := svc.dataPath(rawURL, "")
	metaPath := svc.metaPath(rawURL, "")
	if err := os.WriteFile(binPath, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	meta, err := json.Marshal(cacheEntry{
		URL: rawURL, ContentType: "image/png", Scope: "", Size: 11,
		CachedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(metaPath, meta, 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if oldAge > 0 {
		old := time.Now().UTC().Add(-oldAge)
		for _, p := range []string{binPath, metaPath} {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatalf("chtimes %s: %v", p, err)
			}
		}
	}
	return binPath
}

func TestSweepUnreferencedEntriesMediaFaceNoop(t *testing.T) {
	root := t.TempDir()
	svc := NewProxyService(root)
	bin := seedReviewEntry(t, svc, "https://example.com/a.png", 48*time.Hour)

	// 媒体面有自己的 TTL 回收：误调不得删它的条目。
	if removed := svc.SweepUnreferencedEntries(func(string) bool { return false }, 0, time.Now().UTC()); removed != 0 {
		t.Fatalf("removed = %d, want 0（媒体面 no-op）", removed)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("媒体面条目被删除: %v", err)
	}
}

func TestSweepUnreferencedEntriesReferenceAndGrace(t *testing.T) {
	root := t.TempDir()
	svc := NewReviewMediaService(root, "", nil, nil)
	referenced := seedReviewEntry(t, svc, "https://example.com/ref.png", 48*time.Hour)
	orphanOld := seedReviewEntry(t, svc, "https://example.com/old.png", 48*time.Hour)
	orphanFresh := seedReviewEntry(t, svc, "https://example.com/fresh.png", 0)

	removed := svc.SweepUnreferencedEntries(func(binPath string) bool {
		return binPath == referenced
	}, reviewFaceTestGrace, time.Now().UTC())
	if removed != 1 {
		t.Fatalf("removed = %d, want 1（只收超宽限且无引用的条目）", removed)
	}
	if _, err := os.Stat(referenced); err != nil {
		t.Fatalf("仍被引用的共享文件被删除: %v", err)
	}
	if _, err := os.Stat(orphanFresh); err != nil {
		t.Fatalf("宽限内的孤儿被删除: %v", err)
	}
	if _, err := os.Stat(orphanOld); !os.IsNotExist(err) {
		t.Fatalf("超宽限孤儿未回收: %v", err)
	}
	// 同名 meta 一并回收。
	if _, err := os.Stat(orphanOld[:len(orphanOld)-len(".bin")] + ".json"); !os.IsNotExist(err) {
		t.Fatalf("孤儿 meta 未回收: %v", err)
	}
	// 引用条目的 meta 不受牵连。
	if _, err := os.Stat(referenced[:len(referenced)-len(".bin")] + ".json"); err != nil {
		t.Fatalf("引用条目的 meta 被删除: %v", err)
	}
}

func TestSweepUnreferencedEntriesOrphanMeta(t *testing.T) {
	root := t.TempDir()
	svc := NewReviewMediaService(root, "", nil, nil)
	// 只有 meta 没有 data 的孤儿：超宽限回收，宽限内保留。
	orphan := svc.metaPath("https://example.com/lonely.png", "")
	if err := os.WriteFile(orphan, []byte(`{"url":"https://example.com/lonely.png"}`), 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	fresh := svc.metaPath("https://example.com/newborn.png", "")
	if err := os.WriteFile(fresh, []byte(`{"url":"https://example.com/newborn.png"}`), 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	removed := svc.SweepUnreferencedEntries(func(string) bool { return false }, reviewFaceTestGrace, time.Now().UTC())
	if removed != 1 {
		t.Fatalf("removed = %d, want 1（只收超宽限的孤儿 meta）", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("孤儿 meta 未回收: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("宽限内的孤儿 meta 被删除: %v", err)
	}
}

func TestSweepUnreferencedEntriesReentrant(t *testing.T) {
	root := t.TempDir()
	svc := NewReviewMediaService(root, "", nil, nil)
	seedReviewEntry(t, svc, "https://example.com/a.png", 48*time.Hour)
	seedReviewEntry(t, svc, "https://example.com/b.png", 48*time.Hour)

	// 中途被杀（第二轮只删得掉剩余条目）与连跑两轮都应收敛到同一终态。
	for i := 0; i < 2; i++ {
		svc.SweepUnreferencedEntries(func(string) bool { return false }, reviewFaceTestGrace, time.Now().UTC())
	}
	entries, err := os.ReadDir(filepath.Join(root, "review-screenshot-cache"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	// 目录里剩启动写的 scope 标记（.cache-scope-v2），不算条目。
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".bin") || strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".tmp") {
			t.Fatalf("残留条目 %q（两轮后应收敛干净）", n)
		}
	}
}

// 崩溃残留：写侧 rename data 后、WriteFile meta 前被杀留下的 meta 缺失
// .bin，与损坏 meta 的 .bin——没有闸 key 可持，按孤儿判据（新鲜 stat mtime
// 超宽限 + 删除前重问引用）兜底回收；宽限内的保留。
func TestSweepUnreferencedEntriesCrashResidue(t *testing.T) {
	root := t.TempDir()
	svc := NewReviewMediaService(root, "", nil, nil)
	dir := filepath.Join(root, "review-screenshot-cache")
	orphanOld := filepath.Join(dir, "residue.bin")
	orphanFresh := filepath.Join(dir, "newborn.bin")
	corruptOld := filepath.Join(dir, "corrupt.bin")
	for _, p := range []string{orphanOld, orphanFresh, corruptOld} {
		if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	for _, p := range []string{orphanOld, corruptOld} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}
	if err := os.Chtimes(filepath.Join(dir, "corrupt.json"), old, old); err != nil {
		t.Fatalf("chtimes meta: %v", err)
	}

	removed := svc.SweepUnreferencedEntries(func(string) bool { return false }, reviewFaceTestGrace, time.Now().UTC())
	if removed != 2 {
		t.Fatalf("removed = %d, want 2（崩溃残留与损坏 meta 两条按孤儿判据回收）", removed)
	}
	for _, p := range []string{orphanOld, corruptOld, filepath.Join(dir, "corrupt.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s 未回收: %v", p, err)
		}
	}
	if _, err := os.Stat(orphanFresh); err != nil {
		t.Fatalf("宽限内的新鲜残留被删除: %v", err)
	}
}

// 长下载持闸：同 URL 的下载进行中（闸被占）时扫描跳过该条目，下轮再扫；
// 释放闸后下一轮正常回收。
func TestSweepUnreferencedEntriesSkipsWhenGateHeld(t *testing.T) {
	root := t.TempDir()
	svc := NewReviewMediaService(root, "", nil, nil)
	url := "https://example.com/downloading.png"
	bin := seedReviewEntry(t, svc, url, 48*time.Hour)

	gate := svc.acquireDownloadLock(url)
	gate.sem <- struct{}{} // 模拟同 URL 下载进行中（fetchAndCache 持闸）
	if removed := svc.SweepUnreferencedEntries(func(string) bool { return false }, reviewFaceTestGrace, time.Now().UTC()); removed != 0 {
		t.Fatalf("removed = %d, want 0（持闸期间跳过）", removed)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("持闸期间条目被删除: %v", err)
	}
	// releaseDownloadLock 自带一次 token 消费，等价于释放上面的持有。
	svc.releaseDownloadLock(url, gate)

	if removed := svc.SweepUnreferencedEntries(func(string) bool { return false }, reviewFaceTestGrace, time.Now().UTC()); removed != 1 {
		t.Fatalf("removed = %d, want 1（释放闸后正常回收）", removed)
	}
}

// reviewFaceTestGrace 测试用的回收宽限：与宽限语义无关的固定小值，配合
// seedReviewEntry 的 48h 旧 mtime。
const reviewFaceTestGrace = time.Hour

// 命中续期（touchReviewEntry）：review 面命中刷新 .bin mtime——保留策略的
// 宽限判定依赖「行插入路径的 mtime 必新」；媒体面读路径不 touch。
func TestReviewFaceHitTouchesMtime(t *testing.T) {
	root := t.TempDir()
	svc := NewReviewMediaService(root, "", nil, nil)
	url := "https://example.com/touch.png"
	bin := seedReviewEntry(t, svc, url, 48*time.Hour)
	before, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// meta 完整的命中路径。
	if _, ok, err := svc.getCachedNormalized(url, ""); err != nil || !ok {
		t.Fatalf("getCachedNormalized = ok:%v err:%v, want 命中", ok, err)
	}
	after, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().After(before.ModTime()) {
		t.Fatalf("命中未刷新 mtime: before=%v after=%v", before.ModTime(), after.ModTime())
	}

	// meta 缺失的兜底命中路径（servePersistedData）。
	if err := os.Remove(svc.metaPath(url, "")); err != nil {
		t.Fatalf("remove meta: %v", err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := os.Chtimes(bin, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, ok, err := svc.getCachedNormalized(url, ""); err != nil || !ok {
		t.Fatalf("fallback hit = ok:%v err:%v, want 命中", ok, err)
	}
	after, err = os.Stat(bin)
	if err != nil {
		t.Fatalf("stat after fallback: %v", err)
	}
	if time.Since(after.ModTime()) > time.Minute {
		t.Fatalf("兜底命中未刷新 mtime: %v", after.ModTime())
	}
}

func TestMediaFaceHitDoesNotTouch(t *testing.T) {
	root := t.TempDir()
	svc := NewProxyService(root, WithCacheCapacity(1<<30))
	bin := seedReviewEntry(t, svc, "https://example.com/hot.png", 0)
	before, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if _, ok, err := svc.getCachedNormalized("https://example.com/hot.png", ""); err != nil || !ok {
		t.Fatalf("getCachedNormalized = ok:%v err:%v, want 命中", ok, err)
	}
	after, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("媒体面命中不应刷新 mtime: before=%v after=%v", before.ModTime(), after.ModTime())
	}
}
