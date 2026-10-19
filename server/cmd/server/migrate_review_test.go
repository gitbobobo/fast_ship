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
