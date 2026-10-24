package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// INT-102 审查 r1 测试覆盖缺口：按 design.md 映射补齐行为断言。

// fixtureRecorder 是 fake 模型服务 handler 与测试 goroutine 共享的捕获状态：
// handler goroutine 写、测试 goroutine 读，统一加锁（此前是裸变量轮询，
// -race 下必报 data race）。
type fixtureRecorder struct {
	mu     sync.Mutex
	counts map[string]int
	values map[string]string
}

func newFixtureRecorder() *fixtureRecorder {
	return &fixtureRecorder{counts: map[string]int{}, values: map[string]string{}}
}

// hit 记一次某类请求；keyValue 成对写入捕获值（如 "auth"、auth / "body"、body）。
func (r *fixtureRecorder) hit(kind string, keyValue ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[kind]++
	for i := 0; i+1 < len(keyValue); i += 2 {
		r.values[kind+"."+keyValue[i]] = keyValue[i+1]
	}
}

func (r *fixtureRecorder) count(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[kind]
}

func (r *fixtureRecorder) value(kind, key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[kind+"."+key]
}

func writeBotMemory(t *testing.T, env *botTestEnv, botID, path, content, key string) *model.BotMemoryFile {
	t.Helper()
	repo := repository.NewBotMemoryRepository(env.db)
	now := time.Now().UTC()
	f, err := repo.FindFile(botID, path)
	op := model.BotMemoryOpCreate
	var expected *int64
	if err == nil && f != nil && f.DeletedAt == nil {
		op = model.BotMemoryOpReplace
		v := f.CurrentVersion
		expected = &v
	}
	file, _, werr := repo.WriteVersionTx(env.db, botID, path, op, content, false, expected,
		model.BotMemoryWriterMain, "", "", key, now)
	if werr != nil {
		t.Fatalf("write %s: %v", path, werr)
	}
	return file
}

func seedCompactableLedger(t *testing.T, env *botTestEnv, botID string, extraOld string) {
	t.Helper()
	if extraOld == "" {
		extraOld = "最早的问题 INT-102 thread_id=thr_keep_me"
	}
	seedBotUserMessage(t, env, botID, extraOld)
	seedBotAssistantToolBlock(t, env, botID, "call-old-"+uuid.NewString()[:8], "fs_issue", strings.Repeat("很长的工具结果", 400))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, botID, fmt.Sprintf("保护轮%d", i))
		seedBotAssistantToolBlock(t, env, botID, fmt.Sprintf("call-p%d-%s", i, uuid.NewString()[:6]), "fs_issue", "小结果")
	}
}

func markHistoryConsumed(t *testing.T, env *botTestEnv, bot *model.Bot, run *model.BotRun) int64 {
	t.Helper()
	var maxSeq int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ?", bot.ID).Select("MAX(seq)").Scan(&maxSeq)
	if run != nil {
		run.InputThroughSeq = maxSeq
		env.db.Model(run).Update("input_through_seq", maxSeq)
	}
	if err := env.bots.bots.SetOfferedInputSeq(bot.ID, maxSeq); err != nil {
		t.Fatalf("offered: %v", err)
	}
	bot.OfferedInputSeq = maxSeq
	return maxSeq
}

func archiveAndSummaryTurns(summary string) []fakeModelTurn {
	return []fakeModelTurn{
		{Text: `{"items":[{"text":"用户在推进 INT-102","source_seq":1}]}`},
		{Text: summary},
	}
}

func ptrInt64(v int64) *int64 { return &v }

func countMaintByPurpose(db *gorm.DB, maintID string) (archive, summary int) {
	var reqs []model.BotMaintenanceRequest
	db.Where("maintenance_run_id = ?", maintID).Find(&reqs)
	for _, r := range reqs {
		switch r.Purpose {
		case model.BotPurposeArchive:
			archive++
		case model.BotPurposeSummary:
			summary++
		}
	}
	return
}

func countMainModelRequests(db *gorm.DB, botID string) int {
	var n int64
	db.Model(&model.BotModelRequest{}).
		Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", botID).
		Count(&n)
	return int(n)
}

// ---------- 压缩主路径 ----------

func writeFakeChatSSE(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	chunk := func(delta, finish string) {
		deltaObj := map[string]interface{}{}
		if delta != "" {
			deltaObj["content"] = delta
		}
		raw, _ := json.Marshal(map[string]interface{}{
			"id": "chatcmpl-test", "object": "chat.completion.chunk",
			"created": 1700000000, "model": "fake-model",
			"choices": []map[string]interface{}{{
				"index": 0, "delta": deltaObj, "finish_reason": finish,
			}},
		})
		fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}
	if text != "" {
		chunk(text, "")
	}
	chunk("", "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// expectedSummaryBatches 用与 requestSummary 相同的预算口径复算摘要批次数：
// 摘要回复是脚本定值，批次序列完全确定。生产侧批分逻辑改动时须同步此处——
// 这正是「固定预期批数」契约，防止无限循环/默认成功掩盖批分错误。
func expectedSummaryBatches(t *testing.T, env *botTestEnv, botRow *model.Bot, scriptedSummary string) int {
	t.Helper()
	run := botFixtureRun(t, env, botRow)
	// 复算用的 run 只是预算算子：算完删除，不占用「每机器人一个未结束
	// run」约束，也不得让 botSettled 误判主线已收尾。
	defer func() {
		env.db.Where("id = ?", run.ID).Delete(&model.BotRun{})
	}()
	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan for expected batches: %v", err)
	}
	return expectedSummaryBatchesForPlan(t, env, botRow, plan, scriptedSummary)
}

// expectedSummaryBatchesForPlan 是批数复算的纯口径版本：plan 由调用方给定
// （手工构造的压缩计划同样适用），复算过程不发任何模型请求。序列化超窗的
// 收缩终止条件与生产共用 shrinkSummaryPieceToWindow（独立复算只覆盖批分/
// 预算口径；终止条件一致性由共用实现保证，另有对照用例覆盖其行为）。
func expectedSummaryBatchesForPlan(t *testing.T, env *botTestEnv, botRow *model.Bot, plan *botCompactionPlan, scriptedSummary string) int {
	t.Helper()
	modelName := env.cfg.Bots.Context.SummaryModel
	if modelName == "" {
		modelName = botRow.Model
	}
	auxWindow := env.cfg.Bots.Context.WindowTokensFor(modelName)
	outBudget := env.bots.summaryOutputBudget(botRow, plan)
	blocks := summaryProtocolBlocks(plan.Segment, "")
	sys, rules := botSummarySysPrompt, botSummaryRules
	acc := plan.State.SummaryText
	bi := 0
	var pending []string
	n := 0
	for bi < len(blocks) || len(pending) > 0 {
		accSection := ""
		if acc != "" {
			accSection = "\n[既有滚动摘要]\n" + acc + "\n"
		}
		wrap := botEstimateTokens(sys) + botEstimateTokens(rules) + botEstimateTokens(accSection) + 96
		maxOut := auxWindow - wrap - 128
		if maxOut < 1 {
			t.Fatalf("复算批次：预算容纳不了一批")
		}
		reqOut := outBudget
		if reqOut > maxOut {
			reqOut = maxOut
		}
		inputBudget := auxWindow - wrap - reqOut
		if inputBudget < 32 {
			t.Fatalf("复算批次：预算容纳不了一批")
		}
		var piece string
		if len(pending) > 0 {
			piece = pending[0]
			pending = pending[1:]
			if botEstimateTokens(piece) > inputBudget {
				shards := shardSummaryBlock(piece, inputBudget)
				piece = shards[0]
				pending = append(append([]string{}, shards[1:]...), pending...)
			}
		} else {
			joined := blocks[bi]
			bi++
			for bi < len(blocks) {
				cand := joined + blocks[bi]
				if botEstimateTokens(cand) > inputBudget {
					break
				}
				joined += blocks[bi]
				bi++
			}
			if botEstimateTokens(joined) > inputBudget {
				shards := shardSummaryBlock(joined, inputBudget)
				piece = shards[0]
				pending = append([]string{}, shards[1:]...)
			} else {
				piece = joined
			}
		}
		n++
		overOf := func(p string) int64 {
			return botSerializedInputTokens(summaryBatchMessages(sys, rules, accSection, n, p)) + reqOut - auxWindow
		}
		var fits bool
		piece, pending, fits = shrinkSummaryPieceToWindow(overOf, piece, pending, auxWindow)
		if !fits {
			t.Fatalf("复算批次：最小分片仍超窗（固定包装+输出预算自身超窗）")
		}
		acc = scriptedSummary
	}
	return n
}

func TestBotAutoCompaction(t *testing.T) {
	var archiveN, summaryN, mainN int
	var mainBody []byte
	var summaryCap atomic.Int64 // 有限脚本：超过固定预期批数的摘要请求直接失败
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, `"tools"`):
			mainN++
			mainBody = append([]byte(nil), body...)
			if mainN > 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"script_exhausted"}}`)
				return
			}
			writeFakeChatSSE(w, "主线已处理。")
		case strings.Contains(s, "记忆归档器"):
			archiveN++
			if archiveN > 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"script_exhausted"}}`)
				return
			}
			writeFakeChatSSE(w, `{"items":[{"text":"用户在推进 INT-102","source_seq":1}]}`)
		case strings.Contains(s, "上下文压缩器"):
			summaryN++
			if cap := summaryCap.Load(); cap > 0 && int64(summaryN) > cap {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"script_exhausted"}}`)
				return
			}
			writeFakeChatSSE(w, "滚动摘要：用户在做压缩主线。")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"script_exhausted"}}`)
		}
	}))
	t.Cleanup(srv.Close)

	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "autocomp")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "auto", ProviderID: env.createProviderAt(t, userID, srv.URL, "sk-x0000"), Model: "m"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// INT-159 v11 前缀后重定标（wire +2402B≈+687tok）：实测 F≈8613，
	// F+256≈8869；旧窗口 11200 的 T=8400 不可达，12400 的 T=9300 保留摘要空间。
	env.cfg.Bots.Context.WindowTokens = 12400
	// 归档输入是整段原文：本场景单个大工具结果（≈27k token）装不进主模型
	// 11200 窗口的归档输入预算，auxModelRequest 发送关口会按 INT-115 拒绝这种
	// 请求。归档模型配置成更大窗口的模型——这正是 ArchiveModel 的用法。
	env.cfg.Bots.Context.ArchiveModel = "arch-big"
	env.cfg.Bots.Context.ModelWindows = map[string]int64{"arch-big": 131072}
	seedBotUserMessage(t, env, bot.ID, "最早自动压缩问题")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-old-huge", "fs_issue", strings.Repeat("很长的工具结果", 8000))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, fmt.Sprintf("保护轮%d", i))
		seedBotAssistantToolBlock(t, env, bot.ID, fmt.Sprintf("call-p%d", i), "fs_issue", "小结果")
	}
	botRow, _ := env.bots.bots.Get(bot.ID)
	markHistoryConsumed(t, env, botRow, nil)
	// 探针消息：SendMessage 会追加同样的用户输入并把保护轮窗口右移一格，
	// 复算批数必须看到相同几何；算完删除，SendMessage 再真实落账。
	probe := seedBotUserMessage(t, env, bot.ID, "继续")
	wantBatches := expectedSummaryBatches(t, env, botRow, "滚动摘要：用户在做压缩主线。")
	env.db.Where("id = ?", probe.ID).Delete(&model.BotMessage{})
	if wantBatches < 2 {
		t.Fatalf("本场景应构造出多批摘要，固定预期=%d", wantBatches)
	}
	summaryCap.Store(int64(wantBatches))

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "继续", ClientMessageID: "auto-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "auto compact run", 30*time.Second, func() bool { return env.botSettled(bot.ID) })

	var run model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&run)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("主线应成功，实得 %s err=%v archive=%d summary=%d main=%d", run.Status, run.ErrorMessage, archiveN, summaryN, mainN)
	}
	var n int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ?", bot.ID).Count(&n)
	if n != 1 {
		t.Fatalf("应恰好触发一次自动压缩，实得 %d", n)
	}
	var row model.BotCompaction
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&row)
	if row.Status != model.BotCompactionSucceeded {
		code, msg := "", ""
		if row.ErrorCode != nil {
			code, msg = *row.ErrorCode, *row.ErrorMessage
		}
		var maintReqs []model.BotMaintenanceRequest
		env.db.Where("maintenance_run_id IN (SELECT id FROM bot_maintenance_runs WHERE bot_id = ?)", bot.ID).
			Order("ordinal ASC").Find(&maintReqs)
		t.Fatalf("压缩状态应成功（归档+摘要）: %s code=%q msg=%q after=%d before=%d arch=%d sum=%d main=%d reqs=%+v",
			row.Status, code, msg, row.AfterTokensEstimate, row.BeforeTokensEstimate, archiveN, summaryN, mainN, maintReqs)
	}
	dbArchive, dbSummary := countMaintByPurpose(env.db, row.MaintenanceRunID)
	if dbArchive != 1 || archiveN != 1 {
		t.Fatalf("归档应恰好 1 次，db=%d hit=%d", dbArchive, archiveN)
	}
	if dbSummary != summaryN || summaryN != wantBatches {
		t.Fatalf("摘要批次数应恰为固定预期 %d，db=%d hit=%d", wantBatches, dbSummary, summaryN)
	}
	if mainN != 1 || countMainModelRequests(env.db, bot.ID) != 1 {
		t.Fatalf("主线请求应恰好 1，hit=%d db=%d", mainN, countMainModelRequests(env.db, bot.ID))
	}
	var reqs []model.BotMaintenanceRequest
	env.db.Where("maintenance_run_id = ?", row.MaintenanceRunID).Order("ordinal ASC").Find(&reqs)
	if len(reqs) != 1+wantBatches {
		t.Fatalf("维护请求数应=归档1+摘要%d，实得 %d", wantBatches, len(reqs))
	}
	sawSummary := false
	for i, r := range reqs {
		if r.Purpose == model.BotPurposeArchive {
			if sawSummary {
				t.Fatalf("归档必须在摘要之前")
			}
			if i != 0 {
				t.Fatalf("归档必须是第一条维护请求")
			}
		}
		if r.Purpose == model.BotPurposeSummary {
			sawSummary = true
			if r.Status != model.BotModelRequestSucceeded {
				t.Fatalf("摘要批次 %d 应成功: %s", r.Ordinal, r.Status)
			}
		}
	}
	// 断言窗口取测试实际配置的窗口，不能沿用旧工具集的硬编码阈值。
	trigger := botFloorRatio(env.cfg.Bots.Context.WindowTokens, env.cfg.Bots.Context.CompressionRatioV())
	goal := botFloorRatio(trigger, env.cfg.Bots.Context.TargetRatioV())
	if row.AfterTokensEstimate >= row.BeforeTokensEstimate {
		t.Fatalf("最终投影应净缩减 after=%d before=%d", row.AfterTokensEstimate, row.BeforeTokensEstimate)
	}
	if row.AfterTokensEstimate >= trigger {
		t.Fatalf("最终投影应回到触发阈值以下 after=%d T=%d G=%d", row.AfterTokensEstimate, trigger, goal)
	}
	msgs, tools := rawMessages(t, mainBody)
	msgRaw, _ := json.Marshal(msgs)
	actual := botEstimateBytes(int64(len(msgRaw) + len(tools)))
	if actual >= trigger {
		t.Fatalf("重建主线请求实际体积应 <T actual=%d T=%d", actual, trigger)
	}
	var mainReq model.BotModelRequest
	env.db.Where("run_id = ?", run.ID).First(&mainReq)
	if mainReq.InputTokensEstimate >= trigger {
		t.Fatalf("主线 InputTokensEstimate 应 <T est=%d T=%d", mainReq.InputTokensEstimate, trigger)
	}
}

func TestBotSummaryPreservesIdentifiers(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("摘要漏掉了所有 ID")...)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "ids")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 12400 // INT-159 v11 前缀后重定标：F≈8611，F+256≈8867，旧 T=8400 不可达。
	uid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	seedBotUserMessage(t, env, bot.ID, "处理 Issue INT-102 和 "+uid)
	args := `{"action":"read","thread_id":"thr_keep_me","issue_id":"iss_raw_Case"}`
	seedBotAssistantToolBlockArgs(t, env, bot.ID, "call-id-1", "fs_issue", args, strings.Repeat("结果", 400))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, fmt.Sprintf("保护%d", i))
		seedBotAssistantToolBlock(t, env, bot.ID, fmt.Sprintf("cp-%d", i), "fs_issue", "ok")
	}
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)
	if _, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold); err != nil {
		t.Fatalf("compact: %v", err)
	}
	state, err := repository.NewBotContextRepository(env.db).Find(bot.ID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	manifest := state.IdentifierManifestJSON + state.SummaryText
	for _, id := range []string{"INT-102", uid, "thr_keep_me", "iss_raw_Case"} {
		if !strings.Contains(manifest, id) && !strings.Contains(strings.ToLower(manifest), strings.ToLower(id)) {
			t.Fatalf("附录应保留 %s，实得 %s", id, state.IdentifierManifestJSON)
		}
	}
	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	wire, _ := json.Marshal(built.Request.Messages)
	if !strings.Contains(string(wire), "INT-102") {
		t.Fatalf("注入应含 Issue key")
	}
}

