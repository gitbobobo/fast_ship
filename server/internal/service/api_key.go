package service

import (
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

type CreateApiKeyRequest struct {
	Name string `json:"name" binding:"required,min=1,max=100"`
}

type ApiKeyResponse struct {
	model.ApiKey
	FullKey string `json:"full_key,omitempty"`
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

	return &ApiKeyResponse{
		ApiKey:  *apiKey,
		FullKey: fullKey,
	}, nil
}

func (s *ApiKeyService) List(userID string) ([]model.ApiKey, error) {
	return s.apiKeyRepo.ListByUserID(userID)
}

func (s *ApiKeyService) Delete(id, userID string) error {
	return s.apiKeyRepo.Delete(id, userID)
}
