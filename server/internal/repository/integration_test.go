package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IntegrationRepository（INT-119 用户级）定向测试：复合唯一 upsert、按用户
// 取行、按 App ID 定位、过期检测结果丢弃。

func integrationTestRepo(t *testing.T) (*IntegrationRepository, *gorm.DB) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Integration{}, &model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewIntegrationRepository(db), db
}

func seedIntegrationRow(t *testing.T, db *gorm.DB, userID, provider, appID string, blob []byte) *model.Integration {
	t.Helper()
	now := time.Now().UTC()
	row := &model.Integration{
		ID: uuid.NewString(), UserID: userID, Provider: provider, AppID: appID,
		ConfigEncrypted: blob, DetectionStatus: model.IntegrationDetectionUnchecked,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	return row
}

// TestIntegrationRepo_UpsertScopedByUser (user_id, provider) 复合唯一：同一用户
// 覆盖自己那行，不同用户各存一行互不影响。
func TestIntegrationRepo_UpsertScopedByUser(t *testing.T) {
	repo, db := integrationTestRepo(t)

	upsert := func(userID, appID string, blob []byte) {
		now := time.Now().UTC()
		err := repo.Upsert(&model.Integration{
			ID: uuid.NewString(), UserID: userID, Provider: model.IntegrationProviderGitHubApp,
			AppID: appID, ConfigEncrypted: blob,
			DetectionStatus: model.IntegrationDetectionUnchecked,
			CreatedAt:       now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	upsert("u-1", "111", []byte("blob-1"))
	upsert("u-1", "222", []byte("blob-2"))
	upsert("u-2", "333", []byte("blob-3"))

	row, err := repo.GetByProvider("u-1", model.IntegrationProviderGitHubApp)
	if err != nil || row.AppID != "222" || string(row.ConfigEncrypted) != "blob-2" {
		t.Fatalf("u-1 row = %v %v %s", row, err, row.AppID)
	}
	var n int64
	db.Model(&model.Integration{}).Count(&n)
	if n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

// TestIntegrationRepo_FindAllByAppID 按明文 app_id 列出匹配行：(provider, app_id)
// 全库唯一后，github_app 下同一 App ID 至多一行（第二个用户插行直接撞唯一
// 索引）；其他 provider 的同 App ID 行互不影响；未知 App 返回空集。
func TestIntegrationRepo_FindAllByAppID(t *testing.T) {
	repo, db := integrationTestRepo(t)
	seedIntegrationRow(t, db, "u-1", model.IntegrationProviderGitHubApp, "111", []byte("a"))
	seedIntegrationRow(t, db, "u-2", "other_provider", "111", []byte("b"))

	// 唯一索引兜底：同 provider 下第二个用户插同一 App ID 被拒。
	err := db.Create(&model.Integration{
		ID: uuid.NewString(), UserID: "u-3", Provider: model.IntegrationProviderGitHubApp,
		AppID: "111", ConfigEncrypted: []byte("c"),
		DetectionStatus: model.IntegrationDetectionUnchecked,
		CreatedAt:       time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate app_id insert = %v, want UNIQUE constraint failure", err)
	}

	rows, err := repo.FindAllByAppID("111")
	if err != nil || len(rows) != 1 {
		t.Fatalf("find = %v %v, want 1 github_app row", rows, err)
	}
	if rows[0].UserID != "u-1" {
		t.Fatalf("row = %s, want u-1", rows[0].UserID)
	}
	if rows, err := repo.FindAllByAppID("404"); err != nil || len(rows) != 0 {
		t.Fatalf("missing = %v %v, want empty", rows, err)
	}
}

func TestIntegrationRepo_ListAppIDs(t *testing.T) {
	repo, db := integrationTestRepo(t)
	seedIntegrationRow(t, db, "u-1", model.IntegrationProviderGitHubApp, "222", []byte("a"))
	seedIntegrationRow(t, db, "u-2", model.IntegrationProviderGitHubApp, "111", []byte("b"))
	seedIntegrationRow(t, db, "u-3", "other_provider", "999", []byte("c"))

	ids, err := repo.ListAppIDs(model.IntegrationProviderGitHubApp)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want 2 github_app rows", ids)
	}
}

// TestIntegrationRepo_UpdateDetectionIfCurrent S2：旧密文条件不命中 → false 且
// 状态不变；新密文命中 → 写回检测三列且 updated_at 不变（它是配置保存时间）。
func TestIntegrationRepo_UpdateDetectionIfCurrent(t *testing.T) {
	repo, db := integrationTestRepo(t)
	row := seedIntegrationRow(t, db, "u-1", model.IntegrationProviderGitHubApp, "111", []byte("blob-1"))

	// 旧密文（配置已被再次保存）→ 条件不命中，什么都不写。
	ok, err := repo.UpdateDetectionIfCurrent("u-1", model.IntegrationProviderGitHubApp,
		[]byte("stale-blob"), model.IntegrationDetectionOK, "should be dropped", time.Now().UTC())
	if err != nil || ok {
		t.Fatalf("stale = %v %v, want (false, nil)", ok, err)
	}
	var fresh model.Integration
	if err := db.Where("id = ?", row.ID).First(&fresh).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.DetectionStatus != model.IntegrationDetectionUnchecked || fresh.DetectionDetail != "" || fresh.DetectedAt != nil {
		t.Fatalf("stale detection wrote through: %+v", fresh)
	}
	if !fresh.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("updated_at changed without write: %v → %v", row.UpdatedAt, fresh.UpdatedAt)
	}

	// 当前密文 → 命中，写检测三列，updated_at 不动。
	detectedAt := time.Now().UTC().Add(time.Minute)
	ok, err = repo.UpdateDetectionIfCurrent("u-1", model.IntegrationProviderGitHubApp,
		[]byte("blob-1"), model.IntegrationDetectionOK, "已连接 GitHub App", detectedAt)
	if err != nil || !ok {
		t.Fatalf("current = %v %v, want (true, nil)", ok, err)
	}
	if err := db.Where("id = ?", row.ID).First(&fresh).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.DetectionStatus != model.IntegrationDetectionOK || fresh.DetectedAt == nil {
		t.Fatalf("detection not persisted: %+v", fresh)
	}
	if !fresh.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("updated_at must stay the config save time: %v → %v", row.UpdatedAt, fresh.UpdatedAt)
	}
}
