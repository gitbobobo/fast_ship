package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-102 测试：上下文压缩（折叠/摘要/切点/强制重试）、记忆文件与版本、
// FTS 检索、冻结注入、工具集版本与改名动作。驱动方式与 INT-101 相同：
// 假 OpenAI 兼容服务脚本化回合。

// seedBotUserMessage 直接落一条 user 消息（绕过发送流程，快速构造账本）。
func seedBotUserMessage(t *testing.T, env *botTestEnv, botID, text string) *model.BotMessage {
	t.Helper()
	var msg *model.BotMessage
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		seq, err := env.bots.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(botUserContent{Text: text})
		msg = &model.BotMessage{
			ID: uuid.NewString(), BotID: botID, Seq: seq,
			Kind: model.BotMessageKindUser, ContentJSON: string(raw),
			CreatedAt: time.Now().UTC(),
		}
		return repository.NewBotMessageRepository(tx).AppendTx(tx, msg)
	}); err != nil {
		t.Fatalf("seed user message: %v", err)
	}
	return msg
}

// seedBotAssistantToolBlock 落一个 assistant(tool_calls)+tool_reply 协议块。
// 返回 assistant 行与 tool reply 行；replyText 可构造超大结果。
func seedBotAssistantToolBlock(t *testing.T, env *botTestEnv, botID, callID, toolName, replyText string) (*model.BotMessage, *model.BotMessage) {
	return seedBotAssistantToolBlockArgs(t, env, botID, callID, toolName, "{}", replyText)
}

func seedBotAssistantToolBlockArgs(t *testing.T, env *botTestEnv, botID, callID, toolName, toolArgs, replyText string) (*model.BotMessage, *model.BotMessage) {
	t.Helper()
	var a, tr *model.BotMessage
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		msgs := repository.NewBotMessageRepository(tx)
		seq, err := env.bots.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		content := botAssistantContent{Text: "", ToolCalls: []botAssistantToolCall{{
			CallID: callID, ProviderToolCallID: "prov-" + callID,
			ToolName: toolName, Action: "list", Arguments: toolArgs,
		}}}
		raw, _ := json.Marshal(content)
		a = &model.BotMessage{
			ID: uuid.NewString(), BotID: botID, Seq: seq,
			Kind: model.BotMessageKindAssistant, ContentJSON: string(raw),
			CreatedAt: time.Now().UTC(),
		}
		if err := msgs.AppendTx(tx, a); err != nil {
			return err
		}
		seq2, err := env.bots.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		replyRaw, _ := json.Marshal(botToolReplyContent{
			CallID: callID, ProviderToolCallID: "prov-" + callID,
			ToolName: toolName, Status: "succeeded", Content: replyText,
		})
		// 不设 CallID 列（有指向 bot_tool_calls 的外键）：种子块只造消息
		// 账本，call_id 在正文 JSON 里已带，协议投影只看正文。
		tr = &model.BotMessage{
			ID: uuid.NewString(), BotID: botID, Seq: seq2,
			Kind: model.BotMessageKindToolReply, ContentJSON: string(replyRaw),
			CreatedAt: time.Now().UTC(),
		}
		return msgs.AppendTx(tx, tr)
	}); err != nil {
		t.Fatalf("seed tool block: %v", err)
	}
	return a, tr
}

func botFixtureRun(t *testing.T, env *botTestEnv, bot *model.Bot) *model.BotRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "fixture:" + uuid.NewString(),
		Status: model.BotRunStatusRunning, ConfigSnapshotJSON: "{}",
		CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("fixture run: %v", err)
	}
	return run
}

// ---------- 记忆：路径校验 / CRUD / 版本 / 检索 ----------

func TestBotMemoryPathValidation(t *testing.T) {
	ok := []string{"USER.md", "MEMORY.md", "daily/2026-01-31.md", "daily/2024-02-29.md"}
	for _, p := range ok {
		if err := botValidateMemoryPath(p); err != nil {
			t.Fatalf("%s 应合法: %v", p, err)
		}
	}
	bad := []string{
		"", "user.md", "memory.md", "Daily/2026-01-01.md",
		"../USER.md", "daily/../x.md", "daily//x.md", "/USER.md", `a\b`,
		"daily/2026-13-01.md", "daily/2026-02-30.md", "daily/2023-02-29.md",
		"daily/2026-1-1.md", "notes/x.md", "USER.md/x", "daily/2026-01-01.md/extra",
	}
	for _, p := range bad {
		if err := botValidateMemoryPath(p); err == nil {
			t.Fatalf("%s 应被拒绝", p)
		}
	}
}

