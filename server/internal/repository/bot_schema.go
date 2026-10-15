package repository

import (
	"fmt"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotModels 返回机器人的七张表模型，供 AutoMigrate 与测试建库共用。
func BotModels() []interface{} {
	return []interface{}{
		&model.Bot{},
		&model.BotMessage{},
		&model.BotRun{},
		&model.BotModelRequest{},
		&model.BotToolCall{},
		&model.BotAuthorization{},
		&model.BotEvent{},
	}
}

// botSchemaDDL 是 AutoMigrate 建不了的部分索引 / 复合索引与消息防改写守护。
// 全部幂等（IF NOT EXISTS），重复启动无副作用。每条都必须检查错误。
var botSchemaDDL = []string{
	// bots：按用户列表；不唯一机器人名称（产品决策）。
	`CREATE INDEX IF NOT EXISTS idx_bots_user_created ON bots(user_id, created_at DESC, id)`,

	// bot_messages：账本正序读、幂等键、去重键与来源反查。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_messages_bot_seq ON bot_messages(bot_id, seq)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_messages_client_message ON bot_messages(bot_id, client_message_id)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_messages_dedup ON bot_messages(bot_id, dedup_key)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_messages_run_seq ON bot_messages(run_id, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_messages_call_seq ON bot_messages(call_id, seq)`,
	// 消息只追加：任何 UPDATE 直接失败；删除（整机器人级联清理）不受影响。
	`CREATE TRIGGER IF NOT EXISTS trg_bot_messages_append_only
BEFORE UPDATE ON bot_messages
BEGIN
    SELECT RAISE(ABORT, 'bot_messages is append-only');
END`,

	// bot_runs：触发去重 + 每机器人至多一个未结束主线 run + 列表索引。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_runs_bot_trigger ON bot_runs(bot_id, trigger_key)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_runs_active_once ON bot_runs(bot_id) WHERE finished_at IS NULL`,
	`CREATE INDEX IF NOT EXISTS idx_bot_runs_bot_created ON bot_runs(bot_id, created_at DESC, id)`,

	// bot_model_requests：run 内请求序号唯一。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_model_requests_run_ordinal ON bot_model_requests(run_id, ordinal)`,

	// bot_tool_calls：协议配对唯一 + 状态检索。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_tool_calls_request_provider ON bot_tool_calls(request_id, provider_tool_call_id)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_tool_calls_bot_status ON bot_tool_calls(bot_id, status, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_bot_tool_calls_status_created ON bot_tool_calls(status, created_at)`,

	// bot_authorizations：活跃授权规则唯一（撤销后允许重建同规则）。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_authorizations_active_rule ON bot_authorizations(bot_id, rule_hash) WHERE revoked_at IS NULL`,
}

// EnsureBotSchema 在 AutoMigrate 之后补齐机器人相关索引与守护触发器。
// 任何一条失败都返回错误，由启动方决定退出，绝不带错运行。
func EnsureBotSchema(db *gorm.DB) error {
	for _, ddl := range botSchemaDDL {
		if err := db.Exec(ddl).Error; err != nil {
			return fmt.Errorf("bot schema: %w", err)
		}
	}
	return nil
}
