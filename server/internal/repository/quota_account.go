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

// ListDueAmong 返回 ids 里仍处到期状态（next_retry_at 为空或已到期）的账号：
// 供调度器轮末复查「本轮该抓的账号是否都已消化」——只看本轮起始时的
// due 集合，不把轮内 HTTP 侧新插入的行（如新建账号尚未落写回的 NULL 行）
// 误算成未消化。
func (r *QuotaAccountRepository) ListDueAmong(ids []string, now time.Time) ([]model.QuotaAccount, error) {
	var accounts []model.QuotaAccount
	if len(ids) == 0 {
		return accounts, nil
	}
	err := r.db.Where("id IN ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", ids, now).
		Find(&accounts).Error
	return accounts, err
}

// EarliestNextRetryAt 返回调度器的下一次唤醒时刻与是否存在待处理账号：
// 取全部账号里最早者——SQLite 升序把 NULL 排最前（next_retry_at 为空的行
// ListDue 视为立即到期，返回零值时刻）；未来行提前唤醒；已到期行把等待
// 压到 nextWait 的保底之下限，调度抖动（重算落在到期之后）不会睡满整轮。
// ORDER BY ASC LIMIT 1 走 next_retry_at 索引首行，不产生临时排序。
// 没有任何账号返回 ok=false。
func (r *QuotaAccountRepository) EarliestNextRetryAt() (time.Time, bool, error) {
	var account model.QuotaAccount
	err := r.db.Order("next_retry_at ASC").Limit(1).Take(&account).Error
	var zero time.Time
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return zero, false, nil
		}
		return zero, false, err
	}
	if account.NextRetryAt == nil {
		return zero, true, nil
	}
	return *account.NextRetryAt, true, nil
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