func TestBotMemoryLifecycle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "memlife")
	bot := env.createBot(t, userID)
	repo := repository.NewBotMemoryRepository(env.db)
	now := time.Now().UTC()

	// create → v1
	f, v, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpCreate, "用户喜欢简洁回复", false, nil,
		model.BotMemoryWriterMain, "", "", "k1", now)
	if err != nil || f.CurrentVersion != 1 || v.Version != 1 {
		t.Fatalf("create: %v v=%d", err, f.CurrentVersion)
	}
	// 同 path create 再冲突
	if _, _, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpCreate, "x", false, nil,
		model.BotMemoryWriterMain, "", "", "k2", now); !errors.Is(err, repository.ErrMemoryFileExists) {
		t.Fatalf("重复 create 应冲突: %v", err)
	}
	// replace 带 CAS 失败
	ev0 := int64(0)
	if _, _, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpReplace, "y", false, &ev0,
		model.BotMemoryWriterMain, "", "", "k3", now); !errors.Is(err, repository.ErrMemoryVersionConflict) {
		t.Fatalf("expected_version 不符应冲突: %v", err)
	}
	// replace 带正确 CAS → v2
	ev1 := int64(1)
	f, _, err = repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpReplace, "用户喜欢 Go", false, &ev1,
		model.BotMemoryWriterMain, "", "", "k4", now)
	if err != nil || f.CurrentVersion != 2 {
		t.Fatalf("replace: %v", err)
	}
	// 幂等：同 operation_key 重放不产生新版本
	f2, v2, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpReplace, "用户喜欢 Go", false, &ev1,
		model.BotMemoryWriterMain, "", "", "k4", now)
	if err != nil || f2.CurrentVersion != 2 || v2 == nil {
		t.Fatalf("幂等重放应返回既有版本: %v", err)
	}
	// delete → 墓碑
	f, _, err = repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpDelete, "", true, nil,
		model.BotMemoryWriterMain, "", "", "k5", now)
	if err != nil || f.DeletedAt == nil || f.CurrentVersion != 3 {
		t.Fatalf("delete: %v", err)
	}
	// 墓碑上非 create 写入被拒
	if _, _, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpReplace, "z", false, nil,
		model.BotMemoryWriterMain, "", "", "k6", now); !errors.Is(err, repository.ErrMemoryFileDeleted) {
		t.Fatalf("墓碑文件写入应被拒: %v", err)
	}
	// restore v2 → v4（正文回到 v2 内容）
	f, _, err = repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpRestore, "用户喜欢 Go", false, nil,
		model.BotMemoryWriterMain, "", "", "k7", now)
	if err != nil || f.DeletedAt != nil || f.CurrentVersion != 4 || f.Content != "用户喜欢 Go" {
		t.Fatalf("restore: %v content=%q", err, f.Content)
	}
	// 版本历史完整
	vers, err := repo.ListVersionsTx(env.db, f.ID, 0, 50)
	if err != nil || len(vers) != 4 {
		t.Fatalf("版本历史: %v n=%d", err, len(vers))
	}
}

func TestBotMemoryRetentionCap(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "memcap")
	bot := env.createBot(t, userID)
	repo := repository.NewBotMemoryRepository(env.db)
	now := time.Now().UTC()
	if _, _, err := repo.WriteVersionTx(env.db, bot.ID, "MEMORY.md",
		model.BotMemoryOpCreate, "v0", false, nil, model.BotMemoryWriterMain, "", "", "r0", now); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 1; i <= 55; i++ {
		ev := int64(i)
		if _, _, err := repo.WriteVersionTx(env.db, bot.ID, "MEMORY.md",
			model.BotMemoryOpReplace, fmt.Sprintf("v%d", i), false, &ev,
			model.BotMemoryWriterMain, "", "", fmt.Sprintf("r%d", i), now); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	f, _ := repo.FindFile(bot.ID, "MEMORY.md")
	vers, _ := repo.ListVersionsTx(env.db, f.ID, 0, 200)
	if len(vers) != 50 {
		t.Fatalf("应保留最近 50 版，实得 %d", len(vers))
	}
	if vers[0].Version != 56 {
		t.Fatalf("最新版本应 v56，实得 v%d", vers[0].Version)
	}
}