func TestBotCompactionPreservesLedger(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("摘要")...)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "ledger")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 12400 // INT-159 v11 前缀后重定标：F≈8606，F+256≈8862，旧 T=8400 不可达。
	seedCompactableLedger(t, env, bot.ID, "账本保持")
	var before []model.BotMessage
	env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&before)
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)
	if _, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold); err != nil {
		t.Fatalf("compact: %v", err)
	}
	// 正向断言：本轮确实发布了压缩投影（水位前进），「消息没被删改」不得
	// 因压缩没跑而空洞通过。
	state, err := repository.NewBotContextRepository(env.db).Find(bot.ID)
	if err != nil {
		t.Fatalf("压缩应发布投影状态: %v", err)
	}
	if state.Generation == 0 && state.SummaryThroughSeq == 0 {
		t.Fatalf("应发布压缩投影（generation 或摘要水位前进）: %+v", state)
	}
	var after []model.BotMessage
	env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&after)
	if len(after) < len(before) {
		t.Fatalf("不得删除原始消息: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].ContentJSON != before[i].ContentJSON {
			t.Fatalf("原始账本被改写 seq=%d", before[i].Seq)
		}
	}
}

func TestBotFoldedResultRead(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("摘要")...)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "foldreal")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 12400 // INT-159 v11 前缀后重定标：F≈8565，F+256≈8821，旧 T=8400 不可达。
	seedBotUserMessage(t, env, bot.ID, "q")
	big := strings.Repeat("完整结果正文XY", 800)
	_, tr := seedBotAssistantToolBlock(t, env, bot.ID, "call-real-fold", "fs_issue", big)
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, fmt.Sprintf("p%d", i))
		seedBotAssistantToolBlock(t, env, bot.ID, fmt.Sprintf("pr%d", i), "fs_issue", "小")
	}
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	reqRow := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, PromptRevision: 1, ToolsetVersion: 2,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x",
		TokenEstimator: "bytes/3.5", StartedAt: time.Now().UTC(),
	}
	if err := env.bots.requests.CreateTx(env.db, reqRow); err != nil {
		t.Fatalf("req: %v", err)
	}
	call := &model.BotToolCall{
		ID: "call-real-fold", BotID: bot.ID, RequestID: reqRow.ID,
		ProviderToolCallID: "prov-call-real-fold", ToolName: "fs_issue", Action: "list",
		ArgumentsJSON: "{}", ArgumentsHash: "x",
		Status: model.BotCallStatusSucceeded, ExecutionMode: "sync",
		ExecutionContextJSON: "{}", ResultJSON: &big, ResultChars: int64(len([]rune(big))), ResultItems: 1,
		CreatedAt: time.Now().UTC(),
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("call: %v", err)
	}
	markHistoryConsumed(t, env, botRow, run)
	if _, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold); err != nil {
		t.Fatalf("compact: %v", err)
	}
	state, _ := repository.NewBotContextRepository(env.db).Find(bot.ID)
	var folded map[string]string
	_ = json.Unmarshal([]byte(state.FoldedResultsJSON), &folded)
	if folded[tr.ID] == "" {
		t.Fatalf("真实压缩应折叠大结果")
	}
	page1, err := env.bots.execResultRead(&model.BotToolCall{
		BotID: bot.ID, ArgumentsJSON: `{"action":"read","call_id":"call-real-fold","offset":0,"limit":20}`,
	})
	if err != nil || !strings.Contains(page1, "完整结果") {
		t.Fatalf("read page1: %v %s", err, page1)
	}
	page2, err := env.bots.execResultRead(&model.BotToolCall{
		BotID: bot.ID, ArgumentsJSON: `{"action":"read","call_id":"call-real-fold","offset":20,"limit":20}`,
	})
	if err != nil || !strings.Contains(page2, `"offset":20`) {
		t.Fatalf("read offset: %v %s", err, page2)
	}
	if !strings.Contains(folded[tr.ID], "call-real-fold") || !strings.Contains(folded[tr.ID], "offset=0") {
		t.Fatalf("折叠引用应含 source call_id/offset: %s", folded[tr.ID])
	}
}

func TestBotSummaryCutSafety(t *testing.T) {
	req := strPtrBot("req-1")
	asst2 := func(id string) string {
		raw, _ := json.Marshal(botAssistantContent{ToolCalls: []botAssistantToolCall{{
			CallID: id, ProviderToolCallID: "p-" + id, ToolName: "fs_issue", Action: "list", Arguments: "{}",
		}}})
		return string(raw)
	}
	reply := func(id string) string {
		raw, _ := json.Marshal(botToolReplyContent{CallID: id, ProviderToolCallID: "p-" + id, ToolName: "fs_issue", Status: "succeeded", Content: "ok"})
		return string(raw)
	}
	user := func(text string) string {
		raw, _ := json.Marshal(botUserContent{Text: text})
		return string(raw)
	}
	row := func(seq int64, kind model.BotMessageKind, content string, rid *string) model.BotMessage {
		return model.BotMessage{Seq: seq, Kind: kind, ContentJSON: content, RequestID: rid}
	}

	// 不足三个用户轮：保护全部用户，切点不得覆盖任何用户。
	few := []model.BotMessage{
		row(1, model.BotMessageKindUser, user("u1"), nil),
		row(2, model.BotMessageKindAssistant, asst2("c1"), req),
		row(3, model.BotMessageKindToolReply, reply("c1"), req),
		row(4, model.BotMessageKindUser, user("u2"), nil),
	}
	cut := findBotSummaryCut(few, 3, 3, 10)
	if cut.CutIdx != 0 && cut.ThroughSeq >= 1 {
		if cut.ProtectStartSeq > few[0].Seq {
			t.Fatalf("不足三用户轮应保护从第一用户起: %+v", cut)
		}
	}
	if cut.ProtectStartSeq != 1 {
		t.Fatalf("不足三用户轮 protect=%d", cut.ProtectStartSeq)
	}

	// 多调用 + 夹用户消息：切点不得落在块中间。
	mid := []model.BotMessage{
		row(1, model.BotMessageKindUser, user("old"), nil),
		row(2, model.BotMessageKindAssistant, func() string {
			raw, _ := json.Marshal(botAssistantContent{ToolCalls: []botAssistantToolCall{
				{CallID: "a", ProviderToolCallID: "pa", ToolName: "fs_issue"},
				{CallID: "b", ProviderToolCallID: "pb", ToolName: "fs_issue"},
			}})
			return string(raw)
		}(), req),
		row(3, model.BotMessageKindUser, user("夹在中间"), nil),
		row(4, model.BotMessageKindToolReply, func() string {
			raw, _ := json.Marshal(botToolReplyContent{CallID: "a", ProviderToolCallID: "pa", Status: "succeeded", Content: "1"})
			return string(raw)
		}(), req),
		row(5, model.BotMessageKindToolReply, func() string {
			raw, _ := json.Marshal(botToolReplyContent{CallID: "b", ProviderToolCallID: "pb", Status: "succeeded", Content: "2"})
			return string(raw)
		}(), req),
		row(6, model.BotMessageKindUser, user("p1"), nil),
		row(7, model.BotMessageKindUser, user("p2"), nil),
		row(8, model.BotMessageKindUser, user("p3"), nil),
	}
	cut = findBotSummaryCut(mid, 3, 3, 20)
	if cut.ThroughSeq >= 2 && cut.ThroughSeq < 5 {
		t.Fatalf("切点不得落在多调用块中间: %+v", cut)
	}

	// 未消费输入：seq>inputThrough 的 user 保护。
	cut = findBotSummaryCut(mid, 3, 3, 1)
	if cut.ProtectStartSeq > 3 {
		t.Fatalf("未消费夹心用户应被保护: %+v", cut)
	}

	// 异步回执闭合：accepted 状态的 tool 回复使块可切。
	asyncReq := strPtrBot("req-async")
	asyncRows := []model.BotMessage{
		row(1, model.BotMessageKindUser, user("old"), nil),
		row(2, model.BotMessageKindAssistant, asst2("async1"), asyncReq),
		row(3, model.BotMessageKindToolReply, func() string {
			raw, _ := json.Marshal(botToolReplyContent{CallID: "async1", ProviderToolCallID: "p-async1", Status: "accepted", Content: `{"accepted":true}`})
			return string(raw)
		}(), asyncReq),
		row(4, model.BotMessageKindUser, user("p1"), nil),
		row(5, model.BotMessageKindUser, user("p2"), nil),
		row(6, model.BotMessageKindUser, user("p3"), nil),
	}
	cut = findBotSummaryCut(asyncRows, 3, 3, 20)
	if cut.ThroughSeq < 1 {
		t.Fatalf("异步回执应闭合块并允许切到回执: %+v", cut)
	}
	if cut.ThroughSeq >= 2 && cut.ThroughSeq < 3 {
		t.Fatalf("异步回执不得被从块中间切开: %+v", cut)
	}

	// 跨请求复用同一 provider_tool_call_id：配对必须带 request_id，两个
	// assistant 块各自独立闭合。
	reqA, reqB := strPtrBot("req-A"), strPtrBot("req-B")
	dup := []model.BotMessage{
		row(1, model.BotMessageKindUser, user("old"), nil),
		row(2, model.BotMessageKindAssistant, asst2("c1"), reqA),
		row(3, model.BotMessageKindToolReply, func() string {
			raw, _ := json.Marshal(botToolReplyContent{CallID: "c1", ProviderToolCallID: "p-shared", Status: "succeeded", Content: "a"})
			return string(raw)
		}(), reqA),
		row(4, model.BotMessageKindAssistant, func() string {
			raw, _ := json.Marshal(botAssistantContent{ToolCalls: []botAssistantToolCall{{
				CallID: "c2", ProviderToolCallID: "p-shared", ToolName: "fs_issue", Action: "list", Arguments: "{}",
			}}})
			return string(raw)
		}(), reqB),
		row(5, model.BotMessageKindToolReply, func() string {
			raw, _ := json.Marshal(botToolReplyContent{CallID: "c2", ProviderToolCallID: "p-shared", Status: "succeeded", Content: "b"})
			return string(raw)
		}(), reqB),
		row(6, model.BotMessageKindUser, user("p1"), nil),
		row(7, model.BotMessageKindAssistant, asst2("d1"), req),
		row(8, model.BotMessageKindToolReply, reply("d1"), req),
		row(9, model.BotMessageKindUser, user("p2"), nil),
		row(10, model.BotMessageKindAssistant, asst2("d2"), req),
		row(11, model.BotMessageKindToolReply, reply("d2"), req),
		row(12, model.BotMessageKindUser, user("p3"), nil),
		row(13, model.BotMessageKindAssistant, asst2("d3"), req),
		row(14, model.BotMessageKindToolReply, reply("d3"), req),
	}
	// 修正 provider id：asst2 用的是 p-c1，上面 reply 用 p-shared。对齐。
	dup[1] = row(2, model.BotMessageKindAssistant, func() string {
		raw, _ := json.Marshal(botAssistantContent{ToolCalls: []botAssistantToolCall{{
			CallID: "c1", ProviderToolCallID: "p-shared", ToolName: "fs_issue", Action: "list", Arguments: "{}",
		}}})
		return string(raw)
	}(), reqA)
	cut = findBotSummaryCut(dup, 3, 3, 20)
	if cut.ThroughSeq >= 4 && cut.ThroughSeq < 5 {
		t.Fatalf("跨请求同 provider id 不得把第二块切散: %+v", cut)
	}
	if cut.CutIdx > 0 && dup[cut.CutIdx-1].Seq < 3 {
		t.Fatalf("第一块完整闭合后应允许切到第一块末尾: %+v", cut)
	}

	// 后台输入不得被吞：未消费的 background_result 必须进入保护。
	bgRaw, _ := json.Marshal(botBackgroundResultContent{CallID: "bg1", Status: "succeeded"})
	withBG := []model.BotMessage{
		row(1, model.BotMessageKindUser, user("old"), nil),
		row(2, model.BotMessageKindAssistant, asst2("b1"), req),
		row(3, model.BotMessageKindToolReply, reply("b1"), req),
		row(4, model.BotMessageKindBackgroundResult, string(bgRaw), nil),
		row(5, model.BotMessageKindUser, user("p1"), nil),
		row(6, model.BotMessageKindUser, user("p2"), nil),
		row(7, model.BotMessageKindUser, user("p3"), nil),
	}
	cut = findBotSummaryCut(withBG, 3, 3, 3)
	if cut.ProtectStartSeq > 4 {
		t.Fatalf("未消费后台结果应被保护: %+v", cut)
	}

	// 配对算法覆盖多个 assistant：最近 3 个 assistant 均受保护。
	many := []model.BotMessage{
		row(1, model.BotMessageKindUser, user("u0"), nil),
		row(2, model.BotMessageKindAssistant, asst2("m1"), req),
		row(3, model.BotMessageKindToolReply, reply("m1"), req),
		row(4, model.BotMessageKindUser, user("u1"), nil),
		row(5, model.BotMessageKindAssistant, asst2("m2"), req),
		row(6, model.BotMessageKindToolReply, reply("m2"), req),
		row(7, model.BotMessageKindUser, user("u2"), nil),
		row(8, model.BotMessageKindAssistant, asst2("m3"), req),
		row(9, model.BotMessageKindToolReply, reply("m3"), req),
		row(10, model.BotMessageKindUser, user("u3"), nil),
		row(11, model.BotMessageKindAssistant, asst2("m4"), req),
		row(12, model.BotMessageKindToolReply, reply("m4"), req),
	}
	cut = findBotSummaryCut(many, 3, 3, 20)
	if cut.ProtectStartSeq > 5 {
		t.Fatalf("最近三个 assistant 均应受保护，protect=%d 应不晚于 seq 5", cut.ProtectStartSeq)
	}
	if cut.ThroughSeq >= 5 {
		t.Fatalf("切点不得吃掉受保护的 assistant 块: %+v", cut)
	}
}

func contentForFoldNetClear(t *testing.T, callID string, wantClear, foldTok int64) string {
	t.Helper()
	for n := 1; n < 500000; n++ {
		content := strings.Repeat("a", n)
		tok := botEstimateBytes(int64(n))
		if tok < foldTok {
			continue
		}
		ref := botFoldedReference(callID, botResultItemCount(content), utf8Chars(content))
		net := tok - botEstimateTokens(ref)
		if net == wantClear {
			return content
		}
	}
	t.Fatalf("无法构造净清除=%d 的折叠候选", wantClear)
	return ""
}

func TestBotFoldBatchThreshold(t *testing.T) {
	const W int64 = 10000
	minClear := botCeilRatio(W, 0.02)
	foldTok := botCeilRatio(W, 0.01)

	envLo := setupBotTestEnv(t)
	uLo := createBotTestUser(t, envLo.db, "foldlo")
	botLo := envLo.createBot(t, uLo)
	envLo.cfg.Bots.Context.WindowTokens = W
	seedBotUserMessage(t, envLo, botLo.ID, "old")
	seedBotAssistantToolBlock(t, envLo, botLo.ID, "lo", "fs_issue", contentForFoldNetClear(t, "lo", minClear-1, foldTok))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, envLo, botLo.ID, fmt.Sprintf("p%d", i))
		seedBotAssistantToolBlock(t, envLo, botLo.ID, fmt.Sprintf("tlo%d", i), "fs_issue", "x")
	}
	rowLo, _ := envLo.bots.bots.Get(botLo.ID)
	runLo := botFixtureRun(t, envLo, rowLo)
	markHistoryConsumed(t, envLo, rowLo, runLo)
	planLo, err := envLo.bots.planCompaction(rowLo, runLo, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan lo: %v", err)
	}
	if planLo.FoldedAdded != 0 {
		t.Fatalf("净清除 min-1 不应发布折叠: added=%d cleared=%d min=%d", planLo.FoldedAdded, planLo.FoldedChars, minClear)
	}

	envHi := setupBotTestEnv(t)
	uHi := createBotTestUser(t, envHi.db, "foldhi")
	botHi := envHi.createBot(t, uHi)
	envHi.cfg.Bots.Context.WindowTokens = W
	seedBotUserMessage(t, envHi, botHi.ID, "old")
	seedBotAssistantToolBlock(t, envHi, botHi.ID, "hi", "fs_issue", contentForFoldNetClear(t, "hi", minClear, foldTok))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, envHi, botHi.ID, fmt.Sprintf("p%d", i))
		seedBotAssistantToolBlock(t, envHi, botHi.ID, fmt.Sprintf("thi%d", i), "fs_issue", "x")
	}
	rowHi, _ := envHi.bots.bots.Get(botHi.ID)
	runHi := botFixtureRun(t, envHi, rowHi)
	markHistoryConsumed(t, envHi, rowHi, runHi)
	planHi, err := envHi.bots.planCompaction(rowHi, runHi, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan hi: %v", err)
	}
	if planHi.FoldedAdded == 0 {
		t.Fatalf("达到最小清除门槛应折叠: minClear=%d foldTok=%d", minClear, foldTok)
	}

	env2 := setupBotTestEnv(t, archiveAndSummaryTurns("摘要")...)
	env2.fake.strict = true
	user2 := createBotTestUser(t, env2.db, "foldth2")
	bot2 := env2.createBot(t, user2)
	env2.cfg.Bots.Context.WindowTokens = W
	seedCompactableLedger(t, env2, bot2.ID, "old")
	botRow2, _ := env2.bots.bots.Get(bot2.ID)
	run2 := botFixtureRun(t, env2, botRow2)
	markHistoryConsumed(t, env2, botRow2, run2)
	plan2, err := env2.bots.planCompaction(botRow2, run2, model.BotCompactionForced)
	if err != nil {
		t.Fatalf("forced plan: %v", err)
	}
	if plan2.FoldedAdded == 0 && plan2.FoldedChars > 0 {
		t.Fatalf("强制路径不得绕过最小清除门槛")
	}

	// 折叠已足够：阈值路径不请求摘要。
	env3 := setupBotTestEnv(t, fakeModelTurn{Text: `{"items":[{"text":"归档","source_seq":1}]}`})
	// strict：折叠已足够时任何摘要请求都会让脚本耗尽而失败。
	env3.fake.strict = true
	u3 := createBotTestUser(t, env3.db, "foldok")
	bot3 := env3.createBot(t, u3)
	// INT-156/INT-159/INT-160 合并后前缀再重定标：实测 afterFold=8762 > 旧
	// G=8640，27000 的 G≈9112 才能继续验证「折叠已足够、不发摘要」。
	env3.cfg.Bots.Context.WindowTokens = 27000
	seedBotUserMessage(t, env3, bot3.ID, "old")
	seedBotAssistantToolBlock(t, env3, bot3.ID, "huge", "fs_issue", strings.Repeat("很长的工具结果", 8000))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env3, bot3.ID, fmt.Sprintf("p%d", i))
		seedBotAssistantToolBlock(t, env3, bot3.ID, fmt.Sprintf("ok%d", i), "fs_issue", "x")
	}
	row3, _ := env3.bots.bots.Get(bot3.ID)
	run3 := botFixtureRun(t, env3, row3)
	markHistoryConsumed(t, env3, row3, run3)
	plan3, err := env3.bots.planCompaction(row3, run3, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan enough: %v", err)
	}
	if plan3.FoldedAdded == 0 {
		t.Fatalf("大结果应折叠")
	}
	if plan3.NeedSummary {
		t.Fatalf("折叠已达目标时阈值路径不应再摘要 afterFold=%d goal=%d", plan3.AfterFoldTokens, plan3.GoalTokens)
	}
	if _, err := env3.bots.runCompactionCycle(context.Background(), row3, run3, model.BotCompactionThreshold); err != nil {
		t.Fatalf("fold-only cycle: %v", err)
	}
	var sumN int64
	env3.db.Model(&model.BotMaintenanceRequest{}).
		Where("purpose = ?", model.BotPurposeSummary).Count(&sumN)
	if sumN != 0 {
		t.Fatalf("折叠已足够时摘要请求不得发生，实际 %d", sumN)
	}
}

