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

// AttemptsFor 批量取已重投次数（一次 IN 查询；空 guids 直接返回空 map）。
func (r *GitHubWebhookRedeliveryRepository) AttemptsFor(db *gorm.DB, guids []string) (map[string]int, error) {
	out := map[string]int{}
	if len(guids) == 0 {
		return out, nil
	}
	var rows []model.GitHubWebhookRedelivery
	if err := db.Select("guid", "attempts").
		Where("guid IN ?", guids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.GUID] = row.Attempts
	}
	return out, nil
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
