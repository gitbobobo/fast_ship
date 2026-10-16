package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// QuotaAccountRepository 管理订阅额度账号（INT-109）。
// 单账号操作全部带 user_id 条件——他人账号对调用方不可见；
// ListDue / EarliestNextRetryAt 跨用户，仅供后台调度器使用。
type QuotaAccountRepository struct {
	db *gorm.DB
}

func NewQuotaAccountRepository(db *gorm.DB) *QuotaAccountRepository {
	return &QuotaAccountRepository{db: db}
}

func (r *QuotaAccountRepository) Create(account *model.QuotaAccount) error {
	return r.db.Create(account).Error
}

func (r *QuotaAccountRepository) FindByID(id, userID string) (*model.QuotaAccount, error) {
	var account model.QuotaAccount
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

// FindByIDAny 按 id 取行，不限用户——只给后台调度器用（调度跨用户抓取）。
func (r *QuotaAccountRepository) FindByIDAny(id string) (*model.QuotaAccount, error) {
	var account model.QuotaAccount
	if err := r.db.Where("id = ?", id).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *QuotaAccountRepository) ListByUserID(userID string) ([]model.QuotaAccount, error) {
	var accounts []model.QuotaAccount
	err := r.db.Where("user_id = ?", userID).Order("created_at ASC").Find(&accounts).Error
	return accounts, err
}

// ListDue 返回调度器本轮要抓的账号：next_retry_at 为空（从未抓取/立即该抓）
// 或已到期的全部账号，跨用户。
func (r *QuotaAccountRepository) ListDue(now time.Time) ([]model.QuotaAccount, error) {
	var accounts []model.QuotaAccount
	err := r.db.Where("next_retry_at IS NULL OR next_retry_at <= ?", now).
		Order("created_at ASC").Find(&accounts).Error
	return accounts, err
}

// EarliestNextRetryAt 返回全部账号里最早的未来 next_retry_at，
// 供调度器把下一次唤醒提前到该时刻；没有返回 nil。
func (r *QuotaAccountRepository) EarliestNextRetryAt(now time.Time) (*time.Time, error) {
	var account model.QuotaAccount
	err := r.db.Where("next_retry_at IS NOT NULL AND next_retry_at > ?", now).
		Order("next_retry_at ASC").Limit(1).First(&account).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return account.NextRetryAt, nil
}

// UpdateLabel 只写标签列，不碰凭据与抓取结果，避免与抓取写互相覆盖。
func (r *QuotaAccountRepository) UpdateLabel(id, userID, label string) error {
	res := r.db.Model(&model.QuotaAccount{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]interface{}{
			"label":      label,
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateCredentials 整组替换凭据密文与尾号，并把抓取状态打回 pending：
// 旧快照保留展示，但调度状态清零让新凭据按新账号重新抓取。
func (r *QuotaAccountRepository) UpdateCredentials(id, userID string, encrypted []byte, hint string) error {
	res := r.db.Model(&model.QuotaAccount{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]interface{}{
			"credentials_encrypted": encrypted,
			"credential_hint":       hint,
			"status":                model.QuotaStatusPending,
			"last_error":            "",
			"failure_count":         0,
			"next_retry_at":         nil,
			"updated_at":            time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SaveFetchSuccess 写一次成功抓取：快照原文、套餐名、数据时间与调度状态。
// next_retry_at 由调用方算好（常规间隔或窗口重置提前量）。
func (r *QuotaAccountRepository) SaveFetchSuccess(id, snapshotJSON, planName string, fetchedAt time.Time, nextRetryAt *time.Time) error {
	res := r.db.Model(&model.QuotaAccount{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"snapshot_json":   snapshotJSON,
			"plan_name":       planName,
			"status":          model.QuotaStatusOK,
			"last_error":      "",
			"last_fetched_at": fetchedAt,
			"last_success_at": fetchedAt,
			"failure_count":   0,
			"next_retry_at":   nextRetryAt,
			"updated_at":      time.Now().UTC(),
		})
	return res.Error
}

// SaveFetchFailure 只写失败四件套（status/last_error/failure_count/next_retry_at），
// 不碰快照列——上次成功数据保持展示。行已删除时影响 0 行，静默丢弃结果。
func (r *QuotaAccountRepository) SaveFetchFailure(id, status, lastError string, failureCount int, nextRetryAt time.Time) error {
	res := r.db.Model(&model.QuotaAccount{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":        status,
			"last_error":    lastError,
			"failure_count": failureCount,
			"next_retry_at": nextRetryAt,
			"updated_at":    time.Now().UTC(),
		})
	return res.Error
}

func (r *QuotaAccountRepository) Delete(id, userID string) error {
	res := r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.QuotaAccount{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