func TestBotContextEstimate(t *testing.T) {
	zh := "中文UTF8字节"
	if botEstimateBytes(int64(len(zh))) == botEstimateBytes(int64(len([]rune(zh)))) {
		t.Fatalf("中文应按 UTF-8 字节而非 rune 估算")
	}
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 0, Completion: 1, Total: 1}},
		fakeModelTurn{Text: "second", Usage: &fakeModelUsage{Prompt: 10, Completion: 1, Total: 11}},
	)
	userID := createBotTestUser(t, env.db, "est")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: zh, ClientMessageID: "e0"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "est1", 10*time.Second, func() bool { return env.botSettled(bot.ID) })
	var req0 model.BotModelRequest
	env.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", bot.ID).Order("ordinal ASC").First(&req0)
	if req0.PromptTokens == nil || *req0.PromptTokens != 0 {
		t.Fatalf("合法零应写入 prompt_tokens=0: %+v", req0)
	}
	base0 := env.bots.usageBaselineOf(bot.ID)
	if base0 == nil {
		t.Fatalf("第一次成功请求应留下 usage 基线")
	}
	prefix0 := append([]byte(nil), base0.PrefixJSON...)
	runtime0 := append([]byte(nil), base0.RuntimeJSON...)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "下一句", ClientMessageID: "e1"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	waitForBot(t, "est2", 10*time.Second, func() bool {
		var n int64
		env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&n)
		return n >= 2 && env.botSettled(bot.ID)
	})
	var reqs []model.BotModelRequest
	env.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", bot.ID).Order("started_at ASC").Find(&reqs)
	if len(reqs) < 2 {
		t.Fatalf("应有两次主线请求: %d", len(reqs))
	}
	if reqs[1].InputEstimator != "usage_delta/3.5" {
		t.Fatalf("合法零基线应被使用，第二次估算器应得 usage_delta/3.5，实得 %s est=%d", reqs[1].InputEstimator, reqs[1].InputTokensEstimate)
	}
	base1 := env.bots.usageBaselineOf(bot.ID)
	if base1 == nil {
		t.Fatalf("第二次成功请求应留下 usage 基线")
	}
	added, ok := protocolPrefixAddedBytes(prefix0, base1.PrefixJSON)
	if !ok {
		t.Fatalf("第二次前缀应是第一次的追加")
	}
	wantEst := int64(0) + botEstimateBytes(added+int64(len(base1.RuntimeJSON)-len(runtime0)))
	if reqs[1].InputTokensEstimate != wantEst {
		t.Fatalf("增量估算应为序列化字节差准确值 want=%d got=%d added=%d rtΔ=%d",
			wantEst, reqs[1].InputTokensEstimate, added, int64(len(base1.RuntimeJSON)-len(runtime0)))
	}

	envMiss := setupBotTestEnv(t, fakeModelTurn{Text: "无usage"})
	u2 := createBotTestUser(t, envMiss.db, "estm")
	b2 := envMiss.createBot(t, u2)
	if _, err := envMiss.bots.SendMessage(u2, b2.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "m0"}); err != nil {
		t.Fatalf("send miss: %v", err)
	}
	waitForBot(t, "miss", 10*time.Second, func() bool { return envMiss.botSettled(b2.ID) })
	var rm model.BotModelRequest
	envMiss.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", b2.ID).First(&rm)
	if rm.InputEstimator != "utf8_bytes/3.5" {
		t.Fatalf("缺 usage 应用 utf8 估算，实得 %s", rm.InputEstimator)
	}
	if rm.PromptTokens != nil {
		t.Fatalf("缺 usage 不得写 prompt_tokens")
	}

	envInv := setupBotTestEnv(t,
		fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 10, Completion: 1, Total: 11}},
		fakeModelTurn{Text: "after persona"},
	)
	uInv := createBotTestUser(t, envInv.db, "estinv")
	bInv := envInv.createBot(t, uInv)
	if _, err := envInv.bots.SendMessage(uInv, bInv.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "i0"}); err != nil {
		t.Fatalf("send inv1: %v", err)
	}
	waitForBot(t, "inv1", 10*time.Second, func() bool { return envInv.botSettled(bInv.ID) })
	persona := "新的人设会让基线失效"
	if _, err := envInv.bots.Update(uInv, bInv.ID, &UpdateBotRequest{PersonaPrompt: &persona}); err != nil {
		t.Fatalf("update persona: %v", err)
	}
	if _, err := envInv.bots.SendMessage(uInv, bInv.ID, &SendMessageRequest{Text: "下一句", ClientMessageID: "i1"}); err != nil {
		t.Fatalf("send inv2: %v", err)
	}
	waitForBot(t, "inv2", 10*time.Second, func() bool {
		return countMainModelRequests(envInv.db, bInv.ID) >= 2 && envInv.botSettled(bInv.ID)
	})
	var invReqs []model.BotModelRequest
	envInv.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", bInv.ID).Order("started_at ASC").Find(&invReqs)
	if len(invReqs) < 2 {
		t.Fatalf("基线失效场景应有两次请求")
	}
	if invReqs[len(invReqs)-1].InputEstimator != "utf8_bytes/3.5" {
		t.Fatalf("人设变更后基线应失效，实得 %s", invReqs[len(invReqs)-1].InputEstimator)
	}
}

func TestBotContextLimitRetrySecondFails(t *testing.T) {
	var main int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"tools"`) {
			main++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"maximum context length exceeded","code":"context_length_exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"sum"},"finish_reason":"stop"}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "lim2")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: env.createProviderAt(t, userID, srv.URL, "sk-x0000"), Model: "m"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 16000
	seedCompactableLedger(t, env, bot.ID, "old")
	botRow, _ := env.bots.bots.Get(bot.ID)
	markHistoryConsumed(t, env, botRow, nil)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "go", ClientMessageID: "l2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "second fail", 20*time.Second, func() bool { return env.botSettled(bot.ID) })
	if main != 2 {
		t.Fatalf("主步骤请求数应恰好 2 且第二次后失败，实际 %d", main)
	}
	var run model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&run)
	if run.Status != model.BotRunStatusFailed {
		t.Fatalf("第二次超限后应失败，实得 %s err=%v", run.Status, run.ErrorMessage)
	}
	var mains []model.BotModelRequest
	env.db.Where("run_id = ?", run.ID).Order("ordinal ASC").Find(&mains)
	if len(mains) != 2 {
		t.Fatalf("该 run 主线请求应恰好 2 条，实得 %d", len(mains))
	}
}

func TestBotContextLimitNotOn429(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 窗口探测（INT-115）不计入模型请求数。
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limit exceeded","code":"rate_limit_exceeded"}}`)
	}))
	t.Cleanup(srv.Close)
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r429")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: env.createProviderAt(t, userID, srv.URL, "sk-x0000"), Model: "m"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "429", 10*time.Second, func() bool { return env.botSettled(bot.ID) })
	// INT-137 起 429 改走「未输出前的有限重试」（默认重试 2 次，共 3 次
	// 请求），但仍然不得触发上下文超限的强制压缩，最终按限流失败。
	if n != 3 {
		t.Fatalf("429 应做有限重试共 3 次请求，实际 %d", n)
	}
	var c int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ?", bot.ID).Count(&c)
	if c != 0 {
		t.Fatalf("429 不应产生压缩记录")
	}
}

