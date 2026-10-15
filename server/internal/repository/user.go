package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(user *model.User) error {
	return r.db.Create(user).Error
}

func (r *UserRepository) FindByID(id string) (*model.User, error) {
	var user model.User
	err := r.db.Where("id = ?", id).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// ListByIDs 一次查询多个用户，返回 id -> User 映射，用于协作区批量解析作者信息。
func (r *UserRepository) ListByIDs(ids []string) (map[string]model.User, error) {
	result := make(map[string]model.User, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var users []model.User
	if err := r.db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		result[user.ID] = user
	}
	return result, nil
}

func (r *UserRepository) FindByUsername(username string) (*model.User, error) {
	var user model.User
	err := r.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) FindByEmail(email string) (*model.User, error) {
	var user model.User
	err := r.db.Where("email = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) FindByUsernameOrEmail(login string) (*model.User, error) {
	var user model.User
	err := r.db.Where("username = ? OR email = ?", login, login).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateFields 只写入给定列。禁止整行 Save 回写：调用方持有的 user 副本可能
// 早于一次已提交的改密，全量回写会把 token_version/password_hash 覆盖回旧值，
// 复活本该吊销的全部会话。
func (r *UserRepository) UpdateFields(userID string, fields map[string]any) error {
	return r.db.Model(&model.User{}).Where("id = ?", userID).Updates(fields).Error
}

// UpdatePasswordAndRotateSessions 在同一事务里改密并轮换会话凭证：更新密码哈希、
// token_version 在事务内原子自增并回读、吊销该用户全部未吊销的 refresh token，
// 再写入为当前会话新签发的 refresh token。任一失败整体回滚——已改密的用户
// 不会落在新旧会话都不可用的中间态。并发改密各自拿到不同版本号：自增必须在
// 事务内完成，用调用方预读的 nextVersion 写回会让两次改密停在同一版本。
func (r *UserRepository) UpdatePasswordAndRotateSessions(userID, passwordHash string, next *model.RefreshToken) (int, error) {
	now := time.Now()
	var newVersion int
	err := r.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
			"password_hash": passwordHash,
			"token_version": gorm.Expr("token_version + 1"),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Model(&model.User{}).Where("id = ?", userID).Pluck("token_version", &newVersion).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.RefreshToken{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Create(next).Error
	})
	return newVersion, err
}

func (r *UserRepository) ExistsByUsername(username string) (bool, error) {
	var count int64
	err := r.db.Model(&model.User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

func (r *UserRepository) ExistsByEmail(email string) (bool, error) {
	var count int64
	err := r.db.Model(&model.User{}).Where("email = ?", email).Count(&count).Error
	return count > 0, err
}
