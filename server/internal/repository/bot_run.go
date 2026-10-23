package repository

import (
	"time"

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

// SessionKindOf 返回 run 的会话判别；run 不存在返回 main（调用账本永远挂在
// 某个已存在的 run 下，缺行只发生在极端竞态，按主线处理）。
func (r *BotRunRepository) SessionKindOf(runID string) string {
	if runID == "" {
		return model.BotSessionKindMain
	}
	var kind string
	if err := r.db.Raw("SELECT session_kind FROM bot_runs WHERE id = ?", runID).Scan(&kind).Error; err != nil || kind == "" {
		return model.BotSessionKindMain
	}
	return kind
}

// FindActiveByBot 返回该 bot 唯一的未结束主线 run；没有返回
// gorm.ErrRecordNotFound。定时任务 run 不占主线槽（按 session_kind 收窄）。
func (r *BotRunRepository) FindActiveByBot(botID string) (*model.BotRun, error) {
	var run model.BotRun
	if err := r.db.Where("bot_id = ? AND finished_at IS NULL AND session_kind = ?",
		botID, model.BotSessionKindMain).First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// ListByBot 主线 run 列表（用户可见的 /runs）；定时任务 run 走
// schedule-tasks/{taskId}/runs 读取。
func (r *BotRunRepository) ListByBot(botID string, limit int) ([]model.BotRun, error) {
	var rows []model.BotRun
	err := r.db.Where("bot_id = ? AND session_kind = ?", botID, model.BotSessionKindMain).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListByScheduleTask 返回定时任务最近的运行记录（新→旧），跨任务共享
// bot 级账本但按任务过滤。
func (r *BotRunRepository) ListByScheduleTask(botID, taskID string, limit, offset int) ([]model.BotRun, error) {
	var rows []model.BotRun
	err := r.db.Where("bot_id = ? AND schedule_task_id = ?", botID, taskID).
		Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, err
}

// LatestByScheduleTask 返回任务最近一条运行记录；没有返回
// gorm.ErrRecordNotFound。
func (r *BotRunRepository) LatestByScheduleTask(botID, taskID string) (*model.BotRun, error) {
	var run model.BotRun
	if err := r.db.Where("bot_id = ? AND schedule_task_id = ?", botID, taskID).
		Order("created_at DESC, id DESC").First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// LatestFinishedByScheduleTask 返回任务最近一条已结束的运行记录，供 schedule
// 运行时上下文取「上一次运行」的时间/状态/摘要（进行中的当前 run 不会选中
// 自己）；没有返回 gorm.ErrRecordNotFound。
func (r *BotRunRepository) LatestFinishedByScheduleTask(botID, taskID string) (*model.BotRun, error) {
	var run model.BotRun
	if err := r.db.Where("bot_id = ? AND schedule_task_id = ? AND finished_at IS NOT NULL", botID, taskID).
		Order("finished_at DESC, created_at DESC, id DESC").First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// CountUnfinishedByScheduleTaskTx 统计任务未结束的运行（cron 与手动触发共用）。
// 手动触发（INT-121）的忙碌检查与建 run 必须在同一事务里调用（MaxOpenConns(1)
// 下事务即串行化）；只读预览可传根连接。
func (r *BotRunRepository) CountUnfinishedByScheduleTaskTx(tx *gorm.DB, botID, taskID string) (int64, error) {
	var n int64
	err := tx.Model(&model.BotRun{}).
		Where("bot_id = ? AND schedule_task_id = ? AND finished_at IS NULL", botID, taskID).
		Count(&n).Error
	return n, err
}

// FirstUnfinishedByScheduleTaskTx 返回任务最早一条未结束的运行（按
// created_at ASC, id ASC）；没有返回 gorm.ErrRecordNotFound。调度器用它既判
// 重叠、又拿到钉跳过事件去重键的 run id（INT-148）。
func (r *BotRunRepository) FirstUnfinishedByScheduleTaskTx(tx *gorm.DB, botID, taskID string) (*model.BotRun, error) {
	var run model.BotRun
	if err := tx.Where("bot_id = ? AND schedule_task_id = ? AND finished_at IS NULL", botID, taskID).
		Order("created_at ASC, id ASC").First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *BotRunRepository) ListActive() ([]model.BotRun, error) {
	var rows []model.BotRun
	err := r.db.Where("finished_at IS NULL").Find(&rows).Error
	return rows, err
}

// MarkStopRequested 原子地请求停止一个 run：条件是 run 未结束且（
// acceptedOrdinal < 0 或「受理后没有一条既新又真正包含停止消息的请求」）。
//
// 「有进展」的判定同时看 ordinal 与请求的 input_through_seq：循环可能在停止
// 消息追加前就构建好上下文，只是登记晚了一步——这种请求 ordinal 更大却不含
// 停止消息，不能算进展（否则宽限会放它继续跑，停止被拖到看门狗超时）。
// 只有 input_through_seq >= stopSeq 的请求才说明停止消息已经交给模型。
// 判定与写入在同一条 UPDATE 里完成，与请求登记在单连接下串行，不会取消
// 已经带上停止消息的请求（INT-137）。返回是否命中。
func (r *BotRunRepository) MarkStopRequested(runID string, acceptedOrdinal, stopSeq int64, now time.Time) (bool, error) {
	q := r.db.Model(&model.BotRun{}).Where("id = ? AND finished_at IS NULL", runID)
	if acceptedOrdinal >= 0 {
		q = q.Where(`NOT EXISTS (
			SELECT 1 FROM bot_model_requests
			WHERE run_id = ? AND ordinal > ? AND input_through_seq >= ?)`,
			runID, acceptedOrdinal, stopSeq)
	}
	res := q.Update("stop_requested_at", now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// AdvanceInputThroughSeq 只升不降地推进 run 的输入消费水位：硬停止与主线循环
// 并发写时，持有更旧 seq 的一方不得把已推进的水位覆盖回去（INT-137）。
// 返回是否命中（水位已更高时落空）。
func (r *BotRunRepository) AdvanceInputThroughSeq(runID string, seq int64) (bool, error) {
	res := r.db.Model(&model.BotRun{}).
		Where("id = ? AND input_through_seq < ?", runID, seq).
		Update("input_through_seq", seq)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
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

// MaxOrdinalTx 返回该 run 当前最大的模型请求序号（没有请求为 0）。受理停止
// 时在同一事务里（追加停止消息之前）采样序号，避免「事务提交到采样之间」
// 新登记的请求被算进基线（INT-137）。
func (r *BotModelRequestRepository) MaxOrdinalTx(tx *gorm.DB, runID string) (int64, error) {
	var ordinal int64
	if err := tx.Model(&model.BotModelRequest{}).Where("run_id = ?", runID).
		Select("COALESCE(MAX(ordinal), 0)").Scan(&ordinal).Error; err != nil {
		return 0, err
	}
	return ordinal, nil
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

// LatestMainByBot 返回该机器人主线 run 的最新一条模型请求（进行中或已结束；
// 按 started_at DESC, ordinal DESC），聊天页指标区用。没有返回
// gorm.ErrRecordNotFound。
func (r *BotModelRequestRepository) LatestMainByBot(botID string) (*model.BotModelRequest, error) {
	var row model.BotModelRequest
	err := r.db.Joins("JOIN bot_runs ON bot_runs.id = bot_model_requests.run_id").
		Where("bot_runs.bot_id = ? AND bot_runs.session_kind = ?", botID, model.BotSessionKindMain).
		Order("bot_model_requests.started_at DESC, bot_model_requests.ordinal DESC").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// LatestMainUpstreamByBot 返回最近一次「真正触达上游」的主线请求
// （INT-139 云端复审）：本地失败的行（failed 且无首段、无 usage、无
// provider_request_id——上游根本没处理这段前缀）不计入前缀缓存热度，否则一条 transport 失败的
// 残留行会把激活推迟盖在并不存在的缓存上。排序按上游实际接触时刻而非
// started_at：一条更早开始但刚结束的热请求不得被一条更晚开始的冷请求
// 盖过；server_restarted 的 finished_at 是恢复记账时刻，回退
// started_at——与 mainlinePrefixCacheExpired 的热度公式同源。
// 没有返回 gorm.ErrRecordNotFound。
func (r *BotModelRequestRepository) LatestMainUpstreamByBot(botID string) (*model.BotModelRequest, error) {
	var row model.BotModelRequest
	err := r.db.Joins("JOIN bot_runs ON bot_runs.id = bot_model_requests.run_id").
		Where("bot_runs.bot_id = ? AND bot_runs.session_kind = ?", botID, model.BotSessionKindMain).
		Where("NOT (bot_model_requests.status = ? AND bot_model_requests.first_delta_at IS NULL AND bot_model_requests.prompt_tokens IS NULL AND bot_model_requests.provider_request_id = '')",
			model.BotModelRequestFailed).
		Order("COALESCE(CASE WHEN bot_model_requests.error_code = 'server_restarted' THEN NULL ELSE bot_model_requests.finished_at END, bot_model_requests.started_at) DESC, bot_model_requests.ordinal DESC").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
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
	err := r.db.Where("bot_id = ? AND session_kind = ? AND seq > ?", botID, model.BotSessionKindMain, after).
		Order("seq ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r *BotEventRepository) MaxSeq(botID string) (int64, error) {
	var seq int64
	err := r.db.Model(&model.BotEvent{}).Where("bot_id = ?", botID).
		Select("COALESCE(MAX(seq), 0)").Scan(&seq).Error
	return seq, err
}
