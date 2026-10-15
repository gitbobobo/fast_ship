package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type BotRunRepository struct {
	db *gorm.DB
}

func NewBotRunRepository(db *gorm.DB) *BotRunRepository {
	return &BotRunRepository{db: db}
}

// CreateTx 在事务内创建 run；部分唯一索引保证同一 bot 至多一个未结束 run，
// 撞唯一 (bot_id, trigger_key) 说明触发已受理过。
func (r *BotRunRepository) CreateTx(tx *gorm.DB, run *model.BotRun) error {
	return tx.Create(run).Error
}

func (r *BotRunRepository) FindByID(id string) (*model.BotRun, error) {
	var run model.BotRun
	if err := r.db.Where("id = ?", id).First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// FindActiveByBot 返回该 bot 唯一的未结束 run；没有返回 gorm.ErrRecordNotFound。
func (r *BotRunRepository) FindActiveByBot(botID string) (*model.BotRun, error) {
	var run model.BotRun
	if err := r.db.Where("bot_id = ? AND finished_at IS NULL", botID).
		First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *BotRunRepository) FindByTriggerKeyTx(tx *gorm.DB, botID, triggerKey string) (*model.BotRun, error) {
	var run model.BotRun
	if err := tx.Where("bot_id = ? AND trigger_key = ?", botID, triggerKey).
		First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *BotRunRepository) ListByBot(botID string, limit int) ([]model.BotRun, error) {
	var rows []model.BotRun
	err := r.db.Where("bot_id = ?", botID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r *BotRunRepository) ListActive() ([]model.BotRun, error) {
	var rows []model.BotRun
	err := r.db.Where("finished_at IS NULL").Find(&rows).Error
	return rows, err
}

// UpdateColumns 条件更新 run 列；返回是否命中（0 行 = 状态已被别人落定）。
// where 值为 nil 渲染成 IS NULL（= NULL 永假，会让终态判定失灵）。
func (r *BotRunRepository) UpdateColumns(runID string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	q := r.db.Model(&model.BotRun{}).Where("id = ?", runID)
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

// UpdateColumnsWhereTx 事务版条件更新，供完成前的原子检查-落定闭包使用。
func (r *BotRunRepository) UpdateColumnsWhereTx(tx *gorm.DB, runID string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	q := tx.Model(&model.BotRun{}).Where("id = ?", runID)
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

// BotModelRequestRepository 记录每次真正发往模型的请求。
type BotModelRequestRepository struct {
	db *gorm.DB
}

func NewBotModelRequestRepository(db *gorm.DB) *BotModelRequestRepository {
	return &BotModelRequestRepository{db: db}
}

// NextOrdinalTx 在事务内取 run 内请求序号。
func (r *BotModelRequestRepository) NextOrdinalTx(tx *gorm.DB, runID string) (int64, error) {
	var ordinal int64
	if err := tx.Model(&model.BotModelRequest{}).Where("run_id = ?", runID).
		Select("COALESCE(MAX(ordinal), 0)").Scan(&ordinal).Error; err != nil {
		return 0, err
	}
	return ordinal + 1, nil
}

func (r *BotModelRequestRepository) CreateTx(tx *gorm.DB, req *model.BotModelRequest) error {
	return tx.Create(req).Error
}

func (r *BotModelRequestRepository) FindByID(id string) (*model.BotModelRequest, error) {
	var row model.BotModelRequest
	if err := r.db.Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *BotModelRequestRepository) ListByRun(runID string) ([]model.BotModelRequest, error) {
	var rows []model.BotModelRequest
	err := r.db.Where("run_id = ?", runID).Order("ordinal ASC").Find(&rows).Error
	return rows, err
}

// UpdateColumns 条件更新请求记录。
func (r *BotModelRequestRepository) UpdateColumns(id string, where map[string]interface{}, cols map[string]interface{}) (bool, error) {
	q := r.db.Model(&model.BotModelRequest{}).Where("id = ?", id)
	for col, val := range where {
		q = q.Where(col+" = ?", val)
	}
	res := q.Updates(cols)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListUnfinished 返回状态仍为 running 的模型请求（启动恢复用）。
func (r *BotModelRequestRepository) ListUnfinished() ([]model.BotModelRequest, error) {
	var rows []model.BotModelRequest
	err := r.db.Where("status = ?", model.BotModelRequestRunning).Find(&rows).Error
	return rows, err
}

// BotEventRepository 是 SSE 持久重放账本的仓库：只追加、按 seq 读取。
type BotEventRepository struct {
	db *gorm.DB
}

func NewBotEventRepository(db *gorm.DB) *BotEventRepository {
	return &BotEventRepository{db: db}
}

func (r *BotEventRepository) AppendTx(tx *gorm.DB, event *model.BotEvent) error {
	return tx.Create(event).Error
}

func (r *BotEventRepository) ListAfterSeq(botID string, after int64, limit int) ([]model.BotEvent, error) {
	var rows []model.BotEvent
	err := r.db.Where("bot_id = ? AND seq > ?", botID, after).
		Order("seq ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r *BotEventRepository) MaxSeq(botID string) (int64, error) {
	var seq int64
	err := r.db.Model(&model.BotEvent{}).Where("bot_id = ?", botID).
		Select("COALESCE(MAX(seq), 0)").Scan(&seq).Error
	return seq, err
}
