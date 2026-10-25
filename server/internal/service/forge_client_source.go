package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Forgejo 集成的凭证面（INT-167 任务 3）：审查 resolver 的 Forgejo 分支与
// Forgejo webhook 验签共用一个按集成行现取的来源。明文只在单次调用内存在，
// 不缓存、不进日志、不进响应——缓存会把轮换后的 PAT/密钥钉死在旧值上。

// forgeIntegrationLookup 集成行读取面：生产接 repository.ForgeIntegrationRepository，
// 测试可注入桩。
type forgeIntegrationLookup interface {
	FindByID(id string) (*model.ForgeIntegration, error)
}

// ForgeClientSource 按 forge_integrations 行现取 Forgejo 访问三件套与 webhook
// 验签密钥。实现 forge.ForgejoSource（resolver 的 Forgejo 分支）与 handler 的
// ForgeWebhookSecretSource（forge_webhook.go）。
type ForgeClientSource struct {
	repo   forgeIntegrationLookup
	key    []byte
	logger *zap.Logger
}

// NewForgeClientSource 组装来源；cfg.Encryption.Key 与集成行写入同一密钥。
func NewForgeClientSource(repo forgeIntegrationLookup, cfg *config.Config, logger *zap.Logger) *ForgeClientSource {
	key := []byte(nil)
	if cfg != nil {
		key = []byte(cfg.Encryption.Key)
	}
	return &ForgeClientSource{repo: repo, key: key, logger: logger}
}

// ForgejoClient 集成行必须存在且 kind=forgejo：解密 PAT 后按集成配置构建
// SSRF 安全 client（allow_private_network / 自定义 CA / 基址主机名单透传，
// 与连接检测 Test 同一套构建入口，不另起第二套防护）。
func (s *ForgeClientSource) ForgejoClient(_ context.Context, integrationID string) (*http.Client, string, string, error) {
	row, err := s.findForgejoRow(integrationID)
	if err != nil {
		return nil, "", "", err
	}
	token, err := crypto.Decrypt(row.TokenEncrypted, s.key)
	if err != nil {
		return nil, "", "", errForgeIntegrationDecrypt
	}
	apiBase := forgeAPIBaseURL(row)
	httpClient, err := forges.NewClient(forges.Config{
		AllowedHosts:        forges.AllowedHosts(row.BaseURL, apiBase),
		CACertPEM:           row.CACertPEM,
		AllowPrivateNetwork: row.AllowPrivateNetwork,
		AuthPathPrefix:      apiPathPrefix(apiBase),
	})
	if err != nil {
		s.logger.Error("构建 Forgejo 集成 client 失败", zap.String("integration", integrationID), zap.Error(err))
		return nil, "", "", errors.New("构建 Forgejo 集成访问 client 失败")
	}
	return httpClient, apiBase, string(token), nil
}

// WebhookSecret Forgejo webhook 验签密钥：found=false 表示集成不存在（handler
// 404）；found=true 且 secret 为空表示行存在但密钥未配置（handler 503 拒收）。
// 解密失败按 errForgeIntegrationDecrypt（50000）透出，不回落任何兜底密钥。
func (s *ForgeClientSource) WebhookSecret(_ context.Context, integrationID string) (secret string, found bool, err error) {
	row, err := s.repo.FindByID(integrationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("读取代码仓集成失败: %w", err)
	}
	// 端点只受理 Forgejo 投递：kind 不符的行按「不存在」404——当前 Create
	// 恒写 forgejo，属防御；未来新增 kind 时其 secret 不能用于此验签。
	if row.Kind != model.ForgeIntegrationKindForgejo {
		return "", false, nil
	}
	if len(row.WebhookSecretEncrypted) == 0 {
		return "", true, nil
	}
	raw, err := crypto.Decrypt(row.WebhookSecretEncrypted, s.key)
	if err != nil {
		return "", true, errForgeIntegrationDecrypt
	}
	return strings.TrimSpace(string(raw)), true, nil
}

// findForgejoRow 查集成行并校验 kind：内置 github-default 不落表，天然报
// 「不存在」；kind 不是 forgejo 的行不能进 Forgejo 审查链路。
func (s *ForgeClientSource) findForgejoRow(integrationID string) (*model.ForgeIntegration, error) {
	row, err := s.repo.FindByID(integrationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("代码仓集成 %s 不存在或已删除", integrationID)
		}
		return nil, fmt.Errorf("读取代码仓集成失败: %w", err)
	}
	if row.Kind != model.ForgeIntegrationKindForgejo {
		return nil, fmt.Errorf("代码仓集成 %s 不是 Forgejo 类型", integrationID)
	}
	return row, nil
}