func TestBotMemoryToolEndToEnd(t *testing.T) {
	// 主线驱动：模型调 fs_memory create → 回复用户；然后 fs_memory_read 能读回。
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "c1", Name: "fs_memory",
			Args: `{"action":"create","path":"USER.md","content":"用户叫 Bobo"}`,
		}}},
		fakeModelTurn{Text: "已记住：用户叫 Bobo。"},
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "c2", Name: "fs_memory",
			Args: `{"action":"read","path":"USER.md"}`,
		}}},
		fakeModelTurn{Text: "用户叫 Bobo。"},
	)
	userID := createBotTestUser(t, env.db, "meme2e")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "记住我叫 Bobo", ClientMessageID: "m1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "第一轮完成", 10*time.Second, func() bool { return env.botSettled(bot.ID) })
	f, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "USER.md")
	if err != nil || f.Content != "用户叫 Bobo" || f.CurrentVersion != 1 {
		t.Fatalf("记忆未落库: %v", err)
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "我叫什么？", ClientMessageID: "m2",
	}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	// botSettled 在 run1 完成、run2 尚未创建的间隙就已成立——等第二轮 run
	// 出现并完成。
	waitForBot(t, "第二轮完成", 10*time.Second, func() bool {
		var n int64
		env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&n)
		return n >= 2 && env.botSettled(bot.ID)
	})
	// 记忆工具回复已进账本（tool reply）
	var replies int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindToolReply).Count(&replies)
	if replies < 2 {
		t.Fatalf("应有至少两条记忆工具回复，实得 %d", replies)
	}
}

func TestBotMemoryFTSSearch(t *testing.T) {
	env := setupBotTestEnv(t)
	userA := createBotTestUser(t, env.db, "fts-a")
	userB := createBotTestUser(t, env.db, "fts-b")
	botA := env.createBot(t, userA)
	botB := env.createBot(t, userB)
	repo := repository.NewBotMemoryRepository(env.db)
	now := time.Now().UTC()
	if _, _, err := repo.WriteVersionTx(env.db, botA.ID, "MEMORY.md",
		model.BotMemoryOpCreate, "项目代号是天狼星计划，负责人喜欢用 Golang", false, nil,
		model.BotMemoryWriterMain, "", "", "s1", now); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := repo.WriteVersionTx(env.db, botA.ID, "daily/2026-01-01.md",
		model.BotMemoryOpCreate, "今天讨论了上线节奏", false, nil,
		model.BotMemoryWriterMain, "", "", "s2", now); err != nil {
		t.Fatalf("write2: %v", err)
	}
	if _, _, err := repo.WriteVersionTx(env.db, botB.ID, "MEMORY.md",
		model.BotMemoryOpCreate, "另一台机器人的天狼星笔记", false, nil,
		model.BotMemoryWriterMain, "", "", "s3", now); err != nil {
		t.Fatalf("write3: %v", err)
	}
	// trigram 中文检索 + 机器人隔离
	hits, err := repo.SearchFTS(botA.ID, "天狼星", "", 0, 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("中文检索应命中: %v n=%d", err, len(hits))
	}
	for _, h := range hits {
		if h.BotID != botA.ID {
			t.Fatalf("检索泄露到其他机器人")
		}
	}
	// 文件删除后不再命中（FTS 只覆盖未删除文件）
	if _, _, err := repo.WriteVersionTx(env.db, botA.ID, "MEMORY.md",
		model.BotMemoryOpDelete, "", true, nil, model.BotMemoryWriterMain, "", "", "s4", now); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits2, err := repo.SearchFTS(botA.ID, "天狼星", "", 0, 10)
	if err != nil {
		t.Fatalf("search2: %v", err)
	}
	for _, h := range hits2 {
		if h.Path == "MEMORY.md" {
			t.Fatalf("已删除文件不应命中")
		}
	}
	// 短词（<3 字符）走 LIKE 也能命中
	hits3, err := repo.SearchFTS(botA.ID, "上线", "", 0, 10)
	if err != nil || len(hits3) == 0 {
		t.Fatalf("短词检索应命中: %v", err)
	}
}

