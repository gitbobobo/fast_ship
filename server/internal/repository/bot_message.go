package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotMessageRepository 只提供追加与读取：账本不可改写，没有 Update 方法。
type BotMessageRepository struct {
	db *gorm.DB
}

func NewBotMessageRepository(db *gorm.DB) *BotMessageRepository {
	return &BotMessageRepository{db: db}
}

// AppendTx 在事务内追加一条消息；调用方负责先取号。
func (r *BotMessageRepository) AppendTx(tx *gorm.DB, msg *model.BotMessage) error {
	return tx.Create(msg).Error
}

// MaxSeq 返回当前账本末位 seq；空账本返回 0。
func (r *BotMessageRepository) MaxSeq(botID string) (int64, error) {
	var seq int64
	err := r.db.Model(&model.BotMessage{}).Where("bot_id = ?", botID).
		Select("COALESCE(MAX(seq), 0)").Scan(&seq).Error
	return seq, err
}

// MaxSeqTx 同 MaxSeq，供事务闭包内的最终一致性检查使用。
func (r *BotMessageRepository) MaxSeqTx(tx *gorm.DB, botID string) (int64, error) {
	var seq int64
	err := tx.Model(&model.BotMessage{}).Where("bot_id = ?", botID).
		Select("COALESCE(MAX(seq), 0)").Scan(&seq).Error
	return seq, err
}

// botInputKinds 是会话内算「新输入」的消息类别：user / background_result /
// schedule_event（定时任务事件只出现在主线会话）。
func botInputKinds() []model.BotMessageKind {
	return []model.BotMessageKind{
		model.BotMessageKindUser,
		model.BotMessageKindBackgroundResult,
		model.BotMessageKindScheduleEvent,
	}
}

// HasModelInputSince 报告指定会话在水位之后是否存在新输入。runID 非空时再
// 按 run 收窄：schedule 会话按 run 隔离，同一机器人其他 schedule run 的消息
// 不算自己的新输入；主线传空串不收窄。
func (r *BotMessageRepository) HasModelInputSince(botID, sessionKind, runID string, seq int64) (bool, error) {
	q := r.db.Model(&model.BotMessage{}).Where(
		"bot_id = ? AND session_kind = ? AND seq > ? AND kind IN (?)", botID, sessionKind, seq,
		botInputKinds(),
	)
	if runID != "" {
		q = q.Where("run_id = ?", runID)
	}
	var count int64
	err := q.Count(&count).Error
	return count > 0, err
}

// HasModelInputSinceTx 同 HasModelInputSince，供完成前的原子检查使用。
func (r *BotMessageRepository) HasModelInputSinceTx(tx *gorm.DB, botID, sessionKind, runID string, seq int64) (bool, error) {
	q := tx.Model(&model.BotMessage{}).Where(
		"bot_id = ? AND session_kind = ? AND seq > ? AND kind IN (?)", botID, sessionKind, seq,
		botInputKinds(),
	)
	if runID != "" {
		q = q.Where("run_id = ?", runID)
	}
	var count int64
	err := q.Count(&count).Error
	return count > 0, err
}

// ListMainline 主线分页：页内一律 seq 正序，before/after 都是排他 seq 边界，
// 不用 offset。两边界都缺省（聊天首屏）与只给 before（「加载更早」）都取
// 距账本末尾最近的 limit 条后翻回正序；只给 after（补读）取该边界之后
// 最早的 limit 条。只返回主线消息，定时任务会话不对用户展示。
func (r *BotMessageRepository) ListMainline(botID string, before, after int64, limit int) ([]model.BotMessage, error) {
	q := r.db.Where("bot_id = ? AND session_kind = ?", botID, model.BotSessionKindMain)
	if before > 0 {
		q = q.Where("seq < ?", before)
	}
	if after > 0 {
		q = q.Where("seq > ?", after)
	}
	if after > 0 {
		var rows []model.BotMessage
		err := q.Order("seq ASC").Limit(limit).Find(&rows).Error
		return rows, err
	}
	var rows []model.BotMessage
	if err := q.Order("seq DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// ListAfterSeq 重放用：返回主线 seq 大于 after 的全部后续消息（正序）。
func (r *BotMessageRepository) ListAfterSeq(botID string, after int64, limit int) ([]model.BotMessage, error) {
	return r.ListSeqRange(botID, model.BotSessionKindMain, "", after, 0, limit)
}

// ListSeqRange 按会话与 seq 正序取 (after, through]；through<=0 表示无上界，
// runID 非空时再限定单次运行（定时任务会话按 run 隔离投影）。压缩与构建按
// 页读取，避免滑窗截掉尚未摘要的历史。
func (r *BotMessageRepository) ListSeqRange(botID, sessionKind, runID string, after, through int64, limit int) ([]model.BotMessage, error) {
	q := r.db.Where("bot_id = ? AND session_kind = ? AND seq > ?", botID, sessionKind, after)
	if runID != "" {
		q = q.Where("run_id = ?", runID)
	}
	if through > 0 {
		q = q.Where("seq <= ?", through)
	}
	var rows []model.BotMessage
	err := q.Order("seq ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// FindByClientMessageID 幂等键查找；未命中返回 gorm.ErrRecordNotFound。
func (r *BotMessageRepository) FindByClientMessageID(botID, clientMessageID string) (*model.BotMessage, error) {
	var row model.BotMessage
	if err := r.db.Where("bot_id = ? AND client_message_id = ?", botID, clientMessageID).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByDedupKey 按去重键查找（如 receipt:<call_id>）。
func (r *BotMessageRepository) FindByDedupKey(botID, dedupKey string) (*model.BotMessage, error) {
	var row model.BotMessage
	if err := r.db.Where("bot_id = ? AND dedup_key = ?", botID, dedupKey).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ListByCall 返回某次调用的全部关联消息（回执、结果、决定）。
func (r *BotMessageRepository) ListByCall(botID, callID string) ([]model.BotMessage, error) {
	var rows []model.BotMessage
	err := r.db.Where("bot_id = ? AND call_id = ?", botID, callID).
		Order("seq ASC").Find(&rows).Error
	return rows, err
}

// ListByRun 返回 run 的全部消息。
func (r *BotMessageRepository) ListByRun(botID, runID string) ([]model.BotMessage, error) {
	var rows []model.BotMessage
	err := r.db.Where("bot_id = ? AND run_id = ?", botID, runID).
		Order("seq ASC").Find(&rows).Error
	return rows, err
}

// LastAssistantOfRun 返回 run 内最后一条 assistant 消息；没有返回
// gorm.ErrRecordNotFound。定时任务收尾用它判定静默哨兵与结果摘要。
func (r *BotMessageRepository) LastAssistantOfRun(botID, runID string) (*model.BotMessage, error) {
	var row model.BotMessage
	if err := r.db.Where(
		"bot_id = ? AND run_id = ? AND session_kind = ? AND kind = ?",
		botID, runID, model.BotSessionKindSchedule, model.BotMessageKindAssistant,
	).Order("seq DESC").First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}
