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

// CreateIfTokenVersion 仅当该用户当前 token_version 仍等于 expectedVersion 时才写入。
// 把签发钉死在调用方验证凭证时读到的版本上：改密在两者之间落库则放弃创建，
// 防止并发签发绕过改密的全量吊销扫描。
func (r *RefreshTokenRepository) CreateIfTokenVersion(token *model.RefreshToken, expectedVersion int) (bool, error) {
	var created bool
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var current int
		if err := tx.Model(&model.User{}).Where("id = ?", token.UserID).Pluck("token_version", &current).Error; err != nil {
			return err
		}
		if current != expectedVersion {
			return nil
		}
		if err := tx.Create(token).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

// Rotate 先条件吊销旧行再补发后继：旧行已被吊销（改密全量吊销或登出）时
// RowsAffected=0，事务不再创建新行。顺序不能反过来——先创建会让改密扫描
// 漏掉恰好落在两条语句之间的新令牌。
func (r *RefreshTokenRepository) Rotate(currentID string, next *model.RefreshToken) (bool, error) {
	now := time.Now()
	var rotated bool

	err := r.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.RefreshToken{}).
			Where("id = ? AND revoked_at IS NULL", currentID).
			Update("revoked_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}

		if err := tx.Create(next).Error; err != nil {
			return err
		}
		rotated = true
		return nil
	})
	return rotated, err
}

func (r *RefreshTokenRepository) CleanExpired() error {
	return r.db.Where("expires_at < ? OR revoked_at IS NOT NULL", time.Now()).Delete(&model.RefreshToken{}).Error
}