func TestBotMemoryFrozenSnapshot(t *testing.T) {
	// 注入快照按 generation 冻结：记忆写入立即可被工具读到，但构建出的
	// 前缀保持不变，直到压缩发布推进 generation。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "memfreeze")
	bot := env.createBot(t, userID)
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)

	built1, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build1: %v", err)
	}
	if len(built1.Request.Messages) < 1 {
		t.Fatalf("build1 空")
	}
	count1 := len(built1.Request.Messages)

	// 写入记忆 → 同 generation 内注入不变（前缀稳定）。
	repo := repository.NewBotMemoryRepository(env.db)
	if _, _, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md",
		model.BotMemoryOpCreate, "用户喜欢 Go", false, nil,
		model.BotMemoryWriterMain, "", "", "f1", time.Now().UTC()); err != nil {
		t.Fatalf("write: %v", err)
	}
	built2, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build2: %v", err)
	}
	if len(built2.Request.Messages) != count1 || built2.PrefixHash != built1.PrefixHash {
		t.Fatalf("同 generation 前缀必须逐字节稳定")
	}
	// 模拟压缩发布 generation+1 → 快照刷新，记忆段出现。
	if _, err := repository.NewBotContextRepository(env.db).PublishTx(env.db, bot.ID,
		0, 0, "", "", "{}", time.Now().UTC()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	built3, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build3: %v", err)
	}
	if built3.PrefixHash == built1.PrefixHash {
		t.Fatalf("generation 变化后前缀应更新（记忆注入生效）")
	}
	raw, _ := json.Marshal(built3.Request.Messages)
	if !strings.Contains(string(raw), "用户喜欢 Go") {
		t.Fatalf("新 generation 应注入记忆正文")
	}
}

// ---------- 压缩：切点 / 折叠 / 摘要 / 强制重试 ----------