func TestBotContextLimitNotOnMaxTokens400(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 窗口探测（INT-115）不计入模型请求数。
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"this model does not support max_tokens","code":"invalid_request_error"}}`)
	}))
	t.Cleanup(srv.Close)
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "mt400")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: env.createProviderAt(t, userID, srv.URL, "sk-x0000"), Model: "m"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "m1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "max_tokens 400", 10*time.Second, func() bool { return env.botSettled(bot.ID) })
	if n != 1 {
		t.Fatalf("含 max_tokens 的普通 400 不应重试，实际 %d", n)
	}
}

func TestBotContextLimitRetryResetsNextStep(t *testing.T) {
	if botIsContextLimitError(errors.New("invalid parameter max_tokens")) {
		t.Fatalf("普通文案不得视为超窗")
	}
	if !botIsContextLimitError(fmt.Errorf("please reduce the length of the messages")) {
		t.Fatalf("明确超窗文案应匹配")
	}
}

func TestBotSummaryFailureFallback(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "", FinishReason: "length"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "sumfail")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 12400 // INT-159 v11 前缀后重定标：F≈8606，F+256≈8862，旧 T=8400 不可达。
	seedCompactableLedger(t, env, bot.ID, "失败回退")
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)
	oldThrough := int64(0)
	if st0, e0 := repository.NewBotContextRepository(env.db).Find(bot.ID); e0 == nil {
		oldThrough = st0.SummaryThroughSeq
	}
	out0, _ := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	state, _ := repository.NewBotContextRepository(env.db).Find(bot.ID)
	gotThrough := int64(0)
	if state != nil {
		gotThrough = state.SummaryThroughSeq
	}
	if gotThrough != oldThrough {
		t.Fatalf("摘要失败不得推进水位: old=%d new=%d", oldThrough, gotThrough)
	}
	var row model.BotCompaction
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&row)
	if row.ID == "" {
		t.Fatalf("失败也应落压缩记录")
	}
	if row.Status == model.BotCompactionSucceeded {
		t.Fatalf("length 截断不得发布成功摘要")
	}
	// 有限脚本下的精确请求序列：归档 1 + 摘要 1（截断失败），失败原因必须
	// 显式记录为 summary_request_failed/summary truncated，不得被吞掉。
	if out0 == nil || out0.Run == nil {
		t.Fatalf("length 截断分支应返回维护运行")
	}
	fArch, fSum := countMaintByPurpose(env.db, out0.Run.ID)
	var fTotal int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", out0.Run.ID).Count(&fTotal)
	if fArch != 1 || fSum != 1 || fTotal != 2 {
		t.Fatalf("length 截断分支应恰为归档1+摘要1，实得 total=%d arch=%d sum=%d", fTotal, fArch, fSum)
	}
	if row.ErrorCode == nil || *row.ErrorCode != "summary_request_failed" {
		t.Fatalf("length 截断应记 summary_request_failed，实得 %v", row.ErrorCode)
	}
	if row.ErrorMessage == nil || !strings.Contains(*row.ErrorMessage, "summary truncated") {
		t.Fatalf("失败原因应指向 summary truncated，实得 %v", row.ErrorMessage)
	}

	envHTTP := setupBotTestEnv(t)
	envHTTP.fake.strict = true
	// 用自定义服务让摘要 HTTP 失败：覆盖 bots.BaseURL 需重建 bot。
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 窗口探测（INT-115）不计入维护请求数。
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits++
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	t.Cleanup(srv.Close)
	u3 := createBotTestUser(t, envHTTP.db, "sumhttp")
	b3, err := envHTTP.bots.Create(u3, &CreateBotRequest{Name: "h", ProviderID: envHTTP.createProviderAt(t, u3, srv.URL, "sk-x0000"), Model: "m"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	envHTTP.cfg.Bots.Context.WindowTokens = 12400 // INT-159 v11 前缀后重定标：与上面的截断失败场景共用 F≈8606 的几何。
	seedCompactableLedger(t, envHTTP, b3.ID, "http失败")
	row3, _ := envHTTP.bots.bots.Get(b3.ID)
	run3 := botFixtureRun(t, envHTTP, row3)
	markHistoryConsumed(t, envHTTP, row3, run3)
	outH, _ := envHTTP.bots.runCompactionCycle(context.Background(), row3, run3, model.BotCompactionThreshold)
	st3, ferr := repository.NewBotContextRepository(envHTTP.db).Find(b3.ID)
	if ferr == nil && st3.SummaryThroughSeq > 0 {
		t.Fatalf("HTTP 失败不得发布摘要水位")
	}
	// 精确命中数：HTTP 500 分支应恰为归档 1 + 摘要 1（首个摘要请求即失败，
	// 不得继续空转补发），失败原因记 summary_request_failed。
	if hits != 2 {
		t.Fatalf("HTTP 失败分支应恰为归档1+摘要1 次请求，实得 %d", hits)
	}
	if outH == nil || outH.Run == nil {
		t.Fatalf("HTTP 失败分支应返回维护运行")
	}
	hArch, hSum := countMaintByPurpose(envHTTP.db, outH.Run.ID)
	if hArch != 1 || hSum != 1 {
		t.Fatalf("HTTP 失败分支维护请求应恰为归档1+摘要1，实得 arch=%d sum=%d", hArch, hSum)
	}
	var rowH model.BotCompaction
	envHTTP.db.Where("bot_id = ?", b3.ID).Order("created_at DESC").First(&rowH)
	if rowH.ErrorCode == nil || *rowH.ErrorCode != "summary_request_failed" {
		t.Fatalf("HTTP 失败应记 summary_request_failed，实得 %v", rowH.ErrorCode)
	}

	envEmpty := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "   "},
	)
	envEmpty.fake.strict = true
	uE := createBotTestUser(t, envEmpty.db, "sumempty")
	bE := envEmpty.createBot(t, uE)
	envEmpty.cfg.Bots.Context.WindowTokens = 12400 // INT-159 v11 前缀后重定标：与上面的截断失败场景共用 F≈8606 的几何。
	seedCompactableLedger(t, envEmpty, bE.ID, "空输出")
	rowE, _ := envEmpty.bots.bots.Get(bE.ID)
	runE := botFixtureRun(t, envEmpty, rowE)
	markHistoryConsumed(t, envEmpty, rowE, runE)
	outE, _ := envEmpty.bots.runCompactionCycle(context.Background(), rowE, runE, model.BotCompactionThreshold)
	stE, _ := repository.NewBotContextRepository(envEmpty.db).Find(bE.ID)
	if stE != nil && stE.SummaryThroughSeq > 0 {
		t.Fatalf("空输出不得推进水位")
	}
	// 空输出分支（r5）：水位不推进不足以发现脚本外空转——精确请求序列
	// 归档 1 + 摘要 1，失败原因必须显式指向 summary empty。
	if outE == nil || outE.Run == nil {
		t.Fatalf("空输出分支应返回维护运行")
	}
	eArch, eSum := countMaintByPurpose(envEmpty.db, outE.Run.ID)
	var eTotal int64
	envEmpty.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", outE.Run.ID).Count(&eTotal)
	if eArch != 1 || eSum != 1 || eTotal != 2 {
		t.Fatalf("空输出分支应恰为归档1+摘要1，实得 total=%d arch=%d sum=%d", eTotal, eArch, eSum)
	}
	var emptyComp model.BotCompaction
	envEmpty.db.Where("bot_id = ?", bE.ID).Order("created_at DESC").First(&emptyComp)
	if emptyComp.Status == model.BotCompactionSucceeded {
		t.Fatalf("空输出不得发布成功摘要")
	}
	if emptyComp.ErrorCode == nil || *emptyComp.ErrorCode != "summary_request_failed" {
		t.Fatalf("空输出应记 summary_request_failed，实得 %v", emptyComp.ErrorCode)
	}
	if emptyComp.ErrorMessage == nil || !strings.Contains(*emptyComp.ErrorMessage, "summary empty") {
		t.Fatalf("空输出失败原因应指向 summary empty，实得 %v", emptyComp.ErrorMessage)
	}

	envMid := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "第一批摘要"},
		fakeModelTurn{Text: "", FinishReason: "length"},
	)
	envMid.fake.strict = true
	uM := createBotTestUser(t, envMid.db, "summid")
	bM := envMid.createBot(t, uM)
	envMid.cfg.Bots.Context.WindowTokens = 2048
	seedBotUserMessage(t, envMid, bM.ID, strings.Repeat("第一段很长的历史。", 400))
	seedBotAssistantToolBlock(t, envMid, bM.ID, "mid1", "fs_issue", strings.Repeat("第一块结果", 400))
	seedBotUserMessage(t, envMid, bM.ID, strings.Repeat("第二段很长的历史。", 400))
	seedBotAssistantToolBlock(t, envMid, bM.ID, "mid2", "fs_issue", strings.Repeat("第二块结果", 400))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, envMid, bM.ID, fmt.Sprintf("p%d", i))
		seedBotAssistantToolBlock(t, envMid, bM.ID, fmt.Sprintf("mp%d", i), "fs_issue", "x")
	}
	rowM, _ := envMid.bots.bots.Get(bM.ID)
	runM := botFixtureRun(t, envMid, rowM)
	markHistoryConsumed(t, envMid, rowM, runM)
	_, _ = envMid.bots.runCompactionCycle(context.Background(), rowM, runM, model.BotCompactionThreshold)
	stM, _ := repository.NewBotContextRepository(envMid.db).Find(bM.ID)
	if stM != nil && stM.SummaryThroughSeq > 0 {
		t.Fatalf("分批中途失败不得发布中间摘要水位: %+v", stM)
	}
	var midReqs []model.BotMaintenanceRequest
	envMid.db.Where("maintenance_run_id IN (SELECT id FROM bot_maintenance_runs WHERE bot_id = ?)", bM.ID).
		Order("started_at ASC").Find(&midReqs)
	// INT-115 前置可达性检查：2048 的窗口低于平台固定投影（系统提示+工具
	// 约 5.2k token），固定前缀自身已超窗——归档/摘要发出之前就快速失败，
	// 零维护请求（旧行为会先烧掉归档1+摘要2 才得到同样结论；length 截断的
	// 精确请求序列契约由本测试窗口 8000 的第一段场景继续覆盖）。
	if len(midReqs) != 0 {
		t.Fatalf("固定前缀已超窗应零维护请求快速失败，实得 %d 条", len(midReqs))
	}
	var midRow model.BotCompaction
	envMid.db.Where("bot_id = ?", bM.ID).Order("created_at DESC").First(&midRow)
	if midRow.Status != model.BotCompactionFailed {
		t.Fatalf("快速失败不得发布成功: %s", midRow.Status)
	}
	if midRow.ErrorCode == nil || *midRow.ErrorCode != "context_uncompressible" {
		t.Fatalf("固定前缀超窗应记 context_uncompressible，实得 %v", midRow.ErrorCode)
	}
	if midRow.ErrorMessage == nil || !strings.Contains(*midRow.ErrorMessage, "固定前缀") {
		t.Fatalf("失败消息应指向固定前缀超窗，实得 %v", midRow.ErrorMessage)
	}

	envOver := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "", FinishReason: "length"},
	)
	envOver.fake.strict = true
	uO := createBotTestUser(t, envOver.db, "sumover")
	bO := envOver.createBot(t, uO)
	envOver.cfg.Bots.Context.WindowTokens = 64
	seedBotUserMessage(t, envOver, bO.ID, strings.Repeat("超窗不可压", 50))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, envOver, bO.ID, fmt.Sprintf("p%d", i))
		seedBotAssistantToolBlock(t, envOver, bO.ID, fmt.Sprintf("op%d", i), "fs_issue", "x")
	}
	rowO, _ := envOver.bots.bots.Get(bO.ID)
	runO := botFixtureRun(t, envOver, rowO)
	markHistoryConsumed(t, envOver, rowO, runO)
	_, overErr := envOver.bots.runCompactionCycle(context.Background(), rowO, runO, model.BotCompactionForced)
	if overErr == nil || !errors.Is(overErr, errContextUncompressible) {
		t.Fatalf("仍超限应得 context_uncompressible，实得 %v", overErr)
	}
	var crow model.BotCompaction
	envOver.db.Where("bot_id = ?", bO.ID).Order("created_at DESC").First(&crow)
	gotCode := ""
	if crow.ErrorCode != nil {
		gotCode = *crow.ErrorCode
	}
	if gotCode != "context_uncompressible" {
		t.Fatalf("应记 context_uncompressible，实得 %q", gotCode)
	}
}

func TestBotRememberPreference(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "pref")
	bot := env.createBot(t, userID)
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, ToolName: "fs_memory", Action: "create",
		ArgumentsJSON: `{"action":"create","path":"USER.md","content":"用户喜欢绿茶"}`,
	}
	out, err := env.bots.execMemoryTool(call)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "告知") && !strings.Contains(out, "已保存") {
		t.Fatalf("写入应提示告知用户: %s", out)
	}
	read := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, ToolName: "fs_memory", Action: "read",
		ArgumentsJSON: `{"action":"read","path":"USER.md"}`,
	}
	got, err := env.bots.execMemoryTool(read)
	if err != nil || !strings.Contains(got, "用户喜欢绿茶") {
		t.Fatalf("read 应返回正文: %v %s", err, got)
	}
}

func TestBotMemorySnapshotFrozen(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("摘要")...)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "frz2")
	bot := env.createBot(t, userID)
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	writeBotMemory(t, env, bot.ID, "USER.md", "第一版画像", "frz-a")
	_, _ = repository.NewBotContextRepository(env.db).PublishTx(env.db, bot.ID, 0, 0, "", "", "{}", time.Now().UTC())
	built1, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build1: %v", err)
	}
	writeBotMemory(t, env, bot.ID, "USER.md", "夜间写入不应刷新注入", "frz-b")
	built2, _ := env.bots.buildRunContext(botRow, run)
	if built2.PrefixHash != built1.PrefixHash {
		t.Fatalf("同 generation 夜间/工具写入不得刷新注入")
	}
	env.bots.rt.memMu.Lock()
	env.bots.rt.memSnaps = map[string]*botFrozenMemory{}
	env.bots.rt.memMu.Unlock()
	built3, _ := env.bots.buildRunContext(botRow, run)
	raw, _ := json.Marshal(built3.Request.Messages)
	if !strings.Contains(string(raw), "夜间写入不应刷新注入") {
		t.Fatalf("服务重建后应加载最新快照")
	}
	// INT-159 v11 前缀后重定标：实测 F≈8708，F+256≈8964，
	// 旧窗口 11200 的 T=8400 不可达；12400 的 T=9300 可验证注入刷新。
	// INT-165 v14 前缀后重定标：fs_project 的 include_shelved 使工具字节
	// 23831→24369（≈+135 token），折叠后固定前缀随之增大，12400 已触到
	// 「固定前缀超窗口」的快速失败；12800 恢复裕量。
	env.cfg.Bots.Context.WindowTokens = 12800
	seedCompactableLedger(t, env, bot.ID, "压缩刷新")
	botRow, _ = env.bots.bots.Get(bot.ID)
	markHistoryConsumed(t, env, botRow, run)
	if _, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold); err != nil {
		t.Fatalf("compact: %v", err)
	}
	built4, _ := env.bots.buildRunContext(botRow, run)
	if built4.PrefixHash == built3.PrefixHash {
		t.Fatalf("真实压缩后注入应刷新")
	}
}

func TestBotMemoryInjectionLimits(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "inj")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Memory.UserMaxChars = 8
	body := "你好世界ABCD超长"
	writeBotMemory(t, env, bot.ID, "USER.md", body, "inj1")
	writeBotMemory(t, env, bot.ID, "MEMORY.md", "长期事实", "inj2")
	writeBotMemory(t, env, bot.ID, "daily/2026-01-01.md", "今日笔记不应注入", "inj3")
	_, _ = repository.NewBotContextRepository(env.db).PublishTx(env.db, bot.ID, 0, 0, "", "", "{}", time.Now().UTC())
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, _ := json.Marshal(built.Request.Messages)
	s := string(raw)
	if strings.Contains(s, "今日笔记不应注入") {
		t.Fatalf("daily 不得常驻注入")
	}
	if !strings.Contains(s, "注入已截断") && !strings.Contains(s, string([]rune(botMemoryTruncateMark)[:8])) {
		t.Fatalf("截断标记应计入上限: %s", s)
	}
	f, _ := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "USER.md")
	if f.Content != body {
		t.Fatalf("持久正文不得截断")
	}
}

func TestBotMemoryVersions(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "ver")
	bot := env.createBot(t, userID)
	repo := repository.NewBotMemoryRepository(env.db)
	now := time.Now().UTC()
	f, v, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md", model.BotMemoryOpCreate, "v1", false, nil,
		model.BotMemoryWriterMain, "", "", "op-create", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, v2, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md", model.BotMemoryOpCreate, "v1-replay", false, nil,
		model.BotMemoryWriterMain, "", "", "op-create", now)
	if err != nil || v2.Version != v.Version {
		t.Fatalf("create 重放应幂等: %v", err)
	}
	ev := f.CurrentVersion
	_, _, err = repo.WriteVersionTx(env.db, bot.ID, "USER.md", model.BotMemoryOpDelete, "", true, &ev,
		model.BotMemoryWriterMain, "", "", "op-del", now)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	f, _ = repo.FindFile(bot.ID, "USER.md")
	_, vd, err := repo.WriteVersionTx(env.db, bot.ID, "USER.md", model.BotMemoryOpDelete, "", true, &f.CurrentVersion,
		model.BotMemoryWriterMain, "", "", "op-del", now)
	if err != nil || vd == nil {
		t.Fatalf("delete 重放应幂等: %v", err)
	}
	_, _, err = repo.WriteVersionTx(env.db, bot.ID, "USER.md", model.BotMemoryOpCreate, "重生", false, nil,
		model.BotMemoryWriterMain, "", "", "op-recreate", now)
	if err != nil {
		t.Fatalf("墓碑 create 重建: %v", err)
	}
	_, _, err = repo.WriteVersionTx(env.db, bot.ID, "MISSING.md", model.BotMemoryOpReplace, "x", false, ptrInt64(999),
		model.BotMemoryWriterMain, "", "", "op-miss", now)
	if !errors.Is(err, repository.ErrMemoryFileNotFound) {
		t.Fatalf("缺失文件 replace 应不存在: %v", err)
	}
	if err := env.db.Model(&model.BotMemoryVersion{}).Where("id = ?", v.ID).Update("content", "hack").Error; err == nil {
		t.Fatalf("版本表 UPDATE 应被守护拒绝")
	}
	txErr := env.db.Transaction(func(tx *gorm.DB) error {
		if _, _, err := repository.NewBotMemoryRepository(tx).WriteVersionTx(tx, bot.ID, "MEMORY.md",
			model.BotMemoryOpCreate, "rollback", false, nil, model.BotMemoryWriterMain, "", "", "op-rb", now); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if txErr == nil {
		t.Fatalf("应回滚")
	}
	if _, err := repo.FindFile(bot.ID, "MEMORY.md"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("回滚后不得留下文件")
	}
}

func TestBotMemoryScopeAndPaths(t *testing.T) {
	env := setupBotTestEnv(t)
	a := env.createBot(t, createBotTestUser(t, env.db, "sa"))
	b := env.createBot(t, createBotTestUser(t, env.db, "sb"))
	writeBotMemory(t, env, a.ID, "USER.md", "A私有", "sc-a")
	out, err := env.bots.execMemoryTool(&model.BotToolCall{
		ID: uuid.NewString(), BotID: b.ID, ArgumentsJSON: `{"action":"read","path":"USER.md"}`,
	})
	if err == nil && strings.Contains(out, "A私有") {
		t.Fatalf("跨机器人不得读到正文")
	}
	_, err = env.bots.execMemoryTool(&model.BotToolCall{
		ID: uuid.NewString(), BotID: a.ID, ArgumentsJSON: `{"action":"read","path":" USER.md"}`,
	})
	if err == nil {
		t.Fatalf("带空白路径不得清洗后接受")
	}
	_, err = env.bots.execMemoryTool(&model.BotToolCall{
		ID: uuid.NewString(), BotID: a.ID, ArgumentsJSON: `{"action":"read","path":""}`,
	})
	if err == nil {
		t.Fatalf("空路径应拒绝")
	}
}

func TestBotMemoryFTS5(t *testing.T) {
	env := setupBotTestEnv(t)
	bot := env.createBot(t, createBotTestUser(t, env.db, "fts2"))
	repo := repository.NewBotMemoryRepository(env.db)
	writeBotMemory(t, env, bot.ID, "MEMORY.md", `他说"天狼星"计划`, "fts-u")
	writeBotMemory(t, env, bot.ID, "MEMORY.md", `他说"天狼星"计划已更新`, "fts-u2")
	hits, err := repo.SearchFTS(bot.ID, `"天狼星"`, "", 0, 10)
	if err != nil {
		t.Fatalf("引号字面量: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("引号查询应命中")
	}
	hitsW, err := repo.SearchFTS(bot.ID, "天狼*", "", 0, 10)
	if err != nil {
		t.Fatalf("通配字面量不得当表达式: %v", err)
	}
	_ = hitsW
	empty, err := repo.SearchFTS(bot.ID, "天狼星", "", 99, 10)
	if err != nil {
		t.Fatalf("越界分页: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("越界分页应空")
	}
}

func TestBotPreCompactionArchive(t *testing.T) {
	// 有限脚本：先用空脚本环境复算固定批数，再装入「归档 1 + 摘要 N」的
	// 精确脚本并开 strict——脚本外默认成功不存在，多发的任何请求都会 500。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "arch")
	bot := env.createBot(t, userID)
	// INT-159 v11 前缀后重定标：实测 F≈8606，F+256≈8862，
	// 旧 T=8400 不可达，12400 的 T=9300 保留归档与摘要路径。
	env.cfg.Bots.Context.WindowTokens = 12400
	seedCompactableLedger(t, env, bot.ID, "用户叫 Bobo")
	botRow, _ := env.bots.bots.Get(bot.ID)
	markHistoryConsumed(t, env, botRow, nil)
	wantBatches := expectedSummaryBatches(t, env, botRow, "摘要")
	if wantBatches < 1 {
		t.Fatalf("本场景至少应有一批摘要，复算=%d", wantBatches)
	}
	turns := []fakeModelTurn{{
		Text: `{"items":[{"text":"用户叫 Bobo","source_seq":1},{"text":"工具里的秘密","source_seq":3}]}`,
	}}
	for i := 0; i < wantBatches; i++ {
		turns = append(turns, fakeModelTurn{Text: "摘要"})
	}
	env.fake.mu.Lock()
	env.fake.turns = turns
	env.fake.mu.Unlock()
	env.fake.strict = true
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)
	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if out == nil || out.Run == nil {
		t.Fatalf("compact: missing outcome")
	}
	maint := out.Run
	// 精确请求数：归档恰好 1 次、摘要恰好复算批数，无其他任何请求。
	dbArchive, dbSummary := countMaintByPurpose(env.db, maint.ID)
	var totalReq int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", maint.ID).Count(&totalReq)
	if dbArchive != 1 || dbSummary != wantBatches || totalReq != 1+int64(wantBatches) {
		t.Fatalf("维护请求应恰为归档1+摘要%d，实得 total=%d arch=%d sum=%d", wantBatches, totalReq, dbArchive, dbSummary)
	}
	fresh, _ := repository.NewBotMaintenanceRepository(env.db).FindByID(maint.ID)
	if fresh.ArchiveAttemptedAt == nil {
		t.Fatalf("archive_attempted_at 应持久化")
	}
	var mains int64
	env.db.Model(&model.BotModelRequest{}).Where("run_id = ?", run.ID).Count(&mains)
	if mains != 0 {
		t.Fatalf("归档不得写主线请求")
	}
	f, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, env.bots.botDailyPathForBot(botRow, time.Now().UTC()))
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if strings.Contains(f.Content, "工具里的秘密") {
		t.Fatalf("工具结果不得自动提升")
	}
	// 同周期再入：幂等键复用，不得二次归档请求。
	var before int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ? AND purpose = ?", maint.ID, model.BotPurposeArchive).Count(&before)
	_, _ = env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	var after int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ? AND purpose = ?", maint.ID, model.BotPurposeArchive).Count(&after)
	if after != before {
		t.Fatalf("同周期不得重复归档")
	}

	botRow, _ = env.bots.bots.Get(bot.ID)
	out2, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil && !errors.Is(err, errContextUncompressible) && !errors.Is(err, errContextCompactionFailed) {
		t.Fatalf("second compact: %v", err)
	}
	var maint2 *model.BotMaintenanceRun
	if out2 != nil {
		maint2 = out2.Run
	}
	if maint2 != nil && maint2.ID != maint.ID {
		var arch2 int64
		env.db.Model(&model.BotMaintenanceRequest{}).
			Where("maintenance_run_id = ? AND purpose = ?", maint2.ID, model.BotPurposeArchive).Count(&arch2)
		if arch2 > 1 {
			t.Fatalf("新周期归档请求应至多一次，实得 %d", arch2)
		}
	}

	now := time.Now().UTC()
	crash := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: bot.ID, Kind: model.BotMaintenanceCompaction,
		TriggerKey: "compaction:threshold:g0:s-crash", Status: model.BotMaintenanceRunning,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now, ArchiveAttemptedAt: &now,
	}
	if err := env.db.Create(crash).Error; err != nil {
		t.Fatalf("seed crash maint: %v", err)
	}
	var beforeCrash int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", crash.ID).Count(&beforeCrash)
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recover archive: %v", err)
	}
	var afterCrash int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", crash.ID).Count(&afterCrash)
	if afterCrash != beforeCrash {
		t.Fatalf("重启不得补发归档请求")
	}
	var ccomp model.BotCompaction
	if err := env.db.Where("maintenance_run_id = ?", crash.ID).First(&ccomp).Error; err != nil {
		t.Fatalf("中断压缩应补终态记录: %v", err)
	}
	if ccomp.Status != model.BotCompactionFailed || ccomp.ErrorCode == nil || *ccomp.ErrorCode != "server_restarted" {
		t.Fatalf("终态记录应为 server_restarted: %+v", ccomp)
	}
}

func TestBotNightlyConsolidation(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: `{"user_md":"用户叫 Bobo","memory_md":"项目天狼星","sources":[{"path":"daily/2026-10-15.md","version":1}],"note":"ok"}`})
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "night")
	bot := env.createBot(t, userID)
	writeBotMemory(t, env, bot.ID, "daily/2026-10-15.md", "- 用户叫 Bobo\n- 项目天狼星", "n-d")
	env.cfg.Bots.Memory.NightlyModel = "cheap-nightly"
	env.bots.nowFn = func() time.Time {
		return time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)
	}
	var before int64
	env.db.Model(&model.BotMaintenanceRun{}).Where("bot_id = ? AND kind = ?", bot.ID, model.BotMaintenanceNightly).Count(&before)
	env.bots.scanNightlyOnce()
	var early int64
	env.db.Model(&model.BotMaintenanceRun{}).Where("bot_id = ? AND kind = ?", bot.ID, model.BotMaintenanceNightly).Count(&early)
	if early != before {
		t.Fatalf("03:00 前不应触发")
	}
	env.bots.nowFn = func() time.Time {
		return time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)
	}
	botRow, _ := env.bots.bots.Get(bot.ID)
	env.bots.runNightlyForBot(botRow.ID, "nightly:2026-10-16")
	mem, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "MEMORY.md")
	if err != nil || !strings.Contains(mem.Content, "天狼星") {
		t.Fatalf("应写入 MEMORY.md: %v", err)
	}
	userf, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "USER.md")
	if err != nil || !strings.Contains(userf.Content, "Bobo") {
		t.Fatalf("应写入 USER.md: %v", err)
	}
	daily, _ := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "daily/2026-10-15.md")
	if daily.Content != "- 用户叫 Bobo\n- 项目天狼星" {
		t.Fatalf("不得改写 daily 原文")
	}
	var run model.BotMaintenanceRun
	env.db.Where("bot_id = ? AND trigger_key = ?", bot.ID, "nightly:2026-10-16").First(&run)
	if run.Status != model.BotMaintenanceSucceeded {
		t.Fatalf("夜间运行应成功: %s %v", run.Status, run.ErrorCode)
	}
	if !strings.Contains(run.ResultJSON, "daily/2026-10-15.md") {
		t.Fatalf("应记录已消费版本: %s", run.ResultJSON)
	}
	var mreq model.BotMaintenanceRequest
	env.db.Where("maintenance_run_id = ?", run.ID).First(&mreq)
	if mreq.Model != "cheap-nightly" {
		t.Fatalf("应使用配置的便宜模型，实得 %s", mreq.Model)
	}
	env.bots.runNightlyForBot(botRow.ID, "nightly:2026-10-16")
	var n int64
	env.db.Model(&model.BotMaintenanceRun{}).Where("bot_id = ? AND trigger_key = ?", bot.ID, "nightly:2026-10-16").Count(&n)
	if n != 1 {
		t.Fatalf("每天一次，实得 %d", n)
	}
	env.bots.runNightlyForBot(botRow.ID, "nightly:2026-10-17")
	var empty model.BotMaintenanceRun
	env.db.Where("bot_id = ? AND trigger_key = ?", bot.ID, "nightly:2026-10-17").First(&empty)
	if empty.Status != model.BotMaintenanceSucceeded {
		t.Fatalf("无新笔记应成功跳过: %s", empty.Status)
	}
	env.bots.runNightlyForBot(botRow.ID, "nightly:2026-10-18")
	var replay int64
	env.db.Model(&model.BotMaintenanceRequest{}).
		Where("maintenance_run_id IN (SELECT id FROM bot_maintenance_runs WHERE bot_id = ? AND trigger_key = ?)",
			bot.ID, "nightly:2026-10-18").Count(&replay)
	if replay != 0 {
		t.Fatalf("累计消费水位应避免把已整理 daily 再输入，实得请求 %d", replay)
	}
}

func TestBotNightlyConflictAndRecovery(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t, fakeModelTurn{
		Hold: hold,
		Text: `{"user_md":"模型候选USER","memory_md":"模型候选MEM","sources":[{"path":"daily/2026-10-14.md","version":1}],"note":"ok"}`,
	})
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "nconf")
	bot := env.createBot(t, userID)
	writeBotMemory(t, env, bot.ID, "USER.md", "原USER", "nc-u")
	writeBotMemory(t, env, bot.ID, "daily/2026-10-14.md", "笔记", "nc-d")
	botRow, _ := env.bots.bots.Get(bot.ID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.bots.runNightlyForBot(botRow.ID, "nightly:2026-10-17")
	}()
	_ = env.fake.nextRequest(t, 5*time.Second)
	writeBotMemory(t, env, bot.ID, "USER.md", "主线并发写入", "nc-u2")
	close(hold)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("夜间整理未结束")
	}
	var run model.BotMaintenanceRun
	env.db.Where("bot_id = ? AND trigger_key = ?", bot.ID, "nightly:2026-10-17").First(&run)
	if run.Status != model.BotMaintenanceFailed {
		t.Fatalf("来源变化应失败，实得 %s", run.Status)
	}
	if run.ErrorCode == nil || *run.ErrorCode != "memory_conflict" {
		t.Fatalf("应记 memory_conflict: %+v", run.ErrorCode)
	}
	userf, _ := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "USER.md")
	if userf.Content != "主线并发写入" {
		t.Fatalf("冲突不得覆盖主线写入: %q", userf.Content)
	}
	if _, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "MEMORY.md"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("冲突不得留下半套 MEMORY.md")
	}

	// 崩溃恢复：running 维护运行标 server_restarted，不改文件。
	now := time.Now().UTC()
	crash := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: bot.ID, Kind: model.BotMaintenanceNightly,
		TriggerKey: "nightly:2026-10-18", Status: model.BotMaintenanceRunning,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(crash).Error; err != nil {
		t.Fatalf("seed crash: %v", err)
	}
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	fresh, _ := repository.NewBotMaintenanceRepository(env.db).FindByID(crash.ID)
	if fresh.Status != model.BotMaintenanceFailed || fresh.ErrorCode == nil || *fresh.ErrorCode != "server_restarted" {
		t.Fatalf("恢复应标记 server_restarted: %+v", fresh)
	}

	hold2 := make(chan struct{})
	envD := setupBotTestEnv(t, fakeModelTurn{
		Hold: hold2,
		Text: `{"user_md":"U","memory_md":"M","sources":[{"path":"daily/2026-10-13.md","version":1}],"note":"ok"}`,
	})
	envD.fake.strict = true
	uD := createBotTestUser(t, envD.db, "ndaily")
	botD := envD.createBot(t, uD)
	writeBotMemory(t, envD, botD.ID, "daily/2026-10-13.md", "旧笔记", "nd-d")
	rowD, _ := envD.bots.bots.Get(botD.ID)
	doneD := make(chan struct{})
	go func() {
		defer close(doneD)
		envD.bots.runNightlyForBot(rowD.ID, "nightly:2026-10-20")
	}()
	_ = envD.fake.nextRequest(t, 5*time.Second)
	writeBotMemory(t, envD, botD.ID, "daily/2026-10-13.md", "主线又写了一版", "nd-d2")
	close(hold2)
	select {
	case <-doneD:
	case <-time.After(10 * time.Second):
		t.Fatalf("daily 来源变化整理未结束")
	}
	var runD model.BotMaintenanceRun
	envD.db.Where("bot_id = ? AND trigger_key = ?", botD.ID, "nightly:2026-10-20").First(&runD)
	if runD.Status != model.BotMaintenanceFailed || runD.ErrorCode == nil || *runD.ErrorCode != "memory_conflict" {
		t.Fatalf("daily 来源变化应 memory_conflict: %+v", runD)
	}

	envB := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"user_md":"U1","memory_md":"M1","sources":[{"path":"daily/2026-10-11.md","version":1},{"path":"daily/2026-10-12.md","version":1}],"note":"b1"}`},
	)
	envB.fake.strict = true
	uB := createBotTestUser(t, envB.db, "nbatch")
	botB := envB.createBot(t, uB)
	envB.cfg.Bots.Memory.NightlyModel = "tiny-nightly"
	if envB.cfg.Bots.Context.ModelWindows == nil {
		envB.cfg.Bots.Context.ModelWindows = map[string]int64{}
	}
	envB.cfg.Bots.Context.ModelWindows["tiny-nightly"] = 6000
	// 两份小 daily 必须并入同一批（含标题行包装后仍留有充足正文空间），
	// 模型一次声明两个来源——注入的写失败只有到达提交事务才能被验证。
	writeBotMemory(t, envB, botB.ID, "daily/2026-10-11.md", strings.Repeat("第一日笔记。", 100), "nb-1")
	writeBotMemory(t, envB, botB.ID, "daily/2026-10-12.md", strings.Repeat("第二日笔记。", 100), "nb-2")
	if err := envB.db.Exec(fmt.Sprintf(`CREATE TRIGGER int102_fail_second_nightly
BEFORE INSERT ON bot_memory_versions
WHEN NEW.writer_kind = 'nightly'
 AND (SELECT COUNT(*) FROM bot_memory_versions v
      JOIN bot_memory_files f ON f.id = v.file_id
      WHERE v.writer_kind = 'nightly'
        AND f.path IN ('USER.md','MEMORY.md')
        AND f.bot_id = '%s') >= 1
BEGIN
  SELECT RAISE(ABORT, 'int102 injected write failure');
END`, botB.ID)).Error; err != nil {
		t.Fatalf("trigger: %v", err)
	}
	t.Cleanup(func() { _ = envB.db.Exec(`DROP TRIGGER IF EXISTS int102_fail_second_nightly`) })
	rowB, _ := envB.bots.bots.Get(botB.ID)
	envB.bots.runNightlyForBot(rowB.ID, "nightly:2026-10-21")
	var nightlyReqs int64
	envB.db.Model(&model.BotMaintenanceRequest{}).
		Where("maintenance_run_id IN (SELECT id FROM bot_maintenance_runs WHERE bot_id = ? AND trigger_key = ?)",
			botB.ID, "nightly:2026-10-21").Count(&nightlyReqs)
	if nightlyReqs != 1 {
		t.Fatalf("两份 daily 应并入一批、恰好一次整理请求，实得 %d", nightlyReqs)
	}
	if _, err := repository.NewBotMemoryRepository(envB.db).FindFile(botB.ID, "MEMORY.md"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("第二文件失败应原子回滚，不得留下 MEMORY.md")
	}
	if _, err := repository.NewBotMemoryRepository(envB.db).FindFile(botB.ID, "USER.md"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("第二文件失败应原子回滚，不得留下 USER.md")
	}
	var nightlyVers int64
	envB.db.Model(&model.BotMemoryVersion{}).
		Where("writer_kind = ? AND file_id IN (SELECT id FROM bot_memory_files WHERE bot_id = ? AND path IN ('USER.md','MEMORY.md'))",
			model.BotMemoryWriterNightly, botB.ID).Count(&nightlyVers)
	if nightlyVers != 0 {
		t.Fatalf("版本历史不得留下半套 nightly 版本: %d", nightlyVers)
	}
	var runB model.BotMaintenanceRun
	envB.db.Where("bot_id = ? AND trigger_key = ?", botB.ID, "nightly:2026-10-21").First(&runB)
	if runB.Status != model.BotMaintenanceFailed {
		t.Fatalf("第二文件写入失败应失败，实得 %s err=%v", runB.Status, runB.ErrorCode)
	}
	if runB.ErrorCode == nil || *runB.ErrorCode != "write_failed" {
		t.Fatalf("应记 write_failed，实得 %v", runB.ErrorCode)
	}
	if runB.ErrorMessage == nil || !strings.Contains(*runB.ErrorMessage, "int102 injected write failure") {
		t.Fatalf("失败原因应是注入的写错误，实得 %v", runB.ErrorMessage)
	}

	envSrc := setupBotTestEnv(t, fakeModelTurn{Text: `{"sources":[{"path":"daily/2026-10-10.md","version":1}],"note":"no body"}`})
	envSrc.fake.strict = true
	uS := createBotTestUser(t, envSrc.db, "nsrc")
	botS := envSrc.createBot(t, uS)
	writeBotMemory(t, envSrc, botS.ID, "daily/2026-10-10.md", "只有来源", "ns-d")
	rowS, _ := envSrc.bots.bots.Get(botS.ID)
	envSrc.bots.runNightlyForBot(rowS.ID, "nightly:2026-10-22")
	var runS model.BotMaintenanceRun
	envSrc.db.Where("bot_id = ? AND trigger_key = ?", botS.ID, "nightly:2026-10-22").First(&runS)
	if runS.Status != model.BotMaintenanceFailed {
		t.Fatalf("只有 sources 无正文应失败，实得 %s", runS.Status)
	}
	if _, err := repository.NewBotMemoryRepository(envSrc.db).FindFile(botS.ID, "USER.md"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("source-only 不得写入 USER.md")
	}

	reqCrash := &model.BotMaintenanceRequest{
		ID: uuid.NewString(), MaintenanceRunID: crash.ID, Ordinal: 1,
		Purpose: model.BotPurposeNightly, Model: "m", Status: model.BotModelRequestRunning,
		PrefixHash: "", ToolsHash: "", ToolsetVersion: 2, StartedAt: now,
	}
	if err := env.db.Create(reqCrash).Error; err != nil {
		t.Fatalf("seed crash req: %v", err)
	}
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recover req: %v", err)
	}
	var reqFresh model.BotMaintenanceRequest
	env.db.Where("id = ?", reqCrash.ID).First(&reqFresh)
	if reqFresh.Status != model.BotModelRequestFailed || reqFresh.ErrorCode == nil || *reqFresh.ErrorCode != "server_restarted" {
		t.Fatalf("未完成维护请求应 server_restarted: %+v", reqFresh)
	}
}

