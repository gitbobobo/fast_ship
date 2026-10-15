package main

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 机器人表结构（INT-101）：AutoMigrate + EnsureBotSchema 组合建库；重复启动
// 幂等；部分唯一/守护触发器/CASCADE 全部落到位。

func setupBotSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate user: %v", err)
	}
	if err := db.AutoMigrate(repository.BotModels()...); err != nil {
		t.Fatalf("migrate bot models: %v", err)
	}
	if err := repository.EnsureBotSchema(db); err != nil {
		t.Fatalf("ensure bot schema: %v", err)
	}
	return db
}

func sqliteIndexNames(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	var rows []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_bot%'`).Scan(&rows).Error; err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Name] = true
	}
	return out
}

func TestBotSchema_IndexesAndGuardApplied(t *testing.T) {
	db := setupBotSchemaDB(t)

	indexes := sqliteIndexNames(t, db)
	for _, want := range []string{
		"idx_bots_user_created",
		"idx_bot_messages_bot_seq",
		"idx_bot_messages_client_message",
		"idx_bot_messages_dedup",
		"idx_bot_messages_run_seq",
		"idx_bot_messages_call_seq",
		"idx_bot_runs_bot_trigger",
		"idx_bot_runs_active_once",
		"idx_bot_runs_bot_created",
		"idx_bot_model_requests_run_ordinal",
		"idx_bot_tool_calls_request_provider",
		"idx_bot_tool_calls_bot_status",
		"idx_bot_tool_calls_status_created",
		"idx_bot_authorizations_active_rule",
	} {
		if !indexes[want] {
			t.Fatalf("缺少索引 %s，实际: %v", want, indexes)
		}
	}

	var triggers []string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = 'trg_bot_messages_append_only'`).Scan(&triggers).Error; err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("缺少消息防改写守护触发器")
	}
}

// TestBotSchema_IdempotentRestart 重复执行 EnsureBotSchema（模拟重复启动）
// 必须无副作用。
func TestBotSchema_IdempotentRestart(t *testing.T) {
	db := setupBotSchemaDB(t)
	before := sqliteIndexNames(t, db)
	if err := repository.EnsureBotSchema(db); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after := sqliteIndexNames(t, db)
	if len(before) != len(after) {
		t.Fatalf("重复应用改变了索引集合: %d -> %d", len(before), len(after))
	}
}

func TestBotSchema_PartialUniquesEnforced(t *testing.T) {
	db := setupBotSchemaDB(t)
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u" + userID[:8], PasswordHash: "x", Email: userID + "@t.local"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	bot := &model.Bot{ID: uuid.NewString(), UserID: userID, Name: "b",
		SystemPromptText: "s", PromptRevision: 1, PlatformPromptVersion: 1, ToolsetVersion: 1,
		BaseURL: "http://x", Model: "m", APIKeyEncrypted: []byte("c"), APIKeyLast4: "1234",
		NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: timeNowUTC(), UpdatedAt: timeNowUTC()}
	if err := db.Create(bot).Error; err != nil {
		t.Fatalf("seed bot: %v", err)
	}

	// 每个机器人至多一个未结束 run（部分唯一）。
	run1 := &model.BotRun{ID: uuid.NewString(), BotID: bot.ID, TriggerSource: model.BotTriggerUserMessage,
		TriggerKey: "k1", TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		ConfigSnapshotJSON: "{}", CreatedAt: timeNowUTC()}
	if err := db.Create(run1).Error; err != nil {
		t.Fatalf("seed run1: %v", err)
	}
	run2 := &model.BotRun{ID: uuid.NewString(), BotID: bot.ID, TriggerSource: model.BotTriggerUserMessage,
		TriggerKey: "k2", TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		ConfigSnapshotJSON: "{}", CreatedAt: timeNowUTC()}
	if err := db.Create(run2).Error; err == nil {
		t.Fatalf("同一机器人第二个未结束 run 应被部分唯一索引拒绝")
	}
	// 结束后允许新 run。
	fin := timeNowUTC()
	run1.FinishedAt = &fin
	run1.Status = model.BotRunStatusCompleted
	if err := db.Save(run1).Error; err != nil {
		t.Fatalf("finish run1: %v", err)
	}
	if err := db.Create(run2).Error; err != nil {
		t.Fatalf("结束后应允许新 run: %v", err)
	}

	// 活跃授权部分唯一：同规则未撤销时再建冲突；撤销后可以重建。
	authz := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_project", Action: "create", ScopeKind: "project_collection", ScopeID: userID,
		ConstraintsJSON: "{}", RuleHash: "h1", CreatedAt: timeNowUTC()}
	if err := db.Create(authz).Error; err != nil {
		t.Fatalf("seed authz: %v", err)
	}
	dup := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_project", Action: "create", ScopeKind: "project_collection", ScopeID: userID,
		ConstraintsJSON: "{}", RuleHash: "h1", CreatedAt: timeNowUTC()}
	if err := db.Create(dup).Error; err == nil {
		t.Fatalf("活跃同规则授权应被部分唯一索引拒绝")
	}
	authz.RevokedAt = &fin
	if err := db.Save(authz).Error; err != nil {
		t.Fatalf("revoke authz: %v", err)
	}
	if err := db.Create(dup).Error; err != nil {
		t.Fatalf("撤销后同规则应可重建: %v", err)
	}

	// bot_events 联合主键 (bot_id, seq)。
	ev := &model.BotEvent{BotID: bot.ID, Seq: 1, EventType: "delta", PayloadJSON: "{}", CreatedAt: timeNowUTC()}
	if err := db.Create(ev).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := db.Create(&model.BotEvent{BotID: bot.ID, Seq: 1, EventType: "delta", PayloadJSON: "{}", CreatedAt: timeNowUTC()}).Error; err == nil {
		t.Fatalf("bot_events 主键应唯一")
	}
}

// TestBotSchema_CascadeDelete 整机器人删除级联清理全部七张表；消息守护触发器
// 只拦 UPDATE，不拦级联 DELETE。
func TestBotSchema_CascadeDelete(t *testing.T) {
	db := setupBotSchemaDB(t)
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u" + userID[:8], PasswordHash: "x", Email: userID + "@t.local"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	bot := &model.Bot{ID: uuid.NewString(), UserID: userID, Name: "b",
		SystemPromptText: "s", PromptRevision: 1, PlatformPromptVersion: 1, ToolsetVersion: 1,
		BaseURL: "http://x", Model: "m", APIKeyEncrypted: []byte("c"), APIKeyLast4: "1234",
		NextMessageSeq: 1, NextEventSeq: 1, CreatedAt: timeNowUTC(), UpdatedAt: timeNowUTC()}
	if err := db.Create(bot).Error; err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	run := &model.BotRun{ID: uuid.NewString(), BotID: bot.ID, TriggerSource: model.BotTriggerUserMessage,
		TriggerKey: "k", TriggerContextJSON: "{}", Status: model.BotRunStatusCompleted,
		ConfigSnapshotJSON: "{}", CreatedAt: timeNowUTC()}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	req := &model.BotModelRequest{ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, InputThroughSeq: 0, PromptRevision: 1, ToolsetVersion: 1,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4", StartedAt: timeNowUTC()}
	if err := db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	call := &model.BotToolCall{ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
		ProviderToolCallID: "p1", ToolName: "fs_project", Action: "list",
		ArgumentsJSON: "{}", ArgumentsHash: "x", ExecutionMode: "sync",
		Status: model.BotCallStatusSucceeded, ExecutionContextJSON: "{}", CreatedAt: timeNowUTC()}
	if err := db.Create(call).Error; err != nil {
		t.Fatalf("seed call: %v", err)
	}
	msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID, Seq: 1, RunID: &run.ID, RequestID: &req.ID,
		Kind: model.BotMessageKindUser, ContentJSON: "{}", CreatedAt: timeNowUTC()}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := db.Create(&model.BotEvent{BotID: bot.ID, Seq: 1, EventType: "delta", PayloadJSON: "{}", CreatedAt: timeNowUTC()}).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}

	// 守护触发器对有行的表生效（空表 UPDATE 不触发触发器）。
	guardErr := db.Exec("UPDATE bot_messages SET content_json = 'x'").Error
	if guardErr == nil || !strings.Contains(guardErr.Error(), "append-only") {
		t.Fatalf("守护触发器应拦截 UPDATE: %v", guardErr)
	}

	if err := db.Where("id = ?", bot.ID).Delete(&model.Bot{}).Error; err != nil {
		t.Fatalf("delete bot: %v", err)
	}
	for table, want := range map[string]int64{
		"bots": 0, "bot_runs": 0, "bot_model_requests": 0, "bot_tool_calls": 0,
		"bot_messages": 0, "bot_events": 0,
	} {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM " + table).Scan(&count).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Fatalf("级联删除后 %s 应为空: %d", table, count)
		}
	}
	_ = zap.NewNop
}

func timeNowUTC() time.Time {
	return time.Now().UTC()
}
