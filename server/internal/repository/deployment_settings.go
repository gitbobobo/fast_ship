package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// DeploymentSettingsRepository 读写部署级设置单行（INT-129）：主键固定
// model.DeploymentSettingsID，行不存在时读侧报 gorm.ErrRecordNotFound（由
// service 回落 config），写侧 Upsert 先建行。
type DeploymentSettingsRepository struct {
	db *gorm.DB
}

func NewDeploymentSettingsRepository(db *gorm.DB) *DeploymentSettingsRepository {
	return &DeploymentSettingsRepository{db: db}
}

// Transaction 以短事务执行 fn（B 轮 App 身份的读-改-写需要）。闭包内只用
// tx 句柄与显式收句柄的 Tx 方法，不得触碰池根——单连接池下再借第二条连接
// 会永久自锁，见 docs/internal/database.md。
func (r *DeploymentSettingsRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

// Get 读单行；无行时返回 gorm.ErrRecordNotFound。
func (r *DeploymentSettingsRepository) Get() (*model.DeploymentSettings, error) {
	return r.GetTx(r.db)
}

func (r *DeploymentSettingsRepository) GetTx(tx *gorm.DB) (*model.DeploymentSettings, error) {
	var row model.DeploymentSettings
	if err := tx.Where("id = ?", model.DeploymentSettingsID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Upsert 写入单行的给定列（A 轮 public_url；B 轮 App 身份四列）。行不存在时
// 先按模型默认值建行、再套用给定列，两条语句同在一个事务内。
func (r *DeploymentSettingsRepository) Upsert(fields map[string]any) error {
	return r.Transaction(func(tx *gorm.DB) error {
		return r.UpsertTx(tx, fields)
	})
}

func (r *DeploymentSettingsRepository) UpsertTx(tx *gorm.DB, fields map[string]any) error {
	now := time.Now().UTC()
	updates := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		updates[k] = v
	}
	updates["updated_at"] = now

	var count int64
	if err := tx.Model(&model.DeploymentSettings{}).
		Where("id = ?", model.DeploymentSettingsID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		row := model.DeploymentSettings{
			ID:              model.DeploymentSettingsID,
			PublicURL:       "",
			AppID:           "",
			AppSlug:         "",
			DetectionStatus: model.DeploymentDetectionUnchecked,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return tx.Model(&model.DeploymentSettings{}).
		Where("id = ?", model.DeploymentSettingsID).Updates(updates).Error
}

// UpdateDetectionIfCurrent 按保存时的密文版本条件写回检测结果：配置在检测期间
// 被再次保存（密文变化）时返回 false 且什么都不写——慢检测不得覆盖新配置。
// 写检测三列；slug 非空时一并落 app_slug（检测结果带回的安装链接用）。updated_at
// 是「配置最近保存时间」，不随检测变化。
func (r *DeploymentSettingsRepository) UpdateDetectionIfCurrent(configEncrypted []byte, status, detail, slug string, detectedAt time.Time) (bool, error) {
	updates := map[string]interface{}{
		"detection_status": status,
		"detection_detail": detail,
		"detected_at":      detectedAt,
	}
	if slug != "" {
		updates["app_slug"] = slug
	}
	res := r.db.Model(&model.DeploymentSettings{}).
		Where("id = ? AND config_encrypted = ?", model.DeploymentSettingsID, configEncrypted).
		UpdateColumns(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