func TestBotCompactionSafeCutAndFold(t *testing.T) {
	// 账本：旧轮(用户+大结果工具块) + 最近 3 个用户轮次。
	// 切点必须落在倒数第 3 个用户消息之前，且不切断 tool 块；
	// 超过窗口阈值的大 tool 回复进入折叠映射。
	env := setupBotTestEnv(t, fakeModelTurn{Text: `{"items":[{"text":"旧轮要点：用户在做 INT-101","source_seq":1}]}`}, fakeModelTurn{Text: "滚动摘要：用户在做机器人主线，曾读过大列表。"})
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "compact")
	bot := env.createBot(t, userID)
	botRow, _ := env.bots.bots.Get(bot.ID)
	env.cfg.Bots.Context.WindowTokens = 8000

	seedBotUserMessage(t, env, bot.ID, "最早的问题")
	bigReply := strings.Repeat("很长的工具结果", 400)
	seedBotAssistantToolBlock(t, env, bot.ID, "call-big", "fs_issue", bigReply)
	seedBotUserMessage(t, env, bot.ID, "保护轮1")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-p1", "fs_issue", "小结果1")
	seedBotUserMessage(t, env, bot.ID, "保护轮2")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-p2", "fs_issue", "小结果2")
	seedBotUserMessage(t, env, bot.ID, "保护轮3")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-p3", "fs_issue", "小结果3")

	run := botFixtureRun(t, env, botRow)
	var maxSeq int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ?", bot.ID).Select("MAX(seq)").Scan(&maxSeq)
	run.InputThroughSeq = maxSeq
	env.db.Model(run).Update("input_through_seq", maxSeq)
	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("compaction: %v", err)
	}
	if out == nil || out.Run == nil {
		t.Fatalf("compaction: missing outcome")
	}
	maintRun := out.Run
	state, err := repository.NewBotContextRepository(env.db).Find(bot.ID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Generation != 1 || state.SummaryThroughSeq == 0 {
		t.Fatalf("应发布 generation1 且有摘要水位: %+v", state)
	}
	// 摘要只覆盖保护轮之前的段：倒数第 3 个 user 消息起及其后均不受摘要。
	var protectedSeq int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindUser).
		Order("seq DESC").Limit(1).Offset(2).Pluck("seq", &protectedSeq)
	if protectedSeq == 0 {
		t.Fatalf("未取到保护边界")
	}
	if state.SummaryThroughSeq >= protectedSeq {
		t.Fatalf("摘要水位不得越过最近三轮用户: through=%d protected>=%d", state.SummaryThroughSeq, protectedSeq)
	}
	// 大结果被折叠成单行引用，原文仍在账本
	var folded map[string]string
	if err := json.Unmarshal([]byte(state.FoldedResultsJSON), &folded); err != nil || len(folded) != 1 {
		t.Fatalf("应折叠恰好一条大结果: %v", err)
	}
	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, _ := json.Marshal(built.Request.Messages)
	if strings.Contains(string(raw), bigReply) {
		t.Fatalf("折叠后完整大结果不应进请求")
	}
	if !strings.Contains(string(raw), "会话摘要") || !strings.Contains(string(raw), "机器人主线") {
		t.Fatalf("摘要与标识符附录应注入")
	}
	// 仅折叠（摘要失败的回退形态）下，大结果在请求里变成指向
	// fs_result.read 的单行引用——投影状态是持久行（水位守卫禁止
	// 回退 UPDATE，删行重插构造 fold_only 形态）。
	if err := env.db.Where("bot_id = ?", bot.ID).Delete(&model.BotContextState{}).Error; err != nil {
		t.Fatalf("del state: %v", err)
	}
	foldOnly := &model.BotContextState{
		BotID: bot.ID, Generation: 1, SummaryThroughSeq: 0,
		SummaryText: "", IdentifierManifestJSON: "{}",
		FoldedResultsJSON: state.FoldedResultsJSON,
	}
	if err := env.db.Create(foldOnly).Error; err != nil {
		t.Fatalf("reinsert state: %v", err)
	}
	built2, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	raw2, _ := json.Marshal(built2.Request.Messages)
	if !strings.Contains(string(raw2), "folded") || !strings.Contains(string(raw2), "fs_result") {
		t.Fatalf("折叠引用应指向 fs_result.read: %s", raw2)
	}
	if strings.Contains(string(raw2), bigReply) {
		t.Fatalf("仅折叠态下完整大结果不应进请求")
	}
	// 压缩记录 + 维护运行终态
	var compactions int64
	env.db.Model(&model.BotCompaction{}).Where("maintenance_run_id = ?", maintRun.ID).Count(&compactions)
	if compactions != 1 {
		t.Fatalf("应有一条压缩记录")
	}
	fresh, _ := repository.NewBotMaintenanceRepository(env.db).FindByID(maintRun.ID)
	if fresh.Status != model.BotMaintenanceSucceeded {
		t.Fatalf("维护运行应 succeeded，实得 %s", fresh.Status)
	}
	// 静默存档：archive 回合输出了一个条目 → daily 文件落库
	var dailyCount int64
	env.db.Model(&model.BotMemoryFile{}).Where("bot_id = ? AND path LIKE ?", bot.ID, "daily/%").Count(&dailyCount)
	if dailyCount != 1 {
		t.Fatalf("静默存档应写一个 daily 文件")
	}
}

