package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type JWTBlacklistRepository struct {
	db *gorm.DB
}

func NewJWTBlacklistRepository(db *gorm.DB) *JWTBlacklistRepository {
	return &JWTBlacklistRepository{db: db}
}

func (r *JWTBlacklistRepository) Add(jti string, expiredAt time.Time) error {
	return r.db.Create(&model.JWTBlacklist{
		JTI:       jti,
		ExpiredAt: expiredAt,
	}).Error
}

func (r *JWTBlacklistRepository) Exists(jti string) (bool, error) {
	var count int64
	err := r.db.Model(&model.JWTBlacklist{}).Where("jti = ?", jti).Count(&count).Error
	return count > 0, err
}

// CleanExpired 清理已过期的黑名单记录
func (r *JWTBlacklistRepository) CleanExpired() error {
	return r.db.Where("expired_at < ?", time.Now()).Delete(&model.JWTBlacklist{}).Error
}