func TestBotToolsetUpgradeBoundary(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("摘要")...)
	// 工具集 v3 修正枚举并加入条件必填后 wire 明显变大，压缩的固定前缀更重。
	// 本测试验证的是「压缩边界激活新版本」，窗口与可压缩账本相应放大，使目标
	// 仍可达；脚本外的摘要批次由假服务的默认回复兜底（非 strict）。
	userID := createBotTestUser(t, env.db, "ts2")
	bot := env.createBot(t, userID)
	env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version": 1, "last_told_version": 1, "platform_prompt_version": 1,
		"system_prompt_text": "旧提示：记忆不可用",
	})
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	builtOld, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("old build: %v", err)
	}
	if builtOld.ToolsetVersion != 1 {
		t.Fatalf("压缩前应冻结 v1，实得 %d", builtOld.ToolsetVersion)
	}
	oldHash := builtOld.ToolsHash
	oldBytes := builtOld.ToolsSchemaBytes
	oldHolder := botToolsetVersionOf(1)
	oldWire, err := json.Marshal(oldHolder.schemas)
	if err != nil {
		t.Fatalf("marshal v1: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 20000
	seedBotUserMessage(t, env, bot.ID, "最早的问题 INT-102 thread_id=thr_keep_me")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-old-upgrade", "fs_issue", strings.Repeat("很长的工具结果", 8000))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, fmt.Sprintf("保护轮%d", i))
		seedBotAssistantToolBlock(t, env, bot.ID, fmt.Sprintf("call-p%d", i), "fs_issue", "小结果")
	}
	botRow, _ = env.bots.bots.Get(bot.ID)
	botRow.ToolsetVersion = 1
	botRow.LastToldVersion = 1
	botRow.PlatformPromptVersion = 1
	markHistoryConsumed(t, env, botRow, run)
	if _, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold); err != nil {
		t.Fatalf("compact: %v", err)
	}
	fresh, _ := env.bots.bots.Get(bot.ID)
	if fresh.ToolsetVersion != botToolsetVersion || fresh.PlatformPromptVersion != botPlatformPromptVersion {
		t.Fatalf("压缩后应激活新版本")
	}
	if strings.Contains(fresh.SystemPromptText, "记忆不可用") {
		t.Fatalf("存量提示词应重建")
	}
	builtNew, _ := env.bots.buildRunContext(fresh, run)
	if builtNew.ToolsHash == builtOld.ToolsHash {
		t.Fatalf("新旧 holder 字节应不同")
	}
	env.bots.maybeTellToolset(fresh, run, false)
	env.bots.maybeTellToolset(fresh, run, false)
	var n int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND dedup_key = ?",
		bot.ID, fmt.Sprintf("toolset_notice:%d", botToolsetVersion)).Count(&n)
	if n != 1 {
		t.Fatalf("重复告知不得发生，实得 %d", n)
	}
	stillV1, err := json.Marshal(botToolsetVersionOf(1).schemas)
	if err != nil {
		t.Fatalf("remarshal v1: %v", err)
	}
	if !bytes.Equal(oldWire, stillV1) {
		t.Fatalf("升级不得改写旧 holder 的逐字节序列化")
	}
	newWire, err := json.Marshal(botToolsetVersionOf(builtNew.ToolsetVersion).schemas)
	if err != nil {
		t.Fatalf("marshal new: %v", err)
	}
	if bytes.Equal(oldWire, newWire) {
		t.Fatalf("升级后重新序列化应与旧 holder 不同")
	}
	sumOld := sha256.Sum256(oldWire)
	if hex.EncodeToString(sumOld[:]) != oldHash {
		t.Fatalf("压缩前 tools_hash 应等于当时 v1 逐字节 SHA-256")
	}
	if oldBytes <= 0 || oldBytes != int64(len(oldWire)) {
		t.Fatalf("旧 holder 记录的字节数应等于 v1 schema 重新序列化长度 oldBytes=%d wire=%d", oldBytes, len(oldWire))
	}
	env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version": 1, "platform_prompt_version": 1,
	})
	stale, _ := env.bots.bots.Get(bot.ID)
	if err := env.bots.activateBotPrompt(stale); err != nil {
		t.Fatalf("restart activate: %v", err)
	}
	again, _ := env.bots.bots.Get(bot.ID)
	if again.ToolsetVersion != botToolsetVersion {
		t.Fatalf("重启后应激活最新工具集")
	}

	hold := make(chan struct{})
	envHold := setupBotTestEnv(t,
		fakeModelTurn{Text: "frozen-run", Hold: hold},
		fakeModelTurn{Text: "second"},
	)
	envHold.fake.strict = true
	uH := createBotTestUser(t, envHold.db, "tshold")
	botH := envHold.createBot(t, uH)
	if _, err := envHold.bots.SendMessage(uH, botH.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "ts-h"}); err != nil {
		t.Fatalf("send hold: %v", err)
	}
	reqH := envHold.fake.nextRequest(t, 10*time.Second)
	_, toolsH := rawMessages(t, reqH.Body)
	if _, err := envHold.bots.SendMessage(uH, botH.ID, &SendMessageRequest{Text: "插一句", ClientMessageID: "ts-h2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	close(hold)
	reqH2 := envHold.fake.nextRequest(t, 15*time.Second)
	_, toolsH2 := rawMessages(t, reqH2.Body)
	if string(toolsH) != string(toolsH2) {
		t.Fatalf("普通运行期间工具 holder 字节应保持不变")
	}
	waitForBot(t, "ts hold", 10*time.Second, func() bool { return envHold.botSettled(botH.ID) })

	freshSvc := NewBotService(env.db, env.cfg, zap.NewNop(), env.bots.projects, env.bots.issues, env.bots.threads, env.bots.bindings)
	stale2, _ := env.bots.bots.Get(bot.ID)
	env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version": 1, "platform_prompt_version": 1,
	})
	stale2, _ = env.bots.bots.Get(bot.ID)
	if err := freshSvc.activateBotPrompt(stale2); err != nil {
		t.Fatalf("新实例激活: %v", err)
	}
	latest, _ := env.bots.bots.Get(bot.ID)
	if latest.ToolsetVersion != botToolsetVersion {
		t.Fatalf("真实新实例应激活最新版本")
	}
}

