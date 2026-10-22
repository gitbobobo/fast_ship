package main

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 空 nonce 的存量行被补齐（32 位 hex）；已有 nonce 的行不被改写；
// 重复执行无变化（幂等）。
func TestMigrateBotUntrustedNonceBackfillsEmpty(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	createMigrationUser(t, db, "u-nonce")
	createMigrationBot(t, db, "b-empty", "u-nonce", "")
	createMigrationBot(t, db, "b-empty-2", "u-nonce", "")
	createMigrationBot(t, db, "b-keep", "u-nonce", "abc123")

	if err := migrateBotUntrustedNonce(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate nonce: %v", err)
	}

	for _, id := range []string{"b-empty", "b-empty-2"} {
		var b model.Bot
		if err := db.First(&b, "id = ?", id).Error; err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if len(b.UntrustedNonce) != 32 {
			t.Fatalf("%s nonce 应为 32 位 hex: %q", id, b.UntrustedNonce)
		}
		for _, r := range b.UntrustedNonce {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				t.Fatalf("%s nonce 含非 hex 字符: %q", id, b.UntrustedNonce)
			}
		}
	}
	var keep model.Bot
	if err := db.First(&keep, "id = ?", "b-keep").Error; err != nil {
		t.Fatalf("load b-keep: %v", err)
	}
	if keep.UntrustedNonce != "abc123" {
		t.Fatalf("已有 nonce 不得改写: %q", keep.UntrustedNonce)
	}

	// 幂等：再跑一次所有 nonce 不变。
	first := map[string]string{}
	var bots []model.Bot
	if err := db.Find(&bots).Error; err != nil {
		t.Fatalf("load bots: %v", err)
	}
	for _, b := range bots {
		first[b.ID] = b.UntrustedNonce
	}
	if err := migrateBotUntrustedNonce(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate nonce again: %v", err)
	}
	if err := db.Find(&bots).Error; err != nil {
		t.Fatalf("reload bots: %v", err)
	}
	for _, b := range bots {
		if b.UntrustedNonce != first[b.ID] {
			t.Fatalf("%s nonce 二次执行被改写: %q → %q", b.ID, first[b.ID], b.UntrustedNonce)
		}
	}
}

// 全新空库是 no-op。
func TestMigrateBotUntrustedNonceEmptyDatabase(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrateBotUntrustedNonce(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate nonce on empty db: %v", err)
	}
}

func createMigrationBot(t *testing.T, db *gorm.DB, id, userID, nonce string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&model.Bot{
		ID: id, UserID: userID, Name: "b_" + id, Model: "m",
		UntrustedNonce: nonce,
		CreatedAt:      now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create bot %s: %v", id, err)
	}
}
