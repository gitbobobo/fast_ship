package main

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
// 迁移每次启动都会跑：已经是新结构时必须原地保留数据，不能每次重启清空。
func TestMigrateReviewTablesKeepsCurrentSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ProjectReviewSettings{}, &model.ReviewPullRequest{}, &model.ReviewSeenSHA{}); err != nil {
		t.Fatalf("migrate review tables: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.ReviewPullRequest{
		ProjectID: "p1", RepoFullName: "o/r", PRNumber: 1, UserID: "u1", UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := db.Create(&model.ProjectReviewSettings{
		ProjectID: "p1", UserID: "u1", RepoFullName: "o/r", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := migrateReviewTables(db, zap.NewNop()); err != nil {
			t.Fatalf("migrate #%d: %v", i, err)
		}
	}

	var ledgers, settings int64
	if err := db.Model(&model.ReviewPullRequest{}).Count(&ledgers).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if err := db.Model(&model.ProjectReviewSettings{}).Count(&settings).Error; err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if ledgers != 1 || settings != 1 {
		t.Fatalf("重启后数据被清空: ledgers=%d settings=%d", ledgers, settings)
	}
}

// 旧结构（主键缺 project_id）必须被识别并重建一次。
func TestReviewTablesNeedRebuildDetectsLegacySchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE review_pull_requests (
		repo_full_name text, pr_number integer, project_id text NOT NULL,
		user_id text NOT NULL, updated_at datetime NOT NULL,
		PRIMARY KEY (repo_full_name, pr_number))`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	needs, err := reviewTablesNeedRebuild(db)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !needs {
		t.Fatal("旧主键结构应触发重建")
	}
	if err := migrateReviewTables(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate legacy: %v", err)
	}
	exists, err := sqliteTableExists(db, "review_pull_requests")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Fatal("旧结构表应被删除，等待 AutoMigrate 重建")
	}
}

// 并发下载同一截图源时只能留一行：唯一索引 (repo,pr,head,source_url) + 冲突目标。
func TestReviewAssetsUniquePerSource(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ReviewAsset{}); err != nil {
		t.Fatalf("migrate assets: %v", err)
	}
	createBootIndexes(db)
	now := time.Now().UTC()
	insert := func(id string) {
		asset := &model.ReviewAsset{
			ID: id, RepoFullName: "o/r", PRNumber: 1, HeadSHA: "sha",
			Position: 0, Label: "截图 1", SourceURL: "https://example.com/a.png",
			CreatedAt: now,
		}
		err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "repo_full_name"}, {Name: "pr_number"},
				{Name: "head_sha"}, {Name: "source_url"},
			},
			DoNothing: true,
		}).Create(asset).Error
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("a1")
	insert("a2")
	var n int64
	if err := db.Model(&model.ReviewAsset{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("assets = %d, want 1（同一源地址只留一行）", n)
	}
}

// 删 automation_runs 前要把在途线程登记成墓碑，供新服务停线程/归档。
func TestMigrateReviewTablesKeepsLegacyThreadTombs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE automation_runs (
		id text PRIMARY KEY, user_id text, environment_id text, thread_id text,
		status text, finished_at datetime)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec(`INSERT INTO automation_runs (id,user_id,environment_id,thread_id,status,finished_at)
		VALUES ('r1','u1','e1','th-running','running',NULL),
		       ('r2','u1','e1','th-done','completed','2026-01-01 00:00:00'),
		       ('r3','u2','e2','th-dispatched','dispatched',NULL)`).Error; err != nil {
		t.Fatalf("seed legacy runs: %v", err)
	}

	if err := migrateReviewTables(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var tombs []model.LegacyAutomationThread
	if err := db.Order("thread_id ASC").Find(&tombs).Error; err != nil {
		t.Fatalf("load tombs: %v", err)
	}
	if len(tombs) != 2 || tombs[0].ThreadID != "th-dispatched" || tombs[1].ThreadID != "th-running" {
		t.Fatalf("tombs = %+v, want 仅在途的两条", tombs)
	}
}