func TestBotRenamedAction(t *testing.T) {
	env := setupBotTestEnv(t)
	bot := env.createBot(t, createBotTestUser(t, env.db, "ren"))
	v2 := botToolsetVersionOf(2)
	_, err := botPrepareToolCall(v2, "fs_memory", `{"action":"search","query":"x"}`)
	var renamed *botRenamedActionError
	if !errors.As(err, &renamed) {
		t.Fatalf("应改名: %v", err)
	}
	reply := botToolActionErrorReply(err)
	if !strings.Contains(reply, "action_renamed") {
		t.Fatalf("主线协议回复: %s", reply)
	}
	if _, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "USER.md"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("改名路径不得产生写入副作用")
	}
}

func TestBotToolsetHashWireBytes(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok"})
	userID := createBotTestUser(t, env.db, "hash")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "h1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	req := env.fake.nextRequest(t, 5*time.Second)
	waitForBot(t, "hash run", 10*time.Second, func() bool { return env.botSettled(bot.ID) })
	var payload struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body: %v", err)
	}
	sum := sha256.Sum256(payload.Tools)
	got := hex.EncodeToString(sum[:])
	var row model.BotModelRequest
	env.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", bot.ID).First(&row)
	if row.ToolsHash != got {
		t.Fatalf("tools_hash 应等于完整 tools 数组 SHA-256\n记录=%s\n计算=%s", row.ToolsHash, got)
	}
}

func TestBotPersonaFrozenDuringHold(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "第一轮", Hold: hold},
		fakeModelTurn{Text: "第二轮"},
		fakeModelTurn{Text: "下一run"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "persona")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "你好", ClientMessageID: "pf1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = env.fake.nextRequest(t, 10*time.Second)
	marker := "UNIQUE_PERSONA_MARKER_XYZ"
	if _, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{PersonaPrompt: &marker}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "插队", ClientMessageID: "pf2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	close(hold)
	second := env.fake.nextRequest(t, 15*time.Second)
	if strings.Contains(string(second.Body), marker) {
		t.Fatalf("Hold 期间编辑人设不得改写进行中对话的前缀")
	}
	waitForBot(t, "persona same run", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "下一轮", ClientMessageID: "pf3"}); err != nil {
		t.Fatalf("send3: %v", err)
	}
	third := env.fake.nextRequest(t, 10*time.Second)
	if !strings.Contains(string(third.Body), marker) {
		t.Fatalf("下一 run 应使用新的人设")
	}
	waitForBot(t, "persona next run", 10*time.Second, func() bool { return env.botSettled(bot.ID) })

	holdC := make(chan struct{})
	recC := newFixtureRecorder()
	srvC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "bad-replacement") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"auth_error"}}`)
			return
		}
		switch {
		case strings.Contains(s, "记忆归档器"):
			recC.hit("archive", "auth", auth)
			select {
			case <-holdC:
			case <-r.Context().Done():
				return
			}
			writeFakeChatSSE(w, `{"items":[]}`)
		case strings.Contains(s, "上下文压缩器"):
			recC.hit("summary", "auth", auth)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", `{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}]}`)
			fmt.Fprint(w, "data: [DONE]\n\n")
		case strings.Contains(s, `"tools"`):
			recC.hit("main", "auth", auth, "body", string(body))
			if strings.Contains(s, "UNIQUE_PERSONA_MARKER_COMPACT") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"persona leaked into freeze"}}`)
				return
			}
			writeFakeChatSSE(w, "压缩失败后主线仍用旧人设")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"script_exhausted"}}`)
		}
	}))
	t.Cleanup(srvC.Close)
	envC := setupBotTestEnv(t)
	uC := createBotTestUser(t, envC.db, "personac")
	botC, err := envC.bots.Create(uC, &CreateBotRequest{Name: "pc", ProviderID: envC.createProviderAt(t, uC, srvC.URL, "sk-test-abcdef123456"), Model: "m"})
	if err != nil {
		t.Fatalf("create compact freeze: %v", err)
	}
	envC.cfg.Bots.Context.WindowTokens = 50000
	envC.cfg.Bots.Context.CompressionRatio = 0.2
	for i := 0; i < 30; i++ {
		seedBotUserMessage(t, envC, botC.ID, strings.Repeat("很长的历史内容用于撑满上下文。", 80))
	}
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, envC, botC.ID, fmt.Sprintf("保护%d", i))
	}
	rowC, _ := envC.bots.bots.Get(botC.ID)
	oldRev := rowC.PromptRevision
	markHistoryConsumed(t, envC, rowC, nil)
	if _, err := envC.bots.SendMessage(uC, botC.ID, &SendMessageRequest{Text: "继续", ClientMessageID: "pf-c"}); err != nil {
		t.Fatalf("send compact freeze: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for recC.count("archive") == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if recC.count("archive") == 0 {
		t.Fatalf("应进入归档请求")
	}
	markerC := "UNIQUE_PERSONA_MARKER_COMPACT"
	if _, err := envC.bots.Update(uC, botC.ID, &UpdateBotRequest{PersonaPrompt: &markerC}); err != nil {
		t.Fatalf("update persona during compact: %v", err)
	}
	// 密钥在 provider 上（INT-133）：压缩并发期轮换 provider 密钥，
	// 活动 run 仍按冻结密文发完本轮。
	badKey := "sk-bad-replacement-xxxx"
	if _, err := NewAIProviderService(envC.db, envC.cfg, zap.NewNop()).Update(uC, botC.ProviderID, &UpdateAIProviderRequest{APIKey: &badKey}); err != nil {
		t.Fatalf("update provider key during compact: %v", err)
	}
	close(holdC)
	waitForBot(t, "persona compact fail", 20*time.Second, func() bool { return envC.botSettled(botC.ID) })
	if recC.count("summary") < 1 {
		t.Fatalf("摘要请求应发生，实得 %d", recC.count("summary"))
	}
	if strings.Contains(recC.value("summary", "auth"), "bad-replacement") || strings.Contains(recC.value("archive", "auth"), "bad-replacement") {
		t.Fatalf("辅助请求必须使用冻结密钥 archive=%q summary=%q", recC.value("archive", "auth"), recC.value("summary", "auth"))
	}
	if recC.count("main") != 1 {
		t.Fatalf("压缩失败可继续时主线应恰好 1 次，实得 %d", recC.count("main"))
	}
	// 终态与人设/凭据泄漏的直证：假服务对泄漏正文返回 500，run 若终态不是
	// completed 即说明泄漏漏过；捕获的主线正文与凭据也要逐一核对。
	var runC model.BotRun
	envC.db.Where("bot_id = ?", botC.ID).Order("created_at DESC").First(&runC)
	if runC.Status != model.BotRunStatusCompleted {
		t.Fatalf("压缩失败可继续时 run 应完成，实得 %s err=%v", runC.Status, runC.ErrorMessage)
	}
	if strings.Contains(recC.value("main", "body"), markerC) {
		t.Fatalf("捕获的主线请求正文不得包含冻结后编辑的人设")
	}
	if strings.Contains(recC.value("main", "auth"), "bad-replacement") {
		t.Fatalf("主线请求必须使用冻结密钥，实得 %q", recC.value("main", "auth"))
	}
	var mainReq model.BotModelRequest
	envC.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", botC.ID).First(&mainReq)
	if mainReq.PromptRevision != oldRev {
		t.Fatalf("no_gain/失败不得接收新人设 revision: old=%d got=%d", oldRev, mainReq.PromptRevision)
	}
	stateC, _ := repository.NewBotContextRepository(envC.db).Find(botC.ID)
	if stateC != nil && stateC.Generation > 0 && stateC.SummaryThroughSeq > 0 {
		t.Fatalf("摘要失败不得推进 generation/水位: %+v", stateC)
	}

	// 独立 no_gain 场景：摘要成功但不产生净缩减，冻结同样不得接收新人设。
	holdD := make(chan struct{})
	recD := newFixtureRecorder()
	srvD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "bad-replacement") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"auth_error"}}`)
			return
		}
		switch {
		case strings.Contains(s, "记忆归档器"):
			recD.hit("archive")
			select {
			case <-holdD:
			case <-r.Context().Done():
				return
			}
			writeFakeChatSSE(w, `{"items":[]}`)
		case strings.Contains(s, "上下文压缩器"):
			recD.hit("summary")
			// 摘要比待总结段还长但仍在窗口内：after ≥ before，验收判无净缩减。
			writeFakeChatSSE(w, strings.Repeat("冗长摘要没有净缩减。", 4400))
		case strings.Contains(s, `"tools"`):
			recD.hit("main", "auth", auth, "body", string(body))
			if strings.Contains(s, "UNIQUE_PERSONA_MARKER_NOGAIN") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"persona leaked into no_gain freeze"}}`)
				return
			}
			writeFakeChatSSE(w, "no_gain 后主线仍用旧人设")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"script_exhausted"}}`)
		}
	}))
	t.Cleanup(srvD.Close)
	envD := setupBotTestEnv(t)
	uD := createBotTestUser(t, envD.db, "personad")
	botD, err := envD.bots.Create(uD, &CreateBotRequest{Name: "pd", ProviderID: envD.createProviderAt(t, uD, srvD.URL, "sk-test-abcdef123456"), Model: "m"})
	if err != nil {
		t.Fatalf("create no_gain: %v", err)
	}
	envD.cfg.Bots.Context.WindowTokens = 50000
	envD.cfg.Bots.Context.CompressionRatio = 0.2
	for i := 0; i < 30; i++ {
		seedBotUserMessage(t, envD, botD.ID, strings.Repeat("很长的历史内容用于撑满上下文。", 80))
	}
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, envD, botD.ID, fmt.Sprintf("保护%d", i))
	}
	rowD2, _ := envD.bots.bots.Get(botD.ID)
	oldRevD := rowD2.PromptRevision
	markHistoryConsumed(t, envD, rowD2, nil)
	if _, err := envD.bots.SendMessage(uD, botD.ID, &SendMessageRequest{Text: "继续", ClientMessageID: "pf-d"}); err != nil {
		t.Fatalf("send no_gain: %v", err)
	}
	deadlineD := time.Now().Add(15 * time.Second)
	for recD.count("archive") == 0 && time.Now().Before(deadlineD) {
		time.Sleep(20 * time.Millisecond)
	}
	if recD.count("archive") == 0 {
		t.Fatalf("no_gain 场景应进入归档请求")
	}
	markerD := "UNIQUE_PERSONA_MARKER_NOGAIN"
	if _, err := envD.bots.Update(uD, botD.ID, &UpdateBotRequest{PersonaPrompt: &markerD}); err != nil {
		t.Fatalf("update persona during no_gain: %v", err)
	}
	close(holdD)
	waitForBot(t, "persona no_gain", 20*time.Second, func() bool { return envD.botSettled(botD.ID) })
	var rowD3 model.BotCompaction
	envD.db.Where("bot_id = ?", botD.ID).Order("created_at DESC").First(&rowD3)
	if rowD3.Status != model.BotCompactionNoGain || rowD3.ErrorCode == nil || *rowD3.ErrorCode != "no_reduction" {
		t.Fatalf("长摘要应落 no_gain/no_reduction 且可继续: %s %v", rowD3.Status, rowD3.ErrorCode)
	}
	if recD.count("main") != 1 {
		t.Fatalf("no_gain 可继续时主线应恰好 1 次，实得 %d", recD.count("main"))
	}
	var runD2 model.BotRun
	envD.db.Where("bot_id = ?", botD.ID).Order("created_at DESC").First(&runD2)
	if runD2.Status != model.BotRunStatusCompleted {
		t.Fatalf("no_gain 时 run 应完成，实得 %s err=%v", runD2.Status, runD2.ErrorMessage)
	}
	if strings.Contains(recD.value("main", "body"), markerD) {
		t.Fatalf("no_gain 的主线正文不得包含冻结后编辑的人设")
	}
	if strings.Contains(recD.value("main", "auth"), "bad-replacement") {
		t.Fatalf("no_gain 的主线请求必须使用冻结密钥，实得 %q", recD.value("main", "auth"))
	}
	var mainReqD model.BotModelRequest
	envD.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", botD.ID).First(&mainReqD)
	if mainReqD.PromptRevision != oldRevD {
		t.Fatalf("no_gain 不得接收新人设 revision: old=%d got=%d", oldRevD, mainReqD.PromptRevision)
	}
}

func TestBotResultItemCountShape(t *testing.T) {
	if botResultItemCount(`{"kind":"issue_list","items":[],"total":10,"page":1,"page_size":20}`) != 0 {
		t.Fatalf("空列表包装应得 0")
	}
	if botResultItemCount(`{"kind":"issue_list","items":[{},{}],"total":99,"page":1,"page_size":20}`) != 2 {
		t.Fatalf("{items} 包装应按当前返回条数")
	}
	if botResultItemCount(`[{"a":1},{"b":2},{"c":3}]`) != 3 {
		t.Fatalf("顶层数组应按长度")
	}
	if botResultItemCount(`{"id":"x"}`) != 1 {
		t.Fatalf("普通对象记 1")
	}
}

func TestBotFoldSkipsPendingReceipt(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "foldpend")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 10000
	seedBotUserMessage(t, env, bot.ID, "old")
	callID := "call-pending-fold"
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		msgs := repository.NewBotMessageRepository(tx)
		seq, err := env.bots.bots.NextMessageSeqTx(tx, bot.ID)
		if err != nil {
			return err
		}
		asst, _ := json.Marshal(botAssistantContent{ToolCalls: []botAssistantToolCall{{
			CallID: callID, ProviderToolCallID: "p-" + callID, ToolName: "fs_issue", Action: "update", Arguments: "{}",
		}}})
		if err := msgs.AppendTx(tx, &model.BotMessage{
			ID: uuid.NewString(), BotID: bot.ID, Seq: seq, Kind: model.BotMessageKindAssistant,
			ContentJSON: string(asst), CreatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		seq2, err := env.bots.bots.NextMessageSeqTx(tx, bot.ID)
		if err != nil {
			return err
		}
		reply, _ := json.Marshal(botToolReplyContent{
			CallID: callID, ProviderToolCallID: "p-" + callID, ToolName: "fs_issue",
			Status:  string(model.BotCallStatusPendingConfirmation),
			Content: strings.Repeat("确认回执很大的正文", 400),
		})
		return msgs.AppendTx(tx, &model.BotMessage{
			ID: uuid.NewString(), BotID: bot.ID, Seq: seq2, Kind: model.BotMessageKindToolReply,
			ContentJSON: string(reply), CreatedAt: time.Now().UTC(),
		})
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, fmt.Sprintf("p%d", i))
		seedBotAssistantToolBlock(t, env, bot.ID, fmt.Sprintf("ok%d", i), "fs_issue", "x")
	}
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)
	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.FoldedAdded != 0 {
		t.Fatalf("pending/queued 回执即使调用后来成功也不得折叠: %+v", plan.FoldedAdded)
	}
}

