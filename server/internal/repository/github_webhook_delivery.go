package repository

import (
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// GitHubWebhookDeliveryRepository 是 webhook 幂等账的仓储（原属
// github_pr_review.go，INT-135 起独立成文件）。走调用方传入的 tx/db，
// 与审查调度共用「MaxOpenConns=1、事务内只碰 tx」的纪律。
type GitHubWebhookDeliveryRepository struct {
	db *gorm.DB
}

func NewGitHubWebhookDeliveryRepository(db *gorm.DB) *GitHubWebhookDeliveryRepository {
	return &GitHubWebhookDeliveryRepository{db: db}
}

// InsertTx 插入 delivery 幂等账；撞 PK（重复投递）返回 false 且不视为错误。
func (r *GitHubWebhookDeliveryRepository) InsertTx(tx *gorm.DB, d *model.GitHubWebhookDelivery) (bool, error) {
	err := tx.Create(d).Error
	if err != nil && isUniqueConstraintError(err) {
		return false, nil
	}
	return err == nil, err
}

// isUniqueConstraintError SQLite 唯一约束冲突（本包不引 service 层的同名判定）。
func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// DeleteOlderThanTx 清理 7 天前的 delivery 行（调度循环顺带执行）。
func (r *GitHubWebhookDeliveryRepository) DeleteOlderThanTx(tx *gorm.DB, cutoff time.Time) (int64, error) {
	res := tx.Where("received_at < ?", cutoff).Delete(&model.GitHubWebhookDelivery{})
	return res.RowsAffected, res.Error
}
