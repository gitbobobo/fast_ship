package service

import (
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

type ApiKeyService struct {
	apiKeyRepo *repository.ApiKeyRepository
}

func NewApiKeyService(apiKeyRepo *repository.ApiKeyRepository) *ApiKeyService {
	return &ApiKeyService{apiKeyRepo: apiKeyRepo}
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

func (s *ApiKeyService) Create(userID string, req *CreateApiKeyRequest) (*ApiKeyResponse, error) {
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

	resp := api.ApiKeyCreated{
		Id:         apiKey.ID,
		UserId:     apiKey.UserID,
		Name:       apiKey.Name,
		KeyPrefix:  apiKey.KeyPrefix,
		KeyHash:    apiKey.KeyHash,
		LastUsedAt: api.JSONTimePtr(apiKey.LastUsedAt),
		CreatedAt:  api.JSONTime(apiKey.CreatedAt),
		Key:        api.Ptr(fullKey),
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

func (s *ApiKeyService) Delete(id, userID string) error {
	return s.apiKeyRepo.Delete(id, userID)
}
