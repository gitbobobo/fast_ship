package main

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 升级后旧自动化的通知 kind 已不合法，迁移要把这些行清掉（表本身保留）。
func TestMigrateReviewTablesPurgesLegacyNotifications(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Notification{}); err != nil {
		t.Fatalf("migrate notifications: %v", err)
	}
	now := time.Now().UTC()
	legacy := []model.Notification{
		{ID: "n1", UserID: "u1", Kind: "automation_run_timeout", DedupKey: "k1", StateFingerprint: "f", Title: "t", CreatedAt: now, UpdatedAt: now, LastSeenAt: now},
		{ID: "n2", UserID: "u1", Kind: "agent", DedupKey: "k2", StateFingerprint: "f", Title: "t", CreatedAt: now, UpdatedAt: now, LastSeenAt: now},
		{ID: "n3", UserID: "u1", Kind: model.NotificationReviewRunTimeout, DedupKey: "k3", StateFingerprint: "f", Title: "t", CreatedAt: now, UpdatedAt: now, LastSeenAt: now},
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed notifications: %v", err)
	}

	if err := migrateReviewTables(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var rows []model.Notification
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "n3" {
		t.Fatalf("剩余通知 = %+v, 只应保留审查故障行", rows)
	}
}

// 本 PR 新增的七张审查表尚未发布：结构演进一律 DROP 后由 AutoMigrate 按
// 最新模型重建——迁移必须把旧形态表与数据一起删掉。
func TestMigrateReviewTablesDropsNewReviewTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.ProjectReviewSettings{}, &model.ReviewRun{}, &model.ReviewRunToken{},
		&model.ReviewPullRequest{}, &model.ReviewAsset{}, &model.ReviewDebounce{},
		&model.ReviewSeenSHA{},
	); err != nil {
		t.Fatalf("migrate review tables: %v", err)
	}
	if err := db.Create(&model.ReviewSeenSHA{
		ProjectID: "p", RepoFullName: "o/r", PRNumber: 1, HeadSHA: "s",
		AcceptedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed seen sha: %v", err)
	}

	if err := migrateReviewTables(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{
		"review_runs", "review_run_tokens", "review_pull_requests", "review_assets",
		"review_debounces", "review_seen_shas", "project_review_settings",
	} {
		if exists, err := sqliteTableExists(db, table); err != nil || exists {
			t.Fatalf("表 %s 迁移后应已删除 (exists=%v err=%v)", table, exists, err)
		}
	}
}
