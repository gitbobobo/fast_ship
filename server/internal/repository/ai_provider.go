package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// AIProviderRepository 是 AI 提供商（INT-133）的仓储：全部按 user 隔离；
// 密文不出仓储层，service 层解密。
type AIProviderRepository struct {
	db *gorm.DB
}

func NewAIProviderRepository(db *gorm.DB) *AIProviderRepository {
	return &AIProviderRepository{db: db}
}

func (r *AIProviderRepository) Create(p *model.AIProvider) error {
	return r.db.Create(p).Error
}

func (r *AIProviderRepository) FindByIDAndUser(id, userID string) (*model.AIProvider, error) {
	var p model.AIProvider
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// GetByID 按主键读 provider，不做用户隔离——调度器等内部路径用；用户面查询
// 一律走 FindByIDAndUser。
func (r *AIProviderRepository) GetByID(id string) (*model.AIProvider, error) {
	var p model.AIProvider
	if err := r.db.Where("id = ?", id).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// OpenCircuit 打开/刷新定时任务熔断（INT-137）：until 之前该提供商的定时
// 任务到点直接记 skipped。reason 记失败分类码（见 bot_model_errors.go）。
func (r *AIProviderRepository) OpenCircuit(id string, until time.Time, reason string) error {
	return r.db.Model(&model.AIProvider{}).Where("id = ?", id).Updates(map[string]interface{}{
		"circuit_open_until": until,
		"circuit_reason":     reason,
	}).Error
}

// ClearCircuit 清除熔断标记：用户更新凭证/地址或测试连接成功后调用。
func (r *AIProviderRepository) ClearCircuit(id string) error {
	return r.ClearCircuitTx(r.db, id)
}

// ClearCircuitTx 在调用方事务内清除熔断标记：更新凭证与恢复调度保持原子。
func (r *AIProviderRepository) ClearCircuitTx(tx *gorm.DB, id string) error {
	return tx.Model(&model.AIProvider{}).Where("id = ?", id).Updates(map[string]interface{}{
		"circuit_open_until": nil,
		"circuit_reason":     "",
	}).Error
}

func (r *AIProviderRepository) ListByUser(userID string) ([]model.AIProvider, error) {
	var rows []model.AIProvider
	if err := r.db.Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateConfigTx 在事务内按列写配置：catalog_id 不可改（更换目录归属等于换
// 提供商，应新建一条），密钥缺省不写。
func (r *AIProviderRepository) UpdateConfigTx(tx *gorm.DB, p *model.AIProvider, includeKey bool) error {
	cols := map[string]interface{}{
		"name":       p.Name,
		"base_url":   p.BaseURL,
		"updated_at": p.UpdatedAt,
	}
	if includeKey {
		cols["api_key_encrypted"] = p.APIKeyEncrypted
		cols["api_key_last4"] = p.APIKeyLast4
	}
	return tx.Model(&model.AIProvider{}).Where("id = ?", p.ID).Updates(cols).Error
}

// Delete 删除一行；行不存在返回 gorm.ErrRecordNotFound。
func (r *AIProviderRepository) Delete(id string) error {
	res := r.db.Where("id = ?", id).Delete(&model.AIProvider{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("ai provider not found")
	}
	return nil
}

// CountBotsByProviderIDs 一次查询统计每个提供商被多少机器人引用
// （列表 bot_count 与删除前置检查共用同一口径）。
func (r *AIProviderRepository) CountBotsByProviderIDs(ids []string) (map[string]int64, error) {
	out := make(map[string]int64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ProviderID string
		Count      int64
	}
	if err := r.db.Model(&model.Bot{}).
		Select("provider_id, COUNT(*) AS count").
		Where("provider_id IN (?)", ids).
		Group("provider_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ProviderID] = row.Count
	}
	return out, nil
}