func TestProtocolPrefixAddedBytesIncludesCommas(t *testing.T) {
	prev := []byte(`[{"a":1}]`)
	curr := []byte(`[{"a":1},{"b":22}]`)
	added, ok := protocolPrefixAddedBytes(prev, curr)
	if !ok {
		t.Fatal("curr 应是 prev 的追加")
	}
	if added != int64(len(curr)-len(prev)) {
		t.Fatalf("追加字节应为 len(curr)-len(prev)=%d，实得 %d", len(curr)-len(prev), added)
	}
	if added <= int64(len(`{"b":22}`)) {
		t.Fatalf("必须计入逗号字节 added=%d elem=%d", added, len(`{"b":22}`))
	}
}

func TestSplitUTF8ByTokenBudgetKeepsCharacters(t *testing.T) {
	s := strings.Repeat("中文摘要", 80)
	parts := splitUTF8ByTokenBudget(s, 20)
	if len(parts) < 2 {
		t.Fatalf("应按 UTF-8 字节预算分片，实得 %d", len(parts))
	}
	if strings.Join(parts, "") != s {
		t.Fatalf("分片必须完整覆盖原文")
	}
	for i, p := range parts {
		if !utf8.ValidString(p) {
			t.Fatalf("分片 %d 切断了 UTF-8", i)
		}
		if botEstimateTokens(p) > 20+2 {
			t.Fatalf("分片 %d 超出字节预算: tokens=%d", i, botEstimateTokens(p))
		}
	}
	shards := shardSummaryBlock(strings.Repeat("块", 200), 30)
	if len(shards) < 2 || !strings.Contains(shards[0], "[连续分片 1/") {
		t.Fatalf("超大协议块应带连续分片标记: %+v", shards)
	}
}

func TestEvalCompactionTargetRules(t *testing.T) {
	// G 可达：after 必须 ≤G 且净缩减。F+minSummary≤G（F 由调用方以清空
	// 摘要正文的快照算出，这里直接给固定前缀体积）。
	fixed := int64(100)
	afterOK := fixed + 200
	ev := evalCompactionTarget(afterOK, 2000, fixed, fixed+botMinSummaryTokens, 1500, 3000)
	if !ev.OK || ev.Unreachable || ev.OverWindow || ev.F != fixed {
		t.Fatalf("G 可达且 after≤G 应通过: %+v", ev)
	}
	afterHigh := fixed + 343
	ev = evalCompactionTarget(afterHigh, 2000, fixed, fixed+botMinSummaryTokens, 1500, 3000)
	if ev.OK {
		t.Fatalf("G 可达但 after>G 应拒绝: %+v after=%d", ev, afterHigh)
	}
	// G 不可达：有效目标 = F+min；验收须 after≤effective 且 <T 有净缩减。
	effective := fixed + botMinSummaryTokens
	after := effective
	ev = evalCompactionTarget(after, after+100, fixed, 50, effective+10, after+2000)
	if ev.Unreachable {
		t.Fatalf("有效目标 <T 时不应判不可达: %+v F=%d", ev, ev.F)
	}
	if !ev.OK {
		t.Fatalf("G 不可达但 after≤effective 且 <T 应通过: %+v", ev)
	}
	// after 落在触发阈值内但超出有效目标（F+min）也须拒绝。
	ev = evalCompactionTarget(effective+1, after+100, fixed, 50, effective+10, after+2000)
	if ev.OK {
		t.Fatalf("after>effective 应拒绝: %+v", ev)
	}
	ev = evalCompactionTarget(after, after+100, fixed, 50, effective-1, after+2000)
	if !ev.Unreachable {
		t.Fatalf("有效目标 ≥T 应记录不可达: %+v", ev)
	}
	// 超窗路径同样要算出不可达：原因记录不得被 context_uncompressible 覆盖。
	ev = evalCompactionTarget(after, after+100, fixed, 50, effective-1, after-1)
	if !ev.OverWindow || !ev.Unreachable {
		t.Fatalf("超窗且固定前缀+min ≥T 应同时记录 OverWindow 与 Unreachable: %+v", ev)
	}
	// 无净缩减路径同样保留不可达标记。
	ev = evalCompactionTarget(after, after-1, fixed, 50, effective-1, after+2000)
	if ev.OK || !ev.Unreachable {
		t.Fatalf("无净缩减且不可达应保留 Unreachable: %+v", ev)
	}
	// 回退到无摘要水位的快照 F≡after（没有摘要正文可剥离），不得判不可达。
	ev = evalCompactionTarget(400, 400, 400, 50, 100, 5000)
	if ev.OK || ev.Unreachable || ev.OverWindow || ev.F != 400 {
		t.Fatalf("无摘要正文的回退快照不应判不可达: %+v", ev)
	}
	// 静态反例（r5）：无摘要、fold-only 净收益且低于 T（after=F=14900、
	// before=18000、G=6750、T=15000、W=20000）——守卫第二分支不得把
	// 可发布的折叠结果硬判 Unreachable。
	ev = evalCompactionTarget(14900, 18000, 14900, 6750, 15000, 20000)
	if ev.Unreachable || !ev.OK || ev.OverWindow || ev.F != 14900 {
		t.Fatalf("fold-only 净收益低于 T 应通过验收: %+v", ev)
	}
	// 对照：确有摘要边界（F<after）且 F+min ≥T，仍须记录不可达。
	ev = evalCompactionTarget(14900, 18000, 14800, 6750, 15000, 20000)
	if !ev.Unreachable {
		t.Fatalf("确有摘要正文且 F+min≥T 应记录不可达: %+v", ev)
	}
}

// TestBotSummarySerializedCostShrink 覆盖 r5-S3：满片含大量 `<`/`&` 时转义
// 膨胀可让序列化超出量大于整轮 inputBudget——按序列化成本迭代收缩必须找到
// 可容纳分片并完成摘要，而不是一次收缩失败放弃；每个发出的请求都不得超窗，
// 请求数恰为复算批数（有限脚本，脚本外多发即 500）。
func TestBotSummarySerializedCostShrink(t *testing.T) {
	env := setupBotTestEnv(t)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "escsum")
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "esc", ProviderID: env.createProvider(t, userID, "sk-test-abcdef123456"), Model: "fake-model",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 8000
	botRow, _ := env.bots.bots.Get(bot.ID)

	// 长累积摘要抬高 wrap，正文高转义放大序列化成本。
	escText := strings.Repeat("<a&b>", 4500)
	segRaw, _ := json.Marshal(botUserContent{Text: escText})
	plan := &botCompactionPlan{
		State: &model.BotContextState{
			BotID: bot.ID, Generation: 0,
			SummaryText: strings.Repeat("既有滚动摘要。", 500),
		},
		Reason: model.BotCompactionThreshold,
		Segment: []model.BotMessage{{
			ID: uuid.NewString(), BotID: bot.ID, Seq: 1,
			Kind:        model.BotMessageKindUser,
			ContentJSON: string(segRaw),
			CreatedAt:   time.Now().UTC(),
		}},
	}
	plan.AllRows = plan.Segment
	plan.ThroughSeq = 1
	plan.GoalTokens = 2700
	plan.WindowTokens = 8000
	plan.TriggerTokens = 6000

	// 静态反例前提自检：满片序列化超出量必须大于整轮 inputBudget——旧实现
	// 的「一次收缩」正是败在这里。
	modelName := env.cfg.Bots.Context.SummaryModel
	if modelName == "" {
		modelName = botRow.Model
	}
	auxWindow := env.cfg.Bots.Context.WindowTokensFor(modelName)
	accSection := "\n[既有滚动摘要]\n" + plan.State.SummaryText + "\n"
	wrap := botEstimateTokens(botSummarySysPrompt) + botEstimateTokens(botSummaryRules) + botEstimateTokens(accSection) + 96
	maxOut := auxWindow - wrap - 128
	if maxOut < 1 {
		t.Fatalf("本场景 wrap 过大: %d", wrap)
	}
	reqOut := env.bots.summaryOutputBudget(botRow, plan)
	if reqOut > maxOut {
		reqOut = maxOut
	}
	inputBudget := auxWindow - wrap - reqOut
	if inputBudget < 32 {
		t.Fatalf("本场景 inputBudget 过小: %d", inputBudget)
	}
	blocks := summaryProtocolBlocks(plan.Segment, "")
	fullPiece := shardSummaryBlock(blocks[0], inputBudget)[0]
	over0 := botSerializedInputTokens(summaryBatchMessages(botSummarySysPrompt, botSummaryRules, accSection, 1, fullPiece)) + reqOut - auxWindow
	if over0 <= inputBudget {
		t.Fatalf("反例前提不成立：over(%d) 应大于 inputBudget(%d)", over0, inputBudget)
	}

	want := expectedSummaryBatchesForPlan(t, env, botRow, plan, "滚动摘要已生成。")
	if want < 2 {
		t.Fatalf("高转义长输入应产生多批，复算批数=%d", want)
	}
	turns := make([]fakeModelTurn, 0, want)
	for i := 0; i < want; i++ {
		turns = append(turns, fakeModelTurn{Text: "滚动摘要已生成。"})
	}
	env.fake.mu.Lock()
	env.fake.turns = turns
	env.fake.mu.Unlock()

	now := time.Now().UTC()
	maint := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: bot.ID, Kind: model.BotMaintenanceCompaction,
		TriggerKey: "test:serialized-shrink", Status: model.BotMaintenanceRunning,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(maint).Error; err != nil {
		t.Fatalf("seed maint: %v", err)
	}
	summary, err := env.bots.requestSummary(context.Background(), botRow, maint, plan)
	if err != nil {
		t.Fatalf("迭代收缩后摘要应成功: %v", err)
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("摘要不应为空")
	}
	// 精确请求数 + 每个请求都在窗口内（不发送注定超窗的请求）。
	var bodies [][]byte
	for {
		select {
		case req := <-env.fake.requests:
			bodies = append(bodies, req.Body)
			continue
		default:
		}
		break
	}
	if len(bodies) != want {
		t.Fatalf("摘要请求应恰为复算批数 %d，实得 %d", want, len(bodies))
	}
	for i, b := range bodies {
		if est := botEstimateBytes(int64(len(b))); est > auxWindow {
			t.Fatalf("摘要请求 %d 超窗: est=%d window=%d", i+1, est, auxWindow)
		}
	}
	var n int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", maint.ID).Count(&n)
	if n != int64(want) {
		t.Fatalf("维护请求账本应恰为 %d 条摘要，实得 %d", want, n)
	}
}

// calibrateEscapedAccumulator 用 "<a&b>"×k 作既有滚动摘要并二分调 k，使
// overOf(acc_k)（实测序列化超出量，随 k 单调不减、每步 +4~5 token）落在
// [lo, hi]。构造 r6 静态反例的窗口几何时使用。
func calibrateEscapedAccumulator(t *testing.T, overOf func(acc string) int64, lo, hi int64) string {
	t.Helper()
	val := func(k int) int64 { return overOf(strings.Repeat("<a&b>", k)) }
	hiK := 1
	for val(hiK) < lo {
		hiK *= 2
		if hiK > 1<<22 {
			t.Fatalf("校准失败：overOf 不随 k 增长")
		}
	}
	loK := 0
	for loK+1 < hiK {
		mid := (loK + hiK) / 2
		if val(mid) < lo {
			loK = mid
		} else {
			hiK = mid
		}
	}
	acc := strings.Repeat("<a&b>", hiK)
	if v := overOf(acc); v < lo || v > hi {
		t.Fatalf("校准失败：overOf=%d 不在 [%d,%d]", v, lo, hi)
	}
	return acc
}

// newEscapedSummaryPlan 造高转义单块压缩计划：segment 是一段 <a&b> 长文，
// SummaryText 由调用方校准后回填；GoalTokens 压到下限使 reqOut 恒为
// botMinSummaryTokens，测量口径不受既有摘要长度影响。
func newEscapedSummaryPlan(t *testing.T, env *botTestEnv, botRow *model.Bot, botID string) (*botCompactionPlan, int64, int64) {
	t.Helper()
	segRaw, _ := json.Marshal(botUserContent{Text: strings.Repeat("<a&b>", 4500)})
	plan := &botCompactionPlan{
		State:  &model.BotContextState{BotID: botID, Generation: 0},
		Reason: model.BotCompactionThreshold,
		Segment: []model.BotMessage{{
			ID: uuid.NewString(), BotID: botID, Seq: 1,
			Kind: model.BotMessageKindUser, ContentJSON: string(segRaw),
			CreatedAt: time.Now().UTC(),
		}},
	}
	plan.AllRows = plan.Segment
	plan.ThroughSeq = 1
	plan.GoalTokens = 100
	plan.WindowTokens = 8000
	plan.TriggerTokens = 6000
	reqOut := env.bots.summaryOutputBudget(botRow, plan)
	if reqOut != botMinSummaryTokens {
		t.Fatalf("reqOut 应固定为下限 %d，实得 %d", botMinSummaryTokens, reqOut)
	}
	return plan, reqOut, env.cfg.Bots.Context.WindowTokensFor(modelNameForSummary(env, botRow))
}

// modelNameForSummary 与 requestSummary 同口径取辅助模型名。
func modelNameForSummary(env *botTestEnv, botRow *model.Bot) string {
	if env.cfg.Bots.Context.SummaryModel != "" {
		return env.cfg.Bots.Context.SummaryModel
	}
	return botRow.Model
}

func drainFakeRequests(env *botTestEnv) [][]byte {
	var bodies [][]byte
	for {
		select {
		case req := <-env.fake.requests:
			bodies = append(bodies, req.Body)
			continue
		default:
		}
		break
	}
	return bodies
}

func seedSummaryMaintRun(t *testing.T, env *botTestEnv, botID, key string) *model.BotMaintenanceRun {
	t.Helper()
	now := time.Now().UTC()
	maint := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: botID, Kind: model.BotMaintenanceCompaction,
		TriggerKey: key, Status: model.BotMaintenanceRunning,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(maint).Error; err != nil {
		t.Fatalf("seed maint: %v", err)
	}
	return maint
}

// TestBotSummaryShrinkFitsMinimalPiece 覆盖 r6 静态反例（a）：高转义既有
// 摘要让「空 piece 序列化输入+输出预算」距辅助窗口仅剩 ~16 token——任何
// 原文估算 ≥32 token 的高转义片段都放不下（旧实现收缩到 p<32 即放弃、
// 必然失败），但单字符加连续分片标记的最小分片仍可容纳。终止条件以实测
// 序列化为准时必须把本批降到可容纳分片并完成摘要。
func TestBotSummaryShrinkFitsMinimalPiece(t *testing.T) {
	env := setupBotTestEnv(t)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "minpiece")
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "min", ProviderID: env.createProvider(t, userID, "sk-test-abcdef123456"), Model: "fake-model",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 8000
	botRow, _ := env.bots.bots.Get(bot.ID)
	plan, reqOut, auxWindow := newEscapedSummaryPlan(t, env, botRow, bot.ID)

	overOfAcc := func(acc, piece string) int64 {
		accSection := "\n[既有滚动摘要]\n" + acc + "\n"
		return botSerializedInputTokens(summaryBatchMessages(botSummarySysPrompt, botSummaryRules, accSection, 1, piece)) + reqOut - auxWindow
	}
	// 反例前提自检：空 piece 距窗口剩 9~24 token（目标 ~16）；单字符+连续
	// 分片标记的最小分片仍可容纳；原文估算恰 32 token 的高转义片段放不下
	// ——旧实现的 p<32 提前失败正拦在这里。
	acc := calibrateEscapedAccumulator(t, func(a string) int64 { return overOfAcc(a, "") }, -24, -8)
	if over := overOfAcc(acc, "[连续分片 1/2]\n…"); over > 0 {
		t.Fatalf("反例前提不成立：最小分片应可容纳，over=%d", over)
	}
	piece32 := strings.Repeat("<a&b>", 22)
	if botEstimateTokens(piece32) < 32 {
		t.Fatalf("piece32 原文估算应 ≥32: %d", botEstimateTokens(piece32))
	}
	if over := overOfAcc(acc, piece32); over <= 0 {
		t.Fatalf("反例前提不成立：32 token 高转义片段应放不下，over=%d", over)
	}
	plan.State.SummaryText = acc

	want := expectedSummaryBatchesForPlan(t, env, botRow, plan, "滚动摘要已生成。")
	if want < 2 {
		t.Fatalf("高转义长输入应产生多批，复算批数=%d", want)
	}
	turns := make([]fakeModelTurn, 0, want)
	for i := 0; i < want; i++ {
		turns = append(turns, fakeModelTurn{Text: "滚动摘要已生成。"})
	}
	env.fake.mu.Lock()
	env.fake.turns = turns
	env.fake.mu.Unlock()

	maint := seedSummaryMaintRun(t, env, bot.ID, "test:shrink-minimal-piece")
	summary, err := env.bots.requestSummary(context.Background(), botRow, maint, plan)
	if err != nil {
		t.Fatalf("收缩到最小分片后摘要应成功（旧实现 p<32 会失败）: %v", err)
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("摘要不应为空")
	}
	// 请求数恰为复算批数，且每个请求实测都在窗口内（不发送注定超窗请求）。
	bodies := drainFakeRequests(env)
	if len(bodies) != want {
		t.Fatalf("摘要请求应恰为复算批数 %d，实得 %d", want, len(bodies))
	}
	for i, b := range bodies {
		if est := botEstimateBytes(int64(len(b))); est > auxWindow {
			t.Fatalf("摘要请求 %d 超窗: est=%d window=%d", i+1, est, auxWindow)
		}
	}
	var n int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", maint.ID).Count(&n)
	if n != int64(want) {
		t.Fatalf("维护请求账本应恰为 %d 条摘要，实得 %d", want, n)
	}
}

