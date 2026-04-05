package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type ApiKeyRepository struct {
	db *gorm.DB
}

func NewApiKeyRepository(db *gorm.DB) *ApiKeyRepository {
	return &ApiKeyRepository{db: db}
}

func (r *ApiKeyRepository) Create(key *model.ApiKey) error {
	return r.db.Create(key).Error
}

func (r *ApiKeyRepository) FindByKeyHash(keyHash string) (*model.ApiKey, error) {
	var key model.ApiKey
	err := r.db.Where("key_hash = ?", keyHash).First(&key).Error
	if err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *ApiKeyRepository) ListByUserID(userID string) ([]model.ApiKey, error) {
	var keys []model.ApiKey
	err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&keys).Error
	return keys, err
}

func (r *ApiKeyRepository) Delete(id, userID string) error {
	return r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.ApiKey{}).Error
}

func (r *ApiKeyRepository) UpdateLastUsed(id string) error {
	return r.db.Model(&model.ApiKey{}).Where("id = ?", id).Update("last_used_at", gorm.Expr("datetime('now')")).Error
}
