package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GitHubWebhookRedeliveryRepository 是 webhook 重投次数账的仓储（INT-146）。
// 走调用方传入的 tx/db，与审查调度共用「MaxOpenConns=1、事务内只碰 tx」的纪律。
type GitHubWebhookRedeliveryRepository struct {
	db *gorm.DB
}

func NewGitHubWebhookRedeliveryRepository(db *gorm.DB) *GitHubWebhookRedeliveryRepository {
	return &GitHubWebhookRedeliveryRepository{db: db}
}

// RedeliveryState 同一 guid 在重投账里的状态快照。
type RedeliveryState struct {
	Attempts int
	// Closed 对应 PR 已确认关闭/合并（closed_at 非空）：对账终态，不再重投、
	// 不再发超限通知。
	Closed bool
}

// StatesFor 批量取 guids 的重投状态（一次 IN 查询；空 guids 直接返回空 map）。
func (r *GitHubWebhookRedeliveryRepository) StatesFor(db *gorm.DB, guids []string) (map[string]RedeliveryState, error) {
	out := map[string]RedeliveryState{}
	if len(guids) == 0 {
		return out, nil
	}
	var rows []model.GitHubWebhookRedelivery
	if err := db.Select("guid", "attempts", "closed_at").
		Where("guid IN ?", guids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.GUID] = RedeliveryState{Attempts: row.Attempts, Closed: row.ClosedAt != nil}
	}
	return out, nil
}

// RecordClosed 记 guid 终态：对应 PR 已关闭/合并，此后对账不再重投也不再发
// 超限通知。已存在行只补 closed_at（attempts 保留），无行则建一条只带 closed
// 标记的行；用 upsert 避免读改写竞争。
func (r *GitHubWebhookRedeliveryRepository) RecordClosed(db *gorm.DB, guid string, now time.Time) error {
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "guid"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"closed_at":  now,
			"updated_at": now,
		}),
	}).Create(&model.GitHubWebhookRedelivery{
		GUID:      guid,
		ClosedAt:  &now,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error
}

// RecordAttempt 记一次重投请求：guid 首次出现则建行（attempts=1），否则累加。
// 用 upsert（OnConflict 的 DoUpdates 里 attempts+1）避免读改写竞争。
func (r *GitHubWebhookRedeliveryRepository) RecordAttempt(db *gorm.DB, guid string, now time.Time) error {
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "guid"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"attempts":   gorm.Expr("github_webhook_redeliveries.attempts + 1"),
			"updated_at": now,
		}),
	}).Create(&model.GitHubWebhookRedelivery{
		GUID:      guid,
		Attempts:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error
}

// DeleteOlderThan 清理久未更新的行（与 delivery 幂等账同保留期）。
func (r *GitHubWebhookRedeliveryRepository) DeleteOlderThan(db *gorm.DB, cutoff time.Time) (int64, error) {
	res := db.Where("updated_at < ?", cutoff).Delete(&model.GitHubWebhookRedelivery{})
	return res.RowsAffected, res.Error
}
