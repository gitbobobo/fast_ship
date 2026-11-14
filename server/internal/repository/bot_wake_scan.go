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
		// 水位粗筛作附加条件（等价性）：账本约束下消息 seq ≤ next-1，故
		// 「存在 seq > offered 的输入」必然蕴含 offered < next-1。先算术排除
		// 已追平的机器人（从未聊过、输入全部已消费），这些行不再做
		// bot_messages 索引探测；命中集合不变（第 1 轮审查 Note）。
		Where("bots.offered_input_seq < bots.next_message_seq - 1").
		Where("EXISTS (?)", r.db.Model(&model.BotMessage{}).Select("1").
			Where("bot_messages.bot_id = bots.id").
			Where("bot_messages.session_kind = ?", model.BotSessionKindMain).
			Where("bot_messages.seq > bots.offered_input_seq").
			Where("bot_messages.kind IN (?)", botInputKinds())).
		Scan(&rows).Error
	return rows, err
}
