package repository

import (
	"context"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type T3EnvironmentRepository struct {
	db *gorm.DB
}

func NewT3EnvironmentRepository(db *gorm.DB) *T3EnvironmentRepository {
	return &T3EnvironmentRepository{db: db}
}

func (r *T3EnvironmentRepository) Create(ctx context.Context, env *model.T3Environment) error {
	return r.db.WithContext(ctx).Create(env).Error
}

func (r *T3EnvironmentRepository) FindByIDAndUserID(ctx context.Context, id, userID string) (*model.T3Environment, error) {
	var env model.T3Environment
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&env).Error
	if err != nil {
		return nil, err
	}
	return &env, nil
}

func (r *T3EnvironmentRepository) FindByNameAndUserID(ctx context.Context, name, userID string) (*model.T3Environment, error) {
	var env model.T3Environment
	err := r.db.WithContext(ctx).Where("name = ? AND user_id = ?", name, userID).First(&env).Error
	if err != nil {
		return nil, err
	}
	return &env, nil
}

func (r *T3EnvironmentRepository) ListByUserID(ctx context.Context, userID string) ([]model.T3Environment, error) {
	var envs []model.T3Environment
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC").Find(&envs).Error
	return envs, err
}

// T3PairingUpdate 是一次重新配对产出的列集合（地址+凭证），不含 name 等
// 其它列——重配与改名各写各的列，互不覆盖。
type T3PairingUpdate struct {
	Address              string
	BearerTokenEncrypted []byte
	BearerExpiresAt      time.Time
	BearerScope          string
}

// T3EnvironmentUpdate 是一次合并更新的列集合：只写请求里出现的字段（Name
// 非 nil 才改名、Pairing 非 nil 才覆盖配对产物），updated_at 随成功更新统一
// 推进。一次 UPDATE 落库全部请求字段，中途失败不留下部分写入。
type T3EnvironmentUpdate struct {
	Name      *string
	Pairing   *T3PairingUpdate
	UpdatedAt time.Time
}

// Update 条件 UPDATE（WHERE id + user_id），影响 0 行时返回
// gorm.ErrRecordNotFound——不用 gorm Save：UPDATE 0 行会退化 INSERT，把并发
// 删除的环境原样插回。名称冲突由 (user_id, name) 唯一索引兜底，调用方按
// 原始错误识别唯一约束冲突（并发重名不走 FindByName 预检）。
func (r *T3EnvironmentRepository) Update(ctx context.Context, id, userID string, update *T3EnvironmentUpdate) error {
	columns := map[string]any{"updated_at": update.UpdatedAt}
	if update.Name != nil {
		columns["name"] = *update.Name
	}
	if update.Pairing != nil {
		columns["address"] = update.Pairing.Address
		columns["bearer_token_encrypted"] = update.Pairing.BearerTokenEncrypted
		columns["bearer_expires_at"] = update.Pairing.BearerExpiresAt
		columns["bearer_scope"] = update.Pairing.BearerScope
	}
	res := r.db.WithContext(ctx).Model(&model.T3Environment{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(columns)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *T3EnvironmentRepository) Delete(ctx context.Context, id, userID string) error {
	return r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&model.T3Environment{}).Error
}
