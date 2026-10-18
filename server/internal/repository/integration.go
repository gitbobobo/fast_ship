package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IntegrationRepository 管理用户级外部服务连接（INT-119）：一个用户一个
// provider 一行，(user_id, provider) 复合唯一。
type IntegrationRepository struct {
	db *gorm.DB
}

func NewIntegrationRepository(db *gorm.DB) *IntegrationRepository {
	return &IntegrationRepository{db: db}
}

// Transaction 以短事务执行 fn（INT-119 保存路径的读-改-写必须整体串行）。
// 闭包内只允许事务句柄与显式收句柄的 Tx 方法，不得触碰池根——单连接池下
// 再借第二条连接会永久自锁，见 docs/internal/database.md。
func (r *IntegrationRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

// GetByProvider 取该用户指定 provider 的配置行；无行时返回 gorm.ErrRecordNotFound。
func (r *IntegrationRepository) GetByProvider(userID, provider string) (*model.Integration, error) {
	return r.GetByProviderTx(r.db, userID, provider)
}

func (r *IntegrationRepository) GetByProviderTx(tx *gorm.DB, userID, provider string) (*model.Integration, error) {
	var row model.Integration
	if err := tx.Where("user_id = ? AND provider = ?", userID, provider).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// AppIDTakenByOtherTx 同 provider 下是否已有「别的用户」占用了该 app_id。
// 保存事务内在写入前调用，把重复 App ID 挡成应用层 409（唯一索引是并发兜底）。
func (r *IntegrationRepository) AppIDTakenByOtherTx(tx *gorm.DB, provider, appID, userID string) (bool, error) {
	var n int64
	err := tx.Model(&model.Integration{}).
		Where("provider = ? AND app_id = ? AND user_id <> ?", provider, appID, userID).
		Limit(1).Count(&n).Error
	return n > 0, err
}

// ListByProvider 列出某 provider 的全部集成行，按创建先后稳定排序（webhook
// 验签候选的次序基础）：验签方逐行尝试各自行内的密钥，行序只影响尝试先后，
// 不影响归属结果。
func (r *IntegrationRepository) ListByProvider(provider string) ([]*model.Integration, error) {
	var rows []*model.Integration
	err := r.db.Where("provider = ?", provider).
		Order("created_at ASC, id ASC").Find(&rows).Error
	return rows, err
}

// Upsert 按 (user_id, provider) 复合唯一键整组覆盖连接参数，并把检测状态重置为
// unchecked——保存后的真实结果由调用方检测完成经 UpdateDetectionIfCurrent 落库。
func (r *IntegrationRepository) Upsert(row *model.Integration) error {
	return r.UpsertTx(r.db, row)
}

func (r *IntegrationRepository) UpsertTx(tx *gorm.DB, row *model.Integration) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "provider"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"app_id", "config_encrypted", "detection_status", "detection_detail", "detected_at", "updated_at",
		}),
	}).Create(row).Error
}

// UpdateDetectionIfCurrent 按保存时的密文版本条件写回检测结果：配置在检测期间
// 被再次保存（密文变化）时返回 false 且什么都不写——慢检测不得覆盖新配置。
// 只写检测三列；updated_at 是「配置最近保存时间」，不随检测变化。
func (r *IntegrationRepository) UpdateDetectionIfCurrent(userID, provider string, configEncrypted []byte, status, detail string, detectedAt time.Time) (bool, error) {
	res := r.db.Model(&model.Integration{}).
		Where("user_id = ? AND provider = ? AND config_encrypted = ?", userID, provider, configEncrypted).
		UpdateColumns(map[string]interface{}{
			"detection_status": status,
			"detection_detail": detail,
			"detected_at":      detectedAt,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
