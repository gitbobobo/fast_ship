package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotMaintenanceRepository 是维护运行/请求的仓库：幂等创建（唯一
// (bot_id, kind, trigger_key)）、条件推进状态、请求单独记账。重启恢复把
// running 行置 failed(server_restarted)，不自动重放。

type BotMaintenanceRepository struct {
	db *gorm.DB
}

func NewBotMaintenanceRepository(db *gorm.DB) *BotMaintenanceRepository {
	return &BotMaintenanceRepository{db: db}
}

// CreateTx 在事务内创建维护运行；唯一索引冲突返回 gorm.ErrRecordNotFound
// 之外的唯一性错误（调用方以 errors.Is/As 判断按已存在处理）。
func (r *BotMaintenanceRepository) CreateTx(tx *gorm.DB, run *model.BotMaintenanceRun) error {
	return tx.Create(run).Error
}

// CreateOrFindTx 幂等创建：同 (bot, kind, trigger_key) 已存在时返回既有行
// （created=false），调用方据此跳过或续接。
func (r *BotMaintenanceRepository) CreateOrFindTx(tx *gorm.DB, run *model.BotMaintenanceRun) (*model.BotMaintenanceRun, bool, error) {
	if err := tx.Create(run).Error; err != nil {
		existing, findErr := r.FindByTriggerKeyTx(tx, run.BotID, string(run.Kind), run.TriggerKey)
		if findErr == nil {
			return existing, false, nil
		}
		return nil, false, err
	}
	return run, true, nil
}

// FindByTriggerKeyTx 按幂等键取维护运行。
func (r *BotMaintenanceRepository) FindByTriggerKeyTx(tx *gorm.DB, botID, kind, triggerKey string) (*model.BotMaintenanceRun, error) {
	var row model.BotMaintenanceRun
	if err := tx.Where("bot_id = ? AND kind = ? AND trigger_key = ?", botID, kind, triggerKey).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByID 取维护运行。
func (r *BotMaintenanceRepository) FindByID(id string) (*model.BotMaintenanceRun, error) {
	var row model.BotMaintenanceRun
	if err := r.db.Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateColumns 条件更新维护运行列（where 值 nil 渲染为 IS NULL）。
func (r *BotMaintenanceRepository) UpdateColumns(id string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	return updateColumnsGeneric(r.db, &model.BotMaintenanceRun{}, "id", id, where, cols)
}

// UpdateColumnsTx 事务版条件更新。
func (r *BotMaintenanceRepository) UpdateColumnsTx(tx *gorm.DB, id string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	return updateColumnsGeneric(tx, &model.BotMaintenanceRun{}, "id", id, where, cols)
}

// ListUnfinished 返回仍在 running 的维护运行（启动恢复用）。
func (r *BotMaintenanceRepository) ListUnfinished() ([]model.BotMaintenanceRun, error) {
	var rows []model.BotMaintenanceRun
	err := r.db.Where("status = ?", model.BotMaintenanceRunning).Find(&rows).Error
	return rows, err
}

// ListDueNightly 扫描候选：全部 bot（协调器再按本地日期与配置过滤）。
func (r *BotMaintenanceRepository) ListBotIDs() ([]string, error) {
	var ids []string
	err := r.db.Model(&model.Bot{}).Order("created_at ASC").Pluck("id", &ids).Error
	return ids, err
}

// FindLatestSucceeded 取某机器人某类维护运行最近一次成功记录（夜间整理消费水位）。
func (r *BotMaintenanceRepository) FindLatestSucceeded(botID string, kind model.BotMaintenanceKind) (*model.BotMaintenanceRun, error) {
	var row model.BotMaintenanceRun
	err := r.db.Where("bot_id = ? AND kind = ? AND status = ?", botID, kind, model.BotMaintenanceSucceeded).
		Order("created_at DESC").First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListSucceeded 列出某机器人某类全部成功维护运行（按时间升序，供累计消费水位）。
func (r *BotMaintenanceRepository) ListSucceeded(botID string, kind model.BotMaintenanceKind) ([]model.BotMaintenanceRun, error) {
	var rows []model.BotMaintenanceRun
	err := r.db.Where("bot_id = ? AND kind = ? AND status = ?", botID, kind, model.BotMaintenanceSucceeded).
		Order("created_at ASC").Find(&rows).Error
	return rows, err
}

// ---------- 维护请求 ----------

// NextMaintenanceOrdinalTx 在事务内取运行内请求序号。
func (r *BotMaintenanceRepository) NextMaintenanceOrdinalTx(tx *gorm.DB, runID string) (int64, error) {
	var ordinal int64
	if err := tx.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", runID).
		Select("COALESCE(MAX(ordinal), 0)").Scan(&ordinal).Error; err != nil {
		return 0, err
	}
	return ordinal + 1, nil
}

// CreateRequestTx 在事务内落一条维护模型请求。
func (r *BotMaintenanceRepository) CreateRequestTx(tx *gorm.DB, req *model.BotMaintenanceRequest) error {
	return tx.Create(req).Error
}

// UpdateRequestColumns 条件更新维护请求列。
func (r *BotMaintenanceRepository) UpdateRequestColumns(id string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	return updateColumnsGeneric(r.db, &model.BotMaintenanceRequest{}, "id", id, where, cols)
}

// ListUnfinishedRequests 返回仍 running 的维护请求（启动恢复用）。
func (r *BotMaintenanceRepository) ListUnfinishedRequests() ([]model.BotMaintenanceRequest, error) {
	var rows []model.BotMaintenanceRequest
	err := r.db.Where("status = ?", model.BotModelRequestRunning).Find(&rows).Error
	return rows, err
}

// ListRequestsByRun 列出维护运行的全部请求。
func (r *BotMaintenanceRepository) ListRequestsByRun(runID string) ([]model.BotMaintenanceRequest, error) {
	var rows []model.BotMaintenanceRequest
	err := r.db.Where("maintenance_run_id = ?", runID).Order("ordinal ASC").Find(&rows).Error
	return rows, err
}

// updateColumnsGeneric 是 bot_runs.UpdateColumns 的通用形态：条件更新 + 命中
// 判定，供维护账本复用。
func updateColumnsGeneric(db *gorm.DB, m interface{}, idCol string, id string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	q := db.Model(m).Where(idCol+" = ?", id)
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

// MarkFinishedCols 构造终态列。
func MarkFinishedCols(status model.BotMaintenanceStatus, now time.Time, errCode, errMsg string) map[string]interface{} {
	cols := map[string]interface{}{
		"status":      status,
		"finished_at": now,
	}
	if errCode != "" {
		cols["error_code"] = errCode
		cols["error_message"] = errMsg
	}
	return cols
}
