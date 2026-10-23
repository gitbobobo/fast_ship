package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-160：running wait 唯一索引（idx_bot_tool_calls_running_wait）前的前置
// 去重——旧版本并发漏洞可能在库里留下同（bot, env, thread）的多条 running
// wait；EnsureBotSchema 先于启动恢复执行，不收敛就建不了索引、挡住启动。
func TestEnsureBotSchemaDedupesRunningWaits(t *testing.T) {
	// 不开外键：孤立 call 行足够验证去重与索引，不必连带建 bot/request 行。
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// 只建表不建索引（EnsureBotSchema 之后才存在的形状），模拟升级前存量库。
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	insertWait := func(id, thread string, status model.BotToolCallStatus, created time.Time) {
		t.Helper()
		call := &model.BotToolCall{
			ID: id, BotID: "bot-1", RequestID: "req-" + id,
			ProviderToolCallID: "prov-" + id, Ordinal: 1,
			ToolName: "fs_thread", Action: "wait",
			ArgumentsJSON:        `{"action":"wait","env_id":"env-1","thread_id":"` + thread + `"}`,
			ArgumentsHash:        "h-" + id,
			ExecutionMode:        string(model.BotExecutionAsync),
			Status:               status,
			ExecutionContextJSON: "{}",
			CreatedAt:            created,
		}
		if err := db.Create(call).Error; err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// 同键两条 running（旧漏洞形状）：后到者应被落 server_restarted 终态。
	insertWait("wait-old", "th-1", model.BotCallStatusRunning, now.Add(-time.Hour))
	insertWait("wait-new", "th-1", model.BotCallStatusRunning, now)
	// 不同线程不受影响。
	insertWait("wait-other", "th-2", model.BotCallStatusRunning, now)

	if err := EnsureBotSchema(db); err != nil {
		t.Fatalf("含重复 running wait 的存量库上 EnsureBotSchema 不应失败: %v", err)
	}

	var kept, dropped, other model.BotToolCall
	for id, dst := range map[string]*model.BotToolCall{
		"wait-old": &kept, "wait-new": &dropped, "wait-other": &other,
	} {
		if err := db.Where("id = ?", id).First(dst).Error; err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
	}
	if kept.Status != model.BotCallStatusRunning {
		t.Fatalf("最早受理的 wait 应保留 running，实得 %s", kept.Status)
	}
	if dropped.Status != model.BotCallStatusFailed || dropped.ErrorCode == nil ||
		*dropped.ErrorCode != "server_restarted" {
		t.Fatalf("后到重复 wait 应落 server_restarted 终态: %+v", dropped)
	}
	if dropped.FinishedAt == nil || dropped.ResultJSON == nil ||
		*dropped.ResultJSON != `{"outcome":"server_restarted"}` {
		t.Fatalf("终态行应带 server_restarted 结果: %+v", dropped)
	}
	if other.Status != model.BotCallStatusRunning {
		t.Fatalf("异线程 wait 不应被误清: %s", other.Status)
	}

	// 索引已建好：同键再插 running 必须被拒。
	dup := &model.BotToolCall{
		ID: uuid.NewString(), BotID: "bot-1", RequestID: "req-x",
		ProviderToolCallID: "prov-x", Ordinal: 1,
		ToolName: "fs_thread", Action: "wait",
		ArgumentsJSON: `{"action":"wait","env_id":"env-1","thread_id":"th-1"}`,
		ArgumentsHash: "h-x", ExecutionMode: string(model.BotExecutionAsync),
		Status: model.BotCallStatusRunning, ExecutionContextJSON: "{}",
		CreatedAt: now,
	}
	if err := db.Create(dup).Error; err == nil {
		t.Fatalf("EnsureBotSchema 后同键 running wait 必须被唯一索引拒绝")
	}
}
