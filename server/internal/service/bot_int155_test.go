package service

// INT-155：压缩标识清单的工具调用状态必须经批量查询获得——planCompaction
// 预载后整轮压缩（前置估算、输出预算、摘要成功后的重建）不再点查
// bot_tool_calls；清单内容与逐条 FindByID 的口径一致。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// botToolCallReadProbe 统计对 bot_tool_calls 的查询次数。
func botToolCallReadProbe(t *testing.T, env *botTestEnv) *int {
	t.Helper()
	reads := 0
	const name = "test:int155_call_reads"
	if err := env.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "bot_tool_calls" {
			reads++
		}
	}); err != nil {
		t.Fatalf("register probe: %v", err)
	}
	t.Cleanup(func() {
		if err := env.db.Callback().Query().Remove(name); err != nil {
			t.Errorf("remove probe: %v", err)
		}
	})
	return &reads
}

func TestBotCompactionManifestBatchLoadsCallStatus(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int155")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}

	// 三种引用：库里有终态行、运行中行、以及完全没有账本行的 id。
	_, failedID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusFailed, string(model.BotExecutionSync))
	_, runningID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionAsync))
	seedBotUserMessage(t, env, bot.ID, "最早的问题")
	decl := seedBotAssistantDeclares(t, env, bot.ID, []botSeedCall{
		{CallID: failedID, ProviderID: "prov-failed"},
		{CallID: runningID, ProviderID: "prov-running"},
		{CallID: "call-no-row", ProviderID: "prov-none"},
	})
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	reads := botToolCallReadProbe(t, env)
	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if *reads != 1 {
		t.Fatalf("planCompaction 应只发一次 bot_tool_calls 批量查询，实得 %d", *reads)
	}

	manifest := env.bots.buildIdentifierManifest(plan)
	for _, want := range []string{
		"- call:" + failedID + " fs_issue/list status=failed",
		"- call:" + runningID + " fs_issue/list status=running",
		"- call:call-no-row fs_issue/list status=unknown",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("清单缺行 %q:\n%s", want, manifest)
		}
	}
	seqPart := fmt.Sprintf(" (seq %d)", decl.Seq)
	if !strings.Contains(manifest, "status=failed"+seqPart) {
		t.Fatalf("调用行应带声明消息的 seq=%d:\n%s", decl.Seq, manifest)
	}
	if *reads != 1 {
		t.Fatalf("清单构建不应再查 bot_tool_calls，实得 %d", *reads)
	}
	// 同轮第二次构建（摘要成功后重建）同样命中缓存。
	_ = env.bots.buildIdentifierManifest(plan)
	if *reads != 1 {
		t.Fatalf("同轮重复构建不应重查 bot_tool_calls，实得 %d", *reads)
	}
}

// 手工构造的 plan（无 Calls 预载）退化为一次批量补齐，而不是逐条点查。
func TestBotPendingCallSnapshotLazyBatch(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int155lazy")
	bot := env.createBot(t, userID)
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusSucceeded, string(model.BotExecutionSync))
	a := seedBotAssistantDeclares(t, env, bot.ID, []botSeedCall{
		{CallID: callID, ProviderID: "prov-x"},
		{CallID: "call-missing", ProviderID: "prov-m"},
	})
	plan := &botCompactionPlan{
		State:   &model.BotContextState{BotID: bot.ID},
		AllRows: []model.BotMessage{*a},
	}
	reads := botToolCallReadProbe(t, env)
	lines := env.bots.pendingCallSnapshot(plan)
	if *reads != 1 {
		t.Fatalf("缺预载时应一次批量补齐，实查 %d 次", *reads)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "- call:"+callID+" fs_issue/list status=succeeded") ||
		!strings.Contains(joined, "- call:call-missing fs_issue/list status=unknown") {
		t.Fatalf("状态行不符: %s", joined)
	}
	// 缺失 id 已标记为「查过无此行」，再次构建不再查。
	_ = env.bots.pendingCallSnapshot(plan)
	if *reads != 1 {
		t.Fatalf("缺失 id 不应被重查，实得 %d", *reads)
	}
}

// 完整压缩周期端到端：一轮内 bot_tool_calls 只有 planCompaction 的一次批量
// 查询（前置估算、输出预算、摘要成功后重建清单均命中缓存）。
func TestBotCompactionCycleSingleCallQuery(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("压缩后的摘要")...)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "int155cycle")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 30000
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusSucceeded, string(model.BotExecutionSync))
	seedCompactableLedger(t, env, bot.ID, strings.Repeat("很长的旧历史。", 1200))
	seedBotAssistantDeclares(t, env, bot.ID, []botSeedCall{{CallID: callID, ProviderID: "prov-c"}})
	seedBotToolReplyOnly(t, env, bot.ID, callID, "prov-c", "小结果")
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	reads := botToolCallReadProbe(t, env)
	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil {
		var comp model.BotCompaction
		env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&comp)
		var ce *botCompactionError
		code := ""
		if errors.As(err, &ce) {
			code = ce.Code + " " + ce.Message
		}
		t.Fatalf("cycle: %v (recorded=%v ce=%s)", err, comp.ErrorCode, code)
	}
	if !out.Published {
		t.Fatalf("应正常发布: %+v", out)
	}
	if *reads != 1 {
		t.Fatalf("一轮压缩对 bot_tool_calls 应只有一次批量查询，实得 %d", *reads)
	}
}
