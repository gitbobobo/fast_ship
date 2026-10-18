package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-116 方案 B：config_revision 是连接配置（地址 + 凭证）的单调世代，hub 的
// entry 准入与删除 tombstone 都依赖它。这里锁仓库层语义：只在配对列变更时由
// SQL 自增、连续重配严格递增、改名不递增——updated_at 是墙钟时间、没有提交
// 顺序保证，不能当世代用。

func setupT3EnvironmentRepoTest(t *testing.T) (*T3EnvironmentRepository, *gorm.DB) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.T3Environment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewT3EnvironmentRepository(db), db
}

func TestT3EnvironmentRepositoryConfigRevision(t *testing.T) {
	repo, db := setupT3EnvironmentRepoTest(t)
	ctx := context.Background()

	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u-" + userID[:8], Email: userID[:8] + "@example.com", PasswordHash: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	now := time.Now().UTC()
	env := &model.T3Environment{
		ID:                   uuid.NewString(),
		UserID:               userID,
		Name:                 "机",
		Address:              "https://a.example",
		BearerTokenEncrypted: []byte("tok-0"),
		BearerExpiresAt:      now.Add(time.Hour),
		BearerScope:          "orchestration:read",
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := repo.Create(ctx, env); err != nil {
		t.Fatalf("create env: %v", err)
	}

	load := func() *model.T3Environment {
		t.Helper()
		var row model.T3Environment
		if err := db.Where("id = ?", env.ID).First(&row).Error; err != nil {
			t.Fatalf("load env: %v", err)
		}
		return &row
	}
	if got := load().ConfigRevision; got != 0 {
		t.Fatalf("fresh env config_revision = %d, want 0", got)
	}

	// 只改名：世代不变（「改名不产生新的配置世代」的仓库层证据）。
	newName := "机-2"
	if err := repo.Update(ctx, env.ID, userID, &T3EnvironmentUpdate{Name: &newName, UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if row := load(); row.ConfigRevision != 0 || row.Name != "机-2" {
		t.Fatalf("rename must not bump config_revision, got revision=%d name=%q", row.ConfigRevision, row.Name)
	}

	// 第一次配对 UPDATE：世代自增到 1，配对列随之覆盖。
	first := &T3PairingUpdate{
		Address:              "https://b.example",
		BearerTokenEncrypted: []byte("tok-1"),
		BearerExpiresAt:      now.Add(2 * time.Hour),
		BearerScope:          "orchestration:read",
	}
	if err := repo.Update(ctx, env.ID, userID, &T3EnvironmentUpdate{Pairing: first, UpdatedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("first pairing update: %v", err)
	}
	if row := load(); row.ConfigRevision != 1 || row.Address != "https://b.example" {
		t.Fatalf("first re-pair must bump config_revision to 1, got revision=%d address=%q", row.ConfigRevision, row.Address)
	}

	// 第二次配对 UPDATE：严格递增到 2（并发重配也各自 +1，单调由 SQL 自增保证）。
	second := &T3PairingUpdate{
		Address:              "https://c.example",
		BearerTokenEncrypted: []byte("tok-2"),
		BearerExpiresAt:      now.Add(3 * time.Hour),
		BearerScope:          "orchestration:read",
	}
	if err := repo.Update(ctx, env.ID, userID, &T3EnvironmentUpdate{Pairing: second, UpdatedAt: now.Add(3 * time.Second)}); err != nil {
		t.Fatalf("second pairing update: %v", err)
	}
	if row := load(); row.ConfigRevision != 2 || row.Address != "https://c.example" {
		t.Fatalf("second re-pair must bump config_revision to 2, got revision=%d address=%q", row.ConfigRevision, row.Address)
	}

	// 配对与改名合并提交：世代只随配对自增一次。
	mergedName := "机-3"
	third := &T3PairingUpdate{
		Address:              "https://d.example",
		BearerTokenEncrypted: []byte("tok-3"),
		BearerExpiresAt:      now.Add(4 * time.Hour),
		BearerScope:          "orchestration:read",
	}
	if err := repo.Update(ctx, env.ID, userID, &T3EnvironmentUpdate{Name: &mergedName, Pairing: third, UpdatedAt: now.Add(4 * time.Second)}); err != nil {
		t.Fatalf("merged update: %v", err)
	}
	if row := load(); row.ConfigRevision != 3 || row.Name != "机-3" {
		t.Fatalf("merged update must bump config_revision exactly once, got revision=%d name=%q", row.ConfigRevision, row.Name)
	}
}
