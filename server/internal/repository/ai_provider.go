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

// OpenCircuit 打开定时任务熔断（INT-137）：until 之前该提供商的定时任务到点
// 直接记 skipped。reason 记失败分类码（见 bot_model_errors.go）。窗口已开着
// 时保持原 until 不变（不延长、不换去重键）——同一熔断窗口内的并行/重试失败
// 不得产生新的去重键或第二条主线事件；返回是否真正打开了本次窗口。
// configNotNewerThan 把「这次失败针对的是当前配置」写进同一个 WHERE：检查与
// 写入原子，用户在本 run 开始后换过密钥/地址（含清除熔断）时，迟到的旧凭证
// 失败不会重新熔断新配置。
func (r *AIProviderRepository) OpenCircuit(id string, until time.Time, reason string, configNotNewerThan time.Time) (bool, error) {
	res := r.db.Model(&model.AIProvider{}).
		Where("id = ? AND (circuit_open_until IS NULL OR circuit_open_until <= ?) AND (config_version_at IS NULL OR config_version_at <= ?)",
			id, time.Now().UTC(), configNotNewerThan.UTC()).
		Updates(map[string]interface{}{
			"circuit_open_until": until,
			"circuit_reason":     reason,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
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
// 提供商，应新建一条），密钥缺省不写。credsChanged 为真时同步推进
// config_version_at——熔断守卫据此判断「这次失败针对的是不是当前配置」。
func (r *AIProviderRepository) UpdateConfigTx(tx *gorm.DB, p *model.AIProvider, includeKey, credsChanged bool) error {
	cols := map[string]interface{}{
		"name":       p.Name,
		"base_url":   p.BaseURL,
		"updated_at": p.UpdatedAt,
	}
	if includeKey {
		cols["api_key_encrypted"] = p.APIKeyEncrypted
		cols["api_key_last4"] = p.APIKeyLast4
	}
	if credsChanged {
		cols["config_version_at"] = p.UpdatedAt
	}
	return tx.Model(&model.AIProvider{}).Where("id = ?", p.ID).Updates(cols).Error
}

// MarkConfigVerified 记录「当前配置在此时刻被验证可用」（INT-137）：测试连接
// 用与库存一致的密钥探通时调用，同时清除熔断。条件更新把「验证的是哪份配置」
// 写进 WHERE：探测期间用户改了地址或换了凭证，这次结果就作废（0 行），不会
// 把另一份未经探测的配置标成已验证。expectedConfigAt 为 nil 时只认还没有
// 版本号的旧记录。
func (r *AIProviderRepository) MarkConfigVerified(id, expectedBaseURL string, expectedConfigAt *time.Time, now time.Time) (bool, error) {
	q := r.db.Model(&model.AIProvider{}).Where("id = ? AND base_url = ?", id, expectedBaseURL)
	if expectedConfigAt == nil {
		q = q.Where("config_version_at IS NULL")
	} else {
		q = q.Where("config_version_at IS ?", *expectedConfigAt)
	}
	res := q.Updates(map[string]interface{}{
		"circuit_open_until": nil,
		"circuit_reason":     "",
		"config_version_at":  now,
	})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
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
