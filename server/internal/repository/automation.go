package repository

import (
	"context"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// 自动化（INT-90）四张表的仓储。调度/回报路径大量使用条件 UPDATE（影响 0 行
// 即并发已变更），MaxOpenConns=1 下事务内只允许传 tx 进来的方法。

type AutomationRepository struct {
	db *gorm.DB
}

func NewAutomationRepository(db *gorm.DB) *AutomationRepository {
	return &AutomationRepository{db: db}
}

func (r *AutomationRepository) Create(ctx context.Context, a *model.Automation) error {
	return r.db.WithContext(ctx).Create(a).Error
}

func (r *AutomationRepository) FindByIDAndUserID(ctx context.Context, id, userID string) (*model.Automation, error) {
	var a model.Automation
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AutomationRepository) ListByUserID(ctx context.Context, userID string) ([]model.Automation, error) {
	var items []model.Automation
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC, id DESC").
		Find(&items).Error
	return items, err
}

// AutomationUpdate 是一次配置更新的列集合：nil 字段不写（保留现值）。
// NextRunAt 由 service 依据 cron 重算后传入。
type AutomationUpdate struct {
	Name                *string
	Enabled             *bool
	EnvironmentID       *string
	T3ProjectID         *string
	TriggerType         *model.AutomationTriggerType
	CronExpression      *string
	Timezone            *string
	NextRunAt           *time.Time
	IssueProjectID      **string // 双指针：nil=不改；*nil=置 NULL；**非 nil=写入值
	EventFilterJSON     *string
	PromptTemplate      *string
	ModelSelectionJSON  *string
	ModelSelectionsJSON *string // GitHub PR 多模型（INT-93）
	DebounceSeconds     *int
	RuntimeMode         *string
	WorkspaceJSON       *string
	TimeoutSeconds      *int
	PostAction          *model.AutomationPostAction
	PostOnTimeout       *bool
	UpdatedAt           time.Time
}

// Update 条件 UPDATE（WHERE id + user_id），影响 0 行返回 gorm.ErrRecordNotFound。
func (r *AutomationRepository) Update(ctx context.Context, id, userID string, update *AutomationUpdate) error {
	columns := map[string]any{"updated_at": update.UpdatedAt}
	if update.Name != nil {
		columns["name"] = *update.Name
	}
	if update.Enabled != nil {
		columns["enabled"] = *update.Enabled
	}
	if update.EnvironmentID != nil {
		columns["environment_id"] = *update.EnvironmentID
	}
	if update.T3ProjectID != nil {
		columns["t3_project_id"] = *update.T3ProjectID
	}
	if update.TriggerType != nil {
		columns["trigger_type"] = *update.TriggerType
	}
	if update.CronExpression != nil {
		columns["cron_expression"] = *update.CronExpression
	}
	if update.Timezone != nil {
		columns["timezone"] = *update.Timezone
	}
	if update.NextRunAt != nil {
		columns["next_run_at"] = *update.NextRunAt
	} else if update.TriggerType != nil || update.CronExpression != nil {
		// 触发配置变化但未给新调度时间：置空等待 service 层重算（不应发生，防御）。
		columns["next_run_at"] = nil
	}
	if update.IssueProjectID != nil {
		columns["issue_project_id"] = *update.IssueProjectID
	}
	if update.EventFilterJSON != nil {
		columns["event_filter_json"] = *update.EventFilterJSON
	}
	if update.PromptTemplate != nil {
		columns["prompt_template"] = *update.PromptTemplate
	}
	if update.ModelSelectionJSON != nil {
		columns["model_selection_json"] = *update.ModelSelectionJSON
	}
	if update.ModelSelectionsJSON != nil {
		columns["model_selections_json"] = *update.ModelSelectionsJSON
	}
	if update.DebounceSeconds != nil {
		columns["debounce_seconds"] = *update.DebounceSeconds
	}
	if update.RuntimeMode != nil {
		columns["runtime_mode"] = *update.RuntimeMode
	}
	if update.WorkspaceJSON != nil {
		columns["workspace_json"] = *update.WorkspaceJSON
	}
	if update.TimeoutSeconds != nil {
		columns["timeout_seconds"] = *update.TimeoutSeconds
	}
	if update.PostAction != nil {
		columns["post_action"] = *update.PostAction
	}
	if update.PostOnTimeout != nil {
		columns["post_on_timeout"] = *update.PostOnTimeout
	}
	res := r.db.WithContext(ctx).Model(&model.Automation{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(columns)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *AutomationRepository) Delete(ctx context.Context, id, userID string) error {
	return r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&model.Automation{}).Error
}

// HasActiveRunTx 判断自动化是否有活动运行（未结束，或后置操作还挂起）。
// 供删除守卫与事件触发重入判定共用；必须传当前事务的 tx。
func HasActiveRunTx(tx *gorm.DB, automationID string) (bool, error) {
	var n int64
	err := tx.Model(&model.AutomationRun{}).
		Where("automation_id = ? AND (finished_at IS NULL OR post_state = ?)", automationID, model.AutomationPostStatePending).
		Limit(1).
		Count(&n).Error
	return n > 0, err
}

// ListDueCronAutTx 取到期的 cron 自动化（含期望的 next_run_at 供乐观核对）。
func ListDueCronAutTx(db *gorm.DB, now time.Time, limit int) ([]model.Automation, error) {
	var items []model.Automation
	err := db.
		Where("enabled = ? AND trigger_type = ? AND next_run_at IS NOT NULL AND next_run_at <= ?",
			true, model.AutomationTriggerCron, now).
		Order("next_run_at ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

type AutomationRunRepository struct {
	db *gorm.DB
}

func NewAutomationRunRepository(db *gorm.DB) *AutomationRunRepository {
	return &AutomationRunRepository{db: db}
}

func (r *AutomationRunRepository) CreateInTx(tx *gorm.DB, run *model.AutomationRun) error {
	return tx.Create(run).Error
}

func (r *AutomationRunRepository) FindByIDAndUserID(ctx context.Context, id, userID string) (*model.AutomationRun, error) {
	var run model.AutomationRun
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *AutomationRunRepository) FindByID(ctx context.Context, id string) (*model.AutomationRun, error) {
	var run model.AutomationRun
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *AutomationRunRepository) ListByAutomationID(ctx context.Context, automationID string, limit int) ([]model.AutomationRun, error) {
	var items []model.AutomationRun
	err := r.db.WithContext(ctx).
		Where("automation_id = ?", automationID).
		Order("triggered_at DESC, id DESC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

func (r *AutomationRunRepository) UpdateColumns(ctx context.Context, runID string, columns map[string]any) error {
	return r.db.WithContext(ctx).Model(&model.AutomationRun{}).
		Where("id = ?", runID).
		Updates(columns).Error
}

// UpdateColumnsWhere 根 db 上的条件更新：scheduler/回报路径的认领、终态流转
// 全靠它；返回影响行数（0=条件未命中，状态已被并发改写）。
func (r *AutomationRunRepository) UpdateColumnsWhere(ctx context.Context, runID, where string, args []any, columns map[string]any) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.AutomationRun{}).
		Where("id = ?", runID).
		Where(where, args...).
		Updates(columns)
	return res.RowsAffected, res.Error
}

// UpdateColumnsWhereTx 在给定事务内条件更新运行列，返回影响行数（0=状态已被并发改写）。
func (r *AutomationRunRepository) UpdateColumnsWhereTx(tx *gorm.DB, runID string, where string, args []any, columns map[string]any) (int64, error) {
	res := tx.Model(&model.AutomationRun{}).
		Where("id = ?", runID).
		Where(where, args...).
		Updates(columns)
	return res.RowsAffected, res.Error
}

// ListClaimableTx 取待认领（status 为空串）的运行，交 worker 条件认领。
func (r *AutomationRunRepository) ListClaimableTx(db *gorm.DB, limit int) ([]model.AutomationRun, error) {
	var items []model.AutomationRun
	err := db.
		Where("status = ?", string(model.RunStatusPendingRun)).
		Order("triggered_at ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

// ListActivePastDeadlineTx 取已到回报截止的活动运行（含 dispatched/running）。
func (r *AutomationRunRepository) ListActivePastDeadlineTx(db *gorm.DB, now time.Time, limit int) ([]model.AutomationRun, error) {
	var items []model.AutomationRun
	err := db.
		Where("status IN (?, ?) AND deadline_at <= ?",
			string(model.AutomationRunDispatched), string(model.AutomationRunRunning), now).
		Order("deadline_at ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

// ListDuePostTx 取待执行后置操作且到达重试时间的运行。
func (r *AutomationRunRepository) ListDuePostTx(db *gorm.DB, now time.Time, limit int) ([]model.AutomationRun, error) {
	var items []model.AutomationRun
	err := db.
		Where("post_state = ? AND post_next_attempt_at IS NOT NULL AND post_next_attempt_at <= ?",
			model.AutomationPostStatePending, now).
		Order("post_next_attempt_at ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

// ---- GitHub PR 审查（INT-93）----

// prTriggerTypesIn SQLite IN 占位里的 PR 触发类型清单。
var prTriggerTypesIn = []string{
	string(model.AutomationTriggerGitHubPROpened),
	string(model.AutomationTriggerGitHubPRSynchronize),
}

// CancelSupersededPRRunsTx SHA 变化：该 PR 下旧 SHA 的全部在途运行（排队+进行中，
// 跨规则跨用户跨模型）一律取消并消费已有令牌。返回取消的运行 ID 列表（供事务外
// StopThread），0 行=无在途。
func (r *AutomationRunRepository) CancelSupersededPRRunsTx(tx *gorm.DB, repoFullName string, prNumber int, newSHA string, now time.Time) ([]string, error) {
	var oldRuns []model.AutomationRun
	err := tx.
		Where("repo_full_name = ? AND pr_number = ? AND head_sha <> ? AND status IN (?, ?, ?, ?)",
			repoFullName, prNumber, newSHA,
			model.AutomationRunQueued, model.RunStatusPendingRun,
			model.AutomationRunDispatched, model.AutomationRunRunning).
		Find(&oldRuns).Error
	if err != nil {
		return nil, err
	}
	if len(oldRuns) == 0 {
		return nil, nil
	}
	res := tx.Model(&model.AutomationRun{}).
		Where("repo_full_name = ? AND pr_number = ? AND head_sha <> ? AND status IN (?, ?, ?, ?)",
			repoFullName, prNumber, newSHA,
			model.AutomationRunQueued, model.RunStatusPendingRun,
			model.AutomationRunDispatched, model.AutomationRunRunning).
		Updates(map[string]any{
			"status":        string(model.AutomationRunCancelled),
			"finished_at":   now,
			"error_code":    "superseded",
			"error_message": "PR 已推送新提交，本轮审查被新 SHA 取代",
			"post_state":    string(model.AutomationPostStateNone),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	ids := make([]string, 0, len(oldRuns))
	for i := range oldRuns {
		ids = append(ids, oldRuns[i].ID)
	}
	return ids, nil
}

// ConsumeRunTokensTx 消费一批运行的回报令牌（取消后回报立即失效）。
func (r *AutomationRunRepository) ConsumeRunTokensTx(tx *gorm.DB, runIDs []string, now time.Time) error {
	if len(runIDs) == 0 {
		return nil
	}
	return tx.Model(&model.AutomationRunToken{}).
		Where("run_id IN ? AND consumed_at IS NULL", runIDs).
		Update("consumed_at", now).Error
}

// CountPRInflightTx 用户 PR 触发的在途数（排队中的 queued 不计）。
func (r *AutomationRunRepository) CountPRInflightTx(tx *gorm.DB, userID string) (int64, error) {
	var n int64
	err := tx.Model(&model.AutomationRun{}).
		Where("user_id = ? AND trigger_type IN ? AND status IN (?, ?, ?)",
			userID, prTriggerTypesIn,
			model.RunStatusPendingRun, model.AutomationRunDispatched, model.AutomationRunRunning).
		Count(&n).Error
	return n, err
}

// ListOldestQueuedPRRunsTx 用户排队中的 PR 运行（triggered_at 最老优先）。
func (r *AutomationRunRepository) ListOldestQueuedPRRunsTx(tx *gorm.DB, userID string, limit int) ([]model.AutomationRun, error) {
	var items []model.AutomationRun
	err := tx.
		Where("user_id = ? AND trigger_type IN ? AND status = ?",
			userID, prTriggerTypesIn, model.AutomationRunQueued).
		Order("triggered_at ASC, id ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

// ListUserIDsWithQueuedPRRunsTx 仍有 queued PR 运行的用户（调度循环提升用）。
func (r *AutomationRunRepository) ListUserIDsWithQueuedPRRunsTx(tx *gorm.DB) ([]string, error) {
	var ids []string
	err := tx.Model(&model.AutomationRun{}).
		Where("trigger_type IN ? AND status = ?", prTriggerTypesIn, model.AutomationRunQueued).
		Distinct("user_id").
		Pluck("user_id", &ids).Error
	return ids, err
}

type AutomationRunTokenRepository struct {
	db *gorm.DB
}

func NewAutomationRunTokenRepository(db *gorm.DB) *AutomationRunTokenRepository {
	return &AutomationRunTokenRepository{db: db}
}

func (r *AutomationRunTokenRepository) CreateInTx(tx *gorm.DB, token *model.AutomationRunToken) error {
	return tx.Create(token).Error
}

// FindByHash 返回令牌行；调用方负责校验 expires_at / consumed_at。
func (r *AutomationRunTokenRepository) FindByHash(ctx context.Context, tokenHash string) (*model.AutomationRunToken, error) {
	var t model.AutomationRunToken
	if err := r.db.WithContext(ctx).Where("token_hash = ?", tokenHash).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// Consume 条件消费令牌（consumed_at 为空才写入），返回影响行数（0=已被并发消费）。
func (r *AutomationRunTokenRepository) ConsumeTx(tx *gorm.DB, runID string, now time.Time) (int64, error) {
	res := tx.Model(&model.AutomationRunToken{}).
		Where("run_id = ? AND consumed_at IS NULL", runID).
		Update("consumed_at", now)
	return res.RowsAffected, res.Error
}

type NotificationRepository struct {
	db *gorm.DB
}

func NewNotificationRepository(db *gorm.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

func (r *NotificationRepository) ListByUserID(ctx context.Context, userID string, limit int, unreadOnly bool) ([]model.Notification, error) {
	query := r.db.WithContext(ctx).Where("user_id = ?", userID)
	if unreadOnly {
		query = query.Where("read_at IS NULL")
	}
	var items []model.Notification
	err := query.Order("updated_at DESC, id DESC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *NotificationRepository) CountUnread(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Count(&n).Error
	return n, err
}

func (r *NotificationRepository) FindByIDAndUserID(ctx context.Context, id, userID string) (*model.Notification, error) {
	var n model.Notification
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&n).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

// MarkRead 条件置 read_at（已读保持幂等，不刷新时间）。
func (r *NotificationRepository) MarkRead(ctx context.Context, id, userID string, now time.Time) error {
	res := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("id = ? AND user_id = ? AND read_at IS NULL", id, userID).
		Update("read_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *NotificationRepository) MarkAllRead(ctx context.Context, userID string, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Update("read_at", now)
	return res.RowsAffected, res.Error
}
