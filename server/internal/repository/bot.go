package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ErrNotFound 统一映射 gorm.ErrRecordNotFound，便于 service 层判断。
var ErrNotFound = gorm.ErrRecordNotFound

type BotRepository struct {
	db *gorm.DB
}

func NewBotRepository(db *gorm.DB) *BotRepository {
	return &BotRepository{db: db}
}

func (r *BotRepository) Create(bot *model.Bot) error {
	return r.db.Create(bot).Error
}

// UpdateConfig 只写配置类列，不碰水位；调用方先加载再改。toolset_version
// 是运行时激活水位（只在压缩/重启边界切换），这里不回写——并发编辑不得把
// 已激活的新版本覆盖回旧值（INT-102）。
func (r *BotRepository) UpdateConfig(bot *model.Bot) error {
	return r.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"name":                    bot.Name,
		"persona_prompt":          bot.PersonaPrompt,
		"system_prompt_text":      bot.SystemPromptText,
		"prompt_revision":         bot.PromptRevision,
		"platform_prompt_version": bot.PlatformPromptVersion,
		"base_url":                bot.BaseURL,
		"model":                   bot.Model,
		"api_key_encrypted":       bot.APIKeyEncrypted,
		"api_key_last4":           bot.APIKeyLast4,
		"updated_at":              bot.UpdatedAt,
	}).Error
}

// ActivateToolsetTx 在事务内把激活水位推进到目标工具/平台版本并按当前人设
// 重建平台规则（存量提示词里写着「记忆不可用」，只改常量不会更新存量正文；
// 必须在压缩/重启边界重建、递增 revision 再冻结）。返回是否命中。
func (r *BotRepository) ActivateToolsetTx(tx *gorm.DB, botID string, toolsetVersion, platformPromptVersion int64, systemPromptText string, promptRevision int64, now time.Time) (bool, error) {
	res := tx.Model(&model.Bot{}).
		Where("id = ? AND (toolset_version < ? OR platform_prompt_version < ?)", botID, toolsetVersion, platformPromptVersion).
		Updates(map[string]interface{}{
			"toolset_version":         toolsetVersion,
			"platform_prompt_version": platformPromptVersion,
			"system_prompt_text":      systemPromptText,
			"prompt_revision":         promptRevision,
			"updated_at":              now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// TellToolsetVersionTx 在事务内推进告知水位（CAS 旧水位，防止并发重复告知）。
func (r *BotRepository) TellToolsetVersionTx(tx *gorm.DB, botID string, fromVersion, toVersion int64) (bool, error) {
	res := tx.Model(&model.Bot{}).
		Where("id = ? AND last_told_version = ?", botID, fromVersion).
		Update("last_told_version", toVersion)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func (r *BotRepository) Get(id string) (*model.Bot, error) {
	var bot model.Bot
	if err := r.db.Where("id = ?", id).First(&bot).Error; err != nil {
		return nil, err
	}
	return &bot, nil
}

func (r *BotRepository) FindByIDAndUser(id, userID string) (*model.Bot, error) {
	var bot model.Bot
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&bot).Error; err != nil {
		return nil, err
	}
	return &bot, nil
}

func (r *BotRepository) ListByUser(userID string) ([]model.Bot, error) {
	var bots []model.Bot
	if err := r.db.Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&bots).Error; err != nil {
		return nil, err
	}
	return bots, nil
}

func (r *BotRepository) Delete(id string) error {
	res := r.db.Where("id = ?", id).Delete(&model.Bot{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("bot not found")
	}
	return nil
}

// NextMessageSeqTx 在事务内取号并推进水位。SQLite 没有 FOR UPDATE：生产库
// SetMaxOpenConns(1) 下事务天然串行，读-改-写放在同一短事务内即安全。
func (r *BotRepository) NextMessageSeqTx(tx *gorm.DB, botID string) (int64, error) {
	var seq int64
	if err := tx.Raw("SELECT next_message_seq FROM bots WHERE id = ?", botID).Scan(&seq).Error; err != nil {
		return 0, err
	}
	if seq == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	if err := tx.Exec("UPDATE bots SET next_message_seq = next_message_seq + 1 WHERE id = ?", botID).Error; err != nil {
		return 0, err
	}
	return seq, nil
}

// NextEventSeqTx 同 NextMessageSeqTx，作用于 SSE 事件水位。
func (r *BotRepository) NextEventSeqTx(tx *gorm.DB, botID string) (int64, error) {
	var seq int64
	if err := tx.Raw("SELECT next_event_seq FROM bots WHERE id = ?", botID).Scan(&seq).Error; err != nil {
		return 0, err
	}
	if seq == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	if err := tx.Exec("UPDATE bots SET next_event_seq = next_event_seq + 1 WHERE id = ?", botID).Error; err != nil {
		return 0, err
	}
	return seq, nil
}

// SetOfferedInputSeq 记录已交给某次模型请求的输入水位。
func (r *BotRepository) SetOfferedInputSeq(botID string, seq int64) error {
	return r.db.Model(&model.Bot{}).Where("id = ?", botID).
		Update("offered_input_seq", seq).Error
}
