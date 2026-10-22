package service

import (
	"context"
	"log/slog"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

type ApiKeyService struct {
	apiKeyRepo *repository.ApiKeyRepository
	logger     *slog.Logger
}

func NewApiKeyService(apiKeyRepo *repository.ApiKeyRepository, logger *slog.Logger) *ApiKeyService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ApiKeyService{apiKeyRepo: apiKeyRepo, logger: logger}
}

// toApiKeyResponse 把存储模型投影为契约类型；ApiKeyCreated 仅在创建响应中
// 额外携带一次性的明文 key。
func toApiKeyResponse(key model.ApiKey) api.ApiKey {
	return api.ApiKey{
		Id:         key.ID,
		UserId:     key.UserID,
		Name:       key.Name,
		KeyPrefix:  key.KeyPrefix,
		KeyHash:    key.KeyHash,
		LastUsedAt: api.JSONTimePtr(key.LastUsedAt),
		CreatedAt:  api.JSONTime(key.CreatedAt),
	}
}

func (s *ApiKeyService) log(ctx context.Context) *slog.Logger {
	l := selflog.LoggerOr(ctx, s.logger)
	if selflog.LoggerFromContext(ctx) != nil {
		return l.With(slog.String("module", "api_key"))
	}
	return l
}

func (s *ApiKeyService) Create(ctx context.Context, userID string, req *CreateApiKeyRequest) (*ApiKeyResponse, error) {
	raw, err := GenerateApiKeyRaw()
	if err != nil {
		return nil, errs.ErrInternal
	}

	fullKey := FormatApiKey(raw)
	keyHash := HashApiKey(raw)
	keyPrefix := raw[:8]

	apiKey := &model.ApiKey{
		ID:        uuid.New().String(),
		UserID:    userID,
		Name:      req.Name,
		KeyPrefix: keyPrefix,
		KeyHash:   keyHash,
	}

	if err := s.apiKeyRepo.Create(apiKey); err != nil {
		return nil, errs.ErrInternal
	}

	// 只记元信息，明文 key 不落日志（响应里也仅此一次返回）。
	s.log(ctx).Info("创建 API Key",
		slog.String("user_id", userID),
		slog.String("api_key_id", apiKey.ID),
		slog.String("name", apiKey.Name),
	)

	resp := api.ApiKeyCreated{
		Id:         apiKey.ID,
		UserId:     apiKey.UserID,
		Name:       apiKey.Name,
		KeyPrefix:  apiKey.KeyPrefix,
		KeyHash:    apiKey.KeyHash,
		LastUsedAt: api.JSONTimePtr(apiKey.LastUsedAt),
		CreatedAt:  api.JSONTime(apiKey.CreatedAt),
		Key:        fullKey,
	}
	return &resp, nil
}

func (s *ApiKeyService) List(userID string) ([]api.ApiKey, error) {
	keys, err := s.apiKeyRepo.ListByUserID(userID)
	if err != nil {
		return nil, err
	}
	result := make([]api.ApiKey, 0, len(keys))
	for _, key := range keys {
		result = append(result, toApiKeyResponse(key))
	}
	return result, nil
}

func (s *ApiKeyService) Delete(ctx context.Context, id, userID string) error {
	if err := s.apiKeyRepo.Delete(id, userID); err != nil {
		return err
	}
	s.log(ctx).Info("删除 API Key",
		slog.String("user_id", userID),
		slog.String("api_key_id", id),
	)
	return nil
}
