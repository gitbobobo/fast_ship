package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GitHubPRDebounceRepository 按验签身份分区的定向测试（INT-119）：部署 config
// App 与用户自己的 App 装在同一仓库、命中同一触发器时，两份身份的防抖窗口
// 必须各自成行、各自消费，互不覆盖。

func debounceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.GitHubPRDebounce{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedDebounce(t *testing.T, tx *gorm.DB, repo *GitHubPRDebounceRepository, triggerID, appUserID, sha string, fireAt time.Time) {
	t.Helper()
	err := repo.UpsertTx(tx, &model.GitHubPRDebounce{
		TriggerID: triggerID, RepoFullName: "octocat/hw", PRNumber: 1,
		AppUserID: appUserID, UserID: "rule-owner", ProjectID: "p", InstallationID: 7,
		Action: "synchronize", HeadSHA: sha, FireAt: fireAt, UpdatedAt: fireAt,
	})
	if err != nil {
		t.Fatalf("upsert debounce (%s): %v", appUserID, err)
	}
}

func TestPRDebounce_UpsertPartitionedByIdentity(t *testing.T) {
	db := debounceTestDB(t)
	repo := NewGitHubPRDebounceRepository(db)
	trigger := "trg-1"
	base := time.Now().UTC()

	// 同 trigger、同 repo+PR，两个身份各写一行 + config 身份（空串）一行。
	seedDebounce(t, db, repo, trigger, "user-a", "sha-a", base)
	seedDebounce(t, db, repo, trigger, "user-b", "sha-b", base.Add(time.Minute))
	seedDebounce(t, db, repo, trigger, "", "sha-cfg", base)

	var rows []model.GitHubPRDebounce
	if err := db.Where("trigger_id = ?", trigger).Order("app_user_id").Find(&rows).Error; err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d %v, want 3 (one per identity)", len(rows), err)
	}

	// 窗口内新推送：各自只推进自己的行（含 config 空串身份——主键零值也必须
	// 走更新而不是再插一行撞主键）。
	seedDebounce(t, db, repo, trigger, "user-a", "sha-a2", base.Add(2*time.Minute))
	seedDebounce(t, db, repo, trigger, "", "sha-cfg2", base.Add(2*time.Minute))
	if err := db.Where("trigger_id = ?", trigger).Find(&rows).Error; err != nil || len(rows) != 3 {
		t.Fatalf("rows after re-push = %d %v, want still 3", len(rows), err)
	}
	byUser := map[string]model.GitHubPRDebounce{}
	for _, r := range rows {
		byUser[r.AppUserID] = r
	}
	if byUser["user-a"].HeadSHA != "sha-a2" || byUser["user-b"].HeadSHA != "sha-b" || byUser[""].HeadSHA != "sha-cfg2" {
		t.Fatalf("sha per identity = a:%s b:%s cfg:%s, want a2/b/cfg2",
			byUser["user-a"].HeadSHA, byUser["user-b"].HeadSHA, byUser[""].HeadSHA)
	}
}

func TestPRDebounce_DeleteTxScopedToIdentity(t *testing.T) {
	db := debounceTestDB(t)
	repo := NewGitHubPRDebounceRepository(db)
	trigger := "trg-1"
	base := time.Now().UTC()
	seedDebounce(t, db, repo, trigger, "user-a", "sha-a", base)
	seedDebounce(t, db, repo, trigger, "user-b", "sha-b", base)

	// 只删 user-a 的窗口。
	if err := repo.DeleteTx(db, trigger, "octocat/hw", 1, "user-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var rows []model.GitHubPRDebounce
	if err := db.Where("trigger_id = ?", trigger).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("rows after delete = %d %v, want 1", len(rows), err)
	}
	if rows[0].AppUserID != "user-b" || rows[0].HeadSHA != "sha-b" {
		t.Fatalf("survivor = %s/%s, want user-b/sha-b", rows[0].AppUserID, rows[0].HeadSHA)
	}
}
