package main

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 存量库全员 member：最早创建的用户被提为 admin；重复执行幂等。
func TestMigrateUserRolesPromotesEarliestUser(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour)
	createUserWithRole(t, db, "u-second", model.UserRoleMember, base.Add(time.Minute))
	createUserWithRole(t, db, "u-earliest", model.UserRoleMember, base)
	createUserWithRole(t, db, "u-third", model.UserRoleMember, base.Add(2*time.Minute))

	if err := migrateUserRoles(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate user roles: %v", err)
	}

	var earliest model.User
	if err := db.First(&earliest, "id = ?", "u-earliest").Error; err != nil {
		t.Fatalf("load earliest user: %v", err)
	}
	if earliest.Role != model.UserRoleAdmin {
		t.Fatalf("expected earliest user to become admin, got %q", earliest.Role)
	}

	// 幂等：再跑一次不改变结果。
	if err := migrateUserRoles(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate user roles again: %v", err)
	}
	var roles []model.User
	if err := db.Order("id ASC").Find(&roles).Error; err != nil {
		t.Fatalf("load users: %v", err)
	}
	admins := 0
	for _, u := range roles {
		if u.Role == model.UserRoleAdmin {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("expected exactly 1 admin after re-run, got %d", admins)
	}
}

// 已有 admin 的存量库不改动（幂等：不是每次启动都重选最早用户）。
func TestMigrateUserRolesKeepsExistingAdmin(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour)
	createUserWithRole(t, db, "u-later", model.UserRoleMember, base)
	createUserWithRole(t, db, "u-admin", model.UserRoleAdmin, base.Add(time.Minute))

	if err := migrateUserRoles(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate user roles: %v", err)
	}

	var later model.User
	if err := db.First(&later, "id = ?", "u-later").Error; err != nil {
		t.Fatalf("load member user: %v", err)
	}
	if later.Role != model.UserRoleMember {
		t.Fatalf("expected member user untouched, got %q", later.Role)
	}
}

// 全新空库是 no-op：首个注册用户由注册路径授 admin，迁移不造人。
func TestMigrateUserRolesEmptyDatabase(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := migrateUserRoles(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate user roles on empty db: %v", err)
	}
	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no users created, got %d", count)
	}
}

func createUserWithRole(t *testing.T, db *gorm.DB, id, role string, createdAt time.Time) {
	t.Helper()
	if err := db.Create(&model.User{
		ID:           id,
		Username:     "user_" + id,
		Email:        id + "@example.com",
		PasswordHash: "x",
		Role:         role,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}).Error; err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}
