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

// ListByIDs 按 ID 批量取本机器人调用——快照按窗口内消息的 tool_calls
// 引用补齐最近 N 条窗口外的旧调用（前端渲染卡片靠 call_id 命中）。
func (r *BotToolCallRepository) ListByIDs(botID string, ids []string) ([]model.BotToolCall, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []model.BotToolCall
	err := r.db.Where("bot_id = ? AND id IN ?", botID, ids).Find(&rows).Error
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

// ListRunningWaitCalls 全局读 running 状态的 fs_thread/wait 调用，只供 wait
// 评估器的兜底收编使用。limit 是「每轮最多收编 N 条」的节流阀，不是全集——
// 其他机器人的行可能占满窗口；受理限额与配额计数不得用它，一律走下面按
// bot_id（及 JSON 字段）过滤的查询。
func (r *BotToolCallRepository) ListRunningWaitCalls(limit int) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := r.db.Where("status = ? AND tool_name = ? AND action = ?",
		model.BotCallStatusRunning, "fs_thread", "wait").
		Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// botWaitArgsJSONExpr 是 arguments_json 的安全 JSON 表达式：坏 JSON 行回落
// '{}'，json_extract 取不到字段得 NULL，不会命中过滤条件（与内存解析失败按
// 不匹配处理同口径）。
func botWaitArgsJSONExpr() string {
	return jsonColumnOr("arguments_json", "{}")
}

// botPolicyJSONExpr 是 policy_json 的安全 JSON 表达式（同上，回落 '{}'）。
func botPolicyJSONExpr() string {
	return jsonColumnOr("policy_json", "{}")
}

// ListRunningWaitCallsByBotTx 读本机器人 running 状态的 fs_thread/wait 调用：
// SQL 侧按 bot_id 过滤，行集不受其他机器人影响。受理端判重与限额计数共用；
// 传 r.db 即事务外读。
func (r *BotToolCallRepository) ListRunningWaitCallsByBotTx(tx *gorm.DB, botID string, limit int) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := tx.Where("bot_id = ? AND status = ? AND tool_name = ? AND action = ?",
		botID, model.BotCallStatusRunning, "fs_thread", "wait").
		Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListRunningWaitCallsByBot 是 ListRunningWaitCallsByBotTx 的事务外版本。
func (r *BotToolCallRepository) ListRunningWaitCallsByBot(botID string, limit int) ([]model.BotToolCall, error) {
	return r.ListRunningWaitCallsByBotTx(r.db, botID, limit)
}

// CountRunningWaitCallsTx SQL 侧统计本机器人 running wait 行数（受理事务内
// 二次复核用：同事务里早前插入的行可见，天然覆盖同批计数）。
func (r *BotToolCallRepository) CountRunningWaitCallsTx(tx *gorm.DB, botID string) (int64, error) {
	var n int64
	err := tx.Model(&model.BotToolCall{}).
		Where("bot_id = ? AND status = ? AND tool_name = ? AND action = ?",
			botID, model.BotCallStatusRunning, "fs_thread", "wait").
		Count(&n).Error
	return n, err
}

// FindRunningWaitByThreadTx 找本机器人对指定 (env, thread) 的 running wait
// （受理端判重；env/thread 在 arguments_json 里，经 json_extract 在 SQL 过滤）。
func (r *BotToolCallRepository) FindRunningWaitByThreadTx(tx *gorm.DB, botID, envID, threadID string) (*model.BotToolCall, error) {
	var row model.BotToolCall
	args := botWaitArgsJSONExpr()
	err := tx.Where("bot_id = ? AND status = ? AND tool_name = ? AND action = ?"+
		" AND json_extract("+args+", '$.env_id') = ? AND json_extract("+args+", '$.thread_id') = ?",
		botID, model.BotCallStatusRunning, "fs_thread", "wait", envID, threadID).
		Order("created_at ASC, id ASC").First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CountWaitCallsByThreadSinceTx 统计本机器人对指定 (env, thread) 在窗口内的
// 全部 wait 调用行（含已终态）：1 小时重挂上限的计数来源。
func (r *BotToolCallRepository) CountWaitCallsByThreadSinceTx(tx *gorm.DB, botID, envID, threadID string, since time.Time) (int64, error) {
	var n int64
	args := botWaitArgsJSONExpr()
	err := tx.Model(&model.BotToolCall{}).
		Where("bot_id = ? AND tool_name = ? AND action = ? AND created_at >= ?",
			botID, "fs_thread", "wait", since).
		Where("json_extract("+args+", '$.env_id') = ? AND json_extract("+args+", '$.thread_id') = ?",
			envID, threadID).
		Count(&n).Error
	return n, err
}

// CountWaitCallsByThreadSince 是 CountWaitCallsByThreadSinceTx 的事务外版本。
func (r *BotToolCallRepository) CountWaitCallsByThreadSince(botID, envID, threadID string, since time.Time) (int64, error) {
	return r.CountWaitCallsByThreadSinceTx(r.db, botID, envID, threadID, since)
}

// CountPolicyAutoCallsByThreadSinceTx 统计窗口内本 bot 对该线程按指定规则
// 自动放行的调用行（policy_json 的 auto/rule_id/source 与 arguments_json 的
// env/thread 都在 SQL 过滤），带回最近一行 ID（配额审计的 approved_call_id）。
// 含已失败/取消的行——失败的发送同样消耗一次尝试。只看 source=tier1：
// always-allow 接管的降级候选行同样是 auto=1+tier=tier1+rule_id，但那是用户
// 明示的持久授权执行，不消耗无人值守配额（复审 P2 修正）。
func (r *BotToolCallRepository) CountPolicyAutoCallsByThreadSinceTx(tx *gorm.DB, botID, ruleID, envID, threadID string, since time.Time) (int64, string, error) {
	pol := botPolicyJSONExpr()
	args := botWaitArgsJSONExpr()
	matched := func(q *gorm.DB) *gorm.DB {
		return q.Where("bot_id = ? AND created_at >= ? AND policy_json <> ''", botID, since).
			Where("json_extract("+pol+", '$.auto') = 1 AND json_extract("+pol+", '$.rule_id') = ?"+
				" AND json_extract("+pol+", '$.source') = 'tier1'", ruleID).
			Where("json_extract("+args+", '$.env_id') = ? AND json_extract("+args+", '$.thread_id') = ?",
				envID, threadID)
	}
	var n int64
	if err := matched(tx.Model(&model.BotToolCall{})).Count(&n).Error; err != nil {
		return 0, "", err
	}
	if n == 0 {
		return 0, "", nil
	}
	var last model.BotToolCall
	if err := matched(tx.Model(&model.BotToolCall{})).
		Order("created_at DESC, id DESC").First(&last).Error; err != nil {
		return n, "", err
	}
	return n, last.ID, nil
}

// PolicyAutoCallExistsSinceTx 报告窗口内是否有过同规则、同目标线程的 tier1
// 自动放行（误判检测窗口判定）。目标线程取执行上下文：thread_id 缺省回退
// scope_id，与 botPolicyThreadTargetOf 同口径；env/thread 为空串时不参与过滤。
// 只看 source=tier1：always-allow 接管的用户授权执行不算无人值守自动放行，
// 不应成为误判的参照（复审 P2 修正）。
func (r *BotToolCallRepository) PolicyAutoCallExistsSinceTx(tx *gorm.DB, botID, ruleID, envID, threadID string, since time.Time) (bool, error) {
	pol := botPolicyJSONExpr()
	ec := jsonColumnOr("execution_context_json", "{}")
	q := tx.Model(&model.BotToolCall{}).
		Where("bot_id = ? AND created_at >= ? AND policy_json <> ''", botID, since).
		Where("json_extract("+pol+", '$.auto') = 1 AND json_extract("+pol+", '$.tier') = 'tier1'"+
			" AND json_extract("+pol+", '$.source') = 'tier1'"+
			" AND json_extract("+pol+", '$.rule_id') = ?", ruleID)
	if envID != "" {
		q = q.Where("json_extract("+ec+", '$.env_id') = ?", envID)
	}
	if threadID != "" {
		q = q.Where("(json_extract("+ec+", '$.thread_id') = ?"+
			" OR ((json_extract("+ec+", '$.thread_id') IS NULL OR json_extract("+ec+", '$.thread_id') = '')"+
			" AND json_extract("+ec+", '$.scope_id') = ?))", threadID, threadID)
	}
	var n int64
	err := q.Limit(1).Count(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountScheduleDeniedByRun 统计某次运行里被分级策略同步拒绝的调用数：
// 定时任务会话的 tier3 拒绝必须强制本轮汇报，计数来源就是这些行。
func (r *BotToolCallRepository) CountScheduleDeniedByRun(runID string) (int64, error) {
	var n int64
	err := r.db.Model(&model.BotToolCall{}).
		Joins("JOIN bot_model_requests ON bot_model_requests.id = bot_tool_calls.request_id").
		Where("bot_model_requests.run_id = ? AND bot_tool_calls.policy_json LIKE ?",
			runID, `%"schedule_denied":true%`).
		Count(&n).Error
	return n, err
}

// ListScheduleDeniedByRunTx 读某次运行里被分级策略同步拒绝的调用行（工具、
// 动作与拒绝原因）：强制汇报把被拒清单拼进主线事件，静默哨兵与
// needs_report=false 都吞不掉拒绝原因。
func (r *BotToolCallRepository) ListScheduleDeniedByRunTx(tx *gorm.DB, runID string) ([]model.BotToolCall, error) {
	var rows []model.BotToolCall
	err := tx.Model(&model.BotToolCall{}).
		Joins("JOIN bot_model_requests ON bot_model_requests.id = bot_tool_calls.request_id").
		Where("bot_model_requests.run_id = ? AND bot_tool_calls.policy_json LIKE ?",
			runID, `%"schedule_denied":true%`).
		Order("bot_tool_calls.created_at ASC, bot_tool_calls.id ASC").
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

// UpdateWhereRunningFreshTx 是终态 CAS 的严格版：除 status=running 外还要求
// cancel_requested_at IS NULL。取消标记在本函数读取与写入之间落上的窗口里，
// 成功/失败终态不得覆盖取消——0 行返回后由调用方重读该行裁决改走 cancelled
// 还是放弃（终态已被取消/恢复路径落定）。
func (r *BotToolCallRepository) UpdateWhereRunningFreshTx(tx *gorm.DB, callID string, cols map[string]interface{}) (bool, error) {
	res := tx.Model(&model.BotToolCall{}).
		Where("id = ? AND status = ? AND cancel_requested_at IS NULL",
			callID, model.BotCallStatusRunning).
		Updates(cols)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateWhereUnfinishedTx 按 finished_at IS NULL 定界的事务版更新：启动
// 恢复的 wait 收尾用它——EnsureBotSchema 的去重前置已把重复行移出
// running（仍 unfinished），status 门会把它们挡在恢复外。
func (r *BotToolCallRepository) UpdateWhereUnfinishedTx(tx *gorm.DB, callID string, cols map[string]interface{}) (bool, error) {
	res := tx.Model(&model.BotToolCall{}).
		Where("id = ? AND finished_at IS NULL", callID).
		Updates(cols)
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
		// active 口径与匹配/worker 复核一致：过期未惰性撤销的行不再算有效。
		q = q.Where("revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)",
			time.Now().UTC())
	}
	var rows []model.BotAuthorization
	err := q.Order("created_at DESC, id DESC").Find(&rows).Error
	return rows, err
}

// FindActiveRule 按规则哈希找活跃授权；供匹配与撤销使调用回退时使用。
// INT-160 起同时要求未过期（expires_at 已过的行不再命中，视为已失效；
// 过期行的 revoked_at 由 RevokeExpiredRuleTx 惰性补写，不在这里阻塞匹配）。
func (r *BotAuthorizationRepository) FindActiveRuleTx(tx *gorm.DB, botID, ruleHash string) (*model.BotAuthorization, error) {
	var row model.BotAuthorization
	if err := tx.Where("bot_id = ? AND rule_hash = ? AND revoked_at IS NULL "+
		"AND (expires_at IS NULL OR expires_at > ?)", botID, ruleHash, time.Now().UTC()).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// RevokeExpiredRuleTx 惰性撤销同规则的已过期授权行：部分唯一索引按
// revoked_at IS NULL 判活跃，过期行不撤销会一直占着 (bot_id, rule_hash)，
// 让同一规则的「始终允许」永远建不出来。返回是否撤销了行。
func (r *BotAuthorizationRepository) RevokeExpiredRuleTx(tx *gorm.DB, botID, ruleHash string, now time.Time) (bool, error) {
	res := tx.Model(&model.BotAuthorization{}).
		Where("bot_id = ? AND rule_hash = ? AND revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at <= ?",
			botID, ruleHash, now).
		Update("revoked_at", now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
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
