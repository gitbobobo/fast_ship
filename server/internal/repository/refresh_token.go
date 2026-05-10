package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type RefreshTokenRepository struct {
	db *gorm.DB
}

func NewRefreshTokenRepository(db *gorm.DB) *RefreshTokenRepository {
	return &RefreshTokenRepository{db: db}
}

func (r *RefreshTokenRepository) Create(token *model.RefreshToken) error {
	return r.db.Create(token).Error
}

func (r *RefreshTokenRepository) FindByHash(hash string) (*model.RefreshToken, error) {
	var token model.RefreshToken
	err := r.db.Where("token_hash = ? AND expires_at > ? AND revoked_at IS NULL", hash, time.Now()).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *RefreshTokenRepository) Revoke(id string) error {
	now := time.Now()
	return r.db.Model(&model.RefreshToken{}).Where("id = ?", id).Update("revoked_at", &now).Error
}

func (r *RefreshTokenRepository) RevokeByUserID(userID string) error {
	now := time.Now()
	return r.db.Model(&model.RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", &now).Error
}

func (r *RefreshTokenRepository) Rotate(currentID string, next *model.RefreshToken) error {
	now := time.Now()

	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(next).Error; err != nil {
			return err
		}

		return tx.Model(&model.RefreshToken{}).
			Where("id = ? AND revoked_at IS NULL", currentID).
			Update("revoked_at", &now).
			Error
	})
}

func (r *RefreshTokenRepository) CleanExpired() error {
	return r.db.Where("expires_at < ? OR revoked_at IS NOT NULL", time.Now()).Delete(&model.RefreshToken{}).Error
}