func TestBotFoldedResultReadableViaFsResultRead(t *testing.T) {
	// 折叠引用之后，fs_result.read 仍按 call_id 取回完整结果。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "foldread")
	bot := env.createBot(t, userID)
	seedBotUserMessage(t, env, bot.ID, "q")
	big := strings.Repeat("x", 20000)
	_, tr := seedBotAssistantToolBlock(t, env, bot.ID, "call-r", "fs_issue", big)
	// 调用账本需要真实的 run/request 外键。
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	reqRow := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, PromptRevision: 1, ToolsetVersion: 1,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x",
		TokenEstimator: "chars/4", StartedAt: time.Now().UTC(),
	}
	if err := env.bots.requests.CreateTx(env.db, reqRow); err != nil {
		t.Fatalf("seed req: %v", err)
	}
	call := &model.BotToolCall{
		ID: "call-r", BotID: bot.ID, RequestID: reqRow.ID,
		ProviderToolCallID: "prov-call-r", ToolName: "fs_issue", Action: "list",
		Status: model.BotCallStatusSucceeded, ExecutionMode: "sync",
		ResultJSON: &big, ResultChars: int64(len(big)),
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed call: %v", err)
	}
	// 手工折叠该行并发布
	state := &model.BotContextState{BotID: bot.ID}
	folded := map[string]string{tr.ID: botFoldedReference("call-r", 1, int64(len(big)))}
	rawF, _ := json.Marshal(folded)
	if _, err := repository.NewBotContextRepository(env.db).PublishTx(env.db, bot.ID,
		state.Generation, 0, "", "", string(rawF), time.Now().UTC()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// fs_result.read 取回完整内容
	out, err := env.bots.execResultRead(&model.BotToolCall{
		BotID: bot.ID, ArgumentsJSON: `{"action":"read","call_id":"call-r","offset":0,"limit":30000}`,
	})
	if err != nil || !strings.Contains(out, strings.Repeat("x", 100)) {
		t.Fatalf("fs_result.read 应取回完整结果: %v", err)
	}
}

func TestBotContextLimitRetryOnce(t *testing.T) {
	// 上游首次返回 context_length_exceeded：强制压缩后恰好重试一次成功；
	// 第二次仍超限则 run 落 failed，不再第三次重试。
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 窗口探测（INT-115）不计入模型请求数。
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = body
		reqCount++
		if reqCount == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"context_length_exceeded: maximum context length is 512 tokens","code":"context_length_exceeded"}}`)
			return
		}
		// 后续（含维护请求与重试）一律正常回复
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "limitretry")
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "retry-bot", BaseURL: srv.URL, Model: "m", APIKey: "sk-x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 16000
	botRow, _ := env.bots.bots.Get(bot.ID)
	seedCompactableLedger(t, env, bot.ID, "旧轮次")
	markHistoryConsumed(t, env, botRow, nil)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "触发", ClientMessageID: "r1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run 收尾", 10*time.Second, func() bool { return env.botSettled(bot.ID) })
	var compactions int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ? AND reason = ?", bot.ID, string(model.BotCompactionForced)).Count(&compactions)
	if compactions < 1 {
		t.Fatalf("应有一次强制压缩记录")
	}
	// 重试成功：run 应 completed（不是 failed）
	var run model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&run)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("重试后 run 应 completed，实得 %s err=%v", run.Status, run.ErrorMessage)
	}
	_ = botRow
}

func TestBotOrdinaryErrorNoRetry(t *testing.T) {
	// 普通 400（非上下文超限）不触发压缩与重试。
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 窗口探测（INT-115）不计入模型请求数。
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reqCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"invalid parameter: temperature","code":"invalid_request_error"}}`)
	}))
	t.Cleanup(srv.Close)
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "noretry")
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "e400", BaseURL: srv.URL, Model: "m", APIKey: "sk-x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "hi", ClientMessageID: "e1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run 收尾", 10*time.Second, func() bool { return env.botSettled(bot.ID) })
	if reqCount != 1 {
		t.Fatalf("普通 400 不应重试，实际请求 %d 次", reqCount)
	}
}

// ---------- 工具集版本与改名 ----------

