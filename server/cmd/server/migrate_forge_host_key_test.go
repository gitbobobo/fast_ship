package main

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// host_key 回填与同归一主机唯一索引（INT-166 审查修复）：AutoMigrate 给存量库
// 加出的 NULL 列按 base_url 现算写回（幂等）；唯一索引建在回填后的数据上，
// 解析不出主机键的脏行写空串、被部分唯一索引排除，两条脏行共存不挡启动。
func TestMigrateForgeHostKeys(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ForgeIntegration{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	rows := []model.ForgeIntegration{
		{ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "a", BaseURL: "HTTPS://Forge.Example.com", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "b", BaseURL: "http://git.lan:3000/", CreatedAt: now, UpdatedAt: now},
		// 脏行：base_url 解析不出主机键。
		{ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "dirty", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 模拟存量库：AutoMigrate 加出的 host_key 列全为 NULL。
	if err := db.Exec("UPDATE forge_integrations SET host_key = NULL").Error; err != nil {
		t.Fatalf("reset host_key: %v", err)
	}

	if err := migrateForgeHostKeys(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 幂等：回填后没有 NULL/空串之外的目标行，重复启动无副作用（重复跑一遍
	// 不改值、不报错）。
	if err := migrateForgeHostKeys(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate again: %v", err)
	}

	for _, tc := range []struct{ name, want string }{
		{"a", "forge.example.com:443"},
		{"b", "git.lan:3000"},
		{"dirty", ""},
	} {
		var got model.ForgeIntegration
		if err := db.Where("name = ?", tc.name).First(&got).Error; err != nil {
			t.Fatalf("load %s: %v", tc.name, err)
		}
		if got.HostKey != tc.want {
			t.Errorf("%s host_key = %q, want %q", tc.name, got.HostKey, tc.want)
		}
	}

	// 索引建在回填后的数据上（与 runBootMigrations 同序）；空键脏行不挡建索引。
	createBootIndexes(db)
	dup := model.ForgeIntegration{
		ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "dup",
		BaseURL: "https://forge.example.com", HostKey: "forge.example.com:443",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&dup).Error; err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate host_key create err = %v, want UNIQUE constraint failed", err)
	}
	// 第二条空键脏行与 dirty 共存：部分唯一索引不拦空串。
	extra := model.ForgeIntegration{ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "dirty-2", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&extra).Error; err != nil {
		t.Fatalf("second empty-key row must coexist: %v", err)
	}
}
