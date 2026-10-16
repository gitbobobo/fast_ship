package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotScheduleTaskRepository 定时任务（INT-103）的仓库：任务由机器人工具
// 维护，用户只改 enabled；调度扫描与推进都在短事务内完成。
type BotScheduleTaskRepository struct {
	db *gorm.DB
}

func NewBotScheduleTaskRepository(db *gorm.DB) *BotScheduleTaskRepository {
	return &BotScheduleTaskRepository{db: db}
}

func (r *BotScheduleTaskRepository) Create(task *model.BotScheduleTask) error {
	return r.db.Create(task).Error
}

func (r *BotScheduleTaskRepository) FindByID(id string) (*model.BotScheduleTask, error) {
	var task model.BotScheduleTask
	if err := r.db.Where("id = ?", id).First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// FindByIDAndBot 限定归属的读取；未命中返回 gorm.ErrRecordNotFound。
func (r *BotScheduleTaskRepository) FindByIDAndBot(id, botID string) (*model.BotScheduleTask, error) {
	var task model.BotScheduleTask
	if err := r.db.Where("id = ? AND bot_id = ?", id, botID).First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// ListByBot 按创建时间新→旧列出任务。
func (r *BotScheduleTaskRepository) ListByBot(botID string, limit int) ([]model.BotScheduleTask, error) {
	var rows []model.BotScheduleTask
	err := r.db.Where("bot_id = ?", botID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// UpdateColumns 条件更新任务列；返回是否命中。
func (r *BotScheduleTaskRepository) UpdateColumns(taskID string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	q := r.db.Model(&model.BotScheduleTask{}).Where("id = ?", taskID)
	for col, val := range where {
		if val == nil {
			q = q.Where(col + " IS NULL")
		} else {
			q = q.Where(col+" = ?", val)
		}
	}
	res := q.Updates(cols)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Delete 删除任务；未命中返回 gorm.ErrRecordNotFound（与 FindByIDAndBot 一致，
// 服务层据此映射为「定时任务不存在」）。运行记录不级联（bot_runs.schedule_task_id
// 无外键，账本保留供排查）。
func (r *BotScheduleTaskRepository) Delete(botID, taskID string) error {
	res := r.db.Where("id = ? AND bot_id = ?", taskID, botID).Delete(&model.BotScheduleTask{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListDue 扫描已启用且到期的任务（短事务外粗筛用；逐任务在事务内复核）。
func (r *BotScheduleTaskRepository) ListDue(now time.Time, limit int) ([]model.BotScheduleTask, error) {
	var rows []model.BotScheduleTask
	err := r.db.Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
		Order("next_run_at ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// AdvanceNextRunTx 在事务内 CAS 推进 next_run_at（0 行 = 被并发抢走）。
func (r *BotScheduleTaskRepository) AdvanceNextRunTx(tx *gorm.DB, taskID string, planned, next time.Time) (bool, error) {
	res := tx.Model(&model.BotScheduleTask{}).
		Where("id = ? AND next_run_at = ?", taskID, planned).
		Update("next_run_at", next)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// PauseTx 调度解析失败时暂停任务（next_run_at 置空），不无限循环。
func (r *BotScheduleTaskRepository) PauseTx(tx *gorm.DB, taskID string) error {
	return tx.Model(&model.BotScheduleTask{}).Where("id = ?", taskID).
		Update("next_run_at", nil).Error
}
