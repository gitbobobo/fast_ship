package service

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ForgeClientSource（INT-167 任务 3）定向测试：kind 把关、PAT 解密透出、
// webhook secret 的 found/空/损坏三态。client 构建只验类型（拨号防护由
// pkg/forges 自己的测试覆盖）。

func forgeSourceTestDB(t *testing.T) (*gorm.DB, *config.Config) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	cfg := &config.Config{Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"}}
	return db, cfg
}

// TestForgeClientSource_ForgejoClient 集成行 kind=forgejo：解密 PAT 原样透出，
// API 基址按行展开，client 非 nil。
func TestForgeClientSource_ForgejoClient(t *testing.T) {
	db, cfg := forgeSourceTestDB(t)
	repo := repository.NewForgeIntegrationRepository(db)
	if err := db.AutoMigrate(&model.ForgeIntegration{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	enc, err := crypto.Encrypt([]byte("pat-secret"), []byte(cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	row := &model.ForgeIntegration{
		ID:             "fj-1",
		Kind:           model.ForgeIntegrationKindForgejo,
		Name:           "forge",
		BaseURL:        "https://forge.example.com",
		TokenEncrypted: enc,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	src := NewForgeClientSource(repo, cfg, zap.NewNop())
	httpClient, apiBase, token, err := src.ForgejoClient(context.Background(), "fj-1")
	if err != nil {
		t.Fatalf("ForgejoClient: %v", err)
	}
	if httpClient == nil {
		t.Fatal("httpClient 不应为 nil")
	}
	if apiBase != "https://forge.example.com/api/v1" {
		t.Fatalf("apiBase = %q, want 推导端点", apiBase)
	}
	if token != "pat-secret" {
		t.Fatalf("token = %q, want 解密后的 PAT", token)
	}

	// kind 不是 forgejo → 明确错误；集成不存在 → 明确错误。
	if err := db.Model(&model.ForgeIntegration{}).Where("id = ?", "fj-1").
		Update("kind", model.ForgeIntegrationKindGitHub).Error; err != nil {
		t.Fatalf("update kind: %v", err)
	}
	if _, _, _, err := src.ForgejoClient(context.Background(), "fj-1"); err == nil ||
		!strings.Contains(err.Error(), "不是 Forgejo") {
		t.Fatalf("wrong kind err = %v, want 点明不是 Forgejo", err)
	}
	if _, _, _, err := src.ForgejoClient(context.Background(), "no-such"); err == nil ||
		!strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing err = %v, want 不存在", err)
	}
}

// TestForgeClientSource_ClientCache 传输层按配置指纹复用：同指纹返回同一
// client（一轮审查的分页/写评论共享连接池）；基址/私网开关等传输配置变化
// 重建；行被删后缓存逐出。
func TestForgeClientSource_ClientCache(t *testing.T) {
	db, cfg := forgeSourceTestDB(t)
	repo := repository.NewForgeIntegrationRepository(db)
	if err := db.AutoMigrate(&model.ForgeIntegration{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	enc, err := crypto.Encrypt([]byte("pat-secret"), []byte(cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := db.Create(&model.ForgeIntegration{
		ID: "fj-1", Kind: model.ForgeIntegrationKindForgejo, Name: "forge",
		BaseURL: "https://forge.example.com", TokenEncrypted: enc,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	src := NewForgeClientSource(repo, cfg, zap.NewNop())
	ctx := context.Background()

	c1, _, _, err := src.ForgejoClient(ctx, "fj-1")
	if err != nil {
		t.Fatalf("ForgejoClient: %v", err)
	}
	c2, _, _, err := src.ForgejoClient(ctx, "fj-1")
	if err != nil || c2 != c1 {
		t.Fatalf("同指纹应复用同一 client (err=%v)", err)
	}

	// PAT 轮换不动传输层指纹——凭证随每次调用现解密，不进缓存键。
	if err := db.Model(&model.ForgeIntegration{}).Where("id = ?", "fj-1").
		Update("token_encrypted", enc).Error; err != nil {
		t.Fatalf("rotate: %v", err)
	}
	c3, _, _, err := src.ForgejoClient(ctx, "fj-1")
	if err != nil || c3 != c1 {
		t.Fatalf("PAT 不变传输层应复用 (err=%v)", err)
	}

	// 传输配置变化（私网开关）→ 新 client。
	if err := db.Model(&model.ForgeIntegration{}).Where("id = ?", "fj-1").
		Update("allow_private_network", true).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	c4, _, _, err := src.ForgejoClient(ctx, "fj-1")
	if err != nil || c4 == c1 {
		t.Fatalf("配置变化应重建 client (err=%v)", err)
	}

	// 行被删 → 错误透出且缓存逐出。
	if err := db.Where("id = ?", "fj-1").Delete(&model.ForgeIntegration{}).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, _, err := src.ForgejoClient(ctx, "fj-1"); err == nil {
		t.Fatal("集成删除后应报错")
	}
	if _, ok := src.clients.Load("fj-1"); ok {
		t.Fatal("集成删除后缓存槽应逐出")
	}
}

// TestForgeClientSource_WebhookSecret 密钥三态：已配置（trim 后透出）、行存在
// 未配置（空串）、行不存在（found=false）；密文损坏 → 50000 语义错误。
func TestForgeClientSource_WebhookSecret(t *testing.T) {
	db, cfg := forgeSourceTestDB(t)
	repo := repository.NewForgeIntegrationRepository(db)
	if err := db.AutoMigrate(&model.ForgeIntegration{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	enc, err := crypto.Encrypt([]byte("  whsec  "), []byte(cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	rows := []*model.ForgeIntegration{
		{ID: "fj-ok", Kind: model.ForgeIntegrationKindForgejo, Name: "ok",
			BaseURL: "https://a.example.com", WebhookSecretEncrypted: enc},
		{ID: "fj-bare", Kind: model.ForgeIntegrationKindForgejo, Name: "bare",
			BaseURL: "https://b.example.com"},
	}
	for _, r := range rows {
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	src := NewForgeClientSource(repo, cfg, zap.NewNop())
	ctx := context.Background()

	secret, found, err := src.WebhookSecret(ctx, "fj-ok")
	if err != nil || !found || secret != "whsec" {
		t.Fatalf("configured = (%q,%v,%v), want (whsec,true,nil)", secret, found, err)
	}
	secret, found, err = src.WebhookSecret(ctx, "fj-bare")
	if err != nil || !found || secret != "" {
		t.Fatalf("bare = (%q,%v,%v), want (\"\",true,nil)", secret, found, err)
	}
	_, found, err = src.WebhookSecret(ctx, "no-such")
	if err != nil || found {
		t.Fatalf("missing = (%v,%v), want (false,nil)", found, err)
	}

	if err := db.Model(&model.ForgeIntegration{}).Where("id = ?", "fj-ok").
		Update("webhook_secret_encrypted", []byte("corrupted")).Error; err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	_, _, err = src.WebhookSecret(ctx, "fj-ok")
	if err == nil || !strings.Contains(err.Error(), "解密失败") {
		t.Fatalf("corrupted err = %v, want 50000 解密失败", err)
	}
}
