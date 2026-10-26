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
		t.Fatalf("后到重复 wait 应移出 running 并标 server_restarted: %+v", dropped)
	}
	// finished_at 保持 NULL：RecoverOnStartup 按 unfinished 取行，补
	// server_restarted 终态消息与结果字段——直落终态会让模型手里的
	// running 回执永远等不到终态投影。
	if dropped.FinishedAt != nil {
		t.Fatalf("去重行应留待恢复收尾（finished_at 须为 NULL）: %+v", dropped)
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

// INT-164 存量修正：调度派发曾让 BotRun.created_at 落本地时区文本
// （如 +08:00），与 UTC 窗口文本比较时被挡在窗口外。EnsureBotSchema 须把
// 存量偏移行重写为 UTC 规范文本（瞬时不变、毫秒尾数保留）并作废受影响
// bot 的汇总标记，幂等且不动规范行。
func TestEnsureBotSchemaNormalizesRunCreatedAtOffsets(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 先经模型插入合法行，再直写 created_at 文本模拟存量形态：+08:00 是
	// 旧派发路径的实际落库结果（瞬时与 run-ok 相同）。
	for _, row := range [][3]string{
		{"run-tz", "bot-tz", "2026-10-20 20:16:07.891256+08:00"},
		{"run-ok", "bot-ok", "2026-10-20 04:16:07.891256+00:00"},
	} {
		r := &model.BotRun{
			ID: row[0], BotID: row[1], Status: model.BotRunStatusCompleted,
			SessionKind: model.BotSessionKindSchedule, TriggerSource: model.BotTriggerSchedule,
			TriggerKey: "tk-" + row[0], Summary: "",
			CreatedAt:          time.Date(2026, 10, 20, 12, 16, 7, 0, time.UTC),
			ConfigSnapshotJSON: "{}", TriggerContextJSON: "{}",
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed %s: %v", row[0], err)
		}
		if err := db.Exec("UPDATE bot_runs SET created_at = ? WHERE id = ?", row[2], row[0]).Error; err != nil {
			t.Fatalf("stamp %s: %v", row[0], err)
		}
	}
	// 受影响 bot 已有覆盖标记与汇总行：标记作废重滚，规范行不动。
	db.Exec(`INSERT INTO bot_usage_rollup_days
		(user_id, bot_id, date, timezone, rolled_at, run_count, maint_run_count,
		 request_count, maint_request_count, last_write_ns)
		VALUES ('u1', 'bot-tz', '2026-10-20', 'Asia/Shanghai', '2026-10-21 00:00:00+00:00',
			1, 0, 1, 0, 1)`)
	db.Exec(`INSERT INTO bot_usage_rollup_days
		(user_id, bot_id, date, timezone, rolled_at, run_count, maint_run_count,
		 request_count, maint_request_count, last_write_ns)
		VALUES ('u1', 'bot-ok', '2026-10-20', 'Asia/Shanghai', '2026-10-21 00:00:00+00:00',
			1, 0, 1, 0, 1)`)

	if err := EnsureBotSchema(db); err != nil {
		t.Fatalf("EnsureBotSchema: %v", err)
	}

	var got string
	if err := db.Raw("SELECT quote(created_at) FROM bot_runs WHERE id = 'run-tz'").Scan(&got).Error; err != nil {
		t.Fatalf("read run-tz: %v", err)
	}
	if got != "'2026-10-20 12:16:07.891256+00:00'" {
		t.Fatalf("+08:00 行应重写为同瞬时 UTC 文本，实得 %q", got)
	}
	if err := db.Raw("SELECT quote(created_at) FROM bot_runs WHERE id = 'run-ok'").Scan(&got).Error; err != nil {
		t.Fatalf("read run-ok: %v", err)
	}
	if got != "'2026-10-20 04:16:07.891256+00:00'" {
		t.Fatalf("规范行不应被改写，实得 %q", got)
	}
	var marks int64
	db.Raw("SELECT COUNT(*) FROM bot_usage_rollup_days WHERE bot_id = 'bot-tz'").Scan(&marks)
	if marks != 0 {
		t.Fatalf("受影响 bot 的汇总标记应作废，剩 %d", marks)
	}
	db.Raw("SELECT COUNT(*) FROM bot_usage_rollup_days WHERE bot_id = 'bot-ok'").Scan(&marks)
	if marks != 1 {
		t.Fatalf("未受影响 bot 的标记不应被动，剩 %d", marks)
	}
	// 幂等：二次执行无副作用。
	if err := EnsureBotSchema(db); err != nil {
		t.Fatalf("二次 EnsureBotSchema 应幂等: %v", err)
	}
}
