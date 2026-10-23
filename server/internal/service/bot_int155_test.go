package service

// INT-155：压缩标识清单的工具调用状态经批量查询获得——每次清单构建一次
// IN 查询取回全部 call_id，替代逐条 FindByID。状态按构建时点取库内值：
// 摘要请求期间 running→终态的流转要在重建的清单中立即可见（云端复审 P2）。

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
	// 账本没有 tool_reply 引用，折叠装载不产生 bot_tool_calls 查询。
	if *reads != 0 {
		t.Fatalf("planCompaction 不应查 bot_tool_calls，实得 %d", *reads)
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
		t.Fatalf("一次清单构建应只发一次批量查询，实得 %d", *reads)
	}

	// 两次构建之间状态流转立即可见（不沿用首次构建的快照）。
	env.db.Model(&model.BotToolCall{}).Where("id = ?", runningID).
		Update("status", model.BotCallStatusSucceeded)
	manifest2 := env.bots.buildIdentifierManifest(plan)
	if !strings.Contains(manifest2, "- call:"+runningID+" fs_issue/list status=succeeded") {
		t.Fatalf("重建清单应反映最新状态:\n%s", manifest2)
	}
	if *reads != 2 {
		t.Fatalf("第二次构建应再发一次批量查询，实得 %d", *reads)
	}
}

// 手工构造的 plan（无账本查询预载）同样一次批量取回；两次构建各查一次，
// 状态按构建时点的库内值出。
func TestBotPendingCallSnapshotBatchPerBuild(t *testing.T) {
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
		t.Fatalf("应一次批量取回，实查 %d 次", *reads)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "- call:"+callID+" fs_issue/list status=succeeded") ||
		!strings.Contains(joined, "- call:call-missing fs_issue/list status=unknown") {
		t.Fatalf("状态行不符: %s", joined)
	}
	env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("status", model.BotCallStatusCancelled)
	lines2 := env.bots.pendingCallSnapshot(plan)
	if *reads != 2 {
		t.Fatalf("第二次构建应再查一次，实得 %d", *reads)
	}
	if !strings.Contains(strings.Join(lines2, "\n"), "- call:"+callID+" fs_issue/list status=cancelled") {
		t.Fatalf("第二次构建应取到最新状态: %s", strings.Join(lines2, "\n"))
	}
}

// 完整压缩周期端到端：一轮内 bot_tool_calls 的查询 = planCompaction 折叠
// 装载 1 次 + 每次清单构建 1 次（本轮前置估算/输出预算/摘要后重建共 3 次），
// 与引用数量无关——旧实现同样的路径是 3×T 次点查。
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
	if *reads != 4 {
		t.Fatalf("一轮压缩对 bot_tool_calls 应为 1 次装载 + 3 次清单构建 = 4 次批量查询，实得 %d", *reads)
	}
}
