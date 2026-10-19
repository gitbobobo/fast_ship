package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// InviteRepository 管理一次性注册邀请（INT-129）。库里只存 token 哈希；
// 消费必须在注册事务内做条件更新（WHERE 未用未撤销），防并发重复使用。
type InviteRepository struct {
	db *gorm.DB
}

func NewInviteRepository(db *gorm.DB) *InviteRepository {
	return &InviteRepository{db: db}
}

// Transaction 以短事务执行 fn（注册的用户创建 + 邀请消费必须整体提交或回滚）。
// 闭包内只用 tx 句柄与显式收句柄的 Tx 方法，不得触碰池根——单连接池下再借
// 第二条连接会永久自锁，见 docs/internal/database.md。
func (r *InviteRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

func (r *InviteRepository) Create(invite *model.Invite) error {
	return r.CreateTx(r.db, invite)
}

func (r *InviteRepository) CreateTx(tx *gorm.DB, invite *model.Invite) error {
	return tx.Create(invite).Error
}

// FindByTokenHash 按明文 token 的 SHA-256 反查；无行时 gorm.ErrRecordNotFound。
func (r *InviteRepository) FindByTokenHash(tokenHash string) (*model.Invite, error) {
	return r.FindByTokenHashTx(r.db, tokenHash)
}

func (r *InviteRepository) FindByTokenHashTx(tx *gorm.DB, tokenHash string) (*model.Invite, error) {
	var invite model.Invite
	if err := tx.Where("token_hash = ?", tokenHash).First(&invite).Error; err != nil {
		return nil, err
	}
	return &invite, nil
}

// FindByID 按邀请 ID 反查（撤销端点用）；无行时 gorm.ErrRecordNotFound。
func (r *InviteRepository) FindByID(id string) (*model.Invite, error) {
	var invite model.Invite
	if err := r.db.Where("id = ?", id).First(&invite).Error; err != nil {
		return nil, err
	}
	return &invite, nil
}

// List 全部邀请，按 created_at 倒序（管理页最新的排最前）。
func (r *InviteRepository) List() ([]model.Invite, error) {
	var invites []model.Invite
	err := r.db.Order("created_at DESC, id DESC").Find(&invites).Error
	return invites, err
}

// Revoke 置 revoked_at（仅对未撤销的行生效）。幂等：已撤销的行再撤销
// 影响 0 行也按成功返回，由调用方保证不存在的 ID 报 40432。
func (r *InviteRepository) Revoke(id string, now time.Time) error {
	return r.db.Model(&model.Invite{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", now).Error
}

// ConsumeTx 在注册事务内消费邀请：条件更新 WHERE used_at IS NULL AND
// revoked_at IS NULL，未命中（已被并发使用/撤销）返回 false，调用方整体回滚。
func (r *InviteRepository) ConsumeTx(tx *gorm.DB, id, userID string, now time.Time) (bool, error) {
	res := tx.Model(&model.Invite{}).
		Where("id = ? AND used_at IS NULL AND revoked_at IS NULL", id).
		Updates(map[string]any{
			"used_by":    userID,
			"used_at":    now,
			"updated_at": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
