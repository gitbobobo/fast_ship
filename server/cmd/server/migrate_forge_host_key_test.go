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
	// createBootIndexes 会在 project_review_settings 上建唯一索引（同仓库占用
	// 键 INT-167 起带 integration_id），一并迁出来。
	if err := db.AutoMigrate(&model.ForgeIntegration{}, &model.ProjectReviewSettings{}); err != nil {
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

// host_key 索引谓词升级（R42/R43）：存量库的旧谓词缺 kind='forgejo'——
// sqlite_master 存稿比对出过期的 DROP 重建，当期定义直接跳过不留下
// 唯一约束失效窗口。
func TestForgeHostKeyIndexRebuild(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ForgeIntegration{}, &model.ProjectReviewSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC()
	seed := model.ForgeIntegration{
		ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "a",
		BaseURL: "https://forge.example.com", HostKey: "forge.example.com:443",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 模拟谓词升级前的存量索引：只有 host_key <> ''。
	if err := db.Exec("CREATE UNIQUE INDEX idx_forge_integrations_host_key ON forge_integrations(host_key) WHERE host_key <> ''").Error; err != nil {
		t.Fatalf("create stale index: %v", err)
	}
	if err := createBootIndexes(db); err != nil {
		t.Fatalf("rebuild stale index: %v", err)
	}
	indexSQL := func() string {
		var def string
		if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_forge_integrations_host_key'").Scan(&def).Error; err != nil {
			t.Fatalf("read index def: %v", err)
		}
		return def
	}
	if def := indexSQL(); !strings.Contains(def, "kind = 'forgejo'") {
		t.Fatalf("index not rebuilt, def = %q", def)
	}
	// 谓词已含 kind：未来其它 kind 的同主机行不被索引拦截（与 FindByHostKey
	// 过滤口径一致）。
	other := model.ForgeIntegration{
		ID: uuid.NewString(), Kind: "gitea", Name: "g",
		BaseURL: "https://forge.example.com", HostKey: "forge.example.com:443",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("non-forgejo same host_key must coexist: %v", err)
	}
	// 重复启动：当期定义跳过重建，幂等不报错、定义不变。
	if err := createBootIndexes(db); err != nil {
		t.Fatalf("second boot: %v", err)
	}
	if def := indexSQL(); !strings.Contains(def, "kind = 'forgejo'") {
		t.Fatalf("index def changed unexpectedly: %q", def)
	}
	// 谓词再次演进的形式：含子串但定义不同的索引必须被整段比对认出重建。
	if err := db.Exec("DROP INDEX idx_forge_integrations_host_key").Error; err != nil {
		t.Fatalf("drop for restale: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_forge_integrations_host_key ON forge_integrations(host_key) WHERE host_key <> '' AND kind = 'forgejo' AND name <> ''").Error; err != nil {
		t.Fatalf("create divergent index: %v", err)
	}
	if err := createBootIndexes(db); err != nil {
		t.Fatalf("rebuild divergent index: %v", err)
	}
	if def := indexSQL(); strings.Contains(def, "name <> ''") || !strings.Contains(def, "kind = 'forgejo'") {
		t.Fatalf("divergent index not rebuilt, def = %q", def)
	}
	// forgejo 同主机行仍被唯一索引拦截。
	dup := model.ForgeIntegration{
		ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "dup",
		BaseURL: "https://forge.example.com", HostKey: "forge.example.com:443",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&dup).Error; err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate forgejo host_key err = %v, want UNIQUE constraint failed", err)
	}
}

// github_host 回填（INT-166 R37 Note）：列上线前的存量行是 NULL，旧渲染语义
// 恒为公共 github.com——一次性收成 'github.com'；新写入的空串（=本部署
// GitHub）与已写的 'github.com' 都不能被动。
func TestMigrateProjectGithubHosts(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Project{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows := []model.Project{
		{ID: uuid.NewString(), UserID: "u", Name: "legacy"},
		{ID: uuid.NewString(), UserID: "u", Name: "bare-ghe", GithubHost: ""},
		{ID: uuid.NewString(), UserID: "u", Name: "public-gh", GithubHost: "github.com"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 模拟存量库：github_host 列上线前的行是 NULL（'' 的 bare-ghe 代表新写）。
	if err := db.Exec("UPDATE projects SET github_host = NULL WHERE name = 'legacy'").Error; err != nil {
		t.Fatalf("reset legacy: %v", err)
	}

	if err := migrateProjectGithubHosts(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrateProjectGithubHosts(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate again: %v", err)
	}

	for _, tc := range []struct{ name, want string }{
		{"legacy", "github.com"},
		{"bare-ghe", ""},
		{"public-gh", "github.com"},
	} {
		var got model.Project
		if err := db.Where("name = ?", tc.name).First(&got).Error; err != nil {
			t.Fatalf("load %s: %v", tc.name, err)
		}
		if got.GithubHost != tc.want {
			t.Errorf("%s github_host = %q, want %q", tc.name, got.GithubHost, tc.want)
		}
	}
}
