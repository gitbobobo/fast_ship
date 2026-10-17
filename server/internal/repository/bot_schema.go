package repository

import (
	"fmt"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotModels 返回机器人的全部表模型（七张主线表 + INT-102 的压缩/记忆/维护
// 表 + INT-103 的定时任务表），供 AutoMigrate 与测试建库共用。FTS5 虚表与
// 触发器不在 GORM 模型体系内，由 botSchemaDDL 创建。
func BotModels() []interface{} {
	return []interface{}{
		&model.Bot{},
		&model.BotMessage{},
		&model.BotRun{},
		&model.BotModelRequest{},
		&model.BotToolCall{},
		&model.BotAuthorization{},
		&model.BotEvent{},
		&model.BotContextState{},
		&model.BotCompaction{},
		&model.BotMemoryFile{},
		&model.BotMemoryVersion{},
		&model.BotMaintenanceRun{},
		&model.BotMaintenanceRequest{},
		&model.BotScheduleTask{},
	}
}

// botSchemaDDL 是 AutoMigrate 建不了的部分索引 / 复合索引 / 守护触发器 /
// FTS5 派生索引。全部幂等（IF NOT EXISTS），重复启动无副作用。每条都必须
// 检查错误——任何一条失败都按启动失败处理。
var botSchemaDDL = []string{
	// bots：按用户列表；不唯一机器人名称（产品决策）。
	`CREATE INDEX IF NOT EXISTS idx_bots_user_created ON bots(user_id, created_at DESC, id)`,

	// bot_messages：账本正序读、幂等键、去重键、来源反查与会话隔离读。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_messages_bot_seq ON bot_messages(bot_id, seq)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_messages_client_message ON bot_messages(bot_id, client_message_id)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_messages_dedup ON bot_messages(bot_id, dedup_key)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_messages_run_seq ON bot_messages(run_id, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_messages_call_seq ON bot_messages(call_id, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_messages_bot_session_seq ON bot_messages(bot_id, session_kind, seq)`,
	// 消息只追加：任何 UPDATE 直接失败；删除（整机器人级联清理）不受影响。
	`CREATE TRIGGER IF NOT EXISTS trg_bot_messages_append_only
BEFORE UPDATE ON bot_messages
BEGIN
    SELECT RAISE(ABORT, 'bot_messages is append-only');
END`,

	// bot_runs：触发去重 + 每机器人至多一个未结束主线 run + 列表索引。
	// INT-103 起主线唯一性按 session_kind='main' 收窄（定时任务可并行）；
	// 存量库的旧索引用 DROP+CREATE 幂等替换。
	`DROP INDEX IF EXISTS idx_bot_runs_active_once`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_runs_active_once ON bot_runs(bot_id) WHERE finished_at IS NULL AND session_kind = 'main'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_runs_bot_trigger ON bot_runs(bot_id, trigger_key)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_runs_bot_created ON bot_runs(bot_id, created_at DESC, id)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_runs_schedule_task ON bot_runs(schedule_task_id, created_at DESC)`,

	// bot_model_requests：run 内请求序号唯一。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_model_requests_run_ordinal ON bot_model_requests(run_id, ordinal)`,

	// bot_tool_calls：协议配对唯一 + 状态检索。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_tool_calls_request_provider ON bot_tool_calls(request_id, provider_tool_call_id)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_tool_calls_bot_status ON bot_tool_calls(bot_id, status, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_tool_calls_status_created ON bot_tool_calls(status, created_at)`,

	// bot_authorizations：活跃授权规则唯一（撤销后允许重建同规则）。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_authorizations_active_rule ON bot_authorizations(bot_id, rule_hash) WHERE revoked_at IS NULL`,

	// ---------- INT-102：上下文压缩、记忆与维护 ----------

	// bot_context_states：每机器人一行；generation 单调、摘要水位不后退由
	// 守护触发器保证，发布走 CAS。
	`CREATE TRIGGER IF NOT EXISTS trg_bot_context_states_monotonic
BEFORE UPDATE ON bot_context_states
WHEN NEW.generation <> OLD.generation + 1 OR NEW.summary_through_seq < OLD.summary_through_seq
BEGIN
    SELECT RAISE(ABORT, 'bot_context_states generation must advance by one and summary watermark must not regress');
END`,

	// bot_compactions：一个压缩周期一条最终记录，append-only（级联删除不受影响）。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_compactions_maintenance_run ON bot_compactions(maintenance_run_id)`,
	`CREATE TRIGGER IF NOT EXISTS trg_bot_compactions_append_only
BEFORE UPDATE ON bot_compactions
BEGIN
    SELECT RAISE(ABORT, 'bot_compactions is append-only');
END`,

	// bot_memory_files：文件身份唯一（含墓碑）；列表扫描索引。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_memory_files_bot_path ON bot_memory_files(bot_id, path)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_memory_files_bot_list ON bot_memory_files(bot_id, deleted_at, path)`,
	// 身份不可改（id/bot_id/path）。
	`CREATE TRIGGER IF NOT EXISTS trg_bot_memory_files_identity
BEFORE UPDATE ON bot_memory_files
WHEN OLD.id <> NEW.id OR OLD.bot_id <> NEW.bot_id OR OLD.path <> NEW.path
BEGIN
    SELECT RAISE(ABORT, 'bot_memory_files identity columns are immutable');
END`,
	// 正文/哈希/删除状态改变必须使版本恰好加一，且新版本行与快照一致。
	// DROP 后再建：存量库里旧触发器不覆盖 content_hash、不核对快照。
	`DROP TRIGGER IF EXISTS trg_bot_memory_files_version_step`,
	`CREATE TRIGGER IF NOT EXISTS trg_bot_memory_files_version_step
BEFORE UPDATE OF content, content_hash, deleted_at, current_version ON bot_memory_files
WHEN NEW.current_version <> OLD.current_version + 1
BEGIN
    SELECT RAISE(ABORT, 'bot_memory_files content change must advance current_version by exactly one');
END`,
	`DROP TRIGGER IF EXISTS trg_bot_memory_files_version_row`,
	`CREATE TRIGGER IF NOT EXISTS trg_bot_memory_files_version_row
AFTER UPDATE OF content, content_hash, deleted_at, current_version ON bot_memory_files
WHEN NOT EXISTS (
    SELECT 1 FROM bot_memory_versions v
    WHERE v.file_id = NEW.id AND v.version = NEW.current_version
      AND v.content = NEW.content
      AND v.content_hash = NEW.content_hash
      AND v.deleted = CASE WHEN NEW.deleted_at IS NULL THEN 0 ELSE 1 END
)
BEGIN
    SELECT RAISE(ABORT, 'bot_memory_files update requires a matching version snapshot');
END`,

	// bot_memory_versions：完整版本快照，只追加；幂等键与历史扫描索引。
	// 允许 DELETE（每文件最近 50 版保留策略与整机器人级联清理）。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_memory_versions_file_version ON bot_memory_versions(file_id, version)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_memory_versions_file_operation_key ON bot_memory_versions(file_id, operation_key)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_memory_versions_file_history ON bot_memory_versions(file_id, version DESC)`,
	`CREATE TRIGGER IF NOT EXISTS trg_bot_memory_versions_append_only
BEFORE UPDATE ON bot_memory_versions
BEGIN
    SELECT RAISE(ABORT, 'bot_memory_versions is append-only');
END`,

	// bot_maintenance_runs：触发幂等 + 扫描索引。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_maintenance_runs_bot_kind_trigger ON bot_maintenance_runs(bot_id, kind, trigger_key)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_maintenance_runs_scan ON bot_maintenance_runs(kind, status, created_at)`,
	// bot_maintenance_requests：运行内请求序号唯一。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_maintenance_requests_run_ordinal ON bot_maintenance_requests(maintenance_run_id, ordinal)`,

	// ---------- INT-103：定时任务 ----------

	// bot_schedule_tasks：按机器人列表 + 调度扫描（仅已启用且有计划时间的行）。
	`CREATE INDEX IF NOT EXISTS idx_bot_schedule_tasks_bot_list ON bot_schedule_tasks(bot_id, created_at DESC, id)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_schedule_tasks_due ON bot_schedule_tasks(enabled, next_run_at) WHERE next_run_at IS NOT NULL`,

	// bot_events：会话隔离读（聊天页只返回 main）。
	`CREATE INDEX IF NOT EXISTS idx_bot_events_bot_session_seq ON bot_events(bot_id, session_kind, seq)`,

	// bot_memory_fts 是派生全文索引（trigram），事实来源是文件/版本表；
	// 只索引未删除文件的当前正文，不索引历史版本。同步由文件表触发器完成，
	// 墓碑文件不进入索引。
	`CREATE VIRTUAL TABLE IF NOT EXISTS bot_memory_fts USING fts5(
    file_id UNINDEXED,
    bot_id UNINDEXED,
    path UNINDEXED,
    body,
    tokenize='trigram'
)`,
	`CREATE TRIGGER IF NOT EXISTS trg_bot_memory_files_fts_insert
AFTER INSERT ON bot_memory_files
BEGIN
    INSERT INTO bot_memory_fts(file_id, bot_id, path, body)
    SELECT NEW.id, NEW.bot_id, NEW.path, NEW.content WHERE NEW.deleted_at IS NULL;
END`,
	`CREATE TRIGGER IF NOT EXISTS trg_bot_memory_files_fts_update
AFTER UPDATE ON bot_memory_files
BEGIN
    DELETE FROM bot_memory_fts WHERE file_id = OLD.id;
    INSERT INTO bot_memory_fts(file_id, bot_id, path, body)
    SELECT NEW.id, NEW.bot_id, NEW.path, NEW.content WHERE NEW.deleted_at IS NULL;
END`,
	`CREATE TRIGGER IF NOT EXISTS trg_bot_memory_files_fts_delete
AFTER DELETE ON bot_memory_files
BEGIN
    DELETE FROM bot_memory_fts WHERE file_id = OLD.id;
END`,
}

// EnsureBotSchema 在 AutoMigrate 之后补齐机器人相关索引、守护触发器与 FTS5
// 派生索引。任何一条失败都返回错误，由启动方决定退出，绝不带错运行。
func EnsureBotSchema(db *gorm.DB) error {
	for _, ddl := range botSchemaDDL {
		if err := db.Exec(ddl).Error; err != nil {
			return fmt.Errorf("bot schema: %w", err)
		}
	}
	return nil
}
