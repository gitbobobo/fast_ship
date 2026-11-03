package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
)

// review 截图资产行账保留策略（INT-184）与探测落账/文件复核的行为测试。
// 下载桩的产物直接落在一个真实 githubmedia 服务的缓存目录里，扫描判定与
// 生产同源。

// retentionMedia 下载桩 + 真实回收面：缓存产物 shared.bin 落在 cacheDir
// （review 面的扫描目录）里，行账引用的就是这份磁盘。
func retentionMedia(t *testing.T, root string) *fakeReviewMedia {
	t.Helper()
	cacheDir := filepath.Join(root, "review-screenshot-cache")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	path := filepath.Join(cacheDir, "shared.bin")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatalf("write fake png: %v", err)
	}
	return &fakeReviewMedia{
		cached: githubmedia.CachedImage(path, "image/png", 15),
		sweep:  githubmedia.NewReviewMediaService(root, "", nil, nil),
	}
}

func retentionService(t *testing.T, root string) (*ReviewService, *fakeReviewMedia) {
	t.Helper()
	e := newReviewEngineEnv(t)
	media := retentionMedia(t, root)
	review := e.withT3(nil, media, nil)
	return review, media
}

func seedAsset(t *testing.T, review *ReviewService, integrationID, repo string, prNumber int, headSHA, url string, createdAt time.Time) *model.ReviewAsset {
	t.Helper()
	asset := &model.ReviewAsset{
		ID:            newAssetTestID(),
		IntegrationID: integrationID,
		RepoFullName:  repo,
		PRNumber:      prNumber,
		HeadSHA:       headSHA,
		Position:      0,
		Label:         "截图 1",
		SourceURL:     url,
		StoragePath:   review.media.(interface{ ShotPath() string }).ShotPath(),
		MimeType:      "image/png",
		SizeBytes:     15,
		CreatedAt:     createdAt,
	}
	if err := review.db.Create(asset).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	return asset
}

var assetTestSeq int

func newAssetTestID() string {
	assetTestSeq++
	return fmt.Sprintf("asset-test-%d-%d", assetTestSeq, time.Now().UTC().UnixNano())
}

// fakeReviewMedia 产物路径（桩恒返回同一文件，行账测试需要拿到它）。
func (f *fakeReviewMedia) ShotPath() string {
	if f.cached == nil {
		return ""
	}
	return f.cached.Path
}

