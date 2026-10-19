package main

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
)

// 表里有行时默认拒绝启动（升级会不可逆地丢掉加密凭证）；显式设置
// dropUserIntegrationsEnv=1 后才整表删除；表不存在或为空时直接通过；幂等。
func TestMigrateDropIntegrations(t *testing.T) {
	t.Setenv(dropUserIntegrationsEnv, "")
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 表不存在：直接通过。
	if err := migrateDropIntegrations(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate on fresh db: %v", err)
	}

	// 造一张带索引的 integrations 表（B 轮前模型的样子，含指向 users 的外键）。
	if err := db.Exec(`CREATE TABLE integrations (
		id text PRIMARY KEY,
		user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		provider text NOT NULL,
		app_id text NOT NULL,
		config_encrypted blob NOT NULL,
		detection_status text NOT NULL,
		detection_detail text NOT NULL,
		detected_at datetime,
		created_at datetime NOT NULL,
		updated_at datetime NOT NULL
	)`).Error; err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_integrations_user_provider ON integrations(user_id, provider)`).Error; err != nil {
		t.Fatalf("seed index: %v", err)
	}
	// 外键开着：先落一个真实用户再插集成行。
	now := time.Now().UTC()
	if err := db.Create(&model.User{ID: "u-1", Username: "legacy", Email: "legacy@t", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Exec(`INSERT INTO integrations (id, user_id, provider, app_id, config_encrypted, detection_status, detection_detail, created_at, updated_at)
		VALUES ('i-1', 'u-1', 'github_app', '123', x'00', 'unchecked', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}

	if err := migrateDropIntegrations(db, zap.NewNop()); err == nil {
		t.Fatal("non-empty integrations table must refuse to migrate without opt-in")
	}
	if exists, err := sqliteTableExists(db, "integrations"); err != nil || !exists {
		t.Fatalf("refused migration must keep the table = %t %v", exists, err)
	}

	t.Setenv(dropUserIntegrationsEnv, "1")
	if err := migrateDropIntegrations(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate with opt-in: %v", err)
	}
	if exists, err := sqliteTableExists(db, "integrations"); err != nil || exists {
		t.Fatalf("integrations table still exists after migrate = %t %v", exists, err)
	}

	// 幂等：再跑一次无副作用。
	if err := migrateDropIntegrations(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
}

// 迁移不碰新身份的家：deployment_settings 照常读写。
func TestMigrateDropIntegrationsKeepsDeploymentSettings(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.DeploymentSettings{
		ID: model.DeploymentSettingsID, PublicURL: "https://fs.example.com",
		DetectionStatus: model.DeploymentDetectionUnchecked,
		CreatedAt:       now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed deployment settings: %v", err)
	}

	if err := migrateDropIntegrations(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var row model.DeploymentSettings
	if err := db.First(&row, "id = ?", model.DeploymentSettingsID).Error; err != nil {
		t.Fatalf("read deployment settings: %v", err)
	}
	if row.PublicURL != "https://fs.example.com" {
		t.Fatalf("public_url = %q", row.PublicURL)
	}
}
