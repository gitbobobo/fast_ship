package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
)

// BotWakeScanRow 是 5 秒扫描唤醒的投影：只取判定与触发键所需的最小列，
// 不加载 persona_prompt / system_prompt_text 等人设大字段（INT-227）。
type BotWakeScanRow struct {
	ID             string
	NextMessageSeq int64
}

// ListBotsWithUnconsumedInput 用一条 SQL 选出主线会话内仍存在 seq 大于
// offered_input_seq 的输入消息（user / background_result / schedule_event，
// 与 HasModelInputSince 同一口径）的机器人。后台结果等非输入消息也会推高
// 账本水位，裸水位比较会让完成过回复的空闲机器人永远被扫中空转；这里按
// 真实输入筛选，扫描是周期路径，投影必须保持最小。
func (r *BotRepository) ListBotsWithUnconsumedInput() ([]BotWakeScanRow, error) {
	var rows []BotWakeScanRow
	err := r.db.Table("bots").
		Select("bots.id, bots.next_message_seq").
		Where("EXISTS (?)", r.db.Model(&model.BotMessage{}).Select("1").
			Where("bot_messages.bot_id = bots.id").
			Where("bot_messages.session_kind = ?", model.BotSessionKindMain).
			Where("bot_messages.seq > bots.offered_input_seq").
			Where("bot_messages.kind IN (?)", botInputKinds())).
		Scan(&rows).Error
	return rows, err
}
