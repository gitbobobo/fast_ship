package main

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/glebarez/sqlite"
)

// setupLegacyPRIdentityDB 建一个 INT-93 形态的存量库：github_pr_reviews /
// github_pr_seen_shas / github_pr_debounces 均无 app_user_id 列，且已有数据行。
func setupLegacyPRIdentityDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// 单行 DDL：glebarez 驱动的 DDL 重解析把 tab 当分隔符，多行缩进会解析错位
	// （真实存量库也不会出现这种形态）。
	for _, ddl := range []string{
		"CREATE TABLE `github_pr_reviews` (`repo_full_name` text,`pr_number` integer,`installation_id` integer NOT NULL,`comment_id` integer,`round_number` integer NOT NULL,`head_sha` text NOT NULL,`sync_pending` numeric NOT NULL DEFAULT false,`sync_epoch` integer NOT NULL DEFAULT 0,`sync_error` text,`updated_at` datetime NOT NULL,PRIMARY KEY (`repo_full_name`,`pr_number`))",
		"CREATE TABLE `github_pr_seen_shas` (`repo_full_name` text,`pr_number` integer,`head_sha` text,`seen_at` datetime NOT NULL,PRIMARY KEY (`repo_full_name`,`pr_number`,`head_sha`))",
		"CREATE TABLE `github_pr_debounces` (`automation_id` text,`repo_full_name` text,`pr_number` integer,`user_id` text NOT NULL,`project_id` text NOT NULL,`installation_id` integer NOT NULL,`action` text NOT NULL,`head_sha` text NOT NULL,`pr_title` text,`pr_url` text,`base_ref` text,`head_ref` text,`fire_at` datetime NOT NULL,`updated_at` datetime NOT NULL,PRIMARY KEY (`automation_id`,`repo_full_name`,`pr_number`))",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("create legacy table: %v", err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Exec(`
		INSERT INTO github_pr_reviews (repo_full_name, pr_number, installation_id, comment_id, round_number, head_sha, sync_pending, sync_epoch, sync_error, updated_at)
		VALUES ('octocat/hw', 1, 7, 42, 2, 'sha-1', 1, 3, '', ?)
	`, now).Error; err != nil {
		t.Fatalf("insert legacy review: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO github_pr_seen_shas (repo_full_name, pr_number, head_sha, seen_at)
		VALUES ('octocat/hw', 1, 'sha-1', ?)
	`, now).Error; err != nil {
		t.Fatalf("insert legacy seen sha: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO github_pr_debounces (automation_id, repo_full_name, pr_number, user_id, project_id, installation_id, action, head_sha, fire_at, updated_at)
		VALUES ('a-1', 'octocat/hw', 1, 'u-1', 'p-1', 7, 'opened', 'sha-1', ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert legacy debounce: %v", err)
	}
	return db
}

// TestRunBootMigrations_PRReviewIdentityUpgrade INT-119 存量库升级：INT-93 的
// 评审账三表（无 app_user_id、有数据行）走完整启动入口后——旧行身份回填 ”
// （config 身份）、reviews/seen 的主键重建进 app_user_id（同 PR 可再插入用户
// 身份行、config 身份重复插入仍撞主键）；旧防抖表由 INT-118 的 drop 在身份
// 迁移之前丢弃并按触发器主键重建；重复启动幂等。
func TestRunBootMigrations_PRReviewIdentityUpgrade(t *testing.T) {
	db := setupLegacyPRIdentityDB(t)
	store := storage.NewLocalStorage(t.TempDir())
	key := []byte("12345678901234567890123456789012")

	if err := runBootMigrations(db, store, zap.NewNop(), false, "", key); err != nil {
		t.Fatalf("boot migrations: %v", err)
	}

	// 旧评审行回填 '' 且其余字段原样保留。
	var rev model.GitHubPRReview
	if err := db.Where("repo_full_name = ? AND pr_number = ?", "octocat/hw", 1).First(&rev).Error; err != nil {
		t.Fatalf("reload review: %v", err)
	}
	if rev.AppUserID != "" || rev.CommentID != 42 || rev.RoundNumber != 2 || rev.HeadSHA != "sha-1" ||
		rev.InstallationID != 7 || rev.SyncEpoch != 3 || !rev.SyncPending {
		t.Fatalf("legacy review not backfilled/preserved: %+v", rev)
	}
	// 旧 seen 行回填 ''。
	var seen model.GitHubPRSeenSHA
	if err := db.Where("repo_full_name = ? AND head_sha = ?", "octocat/hw", "sha-1").First(&seen).Error; err != nil {
		t.Fatalf("reload seen: %v", err)
	}
	if seen.AppUserID != "" {
		t.Fatalf("legacy seen app_user_id = %q, want ''", seen.AppUserID)
	}
	// 旧防抖行不升入新账：INT-118 的 dropLegacyPRDebounces 在身份迁移之前整表
	// 丢弃旧主键的防抖账本，AutoMigrate 按 (trigger_id, repo, pr) 重建空表并
	// 带 app_user_id 列（瞬态窗口不保留，后续推送会重新积累）。
	var debounceCount int64
	if err := db.Model(&model.GitHubPRDebounce{}).Count(&debounceCount).Error; err != nil {
		t.Fatalf("count debounces: %v", err)
	}
	if debounceCount != 0 {
		t.Fatalf("debounce rows = %d, want 0 (legacy ledger dropped)", debounceCount)
	}
	hasAppUserID, err := hasSQLiteColumn(db, "github_pr_debounces", "app_user_id")
	if err != nil || !hasAppUserID {
		t.Fatalf("debounce app_user_id column = %t %v, want present", hasAppUserID, err)
	}
	debouncePK := sqlitePrimaryKeyColumns(t, db, "github_pr_debounces")
	if len(debouncePK) != 3 || debouncePK[0] != "trigger_id" {
		t.Fatalf("debounce PK = %v, want trigger_id first", debouncePK)
	}

	// 主键已含 app_user_id：同 PR 的第二身份行可插入。
	if err := db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 1, AppUserID: "user-x",
		InstallationID: 8, RoundNumber: 1, HeadSHA: "sha-1", SyncPending: true, UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("second identity row for same PR rejected: %v", err)
	}
	// config 身份重复插入仍撞主键（旧行已在）。
	err = db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 1, AppUserID: "",
		InstallationID: 9, RoundNumber: 9, HeadSHA: "sha-x", SyncPending: true, UpdatedAt: time.Now().UTC(),
	}).Error
	if err == nil {
		t.Fatal("duplicate config identity row must violate the composite PK")
	}
	// seen 同理：同 SHA 的第二身份可插入。
	if err := db.Create(&model.GitHubPRSeenSHA{
		RepoFullName: "octocat/hw", PRNumber: 1, HeadSHA: "sha-1", AppUserID: "user-x",
		SeenAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("second identity seen row rejected: %v", err)
	}

	// 重复启动幂等：再次走完整迁移不报错、不产生重复行。
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", key); err != nil {
		t.Fatalf("second boot migrations: %v", err)
	}
	var n int64
	if err := db.Model(&model.GitHubPRReview{}).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("reviews after re-run = %d %v, want 2", n, err)
	}
	if err := migratePRReviewIdentity(db, zap.NewNop()); err != nil {
		t.Fatalf("migratePRReviewIdentity re-run: %v", err)
	}
}

// TestMigratePRReviewIdentity_IntermediateColumnForm 曾被补过列但主键未含
// app_user_id 的中间形态（例如零行旧表上 AutoMigrate 成功补列）也要重建。
func TestMigratePRReviewIdentity_IntermediateColumnForm(t *testing.T) {
	db := setupLegacyPRIdentityDB(t)
	// 清空数据、去掉主键重建为「带列但不在主键」的中间形态。
	if err := db.Exec("DROP TABLE github_pr_reviews").Error; err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := db.Exec("CREATE TABLE `github_pr_reviews` (`repo_full_name` text,`pr_number` integer,`app_user_id` text,`installation_id` integer NOT NULL,`comment_id` integer,`round_number` integer NOT NULL,`head_sha` text NOT NULL,`sync_pending` numeric NOT NULL DEFAULT false,`sync_epoch` integer NOT NULL DEFAULT 0,`sync_error` text,`updated_at` datetime NOT NULL,PRIMARY KEY (`repo_full_name`,`pr_number`))").Error; err != nil {
		t.Fatalf("create intermediate table: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Exec(`
		INSERT INTO github_pr_reviews (repo_full_name, pr_number, app_user_id, installation_id, comment_id, round_number, head_sha, sync_pending, sync_epoch, sync_error, updated_at)
		VALUES ('octocat/hw', 2, NULL, 7, 7, 1, 'sha-1', 0, 1, '', ?)
	`, now).Error; err != nil {
		t.Fatalf("insert intermediate row: %v", err)
	}

	if err := migratePRReviewIdentity(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var rev model.GitHubPRReview
	if err := db.Where("repo_full_name = ? AND pr_number = ?", "octocat/hw", 2).First(&rev).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	// 列存在但为 NULL：COALESCE 回填 ''。
	if rev.AppUserID != "" || rev.CommentID != 7 {
		t.Fatalf("intermediate row = %+v, want backfilled", rev)
	}
	if err := db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 2, AppUserID: "user-y",
		InstallationID: 1, RoundNumber: 1, HeadSHA: "sha-1", UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("second identity after rebuild: %v", err)
	}
}
