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

func retentionService(t *testing.T, root string) (*ReviewService, *fakeReviewMedia, *reviewEngineEnv) {
	t.Helper()
	e := newReviewEngineEnv(t)
	media := retentionMedia(t, root)
	review := e.withT3(nil, media, nil)
	return review, media, e
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
	review, media, _ := retentionService(t, t.TempDir())
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
		Update("renewed_at", aged)
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

// 退役守卫：head 上仍有非终态运行时超龄行不退役（重审超龄 head 不能让在
// 途运行丢行与文件）；运行进终态后下一轮恢复退役。
func TestAssetRetentionSkipsHeadsWithActiveRuns(t *testing.T) {
	review, media, e := retentionService(t, t.TempDir())
	repo := "octo/example"
	now := time.Now().UTC()
	integrationID := model.ForgeIntegrationBuiltinGitHubID
	aged := now.Add(-31 * 24 * time.Hour)

	// 超龄行 + 该 head 一个 running 运行：扫描不退役。
	active := seedAsset(t, review, integrationID, repo, 1, "sha-live", "https://img.example/active.png", aged)
	e.seedRun(t, repo, 1, "sha-live", model.ReviewRunRunning, now)
	// 同仓库同 PR 另一个超龄 head 无运行：照常退役。
	idle := seedAsset(t, review, integrationID, repo, 1, "sha-idle", "https://img.example/idle.png", aged)
	idleFile := filepath.Join(filepath.Dir(media.ShotPath()), "idle.bin")
	if err := os.WriteFile(idleFile, []byte("idle"), 0o600); err != nil {
		t.Fatalf("write idle file: %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(idleFile, old, old); err != nil {
		t.Fatalf("chtimes idle file: %v", err)
	}
	review.db.Model(&model.ReviewAsset{}).Where("id = ?", idle.ID).
		Update("storage_path", idleFile)

	review.sweepAssetRetention(now)
	var count int64
	review.db.Model(&model.ReviewAsset{}).Where("id = ?", active.ID).Count(&count)
	if count != 1 {
		t.Fatalf("非终态运行在身的超龄行被退役")
	}
	if _, err := os.Stat(media.ShotPath()); err != nil {
		t.Fatalf("在途运行引用的文件被回收: %v", err)
	}
	review.db.Model(&model.ReviewAsset{}).Where("id = ?", idle.ID).Count(&count)
	if count != 0 {
		t.Fatalf("无运行的超龄行未退役")
	}
	// 引用集合先于退役快照 + 退役即续期：idle 文件宽限内保留，扫到宽限外
	// 才收（在途运行与 keep 行不受影响）。
	review.sweepAssetRetention(now.Add(25 * time.Hour))
	if _, err := os.Stat(idleFile); !os.IsNotExist(err) {
		t.Fatalf("无运行 head 的文件未回收: %v", err)
	}
	if _, err := os.Stat(media.ShotPath()); err != nil {
		t.Fatalf("在途运行引用的文件被回收: %v", err)
	}

	// 运行进终态后恢复退役（探测落账语义：无视觉运行的材料不永久遗留）。
	review.db.Model(&model.ReviewRun{}).Where("head_sha = ?", "sha-live").
		Update("status", model.ReviewRunCompleted)
	review.sweepAssetRetention(now.Add(time.Second))
	review.db.Model(&model.ReviewAsset{}).Where("id = ?", active.ID).Count(&count)
	if count != 0 {
		t.Fatalf("终态后超龄行未退役")
	}
}

// 分批退役：批量上限小于待退役行数时循环删到收敛（多批终止条件）。
func TestAssetRetentionRetireBatches(t *testing.T) {
	review, media, _ := retentionService(t, t.TempDir())
	repo := "octo/example"
	now := time.Now().UTC()
	integrationID := model.ForgeIntegrationBuiltinGitHubID
	aged := now.Add(-31 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		seedAsset(t, review, integrationID, repo, 1, fmt.Sprintf("sha-batch-%d", i), fmt.Sprintf("https://img.example/batch-%d", i), aged)
	}

	orig := reviewAssetRetireBatch
	reviewAssetRetireBatch = 2
	defer func() { reviewAssetRetireBatch = orig }()
	// 行的 created_at 回拨模拟超龄；文件 mtime 同步回拨（生产时序里行退役
	// 时文件已落盘 30 天），否则宽限会正确地保住新鲜文件。
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(media.ShotPath(), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	review.sweepAssetRetention(now)

	var count int64
	review.db.Model(&model.ReviewAsset{}).Where("created_at < ?", now.Add(-reviewAssetRetention)).Count(&count)
	if count != 0 {
		t.Fatalf("count = %d, want 0（多批循环删到收敛，实际剩 %d 行）", count, count)
	}
	// 引用集合先于退役快照 + 退役即续期：释放的字节在下一轮（宽限内）保留，
	// 扫到宽限外才消失。
	review.sweepAssetRetention(now.Add(time.Second))
	if _, err := os.Stat(media.ShotPath()); err != nil {
		t.Fatalf("退役即续期的字节被提前回收: %v", err)
	}
	review.sweepAssetRetention(now.Add(25 * time.Hour))
	if _, err := os.Stat(media.ShotPath()); !os.IsNotExist(err) {
		t.Fatalf("退役释放的字节未在宽限外回收: %v", err)
	}
}

// 探测落账的字节转成普通行按年龄退役：「探测过但没有视觉运行」不永久遗留。
func TestAssetRetentionProbeOnlyBytesCollected(t *testing.T) {
	review, media, _ := retentionService(t, t.TempDir())
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
	// 超龄拨的是保留期时钟 renewed_at（R11：created_at 已不再随落账刷新，
	// 退役截止读 renewed_at）。
	review.db.Model(&model.ReviewAsset{}).Where("1=1").
		Update("renewed_at", now.Add(-31*24*time.Hour))
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
	review, media, _ := retentionService(t, t.TempDir())
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
		review.sweepAssetRetention(now.Add(time.Duration(i) * 25 * time.Hour))
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
	seededCreatedAt := time.Now().UTC().Add(-time.Hour)
	review.db.Model(&model.ReviewAsset{}).Where("1=1").
		Updates(map[string]any{"storage_path": missingPath, "created_at": seededCreatedAt})

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
	// 补账即落账：重下成功刷新 renewed_at（保留期时钟，R6）；created_at 保
	// 持首次落账时刻——AgentScreenshots 拿它判「本轮新行」，被刷新会把重下
	// 的旧行误剔出上一轮集合、changed 误报 true（R11 审查意见）。
	if row.RenewedAt == nil || row.RenewedAt.Before(time.Now().Add(-24*time.Hour)) {
		t.Fatalf("重下成功未刷新 renewed_at: %v", row.RenewedAt)
	}
	if !row.CreatedAt.Equal(seededCreatedAt) {
		t.Fatalf("重下成功不应刷新 created_at: got %v, want %v", row.CreatedAt, seededCreatedAt)
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
		CreatedAt: now.Add(-31 * 24 * time.Hour),
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
	// 修复失败行同样刷新保留期时钟（补账即落账）；created_at 不动（R11）。
	if healed.RenewedAt == nil || healed.RenewedAt.Before(time.Now().Add(-24*time.Hour)) {
		t.Fatalf("修复成功未刷新 renewed_at: %v", healed.RenewedAt)
	}
	if healed.CreatedAt.After(time.Now().Add(-24 * time.Hour)) {
		t.Fatalf("修复成功不应刷新 created_at: %v", healed.CreatedAt)
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

// 同源自检限量（R7）：快照与缓存目录失配的三个档位（快照为空 / 路径全在
// 目录外 / 目录候选远大于快照）都进入限量——每轮最多
// reviewAssetSweepCalmBatch 个文件，剩余的留给运维介入或下一轮，不再一次
// 清空「仍可能有归属」的历史截图。

// seedOrphans 在缓存目录里落 count 个超宽限的孤儿 .bin，返回路径列表。
func seedOrphans(t *testing.T, cacheDir string, count int, now time.Time) []string {
	t.Helper()
	old := now.Add(-48 * time.Hour)
	var out []string
	for i := 0; i < count; i++ {
		p := filepath.Join(cacheDir, fmt.Sprintf("orphan-%02d.bin", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write orphan: %v", err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes orphan: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func countBinFiles(t *testing.T, cacheDir string) int {
	t.Helper()
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".bin" {
			n++
		}
	}
	return n
}

func TestAssetRetentionCalmModeEmptySnapshot(t *testing.T) {
	review, media, _ := retentionService(t, t.TempDir())
	now := time.Now().UTC()
	cacheDir := filepath.Dir(media.ShotPath())
	seedOrphans(t, cacheDir, 28, now)
	// shared.bin 是下载桩在 setup 时落的（mtime 新鲜、无行引用）：一并做旧，
	// 让它进入本轮孤儿判定——快照为空档不豁免任何文件。
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(media.ShotPath(), old, old); err != nil {
		t.Fatalf("chtimes shared: %v", err)
	}

	// 不种任何行：快照为空而目录有条目 → 限量，本轮最多 25 个。目录里的
	// .bin = 28 孤儿 + 下载桩产物 shared.bin（无行引用，同为孤儿），全部
	// 已做旧。断言用操作前的计数，不用 sweep 后的（否则恒真，R8 审查意见）。
	before := countBinFiles(t, cacheDir)
	if before != 29 {
		t.Fatalf("fixture .bin = %d, want 29（防 seed 漂移）", before)
	}
	review.sweepAssetRetention(now)
	if got := countBinFiles(t, cacheDir); got != before-reviewAssetSweepCalmBatch {
		t.Fatalf("remain = %d, want %d（限量档：仅回收 budget 上限）", got, before-reviewAssetSweepCalmBatch)
	}
	// 下一轮继续按限量滴漏收敛（剩余 4 ≤ 预算 25，全部收走）。
	review.sweepAssetRetention(now.Add(time.Second))
	if got := countBinFiles(t, cacheDir); got != 0 {
		t.Fatalf("remain = %d, want 0（下轮继续限量）", got)
	}
}

func TestAssetRetentionCalmModePathsOutsideCacheDir(t *testing.T) {
	review, media, e := retentionService(t, t.TempDir())
	repo := "octo/example"
	now := time.Now().UTC()
	integrationID := model.ForgeIntegrationBuiltinGitHubID
	cacheDir := filepath.Dir(media.ShotPath())
	seedOrphans(t, cacheDir, 28, now)

	// 引用行存在但 storage_path 在缓存目录之外（存储迁移特征）：under==0
	// 档 → 限量。
	row := &model.ReviewAsset{
		ID: "outside-ref", IntegrationID: integrationID, RepoFullName: repo,
		PRNumber: 1, HeadSHA: "sha-out", SourceURL: "https://img.example/out",
		StoragePath: "/elsewhere/outside.bin", CreatedAt: now,
	}
	if err := review.db.Create(row).Error; err != nil {
		t.Fatalf("seed outside row: %v", err)
	}

	// 目录 .bin = 28 孤儿 + 下载桩产物 shared.bin（无行引用，同为孤儿）；
	// 引用行指向目录外，不参与计数。断言用操作前的计数（R8 审查意见）。
	before := countBinFiles(t, cacheDir)
	if before != 29 {
		t.Fatalf("fixture .bin = %d, want 29（防 seed 漂移）", before)
	}
	review.sweepAssetRetention(now)
	if got := countBinFiles(t, cacheDir); got != before-reviewAssetSweepCalmBatch {
		t.Fatalf("remain = %d, want %d（路径全在目录外档限量）", got, before-reviewAssetSweepCalmBatch)
	}
	_ = e
}

func TestAssetRetentionCalmModeRatioCap(t *testing.T) {
	review, media, _ := retentionService(t, t.TempDir())
	repo := "octo/example"
	now := time.Now().UTC()
	integrationID := model.ForgeIntegrationBuiltinGitHubID
	cacheDir := filepath.Dir(media.ShotPath())
	orphans := seedOrphans(t, cacheDir, 28, now)

	// 引用行在本目录内（快照非空且 under>0），但目录候选数（29）远大于快
	// 照规模（1）→ 比例档 → 限量。
	seedAsset(t, review, integrationID, repo, 1, "sha-r", "https://img.example/ratio.png", now)

	review.sweepAssetRetention(now)
	if got := countBinFiles(t, cacheDir); got != len(orphans)-reviewAssetSweepCalmBatch+1 {
		t.Fatalf("remain = %d, want %d（比例档限量，引用文件保留）", got, len(orphans)-reviewAssetSweepCalmBatch+1)
	}
	// 被引用的文件不在限量里，仍保留。
	if _, err := os.Stat(media.ShotPath()); err != nil {
		t.Fatalf("被引用的文件被误删: %v", err)
	}
}

// 命中行重下失败不清既有引用（R12 审查意见）：ENOENT 也可能来自 uploads
// 卷瞬时不可用——storage_path 被刷成空串后，卷恢复的文件会变成无引用孤儿
// 被 sweep 永久删除。失败只刷 error，重下成功才整行落账。
func TestEnsureAssetsRetryFailureKeepsPath(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	media := &fakeReviewMedia{
		cached:  &githubmedia.CachedMedia{Path: "/cache/keep.png"},
		anonErr: &githubmedia.UpstreamStatusError{StatusCode: 503},
	}
	review := e.withT3(nil, media, nil)
	body := "![a](https://img.example/keep.png)"

	seededAt := time.Now().UTC().Add(-time.Hour)
	row := &model.ReviewAsset{
		ID: "keep-ref", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName: repo, PRNumber: 1, HeadSHA: "sha-keep",
		Position: 0, Label: "a", SourceURL: "https://img.example/keep.png",
		StoragePath: "/elsewhere/vanished.bin", MimeType: "image/png", SizeBytes: 9,
		CreatedAt: seededAt, RenewedAt: &seededAt,
	}
	if err := review.db.Create(row).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}

	if _, _, err := review.ensureAssets(ctx, model.ForgeIntegrationBuiltinGitHubID, repo, 1, "sha-keep", body, 0); err != nil {
		t.Fatalf("ensureAssets: %v", err)
	}
	var got model.ReviewAsset
	if err := review.db.Where("source_url = ?", "https://img.example/keep.png").First(&got).Error; err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if got.StoragePath != "/elsewhere/vanished.bin" {
		t.Fatalf("重下失败清掉了既有引用: storage_path = %q", got.StoragePath)
	}
	if got.Error == "" {
		t.Fatal("重下失败应留下 error")
	}
	if !got.CreatedAt.Equal(seededAt) || got.RenewedAt == nil || !got.RenewedAt.Equal(seededAt) {
		t.Fatalf("失败不应动时钟: created_at=%v renewed_at=%v", got.CreatedAt, got.RenewedAt)
	}

	// 卷恢复（重下成功）→ 整行落账 + 续期。
	media.anonErr = nil
	if _, _, err := review.ensureAssets(ctx, model.ForgeIntegrationBuiltinGitHubID, repo, 1, "sha-keep", body, 0); err != nil {
		t.Fatalf("ensureAssets retry: %v", err)
	}
	if err := review.db.Where("source_url = ?", "https://img.example/keep.png").First(&got).Error; err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if got.StoragePath != media.ShotPath() || got.Error != "" {
		t.Fatalf("重下成功未落账: %q/%q", got.StoragePath, got.Error)
	}
	if got.RenewedAt == nil || got.RenewedAt.Before(time.Now().Add(-24*time.Hour)) {
		t.Fatalf("重下成功未续期: %v", got.RenewedAt)
	}
}