// 验收核心用例：同一 storage_path 仍被别的 head 引用时不删。两个 head 的行
// 共享同一份文件，超龄行退役后文件必须还在；引用清零后下一轮回收。
func TestAssetRetentionSharedStoragePathNotDeleted(t *testing.T) {
	review, media := retentionService(t, t.TempDir())
	repo := "octo/example"
	now := time.Now().UTC()
	integrationID := model.ForgeIntegrationBuiltinGitHubID
	aged := now.Add(-31 * 24 * time.Hour)

	// head-a 的行超龄（将退役），head-b 的行新鲜（仍引用同一份文件）。
	seedAsset(t, review, integrationID, repo, 1, "sha-a", "https://img.example/a.png", aged)
	young := seedAsset(t, review, integrationID, repo, 1, "sha-b", "https://img.example/a.png", now)
	sharedPath := media.ShotPath()

	review.sweepAssetRetention(now)

	var count int64
	review.db.Model(&model.ReviewAsset{}).Where("repo_full_name = ?", repo).Count(&count)
	if count != 1 {
		t.Fatalf("rows = %d, want 1（超龄行退役，新鲜行保留）", count)
	}
	var kept model.ReviewAsset
	if err := review.db.Where("id = ?", young.ID).First(&kept).Error; err != nil {
		t.Fatalf("young row missing: %v", err)
	}
	if _, err := os.Stat(sharedPath); err != nil {
		t.Fatalf("共享 storage_path 仍被别的 head 引用时被删除: %v", err)
	}

	// 引用清零：宽限内（mtime 新，下载路径刚写过）不回收；mtime 超宽限后
	// 下一轮收走。
	review.db.Model(&model.ReviewAsset{}).Where("id = ?", young.ID).
		Update("created_at", aged)
	review.sweepAssetRetention(now.Add(time.Second))
	if _, err := os.Stat(sharedPath); err != nil {
		t.Fatalf("宽限内（mtime 新）的无引用文件被提前删除: %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(sharedPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	review.sweepAssetRetention(now.Add(2 * time.Second))
	if _, err := os.Stat(sharedPath); !os.IsNotExist(err) {
		t.Fatalf("引用清零后文件未回收: %v", err)
	}
	review.db.Model(&model.ReviewAsset{}).Where("repo_full_name = ?", repo).Count(&count)
	if count != 0 {
		t.Fatalf("rows = %d, want 0（退役后行清干净）", count)
	}
}

// 探测落账的字节转成普通行按年龄退役：「探测过但没有视觉运行」不永久遗留。
func TestAssetRetentionProbeOnlyBytesCollected(t *testing.T) {
	review, media := retentionService(t, t.TempDir())
	repo := "octo/example"
	now := time.Now().UTC()
	body := "![s](https://img.example/probe.png)"

	probe := review.probeAndRecordScreenshots(context.Background(),
		model.ForgeIntegrationBuiltinGitHubID, repo, 1, "sha-x", 1, body)
	if probe.result != probeReady {
		t.Fatalf("probe = %+v, want probeReady", probe)
	}
	sharedPath := media.ShotPath()

	// 探测即落账：文件有行引用，未超龄扫描不动它。
	review.sweepAssetRetention(now)
	if _, err := os.Stat(sharedPath); err != nil {
		t.Fatalf("探测落账的字节被提前回收: %v", err)
	}

	// 行超龄退役 → 文件宽限内保留，mtime 超宽限后回收（生产时序里行退役
	// 时文件已落盘 30 天，测试回拨 mtime 模拟）。
	review.db.Model(&model.ReviewAsset{}).Where("1=1").
		Update("created_at", now.Add(-31*24*time.Hour))
	review.sweepAssetRetention(now.Add(time.Second))
	var count int64
	review.db.Model(&model.ReviewAsset{}).Count(&count)
	if count != 0 {
		t.Fatalf("rows = %d, want 0", count)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(sharedPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	review.sweepAssetRetention(now.Add(2 * time.Second))
	if _, err := os.Stat(sharedPath); !os.IsNotExist(err) {
		t.Fatalf("探测后没有视觉运行的字节未在扫描周期内回收: %v", err)
	}
}

// 删除路径可重入：任意时点被杀（文件已删/meta 已删/两者都在），下一轮都
// 收敛，且重复扫描不报错、不误删仍被引用的条目。
func TestAssetRetentionSweepReentrant(t *testing.T) {
	review, media := retentionService(t, t.TempDir())
	repo := "octo/example"
	now := time.Now().UTC()
	integrationID := model.ForgeIntegrationBuiltinGitHubID
	aged := now.Add(-31 * 24 * time.Hour)

	keep := seedAsset(t, review, integrationID, repo, 1, "sha-keep", "https://img.example/keep.png", now)
	gone := seedAsset(t, review, integrationID, repo, 1, "sha-gone", "https://img.example/gone.png", aged)
	// 超龄行指向自己的文件（区别于新鲜行共享的 shared.bin）：验证只删超龄
	// 行自己的字节，不伤及仍被引用的条目。
	cacheDir := filepath.Dir(media.ShotPath())
	goneFile := filepath.Join(cacheDir, "gone.bin")
	if err := os.WriteFile(goneFile, []byte("gone"), 0o600); err != nil {
		t.Fatalf("write gone file: %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(goneFile, old, old); err != nil {
		t.Fatalf("chtimes gone file: %v", err)
	}
	review.db.Model(&model.ReviewAsset{}).Where("id = ?", gone.ID).
		Update("storage_path", goneFile)
	// 模拟「行已删、文件还在」的中间态残留：无行引用的孤儿文件。
	orphan := filepath.Join(cacheDir, "orphan.bin")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatalf("chtimes orphan: %v", err)
	}

	for i := 0; i < 3; i++ {
		review.sweepAssetRetention(now.Add(time.Duration(i) * time.Second))
	}
	if _, err := os.Stat(media.ShotPath()); err != nil {
		t.Fatalf("仍被引用的文件被误删: %v", err)
	}
	if _, err := os.Stat(goneFile); !os.IsNotExist(err) {
		t.Fatalf("超龄行自己的文件未回收: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("崩溃残留的孤儿文件未收敛: %v", err)
	}
	var goneRow model.ReviewAsset
	if err := review.db.Where("id = ?", gone.ID).First(&goneRow).Error; err == nil {
		t.Fatalf("超龄行未退役")
	}
	var keepRow model.ReviewAsset
	if err := review.db.Where("id = ?", keep.ID).First(&keepRow).Error; err != nil {
		t.Fatalf("新鲜行被误删: %v", err)
	}
}

// ensureAssets 复核：命中行的文件缺失时按失败重下并更新原行（删除竞态的
// 自愈路径）；文件在时不重下。
func TestEnsureAssetsReverifiesMissingFile(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, e.media, nil)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	body := "![a](https://img.example/rv.png)"
	missingPath := filepath.Join(t.TempDir(), "vanished.bin")

	seedAsset(t, review, model.ForgeIntegrationBuiltinGitHubID, repo, 81, "sha-a", "https://img.example/rv.png", time.Now().UTC())
	review.db.Model(&model.ReviewAsset{}).Where("1=1").
		Updates(map[string]any{"storage_path": missingPath})

	if _, _, err := review.ensureAssets(ctx, model.ForgeIntegrationBuiltinGitHubID, repo, 81, "sha-a", body, 0); err != nil {
		t.Fatalf("ensureAssets: %v", err)
	}
	if e.media.anonCalls != 1 {
		t.Fatalf("anonCalls = %d, want 1（缺失文件必须重下）", e.media.anonCalls)
	}
	var row model.ReviewAsset
	if err := review.db.Where("source_url = ?", "https://img.example/rv.png").First(&row).Error; err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if row.StoragePath != e.media.ShotPath() || row.Error != "" {
		t.Fatalf("row = %q/%q, want 重下后的路径与空 error", row.StoragePath, row.Error)
	}

	// 文件在的命中行不重下。
	before := e.media.anonCalls
	review.db.Model(&model.ReviewAsset{}).Where("source_url = ?", "https://img.example/rv.png").
		Update("storage_path", e.media.ShotPath())
	if _, _, err := review.ensureAssets(ctx, model.ForgeIntegrationBuiltinGitHubID, repo, 81, "sha-a", body, 0); err != nil {
		t.Fatalf("ensureAssets: %v", err)
	}
	if e.media.anonCalls != before {
		t.Fatalf("anonCalls = %d, want %d（文件在的命中行不应重下）", e.media.anonCalls, before)
	}

	// 非 ENOENT 的 Stat 错误（含 NUL 的非法路径回 EINVAL，不是 ErrNotExist）
	// 保持原行：不重下、不清路径——瞬时 I/O 误判缺失会把有效字节送进保留
	// 策略的回收（INT-184 审查意见）。
	before = e.media.anonCalls
	badPath := e.media.ShotPath() + "\x00bad"
	review.db.Model(&model.ReviewAsset{}).Where("source_url = ?", "https://img.example/rv.png").
		Update("storage_path", badPath)
	if _, _, err := review.ensureAssets(ctx, model.ForgeIntegrationBuiltinGitHubID, repo, 81, "sha-a", body, 0); err != nil {
		t.Fatalf("ensureAssets: %v", err)
	}
	if e.media.anonCalls != before {
		t.Fatalf("anonCalls = %d, want %d（非 ENOENT 错误不应触发重下）", e.media.anonCalls, before)
	}
	var kept model.ReviewAsset
	if err := review.db.Where("source_url = ?", "https://img.example/rv.png").First(&kept).Error; err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if kept.StoragePath != badPath || kept.Error != "" {
		t.Fatalf("非 ENOENT 错误改写了原行: %+v", kept)
	}
}

// 探测落账：成功引用写行（含 position/label）；已有失败行用本次结果修复；
// 已有成功行原样保留，不因探测重写。
func TestProbeRecordAssets(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, e.media, nil)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	// 预置：失败行（heal 目标）+ 成功行（不被改写）。
	failed := &model.ReviewAsset{
		ID: "probe-failed", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName: repo, PRNumber: 91, HeadSHA: "sha-p", Position: 0,
		SourceURL: "https://img.example/heal.png", Error: "下载超时",
		CreatedAt: now,
	}
	valid := &model.ReviewAsset{
		ID: "probe-valid", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName: repo, PRNumber: 91, HeadSHA: "sha-p", Position: 1,
		SourceURL: "https://img.example/valid.png", StoragePath: "/elsewhere/valid.bin",
		MimeType: "image/png", SizeBytes: 7, CreatedAt: now,
	}
	for _, a := range []*model.ReviewAsset{failed, valid} {
		if err := review.db.Create(a).Error; err != nil {
			t.Fatalf("seed %s: %v", a.ID, err)
		}
	}

	body := "![h](https://img.example/heal.png) ![v](https://img.example/valid.png) ![n](https://img.example/new.png)"
	probe := review.probeAndRecordScreenshots(context.Background(),
		model.ForgeIntegrationBuiltinGitHubID, repo, 91, "sha-p", 1, body)
	if probe.result != probeReady {
		t.Fatalf("probe = %+v, want probeReady", probe)
	}

	var healed model.ReviewAsset
	if err := review.db.Where("source_url = ?", "https://img.example/heal.png").First(&healed).Error; err != nil {
		t.Fatalf("heal row missing: %v", err)
	}
	if healed.StoragePath != e.media.ShotPath() || healed.Error != "" {
		t.Fatalf("失败行未修复: %q/%q", healed.StoragePath, healed.Error)
	}
	var untouched model.ReviewAsset
	if err := review.db.Where("source_url = ?", "https://img.example/valid.png").First(&untouched).Error; err != nil {
		t.Fatalf("valid row missing: %v", err)
	}
	if untouched.StoragePath != "/elsewhere/valid.bin" || untouched.SizeBytes != 7 {
		t.Fatalf("成功行被探测重写: %+v", untouched)
	}
	var created model.ReviewAsset
	if err := review.db.Where("source_url = ?", "https://img.example/new.png").First(&created).Error; err != nil {
		t.Fatalf("新引用未落账: %v", err)
	}
	if created.StoragePath != e.media.ShotPath() || created.Position != 2 || created.Label != "n" {
		t.Fatalf("新行字段不符: %+v", created)
	}
}

// 保留策略循环随 Start 启停（幂等 Start 不会叠加第二个循环）。
func TestAssetRetentionLoopStartStop(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, e.media, nil)
	review.Start()
	review.Start() // 幂等
	review.Stop()
	review.Stop() // 幂等
}
