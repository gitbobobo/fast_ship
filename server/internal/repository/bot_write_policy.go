package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotWritePolicyRuleRepository 管分级放行规则的独立开关与误判计数（INT-160）。
// 行不存在即启用：读路径按「缺行=启用」解释，写路径才落行。
type BotWritePolicyRuleRepository struct {
	db *gorm.DB
}

func NewBotWritePolicyRuleRepository(db *gorm.DB) *BotWritePolicyRuleRepository {
	return &BotWritePolicyRuleRepository{db: db}
}

// GetTx 事务内读单条规则；无行返回 gorm.ErrRecordNotFound（调用方按启用解释）。
func (r *BotWritePolicyRuleRepository) GetTx(tx *gorm.DB, botID, ruleID string) (*model.BotWritePolicyRule, error) {
	var row model.BotWritePolicyRule
	if err := tx.Where("bot_id = ? AND rule_id = ?", botID, ruleID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ListByBot 读某机器人的全部规则行（只覆盖显式落过行的规则）。
func (r *BotWritePolicyRuleRepository) ListByBot(botID string) ([]model.BotWritePolicyRule, error) {
	var rows []model.BotWritePolicyRule
	err := r.db.Where("bot_id = ?", botID).Find(&rows).Error
	return rows, err
}

// UpsertEnabled 写开关与误判状态（读-改-写；SQLite 单写者，调用方保证
// 同一 (bot, rule) 的并发修改只有检测器与用户开关两处，且都以最新行为准）。
func (r *BotWritePolicyRuleRepository) UpsertEnabled(row *model.BotWritePolicyRule) error {
	return r.db.Save(row).Error
}

// UpsertEnabledTx 事务版写开关与误判状态：误判检测的「读行 → 计数推进 →
// 关闭落库 → 通知落库」必须整体在一个事务里，否则并发丢更新、或崩溃把
// 关闭与通知拆成两半（已关规则直接 return，通知就永远丢了）。
func (r *BotWritePolicyRuleRepository) UpsertEnabledTx(tx *gorm.DB, row *model.BotWritePolicyRule) error {
	return tx.Save(row).Error
}