// TestBotSummaryShrinkWrapperOverWindow 覆盖 r6 静态反例（b）：高转义既有
// 摘要让固定包装（sys/rules/既有摘要段）+输出预算本身超辅助窗口——空
// piece 序列化已超窗，连单字符+连续分片标记的最小分片也放不下，任何请求
// 都注定超窗；此时外层估算口径的批分预算仍宽裕（切片检查不拦），必须由
// 收缩迭代的实测终止条件干净失败，且一个请求都不发出。
func TestBotSummaryShrinkWrapperOverWindow(t *testing.T) {
	env := setupBotTestEnv(t)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "wrapover")
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "wrap", ProviderID: env.createProvider(t, userID, "sk-test-abcdef123456"), Model: "fake-model",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 8000
	botRow, _ := env.bots.bots.Get(bot.ID)
	plan, reqOut, auxWindow := newEscapedSummaryPlan(t, env, botRow, bot.ID)

	overOfAcc := func(acc, piece string) int64 {
		accSection := "\n[既有滚动摘要]\n" + acc + "\n"
		return botSerializedInputTokens(summaryBatchMessages(botSummarySysPrompt, botSummaryRules, accSection, 1, piece)) + reqOut - auxWindow
	}
	// 反例前提自检：空 piece 序列化+输出预算已超窗 1~24 token，最小分片更
	// 放不下；估算口径的切片预算仍 ≥32 token，外层检查不会先失败。
	acc := calibrateEscapedAccumulator(t, func(a string) int64 { return overOfAcc(a, "") }, 1, 24)
	accSection := "\n[既有滚动摘要]\n" + acc + "\n"
	wrap := botEstimateTokens(botSummarySysPrompt) + botEstimateTokens(botSummaryRules) + botEstimateTokens(accSection) + 96
	if maxOut := auxWindow - wrap - 128; maxOut < 1 {
		t.Fatalf("本场景应由收缩终止条件失败，外层 maxOut 却先拦住: %d", maxOut)
	}
	if inputBudget := auxWindow - wrap - reqOut; inputBudget < 32 {
		t.Fatalf("本场景应由收缩终止条件失败，外层 inputBudget 却先拦住: %d", inputBudget)
	}
	if over := overOfAcc(acc, "[连续分片 1/2]\n…"); over <= 0 {
		t.Fatalf("反例前提不成立：最小分片应放不下，over=%d", over)
	}
	plan.State.SummaryText = acc

	maint := seedSummaryMaintRun(t, env, bot.ID, "test:shrink-wrapper-over")
	_, err = env.bots.requestSummary(context.Background(), botRow, maint, plan)
	if !errors.Is(err, errContextCompactionFailed) {
		t.Fatalf("固定包装+输出预算自身超窗应干净失败 context_compaction_failed: %v", err)
	}
	// 不发出任何注定超窗的请求。
	for _, b := range drainFakeRequests(env) {
		body := string(b)
		if len(body) > 120 {
			body = body[:120]
		}
		t.Fatalf("不应发出任何模型请求: %s", body)
	}
	var n int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", maint.ID).Count(&n)
	if n != 0 {
		t.Fatalf("不应记账任何维护请求，实得 %d", n)
	}
}

// TestBotBoundarySnapshotFixedTokensSameProjection 覆盖 r5-S8（同一快照）：
// buildBoundarySnapshot 单次物化同时给出 after 与 F，F 等于清空摘要正文后
// 同一投影的独立复核，且差值恰为摘要正文转义后的协议开销。
func TestBotBoundarySnapshotFixedTokensSameProjection(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("压缩后的摘要")...)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "fixedtok")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 8000
	seedCompactableLedger(t, env, bot.ID, "固定前缀")
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)
	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	summary := "滚动摘要 <tag> & 已生成"
	manifest := env.bots.buildIdentifierManifest(plan)
	foldedJSON := botMustJSON(plan.Folded)
	snap := env.bots.buildBoundarySnapshot(botRow, plan, summary, manifest, foldedJSON, plan.ThroughSeq)
	if snap.AfterTokens <= 0 || snap.FixedTokens <= 0 {
		t.Fatalf("快照体积应非零: after=%d fixed=%d", snap.AfterTokens, snap.FixedTokens)
	}
	if snap.FixedTokens >= snap.AfterTokens {
		t.Fatalf("清空摘要正文应缩小投影: after=%d fixed=%d", snap.AfterTokens, snap.FixedTokens)
	}
	// F 与独立复核一致：外部状态未变时，清空正文全新物化应得同一体积。
	indep := env.bots.buildBoundarySnapshot(botRow, plan, "", manifest, foldedJSON, plan.ThroughSeq)
	if snap.FixedTokens != indep.AfterTokens {
		t.Fatalf("F 应等于独立复核: F=%d indep=%d", snap.FixedTokens, indep.AfterTokens)
	}
	// after−F 恰为「摘要正文（含围栏）」转义后的字节开销（转义开销不得计入 F）。
	escRaw, _ := json.Marshal(botFenceUntrusted(botNonce(t, env, bot.ID), "summary", summary))
	escTokens := botEstimateBytes(int64(len(escRaw) - 2))
	if d := snap.AfterTokens - snap.FixedTokens; d < escTokens-1 || d > escTokens {
		t.Fatalf("after−F 应≈正文转义开销: d=%d esc=%d", d, escTokens)
	}
	// through=0（投影里没有摘要消息）时 F ≡ after。
	noMsg := env.bots.buildBoundarySnapshot(botRow, plan, summary, manifest, foldedJSON, 0)
	if noMsg.FixedTokens != noMsg.AfterTokens {
		t.Fatalf("无摘要消息时 F 应等于 after: F=%d after=%d", noMsg.FixedTokens, noMsg.AfterTokens)
	}
}

// TestBotScheduleRunActivatesStaleBot 重启后只跑定时任务（schedule 路径）：
// 旧版本机器人重启后的第一次运行如果是 schedule run，本次发出的请求也必须
// 带新 system 前缀，而不是等下一次主线运行才补上。会话内解析不推进 bots 行
// 激活水位（INT-139 R11 复审：写回会洗掉主线窗口内的推迟判定）。
func TestBotScheduleRunActivatesStaleBot(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "定时汇报：已完成"})
	userID := createBotTestUser(t, env.db, "schedact5")
	bot := env.createBot(t, userID)
	const oldPrompt = "旧平台提示：没有默认人设段落"
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version":         botToolsetVersion,
		"platform_prompt_version": 4,
		"system_prompt_text":      oldPrompt,
	}).Error; err != nil {
		t.Fatalf("stale bot: %v", err)
	}

	// needs_report=false：汇报事件会唤醒主线 run（合法的主线入口激活），
	// 与本测试要断言的「schedule 会话不推进行水位」口径无关，关掉去抖。
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	env.bots.wakeScheduleRun(bot.ID, run.ID)
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("schedule run 应完成: %s %v", done.Status, done.ErrorMessage)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	// 行水位保持不动：主线窗口内的推迟激活依赖这个 stale 判定。
	if fresh.PlatformPromptVersion != 4 || fresh.ToolsetVersion != botToolsetVersion ||
		fresh.SystemPromptText != oldPrompt {
		t.Fatalf("schedule run 不得推进 bots 行激活水位: prompt=%d toolset=%d",
			fresh.PlatformPromptVersion, fresh.ToolsetVersion)
	}
	req := env.fake.nextRequest(t, 10*time.Second)
	elems, _ := rawMessages(t, req.Body)
	if len(elems) == 0 {
		t.Fatalf("schedule 请求应含 messages")
	}
	var sys struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(elems[0], &sys); err != nil {
		t.Fatalf("decode system message: %v", err)
	}
	if sys.Role != "system" {
		t.Fatalf("首条消息应为 system: %s", sys.Role)
	}
	if !strings.Contains(sys.Content, "说话方式") || strings.Contains(sys.Content, oldPrompt) {
		t.Fatalf("schedule 请求的 system 消息应为激活后的新提示词: %q", sys.Content)
	}
}

// TestBotCompactionPublishPromptChanged 压缩发布的提示词来源校验：提示词快照
// 是「读-建-发布」之间不可分割的负载（system_prompt_text 与验收用的
// AfterTokens 都由来源行派生）。快照构造后、发布前人设被外部编辑时，发布
// 必须按 prompt_changed 冲突失败：库内提示词保持新人设、generation 不推进、
// 压缩账本与维护运行记录该错误码——绝不能拿旧快照的验收结果覆盖新提示词。
// 冲突失败还不得污染 run 冻结副本：主循环要按它重建请求，被改成库内新状态
// 会让本轮与下轮的前缀/tools_hash 来回翻转。
func TestBotCompactionPublishPromptChanged(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, "记忆归档器"):
			<-hold
			writeFakeChatSSE(w, `{"items":[]}`)
		case strings.Contains(s, "上下文压缩器"):
			writeFakeChatSSE(w, "滚动摘要：来源冲突回归。")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"unexpected_request"}}`)
		}
	}))
	t.Cleanup(srv.Close)

	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "pubconf")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "pubconf", ProviderID: env.createProviderAt(t, userID, srv.URL, "sk-x0000"), Model: "m"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 8400
	env.cfg.Bots.Context.ArchiveModel = "arch-big"
	env.cfg.Bots.Context.ModelWindows = map[string]int64{"arch-big": 131072}
	seedBotUserMessage(t, env, bot.ID, "最早的来源冲突问题")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-old-conflict", "fs_issue", strings.Repeat("很长的工具结果", 8000))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, fmt.Sprintf("保护轮%d", i))
		seedBotAssistantToolBlock(t, env, bot.ID, fmt.Sprintf("call-pc%d", i), "fs_issue", "小结果")
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	// T1：按库内当前行构造快照与计划（对应「读-建」阶段）。
	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.NeedSummary || len(plan.Segment) == 0 {
		t.Fatalf("场景应需要摘要: needSummary=%v seg=%d", plan.NeedSummary, len(plan.Segment))
	}
	summary := "滚动摘要：来源冲突回归。"
	manifest := env.bots.buildIdentifierManifest(plan)
	foldedJSON := botMustJSON(plan.Folded)
	snap := env.bots.buildBoundarySnapshot(botRow, plan, summary, manifest, foldedJSON, plan.ThroughSeq)
	if snap.SourcePersonaPrompt != botRow.PersonaPrompt || snap.SourcePromptRevision != botRow.PromptRevision {
		t.Fatalf("快照来源应等于构造时读到的行: %+v vs rev=%d persona=%q",
			snap.SourcePromptRevision, botRow.PromptRevision, botRow.PersonaPrompt)
	}

	// T2：外部（另一写者）在发布前改人设——快照来源已失效。
	newPersona := "UNIQUE_NEW_PERSONA_AFTER_SNAPSHOT"
	if _, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{PersonaPrompt: &newPersona}); err != nil {
		t.Fatalf("update persona: %v", err)
	}
	afterUpdate, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load after update: %v", err)
	}

	// T3：按旧快照发布——必须按 prompt_changed 冲突失败并落账。
	now := time.Now().UTC()
	maintRun := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: bot.ID, Kind: model.BotMaintenanceCompaction,
		TriggerKey: fmt.Sprintf("compaction:threshold:g%d:s%d", plan.State.Generation, plan.MaxSeq),
		Status:     model.BotMaintenanceRunning, ConfigSnapshotJSON: "{}",
		InputSnapshotJSON: "{}", ResultJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(maintRun).Error; err != nil {
		t.Fatalf("seed maintenance run: %v", err)
	}
	// 发布前冻结 run 副本的五个提示词/工具集字段：失败路径不得改动它们。
	frozenCopy := botRunFreeze{
		SystemPromptText:      botRow.SystemPromptText,
		PromptRevision:        botRow.PromptRevision,
		PlatformPromptVersion: botRow.PlatformPromptVersion,
		PersonaPrompt:         botRow.PersonaPrompt,
		ToolsetVersion:        botRow.ToolsetVersion,
	}
	_, publishErr := env.bots.publishCompaction(botRow, maintRun, plan, model.BotCompactionSucceeded,
		summary, manifest, foldedJSON, plan.ThroughSeq, snap, "", "")
	var ce *botCompactionError
	if !errors.As(publishErr, &ce) || ce.Code != "prompt_changed" {
		t.Fatalf("发布应返回 prompt_changed，实得 %v", publishErr)
	}

	// 库内提示词仍是新人设、水位未被旧快照覆盖。
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load fresh: %v", err)
	}
	if fresh.PersonaPrompt != newPersona {
		t.Fatalf("库内人设应保持新人设: %q", fresh.PersonaPrompt)
	}
	if fresh.PromptRevision != afterUpdate.PromptRevision {
		t.Fatalf("库内 revision 不得被旧快照覆盖: %d vs %d", fresh.PromptRevision, afterUpdate.PromptRevision)
	}
	if fresh.SystemPromptText != buildBotSystemPrompt(newPersona, fresh.UntrustedNonce) {
		t.Fatalf("库内系统提示词应仍是按新人设构建的版本")
	}
	// generation 未推进。
	state, stateErr := repository.NewBotContextRepository(env.db).Find(bot.ID)
	if stateErr == nil && state.Generation != plan.State.Generation {
		t.Fatalf("冲突发布不得推进 generation: %d vs %d", state.Generation, plan.State.Generation)
	}
	// 压缩账本与维护运行都记录 prompt_changed。
	var row model.BotCompaction
	if err := env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&row).Error; err != nil {
		t.Fatalf("load compaction row: %v", err)
	}
	if row.ErrorCode == nil || *row.ErrorCode != "prompt_changed" {
		t.Fatalf("bot_compactions 应记录 prompt_changed，实得 %v", row.ErrorCode)
	}
	if row.Status != model.BotCompactionNoGain {
		t.Fatalf("冲突发布应按无进展落账，实得 %s", row.Status)
	}
	var mr model.BotMaintenanceRun
	if err := env.db.Where("id = ?", maintRun.ID).First(&mr).Error; err != nil {
		t.Fatalf("load maintenance run: %v", err)
	}
	if mr.Status != model.BotMaintenanceFailed || mr.ErrorCode == nil || *mr.ErrorCode != "prompt_changed" {
		t.Fatalf("维护运行应失败且记录 prompt_changed: %s %v", mr.Status, mr.ErrorCode)
	}
	// 失败发布不得污染 run 副本：五个提示词/工具集字段与失败前逐字段相同
	//（事务回滚后 bot 保持 run 冻结状态，成功路径才由 installBoundarySnapshot
	// 按快照对齐）。
	if botRow.SystemPromptText != frozenCopy.SystemPromptText || botRow.PromptRevision != frozenCopy.PromptRevision ||
		botRow.PlatformPromptVersion != frozenCopy.PlatformPromptVersion || botRow.PersonaPrompt != frozenCopy.PersonaPrompt ||
		botRow.ToolsetVersion != frozenCopy.ToolsetVersion {
		t.Fatalf("失败发布不得改动 run 冻结副本: prompt=%q rev=%d plat=%d persona=%q toolset=%d",
			botRow.SystemPromptText, botRow.PromptRevision, botRow.PlatformPromptVersion,
			botRow.PersonaPrompt, botRow.ToolsetVersion)
	}
	close(hold)
}

// TestBotCapabilityStatusFrozenToolset 能力查询按调用方透传的本 run 冻结工具集
// 水位判 available，不读 bots 行活值：冻结 v2 的会话即使库里已被并发 run 激活
// 到 v3+ 也答不可用（本会话工具面里没有 fs_schedule 动作）；当前版本答可用；
// fs_memory 不分版本可用。
func TestBotCapabilityStatusFrozenToolset(t *testing.T) {
	env := setupBotTestEnv(t)
	sched := &model.BotToolCall{BotID: "cap", ToolName: "fs_schedule", Action: "status"}

	raw, err := env.bots.execCapabilityStatus(sched, 2)
	if err != nil {
		t.Fatalf("v2 status: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, _ := out["available"].(bool); v {
		t.Fatalf("冻结 v2 的会话应答 available=false: %v", out)
	}
	if note, _ := out["note"].(string); note == "" {
		t.Fatalf("不可用时应带升级提示 note: %v", out)
	}

	raw, err = env.bots.execCapabilityStatus(sched, botToolsetVersion)
	if err != nil {
		t.Fatalf("v%d status: %v", botToolsetVersion, err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, _ := out["available"].(bool); !v {
		t.Fatalf("当前版本会话应答 available=true: %v", out)
	}

	mem := &model.BotToolCall{BotID: "cap", ToolName: "fs_memory", Action: "status"}
	raw, err = env.bots.execCapabilityStatus(mem, 1)
	if err != nil {
		t.Fatalf("memory status: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, _ := out["available"].(bool); !v {
		t.Fatalf("fs_memory 不分版本应可用: %v", out)
	}
	if cap, _ := out["capability"].(string); cap != "memory" {
		t.Fatalf("fs_memory 的 capability 应为 memory: %v", out)
	}
}
