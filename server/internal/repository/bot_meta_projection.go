package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
)

// 周期维护扫描的元数据投影（INT-228）：夜间扫描、预算评估与用量日汇总每
// 60 秒只需要 bot 的 id、归属用户时区与名字。这里集中放投影查询，调用方
// 不为这些元数据 Get 整行——整行读取会把已拼好的系统提示词与 provider
// 密文（APIKeyEncrypted）反复拷进内存。投影列不含任何密钥或提示词字段。

// BotOwnerTimezone 是夜间扫描候选行：bot id 与归属用户声明的时区。用户行
// 已随级联删除消失时 Timezone 为空串，调用方按未声明回退部署配置。
type BotOwnerTimezone struct {
	BotID    string
	Timezone string
}

// ListBotOwnerTimezones 一条查询取出全部 bot 的 id 与归属用户时区，按 bot
// 创建顺序返回。LEFT JOIN 保证用户行缺失不丢扫描候选。
func (r *BotMaintenanceRepository) ListBotOwnerTimezones() ([]BotOwnerTimezone, error) {
	var rows []BotOwnerTimezone
	err := r.db.Model(&model.Bot{}).
		Select("bots.id AS bot_id", "COALESCE(users.timezone, '') AS timezone").
		Joins("LEFT JOIN users ON users.id = bots.user_id").
		Order("bots.created_at ASC").
		Scan(&rows).Error
	return rows, err
}

// BotMeta 是预算评估与用量日汇总的目标行：id、归属用户与名字。
type BotMeta struct {
	ID     string
	UserID string
	Name   string
}

// BotMetasByIDs 按 id 批量取投影行，只查 id/user_id/name 三列；已删除的
// bot 不返回行，调用方按目标缺失跳过。
func (r *BotRepository) BotMetasByIDs(ids []string) ([]BotMeta, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []BotMeta
	err := r.db.Model(&model.Bot{}).
		Select("id", "user_id", "name").
		Where("id IN ?", ids).
		Scan(&rows).Error
	return rows, err
}
