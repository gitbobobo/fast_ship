package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AI 提供商迁移（INT-133）：按旧 bots 形状（base_url + 密钥列、无 provider_id）
// 建表并写入存量行，走 runBootMigrations 完整启动迁移，断言 provider 去重聚合、
// 目录归属判定、provider_id 回填与旧列删除；第二次启动检测不到旧列即整体跳过。
//
// 旧表用影子模型 AutoMigrate 建（而非手写 DDL）：与 migrate_automation_triggers
// 同一约定——影子模型生成的 DDL 与上一发布版本逐字节一致，才代表真实存量库。

// legacyBot INT-133 之前的 bots 模型投影：地址与密钥三列直接落在 bot 行上。
type legacyBot struct {
	ID            string `gorm:"type:text;primaryKey" json:"id"`
	UserID        string `gorm:"type:text;not null" json:"user_id"`
	Name          string `gorm:"type:text;not null" json:"name"`
	PersonaPrompt string `gorm:"type:text;not null" json:"persona_prompt"`
	// SystemPromptText 是已拼好的人设与平台规则全文。
	SystemPromptText      string `gorm:"type:text;not null" json:"-"`
	PromptRevision        int64  `gorm:"not null" json:"prompt_revision"`
	PlatformPromptVersion int64  `gorm:"not null" json:"platform_prompt_version"`
	ToolsetVersion        int64  `gorm:"not null" json:"toolset_version"`
	LastToldVersion       int64  `gorm:"not null;default:1" json:"last_told_version"`

	BaseURL string `gorm:"type:text;not null" json:"base_url"`
	// Model 列存模型名；列名 model 不是 SQLite 保留字，GORM 会加引号。
	Model string `gorm:"column:model;type:text;not null" json:"model"`
	// APIKeyEncrypted 密文（AES-GCM）。
	APIKeyEncrypted []byte `gorm:"type:blob;not null" json:"-"`
	APIKeyLast4     string `gorm:"type:text;not null" json:"api_key_last4"`

	NextMessageSeq   int64 `gorm:"not null" json:"next_message_seq"`
	NextEventSeq     int64 `gorm:"not null" json:"next_event_seq"`
	OfferedInputSeq  int64 `gorm:"not null" json:"offered_input_seq"`
	MemoryArchiveSeq int64 `gorm:"not null;default:0" json:"-"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	User model.User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (legacyBot) TableName() string { return "bots" }

// setupLegacyBotsDB 建用户 + 旧形状 bots 表并插入存量行；返回打开的库。
func setupLegacyBotsDB(t *testing.T, bots ...legacyBot) *gorm.DB {
	t.Helper()
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &legacyBot{}); err != nil {
		t.Fatalf("migrate legacy bots: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i, id := range []string{"u-a", "u-b"} {
		if err := db.Create(&model.User{ID: id, Username: "u_" + id, Email: id + "@example.com", PasswordHash: "x", CreatedAt: base, UpdatedAt: base.Add(time.Duration(i) * time.Second)}).Error; err != nil {
			t.Fatalf("create user %s: %v", id, err)
		}
	}
	for i := range bots {
		if err := db.Create(&bots[i]).Error; err != nil {
			t.Fatalf("create legacy bot %s: %v", bots[i].ID, err)
		}
	}
	return db
}

func runBootMigrationsFor(t *testing.T, db *gorm.DB) {
	t.Helper()
	store := storage.NewLocalStorage(t.TempDir())
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte(migrateTestKey)); err != nil {
		t.Fatalf("runBootMigrations: %v", err)
	}
}

func botProviderIDOf(t *testing.T, db *gorm.DB, botID string) string {
	t.Helper()
	var providerID string
	if err := db.Raw("SELECT provider_id FROM bots WHERE id = ?", botID).Scan(&providerID).Error; err != nil {
		t.Fatalf("load provider_id: %v", err)
	}
	return providerID
}

func botsTableColumns(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	var columns []struct{ Name string }
	if err := db.Raw("PRAGMA table_info(bots)").Scan(&columns).Error; err != nil {
		t.Fatalf("table_info: %v", err)
	}
	out := map[string]bool{}
	for _, c := range columns {
		out[c.Name] = true
	}
	return out
}

func TestMigrateBotAIProvidersAggregatesAndDrops(t *testing.T) {
	// 同用户同 (地址, 密文) 的两个机器人聚合为一条 provider；不同地址/不同
	// 用户各自成条。目录按 api 地址反查归属（deepseek 命中目录；未知地址落
	// custom，同名冲突加序号）。
	deepseekCipher := []byte("cipher-deepseek-aaaa")
	deepseekCipher2 := []byte("cipher-deepseek-bbbb")
	selfHostCipher := []byte("cipher-selfhost-cccc")
	db := setupLegacyBotsDB(t,
		legacyBot{ID: "bot-1", UserID: "u-a", Name: "b1", PersonaPrompt: "", SystemPromptText: "s",
			PromptRevision: 1, PlatformPromptVersion: 8, ToolsetVersion: 7, LastToldVersion: 7,
			BaseURL: "https://api.deepseek.com", Model: "deepseek-chat",
			APIKeyEncrypted: deepseekCipher, APIKeyLast4: "aaaa",
			NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		legacyBot{ID: "bot-2", UserID: "u-a", Name: "b2", PersonaPrompt: "", SystemPromptText: "s",
			PromptRevision: 1, PlatformPromptVersion: 8, ToolsetVersion: 7, LastToldVersion: 7,
			// 尾部 / 不影响聚合：与 bot-1 同一 provider。
			BaseURL: "https://api.deepseek.com", Model: "deepseek-reasoner",
			APIKeyEncrypted: deepseekCipher, APIKeyLast4: "aaaa",
			NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		legacyBot{ID: "bot-3", UserID: "u-a", Name: "b3", PersonaPrompt: "", SystemPromptText: "s",
			PromptRevision: 1, PlatformPromptVersion: 8, ToolsetVersion: 7, LastToldVersion: 7,
			// 目录外地址 → custom；第二个 custom 命名「自定义 2」。
			BaseURL: "https://selfhost.example.com/v1", Model: "m",
			APIKeyEncrypted: selfHostCipher, APIKeyLast4: "cccc",
			NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		legacyBot{ID: "bot-4", UserID: "u-a", Name: "b4", PersonaPrompt: "", SystemPromptText: "s",
			PromptRevision: 1, PlatformPromptVersion: 8, ToolsetVersion: 7, LastToldVersion: 7,
			BaseURL: "https://other.example.com/v1", Model: "m",
			APIKeyEncrypted: deepseekCipher2, APIKeyLast4: "bbbb",
			NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		legacyBot{ID: "bot-5", UserID: "u-b", Name: "b5", PersonaPrompt: "", SystemPromptText: "s",
			PromptRevision: 1, PlatformPromptVersion: 8, ToolsetVersion: 7, LastToldVersion: 7,
			// 与 bot-1 同地址同密文但不同用户：provider 按用户隔离，不得共用。
			BaseURL: "https://api.deepseek.com", Model: "deepseek-chat",
			APIKeyEncrypted: append([]byte(nil), deepseekCipher...), APIKeyLast4: "aaaa",
			NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
	)

	runBootMigrationsFor(t, db)

	// provider 去重结果：u-a 三条（deepseek、自定义、自定义 2），u-b 一条。
	var providers []model.AIProvider
	if err := db.Order("created_at ASC, id ASC").Find(&providers).Error; err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 4 {
		t.Fatalf("应聚合出 4 条 provider，实得 %d: %+v", len(providers), providers)
	}
	byBot := map[string]model.AIProvider{}
	for _, p := range providers {
		if p.UserID == "u-a" && p.CatalogID == "deepseek" {
			if p.BaseURL != "https://api.deepseek.com" {
				t.Fatalf("provider 地址应规范化去尾 /: %q", p.BaseURL)
			}
			byBot["deepseek-a"] = p
		}
		if p.UserID == "u-b" && p.CatalogID == "deepseek" {
			byBot["deepseek-b"] = p
		}
		if p.CatalogID == "custom" && p.BaseURL == "https://selfhost.example.com/v1" {
			byBot["custom"] = p
		}
		if p.CatalogID == "custom" && p.BaseURL == "https://other.example.com/v1" {
			byBot["custom2"] = p
		}
	}
	for _, key := range []string{"deepseek-a", "deepseek-b", "custom", "custom2"} {
		if byBot[key].ID == "" {
			t.Fatalf("缺少 %s provider: %+v", key, providers)
		}
	}
	if byBot["deepseek-a"].ID == byBot["deepseek-b"].ID {
		t.Fatalf("不同用户的 provider 不得共享")
	}
	if byBot["deepseek-a"].Name == "" || byBot["deepseek-a"].Name != "DeepSeek" {
		t.Fatalf("目录 provider 名应取目录名: %q", byBot["deepseek-a"].Name)
	}
	names := map[string]bool{byBot["custom"].Name: true, byBot["custom2"].Name: true}
	if len(names) != 2 {
		t.Fatalf("同名 custom 应加序号区分: %+v %+v", byBot["custom"], byBot["custom2"])
	}

	// 回填：同组机器人共享 provider_id。
	if got := botProviderIDOf(t, db, "bot-1"); got != byBot["deepseek-a"].ID {
		t.Fatalf("bot-1 应指向 deepseek provider")
	}
	if got := botProviderIDOf(t, db, "bot-2"); got != byBot["deepseek-a"].ID {
		t.Fatalf("bot-2 与 bot-1 应共享 provider")
	}
	if got := botProviderIDOf(t, db, "bot-3"); got != byBot["custom"].ID {
		t.Fatalf("bot-3 应指向 custom provider")
	}
	if got := botProviderIDOf(t, db, "bot-5"); got != byBot["deepseek-b"].ID {
		t.Fatalf("bot-5 应指向自己的 provider")
	}

	// 旧列删除。
	columns := botsTableColumns(t, db)
	for _, c := range []string{"base_url", "api_key_encrypted", "api_key_last4"} {
		if columns[c] {
			t.Fatalf("bots 表不应再有 %s 列", c)
		}
	}
	if !columns["provider_id"] {
		t.Fatalf("bots 表应有 provider_id 列")
	}

	// 第二次启动：检测不到旧列，整体跳过且不产生新 provider、不动既有引用。
	if err := migrateBotAIProviders(db, zap.NewNop(), []byte(migrateTestKey)); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var count int64
	db.Model(&model.AIProvider{}).Count(&count)
	if count != 4 {
		t.Fatalf("第二次迁移不得新建 provider: %d", count)
	}
	if got := botProviderIDOf(t, db, "bot-1"); got != byBot["deepseek-a"].ID {
		t.Fatalf("第二次迁移不得改动引用")
	}
	_ = botsTableColumns(t, db)
}

// 全新库（无 bots 旧列）直接跳过迁移；EnsureBotSchema 在迁移后正常补索引。
func TestMigrateBotAIProvidersSkipsFreshDB(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	runBootMigrationsFor(t, db)

	var count int64
	db.Model(&model.AIProvider{}).Count(&count)
	if count != 0 {
		t.Fatalf("全新库不应有 provider: %d", count)
	}
	if err := migrateBotAIProviders(db, zap.NewNop(), []byte(migrateTestKey)); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	_ = uuid.NewString
}

// TestMigrateBotAIProvidersDedupByPlaintextKey 明文去重的回归：AES-GCM 每次
// 随机 nonce，同一明文两次加密密文不同；两条同明文（不同密文）的旧 bot 必须
// 聚成 1 条 provider 并共同指向它。明文不同的行各自成条；密文损坏（解密
// 失败）的行不中断启动且仍被回填（按密文字节自成一组）。
func TestMigrateBotAIProvidersDedupByPlaintextKey(t *testing.T) {
	key := []byte(migrateTestKey)
	enc := func(plain string) []byte {
		c, err := crypto.Encrypt([]byte(plain), key)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		return c
	}
	cipherA1 := enc("sk-shared-key-0001")
	cipherA2 := enc("sk-shared-key-0001")
	if bytes.Equal(cipherA1, cipherA2) {
		t.Fatalf("同一明文两次加密不得产生相同密文")
	}
	cipherB := enc("sk-other-key-2222")
	// 短于 GCM nonce，解密必然失败。
	corrupt := []byte("bad")

	legacy := func(id, base string, cipher []byte, last4 string) legacyBot {
		return legacyBot{ID: id, UserID: "u-a", Name: id, SystemPromptText: "s",
			PromptRevision: 1, PlatformPromptVersion: 8, ToolsetVersion: 7, LastToldVersion: 7,
			BaseURL: base, Model: "m", APIKeyEncrypted: cipher, APIKeyLast4: last4,
			NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	}
	db := setupLegacyBotsDB(t,
		legacy("bot-d1", "https://api.deepseek.com", cipherA1, "0001"),
		legacy("bot-d2", "https://api.deepseek.com", cipherA2, "0001"),
		legacy("bot-d3", "https://api.deepseek.com", cipherB, "2222"),
		legacy("bot-d4", "https://api.deepseek.com", corrupt, "zzzz"),
	)

	runBootMigrationsFor(t, db)

	// 4 条旧机器人聚成 3 条 provider：d1/d2 同明文共享，d3 明文不同，d4 密文
	// 损坏自成一组。
	var count int64
	db.Model(&model.AIProvider{}).Count(&count)
	if count != 3 {
		t.Fatalf("同明文应聚合为一条 provider，实得 %d 条", count)
	}
	p1 := botProviderIDOf(t, db, "bot-d1")
	p2 := botProviderIDOf(t, db, "bot-d2")
	p3 := botProviderIDOf(t, db, "bot-d3")
	p4 := botProviderIDOf(t, db, "bot-d4")
	if p1 == "" || p1 != p2 {
		t.Fatalf("同明文的两条机器人应共享 provider: %q vs %q", p1, p2)
	}
	if p3 == "" || p3 == p1 || p3 == p4 {
		t.Fatalf("明文不同的机器人应有独立 provider: %q", p3)
	}
	if p4 == "" || p4 == p1 {
		t.Fatalf("解密失败的行仍应回填自己的 provider: %q", p4)
	}
	// 共享 provider 的密文是组内第一条密文原样搬运（不重新加密）。
	var shared model.AIProvider
	if err := db.Where("id = ?", p1).First(&shared).Error; err != nil {
		t.Fatalf("load shared provider: %v", err)
	}
	if !bytes.Equal(shared.APIKeyEncrypted, cipherA1) && !bytes.Equal(shared.APIKeyEncrypted, cipherA2) {
		t.Fatalf("provider 密文应来自组内既有行")
	}
}
