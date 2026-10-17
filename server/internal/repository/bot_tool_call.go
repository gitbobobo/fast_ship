package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotToolCallRepository 管调用账本：创建先于执行，状态迁移全部用条件 UPDATE，
// 保证同一 call 只有一次认领、一个终态。
type BotToolCallRepository struct {
	db *gorm.DB
}

func NewBotToolCallRepository(db *gorm.DB) *BotToolCallRepository {
	return &BotToolCallRepository{db: db}
}

func (r *BotToolCallRepository) CreateTx(tx *gorm.DB, call *model.BotToolCall) error {
	return tx.Create(call).Error
}

func (r *BotToolCallRepository) FindByID(callID string) (*model.BotToolCall, error) {
	var row model.BotToolCall
	if err := r.db.Where("id = ?", callID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *BotToolCallRepository) FindByIDTx(tx *gorm.DB, callID string) (*model.BotToolCall, error) {
	var row model.BotToolCall
	if err := tx.Where("id = ?", callID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *BotToolCallRepository) ListByBot(botID string, statuses []string, limit int) ([]model.BotToolCall, error) {
	q := r.db.Where("bot_id = ?", botID)
	if len(statuses) > 0 {
		q = q.Where("status IN (?)", statuses)
	}
	var rows []model.BotToolCall
	err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListQueued 供后台 worker 扫描待执行调用。
func (r *BotToolCallRepository) ListQueued(limit int) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := r.db.Where("status = ?", model.BotCallStatusQueued).
		Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListPendingConfirmation 列出等待用户确认的调用。
func (r *BotToolCallRepository) ListPendingConfirmation(botID string, limit int) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := r.db.Where("bot_id = ? AND status = ?", botID, model.BotCallStatusPendingConfirmation).
		Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListUnfinishedByBot 列出本机器人全部未终态调用（快照收录：数量有界且
// 必须可操作）。
func (r *BotToolCallRepository) ListUnfinishedByBot(botID string, limit int) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := r.db.Where("bot_id = ? AND finished_at IS NULL", botID).
		Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// CountUnsettledByRun 统计某次运行里还没落终态的工具调用（调用账本没有 run 列，
// 经它所属的模型请求关联到 run）。schedule 会话必须等自己的异步写结算后才能
// 收尾：调用结果（可能失败）是本次运行结果的一部分。
func (r *BotToolCallRepository) CountUnsettledByRun(runID string) (int64, error) {
	var n int64
	err := r.db.Model(&model.BotToolCall{}).
		Joins("JOIN bot_model_requests ON bot_model_requests.id = bot_tool_calls.request_id").
		Where("bot_model_requests.run_id = ? AND bot_tool_calls.finished_at IS NULL", runID).
		Count(&n).Error
	return n, err
}

// ListUnfinishedByBotPage 带偏移分页（快照取全用；offset 语义按同序稳定）。
func (r *BotToolCallRepository) ListUnfinishedByBotPage(botID string, offset, limit int) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := r.db.Where("bot_id = ? AND finished_at IS NULL", botID).
		Order("created_at ASC, id ASC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, err
}

// ListQueuedSync 全局分页读排队中的同步调用（启动恢复用：后台扫描只认领
// 异步调用，这些调用只能由恢复补终态）。
func (r *BotToolCallRepository) ListQueuedSync(limit int) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := r.db.Where("status = ? AND execution_mode = ?",
		model.BotCallStatusQueued, model.BotExecutionSync).
		Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListUnfinished 返回所有未到终态的调用（启动恢复与超时扫描用）。
func (r *BotToolCallRepository) ListUnfinished() ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := r.db.Where("finished_at IS NULL AND status NOT IN (?)",
		[]model.BotToolCallStatus{model.BotCallStatusPendingConfirmation, model.BotCallStatusQueued}).
		Find(&rows).Error
	return rows, err
}

// ClaimQueuedTx CAS 认领 queued→running；同一 call 只有一个 worker 能认领。
func (r *BotToolCallRepository) ClaimQueuedTx(tx *gorm.DB, callID string, startedAt time.Time) (bool, error) {
	res := tx.Model(&model.BotToolCall{}).
		Where("id = ? AND status = ?", callID, model.BotCallStatusQueued).
		Updates(map[string]interface{}{"status": model.BotCallStatusRunning, "started_at": startedAt})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateWhere 条件更新调用列；fromStatus 为空表示不限原状态。
// cols 中值为 nil 写 NULL（清空列），不参与 WHERE。
func (r *BotToolCallRepository) UpdateWhere(callID string, fromStatus string, cols map[string]interface{}) (bool, error) {
	q := r.db.Model(&model.BotToolCall{}).Where("id = ?", callID)
	if fromStatus != "" {
		q = q.Where("status = ?", fromStatus)
	}
	res := q.Updates(cols)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateWhereTx 事务版条件更新（确认决定在事务内 CAS）。
func (r *BotToolCallRepository) UpdateWhereTx(tx *gorm.DB, callID string, fromStatus string, cols map[string]interface{}) (bool, error) {
	q := tx.Model(&model.BotToolCall{}).Where("id = ?", callID)
	if fromStatus != "" {
		q = q.Where("status = ?", fromStatus)
	}
	res := q.Updates(cols)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// BackfillResultItems 为存量调用补记结果条数（首次折叠时解析 ResultJSON
// 补算）；只补 result_items 仍为 0 且结果非空的行。
func (r *BotToolCallRepository) BackfillResultItems(callID string, items int64) (bool, error) {
	res := r.db.Model(&model.BotToolCall{}).
		Where("id = ? AND result_items = 0", callID).
		Update("result_items", items)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// BotAuthorizationRepository 管结构化「始终允许」：撤销只写 revoked_at。
type BotAuthorizationRepository struct {
	db *gorm.DB
}

func NewBotAuthorizationRepository(db *gorm.DB) *BotAuthorizationRepository {
	return &BotAuthorizationRepository{db: db}
}

func (r *BotAuthorizationRepository) CreateTx(tx *gorm.DB, authz *model.BotAuthorization) error {
	return tx.Create(authz).Error
}

func (r *BotAuthorizationRepository) FindByID(authzID string) (*model.BotAuthorization, error) {
	var row model.BotAuthorization
	if err := r.db.Where("id = ?", authzID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *BotAuthorizationRepository) FindByIDAndBot(authzID, botID string) (*model.BotAuthorization, error) {
	var row model.BotAuthorization
	if err := r.db.Where("id = ? AND bot_id = ?", authzID, botID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *BotAuthorizationRepository) ListByBot(botID string, activeOnly bool) ([]model.BotAuthorization, error) {
	q := r.db.Where("bot_id = ?", botID)
	if activeOnly {
		q = q.Where("revoked_at IS NULL")
	}
	var rows []model.BotAuthorization
	err := q.Order("created_at DESC, id DESC").Find(&rows).Error
	return rows, err
}

// FindActiveRule 按规则哈希找活跃授权；供匹配与撤销使调用回退时使用。
func (r *BotAuthorizationRepository) FindActiveRuleTx(tx *gorm.DB, botID, ruleHash string) (*model.BotAuthorization, error) {
	var row model.BotAuthorization
	if err := tx.Where("bot_id = ? AND rule_hash = ? AND revoked_at IS NULL", botID, ruleHash).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Revoke 撤销（幂等：已撤销返回 false）。
func (r *BotAuthorizationRepository) Revoke(authzID string, revokedAt time.Time) (bool, error) {
	res := r.db.Model(&model.BotAuthorization{}).
		Where("id = ? AND revoked_at IS NULL", authzID).
		Update("revoked_at", revokedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
