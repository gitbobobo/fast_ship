package repository

import (
	"bytes"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// INT-133：bot 的地址/密钥/last4/提供商名/目录 ID 是 provider 派生字段
// （gorm:"-"），由仓库层在读取时补齐。任何 Find 返回的 bot 都必须带全派生值，
// 否则 run 冻结与 schedule 派发拿到空地址。

func setupBotProviderTestDB(t *testing.T) (*gorm.DB, *BotRepository, *AIProviderRepository) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(append(BotModels(), &model.User{})...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewBotRepository(db), NewAIProviderRepository(db)
}

func seedProviderBot(t *testing.T, db *gorm.DB, userID string) (*model.AIProvider, *model.Bot) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{
		ID: userID, Username: "u-" + userID, Email: userID + "@test.local",
		PasswordHash: "x", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	provider := &model.AIProvider{
		ID: uuid.NewString(), UserID: userID, CatalogID: "zai", Name: "Z.AI",
		BaseURL:         "https://api.z.ai/api/paas/v4",
		APIKeyEncrypted: []byte("cipher-provider-xyz"), APIKeyLast4: "9876",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(provider).Error; err != nil {
		t.Fatalf("create provider: %v", err)
	}
	bot := &model.Bot{
		ID: uuid.NewString(), UserID: userID, Name: "bot-" + uuid.NewString()[:8],
		PersonaPrompt: "p", SystemPromptText: "p", PromptRevision: 1,
		PlatformPromptVersion: 1, ToolsetVersion: 1, LastToldVersion: 1,
		ProviderID: provider.ID, Model: "glm-4.6",
		NextMessageSeq: 1, NextEventSeq: 1, OfferedInputSeq: 0,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(bot).Error; err != nil {
		t.Fatalf("create bot: %v", err)
	}
	return provider, bot
}

func assertDerivedFrom(t *testing.T, got *model.Bot, provider *model.AIProvider) {
	t.Helper()
	if got.BaseURL != provider.BaseURL {
		t.Fatalf("BaseURL 应派生自 provider: %q", got.BaseURL)
	}
	if !bytes.Equal(got.APIKeyEncrypted, provider.APIKeyEncrypted) {
		t.Fatalf("密钥密文应派生自 provider")
	}
	if got.APIKeyLast4 != provider.APIKeyLast4 {
		t.Fatalf("last4 应派生自 provider: %q", got.APIKeyLast4)
	}
	if got.ProviderName != provider.Name || got.CatalogID != provider.CatalogID {
		t.Fatalf("provider 名/目录 ID 应派生: %q %q", got.ProviderName, got.CatalogID)
	}
}

func TestBotReadsFillProviderDerivedFields(t *testing.T) {
	db, botRepo, _ := setupBotProviderTestDB(t)
	userID := "u-derived"
	provider, bot := seedProviderBot(t, db, userID)

	// Get / FindByIDAndUser。
	for name, got := range map[string]*model.Bot{
		"Get":             mustBot(t, func() (*model.Bot, error) { return botRepo.Get(bot.ID) }),
		"FindByIDAndUser": mustBot(t, func() (*model.Bot, error) { return botRepo.FindByIDAndUser(bot.ID, userID) }),
	} {
		assertDerivedFrom(t, got, provider)
		_ = name
	}

	// ListByUser。
	rows, err := botRepo.ListByUser(userID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %v %d", err, len(rows))
	}
	assertDerivedFrom(t, &rows[0], provider)

	// 事务内读取（run 前置激活与 schedule 派发走这条路径）。
	var inTx *model.Bot
	if err := db.Transaction(func(tx *gorm.DB) error {
		row, err := NewBotRepository(tx).Get(bot.ID)
		if err != nil {
			return err
		}
		inTx = row
		return nil
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
	assertDerivedFrom(t, inTx, provider)

	// 轮换 provider 密钥后，下一次读取即生效（不复制进 bots 行）。
	if err := db.Model(&model.AIProvider{}).Where("id = ?", provider.ID).
		Updates(map[string]interface{}{"api_key_encrypted": []byte("rotated-cipher"), "api_key_last4": "1111"}).Error; err != nil {
		t.Fatalf("rotate: %v", err)
	}
	fresh := mustBot(t, func() (*model.Bot, error) { return botRepo.Get(bot.ID) })
	if fresh.APIKeyLast4 != "1111" || !bytes.Equal(fresh.APIKeyEncrypted, []byte("rotated-cipher")) {
		t.Fatalf("轮换后读取应拿到新密文: %+v", fresh)
	}
}

// 写路径不写派生列：Create/UpdateConfigTx 只写引用，读回时派生值来自 provider。
func TestBotWritesDoNotTouchDerivedColumns(t *testing.T) {
	db, botRepo, _ := setupBotProviderTestDB(t)
	userID := "u-writes"
	provider, bot := seedProviderBot(t, db, userID)

	// Create 写路径：构造体上的派生字段即使被误填也不落库（gorm:"-"）。
	stale := *bot
	stale.ID = uuid.NewString()
	stale.BaseURL = "https://stale.example.com"
	stale.APIKeyEncrypted = []byte("stale")
	stale.APIKeyLast4 = "0000"
	if err := botRepo.Create(&stale); err != nil {
		t.Fatalf("create: %v", err)
	}
	assertDerivedFrom(t, mustBot(t, func() (*model.Bot, error) { return botRepo.Get(bot.ID) }), provider)
	assertDerivedFrom(t, mustBot(t, func() (*model.Bot, error) { return botRepo.Get(stale.ID) }), provider)

	// UpdateConfigTx：引用列与名称持久化；派生值不参与写入（误填也不落库）。
	bot.Name = "renamed"
	if err := db.Transaction(func(tx *gorm.DB) error {
		return botRepo.UpdateConfigTx(tx, bot, false)
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	var row struct {
		ProviderID string
	}
	if err := db.Raw("SELECT provider_id FROM bots WHERE id = ?", bot.ID).Scan(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.ProviderID != provider.ID {
		t.Fatalf("provider_id 引用应持久化: %q", row.ProviderID)
	}
	// base_url / api_key 列已随模型移除；列不存在即写路径无从触碰派生值。
}

func mustBot(t *testing.T, fn func() (*model.Bot, error)) *model.Bot {
	t.Helper()
	bot, err := fn()
	if err != nil {
		t.Fatalf("read bot: %v", err)
	}
	return bot
}