func TestBotToolsetVersions(t *testing.T) {
	v1 := botToolsetVersionOf(1)
	v2 := botToolsetVersionOf(2)
	if v1 == v2 || v1.hash == v2.hash {
		t.Fatalf("v1/v2 工具集必须不同（hash 随实际字节变化）")
	}
	if _, ok := v2.byName["fs_memory_search"]; !ok {
		t.Fatalf("v2 应含 fs_memory_search")
	}
	if _, ok := v1.byName["fs_memory_search"]; ok {
		t.Fatalf("v1 不应含 fs_memory_search")
	}
	if len(v2.schemas) != 11 {
		t.Fatalf("v2 应为 11 个工具，实得 %d", len(v2.schemas))
	}
	// v1 会话调用 fs_memory/search → 未知动作（v1 占位不含 search）
	if _, err := botPrepareToolCall(v1, "fs_memory", `{"action":"search","query":"x"}`); err == nil {
		t.Fatalf("v1 fs_memory/search 应报错")
	}
	// v2 改名动作 → botRenamedActionError
	_, err := botPrepareToolCall(v2, "fs_memory", `{"action":"search","query":"x"}`)
	var renamed *botRenamedActionError
	if !errors.As(err, &renamed) || renamed.RenamedTo != "fs_memory_search/search" {
		t.Fatalf("应回改名提示，实得 %v", err)
	}
	reply := botToolActionErrorReply(err)
	if !strings.Contains(reply, "action_renamed") || !strings.Contains(reply, "fs_memory_search") {
		t.Fatalf("改名回复应含结构化提示: %s", reply)
	}
}

func TestBotToolsetUpgradeOnCompactionBoundary(t *testing.T) {
	// 存量 bot（toolset_version=1）在压缩发布后推进到 v2 并留 lifecycle 告知。
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[]"}, fakeModelTurn{Text: "摘要"})
	userID := createBotTestUser(t, env.db, "tsupg")
	bot := env.createBot(t, userID)
	env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version": 1, "last_told_version": 1, "platform_prompt_version": 1,
	})
	botRow, _ := env.bots.bots.Get(bot.ID)
	env.cfg.Bots.Context.WindowTokens = 8000
	seedBotUserMessage(t, env, bot.ID, strings.Repeat("旧历史需要被摘要。", 400))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, fmt.Sprintf("u%d", i))
	}
	run := botFixtureRun(t, env, botRow)
	var maxSeq int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ?", bot.ID).Select("MAX(seq)").Scan(&maxSeq)
	run.InputThroughSeq = maxSeq
	if _, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold); err != nil {
		t.Fatalf("compaction: %v", err)
	}
	fresh, _ := env.bots.bots.Get(bot.ID)
	if fresh.ToolsetVersion != botToolsetVersion {
		t.Fatalf("压缩边界应推进工具集到 v%d，实得 v%d", botToolsetVersion, fresh.ToolsetVersion)
	}
	env.bots.maybeTellToolset(fresh, run)
	var lifecycle int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND dedup_key = ?",
		bot.ID, fmt.Sprintf("toolset_notice:%d", botToolsetVersion)).Count(&lifecycle)
	if lifecycle != 1 {
		t.Fatalf("应有一条 lifecycle 告知消息")
	}
}

// ---------- 存档去重（纯函数） ----------

func TestBotArchiveDedup(t *testing.T) {
	existing := "- 用户叫 Bobo\n- 项目代号天狼星\n"
	items := []string{
		"用户叫 Bobo",   // 完全相同 → 拒
		"项目代号天狼星",    // 完全相同 → 拒
		"新的进展：完成了压缩", // 新条目 → 收
	}
	got := botFilterArchiveItems(existing, items)
	if len(got) != 1 || got[0] != "新的进展：完成了压缩" {
		t.Fatalf("去重结果不符: %v", got)
	}
	// 长段近似重复：Jaccard ≥0.9 拒（条目须 ≥128 字符才走相似度规则）
	long := strings.Repeat("这是一段足够长的归档条目，用来触发相似度判定。", 8)
	existing2 := "- " + long + "\n"
	if got := botFilterArchiveItems(existing2, []string{long + "。"}); len(got) != 0 {
		t.Fatalf("近似长段应被拒: %v", got)
	}
}

// ---------- 估计器 ----------

func TestBotEstimate(t *testing.T) {
	if botEstimateBytes(0) != 0 {
		t.Fatalf("0 字节应估 0")
	}
	if got := botEstimateBytes(350); got != 100 {
		t.Fatalf("350 字节应估约 100，实得 %d", got)
	}
	// 缺失与零 usage 的区分不受估算影响：UsageComplete=false 时 tokens 为 nil。
	r := &model.BotRun{}
	if r.PromptTokens != nil || r.UsageComplete {
		t.Fatalf("无 usage 的 run tokens 必须为 nil")
	}
}
