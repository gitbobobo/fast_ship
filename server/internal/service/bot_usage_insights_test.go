package service

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// 花费页第二期（INT-163）服务层测试：缓存命中按天/机器人/任务、工具定义占比、
// 维护占比与用途拆分、失败重试浪费、空转判定、context_growth 与换模型差价。
// 账本直接 seed（成本口径与 INT-162 测试一致：缺失 ≠ 0），断言值全部手算。

func seedInsightRequest(t *testing.T, env *botTestEnv, runID string, mutate func(*model.BotModelRequest)) *model.BotModelRequest {
	t.Helper()
	var maxOrdinal int64
	if err := env.db.Model(&model.BotModelRequest{}).Where("run_id = ?", runID).
		Select("COALESCE(MAX(ordinal), 0)").Scan(&maxOrdinal).Error; err != nil {
		t.Fatalf("next ordinal: %v", err)
	}
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: runID, Ordinal: maxOrdinal + 1,
		Status: model.BotModelRequestSucceeded, StartedAt: time.Now().UTC(),
		PrefixHash: "p", ToolsHash: "t", TokenEstimator: "chars/4",
	}
	if mutate != nil {
		mutate(req)
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed insight request: %v", err)
	}
	return req
}

func seedInsightMaintenance(t *testing.T, env *botTestEnv, botID string, parentRunID *string, mutate func(*model.BotMaintenanceRun)) *model.BotMaintenanceRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: botID,
		Kind: model.BotMaintenanceCompaction, TriggerKey: "insight-maint-" + uuid.NewString(),
		ParentRunID: parentRunID, Status: model.BotMaintenanceSucceeded,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if mutate != nil {
		mutate(run)
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed insight maintenance: %v", err)
	}
	return run
}

func seedInsightMaintenanceRequest(t *testing.T, env *botTestEnv, maintRunID string, mutate func(*model.BotMaintenanceRequest)) *model.BotMaintenanceRequest {
	t.Helper()
	var maxOrdinal int64
	if err := env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", maintRunID).
		Select("COALESCE(MAX(ordinal), 0)").Scan(&maxOrdinal).Error; err != nil {
		t.Fatalf("next maint ordinal: %v", err)
	}
	now := time.Now().UTC()
	req := &model.BotMaintenanceRequest{
		ID: uuid.NewString(), MaintenanceRunID: maintRunID, Ordinal: maxOrdinal + 1,
		Purpose: model.BotPurposeSummary, Model: "fake-model",
		Status:     model.BotModelRequestSucceeded,
		PrefixHash: "p", ToolsHash: "", ToolsetVersion: 1,
		StartedAt: now, FinishedAt: &now,
	}
	if mutate != nil {
		mutate(req)
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed insight maintenance request: %v", err)
	}
	return req
}

// seedInsightToolCall 落一条工具调用（空转判定的读/写动作由 toolName/action 决定）。
func seedInsightToolCall(t *testing.T, env *botTestEnv, botID, requestID, toolName, action string) {
	t.Helper()
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: botID, RequestID: requestID,
		ProviderToolCallID: "tc-" + uuid.NewString()[:8], Ordinal: 1,
		ToolName: toolName, Action: action,
		ArgumentsJSON: "{}", ArgumentsHash: "ah",
		ExecutionMode: string(model.BotExecutionSync),
		Status:        model.BotCallStatusSucceeded,
		CreatedAt:     now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed tool call: %v", err)
	}
}

// insightBotOf 按 bot_id 取 insights 的机器人行。
func insightBotOf(t *testing.T, ins *api.BotUsageInsights, botID string) api.BotUsageInsightsBot {
	t.Helper()
	for _, b := range ins.Bots {
		if b.BotId == botID {
			return b
		}
	}
	t.Fatalf("insights 里找不到机器人 %s: %+v", botID, ins.Bots)
	return api.BotUsageInsightsBot{}
}

// insightTaskOf 按 task_id 取 insights 的任务行。
func insightTaskOf(t *testing.T, ins *api.BotUsageInsights, taskID string) api.BotUsageInsightsTask {
	t.Helper()
	for _, task := range ins.ScheduleTasks {
		if task.TaskId == taskID {
			return task
		}
	}
	t.Fatalf("insights 里找不到任务 %s: %+v", taskID, ins.ScheduleTasks)
	return api.BotUsageInsightsTask{}
}

// 缓存命中率：分子分母只含 usage 完整请求（bot_model_requests 按 prompt_tokens
// 非空、维护按 usage_complete）；cached>prompt 逐行钳制；按天记 run / 维护运行
// 自己的 created_at；无数据日 rate 为 null、分母为 0 的有数据日是 0 不是 null。
func TestBotUsageInsightsCacheHit(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightcache")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "UTC")
	yesterday := time.Now().UTC().Add(-24 * time.Hour)

	// 今天的 run：prompt=1000 cached=400；异常行 prompt=300 cached=500 钳到 300；
	// 缺 usage 的行不计。
	runToday := seedUsageRun(t, env, bot.ID, nil)
	seedInsightRequest(t, env, runToday.ID, func(r *model.BotModelRequest) {
		prompt, cached, completion := int64(1000), int64(400), int64(100)
		r.PromptTokens, r.CachedTokens, r.CompletionTokens = &prompt, &cached, &completion
	})
	seedInsightRequest(t, env, runToday.ID, func(r *model.BotModelRequest) {
		prompt, cached := int64(300), int64(500)
		r.PromptTokens, r.CachedTokens = &prompt, &cached
	})
	seedInsightRequest(t, env, runToday.ID, nil)

	// 昨天的 run：prompt=200 无 cached（0 不是 null）。
	runYday := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.CreatedAt = yesterday })
	seedInsightRequest(t, env, runYday.ID, func(r *model.BotModelRequest) {
		prompt := int64(200)
		r.PromptTokens = &prompt
	})

	// 维护：usage_complete=true 的 prompt=500 cached=250 计入；false 的不计。
	maint := seedInsightMaintenance(t, env, bot.ID, nil, nil)
	seedInsightMaintenanceRequest(t, env, maint.ID, func(r *model.BotMaintenanceRequest) {
		prompt, cached := int64(500), int64(250)
		r.UsageComplete, r.PromptTokens, r.CachedTokens = true, &prompt, &cached
	})
	seedInsightMaintenanceRequest(t, env, maint.ID, func(r *model.BotMaintenanceRequest) {
		prompt := int64(300)
		r.PromptTokens = &prompt
	})

	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	b := insightBotOf(t, ins, bot.ID)
	// 机器人级与总览/按天/按任务同一分子（统一聚合里 cached 逐行钳制，
	// 异常行钳到 prompt）：(400+300+250)/(1000+300+200+500) = 950/2000 = 0.475。
	if b.CacheHitRate == nil || !costEq(*b.CacheHitRate, 0.475) {
		t.Fatalf("机器人缓存命中率应为 0.475（cached 逐行钳制后与按天同口径），实得 %v", b.CacheHitRate)
	}
	if len(ins.CacheHitByDay) != 7 {
		t.Fatalf("7d 应有 7 个日行，实得 %d", len(ins.CacheHitByDay))
	}
	dayRate := map[string]*float64{}
	for _, d := range ins.CacheHitByDay {
		dayRate[d.Date] = d.Rate
	}
	today := time.Now().UTC().Format("2006-01-02")
	yday := yesterday.Format("2006-01-02")
	// 今天：(400+300+250)/(1000+300+500) = 950/1800。
	if dayRate[today] == nil || !costEq(*dayRate[today], 950.0/1800.0) {
		t.Fatalf("今天缓存命中率应为 %v，实得 %v", 950.0/1800.0, dayRate[today])
	}
	// 昨天：0/200 = 0（有完整 usage 无命中），不是 null。
	if dayRate[yday] == nil || !costEq(*dayRate[yday], 0) {
		t.Fatalf("昨天缓存命中率应为 0（分母 200），实得 %v", dayRate[yday])
	}
	for _, d := range ins.CacheHitByDay {
		if d.Date != today && d.Date != yday && d.Rate != nil {
			t.Fatalf("无数据日 %s 的 rate 应为 null，实得 %v", d.Date, d.Rate)
		}
	}
}

// 工具定义占比：分子分母同集合（usage 完整的主线/定时请求），维护请求不参与；
// 缺 usage 的行两边都不进；旧数据 tools_tokens_estimate=0 输出 share=0 而非 null；
// 无完整 usage 请求时 share 为 null。
func TestBotUsageInsightsToolsShare(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insighttools")
	bot := env.createBot(t, userID)

	run := seedUsageRun(t, env, bot.ID, nil)
	seedInsightRequest(t, env, run.ID, func(r *model.BotModelRequest) {
		prompt := int64(1000)
		r.PromptTokens, r.ToolsTokensEstimate = &prompt, 100
	})
	seedInsightRequest(t, env, run.ID, func(r *model.BotModelRequest) {
		prompt := int64(500)
		r.PromptTokens, r.ToolsTokensEstimate = &prompt, 50
	})
	seedInsightRequest(t, env, run.ID, func(r *model.BotModelRequest) {
		r.ToolsTokensEstimate = 999 // 缺 usage：不计入分子
	})
	maint := seedInsightMaintenance(t, env, bot.ID, nil, nil)
	seedInsightMaintenanceRequest(t, env, maint.ID, func(r *model.BotMaintenanceRequest) {
		prompt := int64(5000)
		r.UsageComplete, r.PromptTokens = true, &prompt // 维护不参与工具占比
	})

	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	b := insightBotOf(t, ins, bot.ID)
	if b.Tools.PromptTokens != 1500 || b.Tools.ToolsTokensEstimate != 150 {
		t.Fatalf("工具占比分子分母不符: %+v", b.Tools)
	}
	if b.Tools.Share == nil || !costEq(*b.Tools.Share, 0.1) {
		t.Fatalf("工具定义占比应为 0.1，实得 %v", b.Tools.Share)
	}

	// 旧数据：usage 完整但 tools_tokens_estimate=0 → share=0（显式 0 ≠ null）。
	bot2 := env.createBot(t, userID)
	run2 := seedUsageRun(t, env, bot2.ID, nil)
	seedInsightRequest(t, env, run2.ID, func(r *model.BotModelRequest) {
		prompt := int64(800)
		r.PromptTokens = &prompt // tools 缺省 0
	})
	// 无任何完整 usage 请求的机器人：share = null。
	bot3 := env.createBot(t, userID)
	run3 := seedUsageRun(t, env, bot3.ID, nil)
	seedInsightRequest(t, env, run3.ID, nil)

	ins, err = env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights again: %v", err)
	}
	old := insightBotOf(t, ins, bot2.ID)
	if old.Tools.Share == nil || !costEq(*old.Tools.Share, 0) {
		t.Fatalf("旧数据 share 应为显式 0，实得 %v", old.Tools.Share)
	}
	empty := insightBotOf(t, ins, bot3.ID)
	if empty.Tools.Share != nil || empty.Tools.PromptTokens != 0 {
		t.Fatalf("无完整 usage 时 share 应为 null: %+v", empty.Tools)
	}
}

// 维护占比：cost 含挂到 run 名下的维护（口径大于总览维护桶）；share 分母 =
// 总览三桶之和；用途按窗口内出现过的列出、cost 降序 null 最后；全未计价用途
// cost 为 null；机器人总花费为 0 时 share 为 null。
func TestBotUsageInsightsMaintenanceShare(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightmaint")
	bot := env.createBot(t, userID)

	main := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.CostEstimate = ptrCost(1.0) })
	seedUsageRequest(t, env, main.ID, ptrCost(1.0), "m-hash")
	// 无父维护：summary 0.3 + archive 0.1。
	m1 := seedInsightMaintenance(t, env, bot.ID, nil, nil)
	seedInsightMaintenanceRequest(t, env, m1.ID, func(r *model.BotMaintenanceRequest) {
		r.Purpose, r.CostEstimate = model.BotPurposeSummary, ptrCost(0.3)
		r.UsageComplete = true
	})
	seedInsightMaintenanceRequest(t, env, m1.ID, func(r *model.BotMaintenanceRequest) {
		r.Purpose, r.CostEstimate = model.BotPurposeArchive, ptrCost(0.1)
		r.UsageComplete = true
	})
	// 挂到 run 名下的维护（总览桶不计它，这里计入）：archive 全未计价。
	m2 := seedInsightMaintenance(t, env, bot.ID, &main.ID, nil)
	seedInsightMaintenanceRequest(t, env, m2.ID, func(r *model.BotMaintenanceRequest) {
		r.Purpose = model.BotPurposeArchive // 未计价
	})
	// 无父维护 nightly 只有未计价请求：用途列出、cost null。
	m3 := seedInsightMaintenance(t, env, bot.ID, nil, nil)
	seedInsightMaintenanceRequest(t, env, m3.ID, func(r *model.BotMaintenanceRequest) {
		r.Purpose = model.BotPurposeNightly
	})

	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	b := insightBotOf(t, ins, bot.ID)
	if b.Maintenance.Cost == nil || !costEq(*b.Maintenance.Cost, 0.4) {
		t.Fatalf("维护成本应为全部已计价维护 0.4，实得 %v", b.Maintenance.Cost)
	}
	// 分母 = 总览三桶之和 1.0 + 0.4 = 1.4。
	if b.Maintenance.Share == nil || !costEq(*b.Maintenance.Share, 0.4/1.4) {
		t.Fatalf("维护占比应为 0.4/1.4，实得 %v", b.Maintenance.Share)
	}
	if len(b.Maintenance.ByPurpose) != 3 {
		t.Fatalf("应列出 3 个出现过的用途: %+v", b.Maintenance.ByPurpose)
	}
	if b.Maintenance.ByPurpose[0].Purpose != "summary" ||
		b.Maintenance.ByPurpose[0].Cost == nil || !costEq(*b.Maintenance.ByPurpose[0].Cost, 0.3) {
		t.Fatalf("用途应按 cost 降序首位 summary 0.3: %+v", b.Maintenance.ByPurpose)
	}
	if b.Maintenance.ByPurpose[1].Purpose != "archive" ||
		b.Maintenance.ByPurpose[1].Cost == nil || !costEq(*b.Maintenance.ByPurpose[1].Cost, 0.1) {
		t.Fatalf("archive 应为 0.1: %+v", b.Maintenance.ByPurpose[1])
	}
	if b.Maintenance.ByPurpose[2].Purpose != "nightly" || b.Maintenance.ByPurpose[2].Cost != nil {
		t.Fatalf("nightly 全未计价应为 null: %+v", b.Maintenance.ByPurpose[2])
	}

	// 机器人总花费为 0（全未计价）：share 为 null、cost 为 null。
	bot2 := env.createBot(t, userID)
	run2 := seedUsageRun(t, env, bot2.ID, nil)
	seedInsightRequest(t, env, run2.ID, nil) // 未计价
	n2 := seedInsightMaintenance(t, env, bot2.ID, nil, nil)
	seedInsightMaintenanceRequest(t, env, n2.ID, func(r *model.BotMaintenanceRequest) {
		r.UsageComplete = true // usage 完整但无价
	})
	ins, err = env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights again: %v", err)
	}
	b2 := insightBotOf(t, ins, bot2.ID)
	if b2.Maintenance.Cost != nil || b2.Maintenance.Share != nil {
		t.Fatalf("全未计价时维护金额与占比应为 null: %+v", b2.Maintenance)
	}
}

// P90（nearest-rank）落在总览投影上：bot 10 个已计价 run 0.1..1.0 → 0.9；
// 任务 4 个 run 0.2/0.4/0.6/0.9 → 0.9；无已计价 run → null。
func TestBotUsageInsightsP90(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightp90")
	bot := env.createBot(t, userID)
	for i := 1; i <= 10; i++ {
		seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.CostEstimate = ptrCost(float64(i) * 0.1) })
	}
	task := seedScheduleTask(t, env, bot.ID, nil)
	for _, c := range []float64{0.4, 0.2, 0.9, 0.6} {
		cost := c
		seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
			r.TriggerSource = model.BotTriggerSchedule
			r.SessionKind = model.BotSessionKindSchedule
			taskID := task.ID
			r.ScheduleTaskID = &taskID
			r.CostEstimate = &cost
		})
	}
	bot2 := env.createBot(t, userID)
	seedUsageRun(t, env, bot2.ID, nil) // 无已计价

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	var got *float64
	for _, b := range overview.Bots {
		if b.BotId == bot.ID {
			got = b.P90RunCost
		}
	}
	if got == nil || !costEq(*got, 0.9) {
		t.Fatalf("bot P90 应为 0.9（ceil(0.9*10)-1），实得 %v", got)
	}
	var taskP90 *float64
	for _, taskItem := range overview.ScheduleTasks {
		if taskItem.TaskId == task.ID {
			taskP90 = taskItem.P90RunCost
		}
	}
	if taskP90 == nil || !costEq(*taskP90, 0.9) {
		t.Fatalf("任务 P90 应为 0.9（ceil(0.9*4)-1），实得 %v", taskP90)
	}
	for _, b := range overview.Bots {
		if b.BotId == bot2.ID && b.P90RunCost != nil {
			t.Fatalf("无已计价 run 的 P90 应为 null，实得 %v", b.P90RunCost)
		}
	}
}

// 失败与重试浪费：同 run 两次同 hash 失败后成功 → retried=2；failed_requests
// = 2（含维护侧）；failed_requests_cost 只加已计价失败行；failed_runs_cost =
// run 自身 + 关联维护；by_error 按 cost 降序、null-cost 组按码排序。
func TestBotUsageInsightsFailureRetry(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightfail")
	bot := env.createBot(t, userID)

	// 两次 429 失败（一次已计价一次未计价）后第三次成功。
	runF := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.Status = model.BotRunStatusFailed
		r.CostEstimate = ptrCost(0.5)
	})
	seedInsightRequest(t, env, runF.ID, func(r *model.BotModelRequest) {
		r.Status = model.BotModelRequestFailed
		r.RequestHash = "h1"
		r.ErrorCode = ptrString("rate_limit")
		r.CostEstimate = ptrCost(0.05)
		prompt := int64(100)
		r.PromptTokens = &prompt
	})
	seedInsightRequest(t, env, runF.ID, func(r *model.BotModelRequest) {
		r.Status = model.BotModelRequestFailed
		r.RequestHash = "h1"
		r.ErrorCode = ptrString("rate_limit")
		// 未计价失败行：不进 failed_requests_cost
	})
	seedInsightRequest(t, env, runF.ID, func(r *model.BotModelRequest) {
		r.RequestHash = "h1" // 最终成功那次也计 1 次重试
		r.CostEstimate = ptrCost(0.2)
		prompt := int64(100)
		r.PromptTokens = &prompt
	})
	// 关联维护：failed_runs_cost 的维护成分。
	seedUsageMaintenance(t, env, bot.ID, &runF.ID, ptrCost(0.25))

	// 另一 run：一次未计价失败（server_error）+ 成功 → retried 再 +1。
	runG := seedUsageRun(t, env, bot.ID, nil)
	seedInsightRequest(t, env, runG.ID, func(r *model.BotModelRequest) {
		r.Status = model.BotModelRequestFailed
		r.RequestHash = "h2"
		r.ErrorCode = ptrString("server_error")
	})
	seedInsightRequest(t, env, runG.ID, func(r *model.BotModelRequest) {
		r.RequestHash = "h2"
	})

	// 维护侧失败请求（quota 已计价 0.01）。
	maint := seedInsightMaintenance(t, env, bot.ID, nil, nil)
	seedInsightMaintenanceRequest(t, env, maint.ID, func(r *model.BotMaintenanceRequest) {
		r.Status = model.BotModelRequestFailed
		r.ErrorCode = ptrString("quota")
		r.CostEstimate = ptrCost(0.01)
	})

	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	b := insightBotOf(t, ins, bot.ID)
	f := b.Failures
	if f.RetriedRequests != 3 {
		t.Fatalf("retried_requests 应为 3（h1 两次 + h2 一次），实得 %d", f.RetriedRequests)
	}
	if f.FailedRequests != 4 {
		t.Fatalf("failed_requests 应为 4（主线 3 + 维护 1），实得 %d", f.FailedRequests)
	}
	if f.FailedRequestsCost == nil || !costEq(*f.FailedRequestsCost, 0.06) {
		t.Fatalf("failed_requests_cost 应为已计价失败行 0.05+0.01，实得 %v", f.FailedRequestsCost)
	}
	if f.FailedRuns != 1 {
		t.Fatalf("failed_runs 应为 1，实得 %d", f.FailedRuns)
	}
	if f.FailedRunsCost == nil || !costEq(*f.FailedRunsCost, 0.75) {
		t.Fatalf("failed_runs_cost 应为 0.5+0.25，实得 %v", f.FailedRunsCost)
	}
	if len(f.ByError) != 3 {
		t.Fatalf("应有 3 个错误码组: %+v", f.ByError)
	}
	if f.ByError[0].ErrorCode == nil || *f.ByError[0].ErrorCode != "rate_limit" ||
		f.ByError[0].Requests != 2 || f.ByError[0].Cost == nil || !costEq(*f.ByError[0].Cost, 0.05) {
		t.Fatalf("rate_limit 组应为 2 次 / 0.05: %+v", f.ByError[0])
	}
	if f.ByError[1].ErrorCode == nil || *f.ByError[1].ErrorCode != "quota" {
		t.Fatalf("quota 组应按 cost 0.01 排第二: %+v", f.ByError[1])
	}
	if f.ByError[2].ErrorCode == nil || *f.ByError[2].ErrorCode != "server_error" || f.ByError[2].Cost != nil {
		t.Fatalf("server_error 全未计价应为 null 且排最后: %+v", f.ByError[2])
	}
}

// 空转：silent / 无工具调用 / 无写操作三种 run 分别被识别（条件可叠加命中），
// skipped 与进行中 run 不进分母；idle_cost 只含空转 run 的显示成本。
func TestBotUsageInsightsIdle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightidle")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	mk := func(status model.BotRunStatus, silent bool, cost *float64) *model.BotRun {
		return seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
			r.TriggerSource = model.BotTriggerSchedule
			r.SessionKind = model.BotSessionKindSchedule
			taskID := task.ID
			r.ScheduleTaskID = &taskID
			r.Status = status
			r.Silent = silent
			r.CostEstimate = cost
		})
	}
	// run1：silent + 有写操作 → 只命中条件一。
	run1 := mk(model.BotRunStatusCompleted, true, ptrCost(0.2))
	req1 := seedInsightRequest(t, env, run1.ID, nil)
	seedInsightToolCall(t, env, bot.ID, req1.ID, "fs_issue", "create")
	// run2：无任何工具调用 → 命中条件二和条件三（叠加）。
	run2 := mk(model.BotRunStatusCompleted, false, nil)
	seedInsightRequest(t, env, run2.ID, func(r *model.BotModelRequest) {
		prompt, cached := int64(400), int64(100) // 任务缓存命中率 = 100/400
		r.PromptTokens, r.CachedTokens = &prompt, &cached
	})
	// run3：只有只读调用 → 命中条件三。
	run3 := mk(model.BotRunStatusCompleted, false, ptrCost(0.1))
	req3 := seedInsightRequest(t, env, run3.ID, nil)
	seedInsightToolCall(t, env, bot.ID, req3.ID, "fs_project", "list")
	// run4：有写操作且非 silent、有调用 → 不空转。
	run4 := mk(model.BotRunStatusCompleted, false, nil)
	req4 := seedInsightRequest(t, env, run4.ID, nil)
	seedInsightToolCall(t, env, bot.ID, req4.ID, "fs_schedule", "run")
	// 进行中与 skipped：不进分母。
	mk(model.BotRunStatusRunning, false, nil)
	mk(model.BotRunStatusSkipped, true, nil)

	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	item := insightTaskOf(t, ins, task.ID)
	if item.EvaluatedRuns != 4 {
		t.Fatalf("分母应为 4 个非 skipped 终态 run，实得 %d", item.EvaluatedRuns)
	}
	if item.SilentRuns != 1 || item.NoToolCallRuns != 1 || item.NoWriteCallRuns != 2 {
		t.Fatalf("三条件计数应为 1/1/2: %+v", item)
	}
	if item.IdleRuns != 3 {
		t.Fatalf("空转 run 应为 3（silent + 无调用 + 只读），实得 %d", item.IdleRuns)
	}
	if item.IdleRate == nil || !costEq(*item.IdleRate, 0.75) {
		t.Fatalf("idle_rate 应为 0.75，实得 %v", item.IdleRate)
	}
	if item.IdleCost == nil || !costEq(*item.IdleCost, 0.3) {
		t.Fatalf("idle_cost 应为 0.2+0.1，实得 %v", item.IdleCost)
	}
	if item.CacheHitRate == nil || !costEq(*item.CacheHitRate, 0.25) {
		t.Fatalf("任务缓存命中率应为 0.25，实得 %v", item.CacheHitRate)
	}
}

// 换模型差价：按 run 冻结快照 (catalog, model) 分组，重算用已计价请求的 token
// 合计（线性公式一次求价，cache_read 生效）；缺 catalog_id 的旧快照、custom /
// 无已计价请求、目录查无或模型已不在该目录的组不出现（双重校验，actual_cost
// 保持落库合计）；候选只含同目录 input/output 完整的模型，按 cost 升序至多
// 3 个，全组无候选（目录里没有其他单价完整模型）的组同样不出现。
func TestBotUsageInsightsModelAlternatives(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightmodel")
	bot := env.createBot(t, userID) // custom provider：custom 与缺 catalog_id 的快照都不进分组

	// 组1：deepseek/deepseek-flash，两个已计价请求（一个带缓存命中）。
	// 目录价：input 0.15 / output 0.6 / cache_read 0.003。
	snapFlash := `{"catalog_id":"deepseek","model":"deepseek-flash"}`
	run1 := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.ConfigSnapshotJSON = snapFlash })
	seedInsightRequest(t, env, run1.ID, func(r *model.BotModelRequest) {
		prompt, cached, completion := int64(1000000), int64(400000), int64(100000)
		r.PromptTokens, r.CachedTokens, r.CompletionTokens = &prompt, &cached, &completion
		r.CostEstimate = ptrCost(0.1512) // 0.6M*0.15 + 0.4M*0.003 + 0.1M*0.6
	})
	seedInsightRequest(t, env, run1.ID, func(r *model.BotModelRequest) {
		prompt, completion := int64(1000000), int64(100000)
		r.PromptTokens, r.CompletionTokens = &prompt, &completion
		r.CostEstimate = ptrCost(0.21) // 1M*0.15 + 0.1M*0.6
	})
	// 组2：deepseek/deepseek-v4-pro（actual_cost 更高，排序在前）。
	snapPro := `{"catalog_id":"deepseek","model":"deepseek-v4-pro"}`
	run2 := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.ConfigSnapshotJSON = snapPro })
	seedInsightRequest(t, env, run2.ID, func(r *model.BotModelRequest) {
		prompt := int64(1000000)
		r.PromptTokens = &prompt
		r.CostEstimate = ptrCost(0.66)
	})
	// 快照缺 catalog_id（seedUsageRun 默认 "{}"）→ 不参与换模型分组。
	run3 := seedUsageRun(t, env, bot.ID, nil)
	seedInsightRequest(t, env, run3.ID, func(r *model.BotModelRequest) {
		r.CostEstimate = ptrCost(0.1)
		prompt := int64(100)
		r.PromptTokens = &prompt
	})
	// 只有未计价请求的组不出现（deepseek/deepseek-v4-flash）。
	snapV4 := `{"catalog_id":"deepseek","model":"deepseek-v4-flash"}`
	run4 := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.ConfigSnapshotJSON = snapV4 })
	seedInsightRequest(t, env, run4.ID, func(r *model.BotModelRequest) {
		prompt := int64(1000) // usage 完整但无价
		r.PromptTokens = &prompt
	})
	// 目录查无的组不出现：actual_cost 是历史落库值，跟不存在目录的现价候选
	// 相减没有意义。
	snapGhost := `{"catalog_id":"not-a-catalog","model":"ghost-model"}`
	run5 := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.ConfigSnapshotJSON = snapGhost })
	seedInsightRequest(t, env, run5.ID, func(r *model.BotModelRequest) {
		r.CostEstimate = ptrCost(0.2)
		prompt := int64(1000)
		r.PromptTokens = &prompt
	})
	// 全组候选为空的组不出现：coralbricks 目录里只有这一个 input/output 单价
	// 完整的模型，没有可比较的候选，空卡不上前端。
	snapSolo := `{"catalog_id":"coralbricks","model":"deepseek-v4.1-flash-fast"}`
	run7 := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.ConfigSnapshotJSON = snapSolo })
	seedInsightRequest(t, env, run7.ID, func(r *model.BotModelRequest) {
		r.CostEstimate = ptrCost(0.05)
		prompt := int64(1000)
		r.PromptTokens = &prompt
	})

	// 机器人当前目录已换成 deepseek；旧 run 快照只冻结了模型名（无
	// catalog_id），即使该模型在当前目录真实存在——回退当前目录能拼出
	// (deepseek, deepseek-v4-pro) 并通过双重校验的假组——也不参与分组：
	// actual_cost 是旧目录的账，跟当前目录的现价候选相减没有意义。
	catProvider, err := newProviderTestService(t, env).Create(userID, &CreateAIProviderRequest{
		CatalogID: "deepseek", APIKey: "sk-cat-1234",
	})
	if err != nil {
		t.Fatalf("create catalog provider: %v", err)
	}
	bot2, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "insight-model-switch", ProviderID: catProvider.ID, Model: "deepseek-flash",
	})
	if err != nil {
		t.Fatalf("create catalog bot: %v", err)
	}
	snapLegacy := `{"model":"deepseek-v4-pro"}`
	run6 := seedUsageRun(t, env, bot2.ID, func(r *model.BotRun) { r.ConfigSnapshotJSON = snapLegacy })
	seedInsightRequest(t, env, run6.ID, func(r *model.BotModelRequest) {
		r.CostEstimate = ptrCost(0.3)
		prompt := int64(2000)
		r.PromptTokens = &prompt
	})

	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	b := insightBotOf(t, ins, bot.ID)
	if len(b.ModelAlternatives) != 2 {
		t.Fatalf("应只有 2 个已计价目录组（缺 catalog_id、无价与无候选组跳过）: %+v", b.ModelAlternatives)
	}
	pro := b.ModelAlternatives[0]
	if pro.CatalogId != "deepseek" || pro.Model != "deepseek-v4-pro" ||
		pro.Requests != 1 || !costEq(pro.ActualCost, 0.66) {
		t.Fatalf("v4-pro 组不符: %+v", pro)
	}
	// 候选重算：prompt=1M → flash 系 0.15、v4-pro 自己被排除；按 (cost, model)
	// 升序取至多 3 个。
	if len(pro.Candidates) != 3 {
		t.Fatalf("v4-pro 候选应为 3 个: %+v", pro.Candidates)
	}
	for _, c := range pro.Candidates {
		if c.Model == "deepseek-v4-pro" {
			t.Fatalf("候选不应含当前模型: %+v", pro.Candidates)
		}
		if !costEq(c.Cost, 0.15) || !costEq(c.Saving, 0.66-0.15) {
			t.Fatalf("候选重算应为 0.15 / saving 0.51: %+v", c)
		}
	}
	flash := b.ModelAlternatives[1]
	if flash.Requests != 2 || !costEq(flash.ActualCost, 0.3612) {
		t.Fatalf("flash 组应 2 个请求 / actual 0.3612: %+v", flash)
	}
	// 同一批 token（2M prompt / 400k cached / 200k completion）重算：flash 系 =
	// 1.6M*0.15 + 0.4M*0.003 + 0.2M*0.6 = 0.3612；v4-pro = 1.4608。目录里除当前
	// 模型外恰好 3 个全价模型，按 cost 升序排列。
	if len(flash.Candidates) != 3 {
		t.Fatalf("flash 候选应为 3 个: %+v", flash.Candidates)
	}
	if flash.Candidates[0].Model != "deepseek-v4-flash" || !costEq(flash.Candidates[0].Cost, 0.3612) || !costEq(flash.Candidates[0].Saving, 0) {
		t.Fatalf("最便宜候选应为 v4-flash 0.3612: %+v", flash.Candidates[0])
	}
	if flash.Candidates[1].Model != "deepseek-v4-flash-vision-exp" || !costEq(flash.Candidates[1].Cost, 0.3612) {
		t.Fatalf("并列候选应按模型名稳定排序: %+v", flash.Candidates[1])
	}
	if flash.Candidates[2].Model != "deepseek-v4-pro" || !costEq(flash.Candidates[2].Cost, 1.4608) || !costEq(flash.Candidates[2].Saving, -1.0996) {
		t.Fatalf("更贵的 v4-pro 候选应为 1.4608 / saving -1.0996: %+v", flash.Candidates[2])
	}
	// 缺 catalog_id 的旧快照不参与分组（哪怕快照模型就在机器人当前目录），
	// 且既有组的候选与排序不受新增数据影响。
	b2 := insightBotOf(t, ins, bot2.ID)
	if len(b2.ModelAlternatives) != 0 {
		t.Fatalf("缺 catalog_id 的旧快照不应产生换模型组: %+v", b2.ModelAlternatives)
	}
}

// context_growth：每个有请求的 run 取 ordinal 最小的请求，usage 完整取
// prompt_tokens、缺 usage 回退 input_tokens_estimate 并标 estimated，按时间升序。
func TestBotUsageInsightsContextGrowth(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightgrowth")
	bot := env.createBot(t, userID)

	old := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = time.Now().UTC().Add(-24 * time.Hour)
	})
	seedInsightRequest(t, env, old.ID, func(r *model.BotModelRequest) {
		prompt := int64(1000)
		r.PromptTokens = &prompt
	})
	// 更早的请求不产生第二个点（只取 ordinal 最小）。
	seedInsightRequest(t, env, old.ID, func(r *model.BotModelRequest) {
		prompt := int64(9999)
		r.PromptTokens = &prompt
	})
	fresh := seedUsageRun(t, env, bot.ID, nil)
	seedInsightRequest(t, env, fresh.ID, func(r *model.BotModelRequest) {
		r.InputTokensEstimate = 800 // 缺 usage：回退估算并标 estimated
	})

	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	b := insightBotOf(t, ins, bot.ID)
	if len(b.ContextGrowth) != 2 || b.ContextGrowthTruncated {
		t.Fatalf("应有 2 个点且不截断: %+v", b.ContextGrowth)
	}
	first, second := b.ContextGrowth[0], b.ContextGrowth[1]
	if first.RunId != old.ID || first.InputTokens != 1000 || first.Estimated || first.SessionKind != "main" {
		t.Fatalf("首点应为旧 run 的完整 usage: %+v", first)
	}
	if second.RunId != fresh.ID || second.InputTokens != 800 || !second.Estimated {
		t.Fatalf("次点应为新 run 的估算回退: %+v", second)
	}
	if !(first.At < second.At) {
		t.Fatalf("点应按时间升序: %s -> %s", first.At, second.At)
	}
}

// context_growth 截断：超过 200 点保留最新 200 个并置 truncated。
func TestBotUsageInsightsContextGrowthTruncates(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insighttrunc")
	bot := env.createBot(t, userID)
	for i := 0; i < usageContextGrowthMaxPoints+1; i++ {
		run := seedUsageRun(t, env, bot.ID, nil)
		estimate := int64(i)
		seedInsightRequest(t, env, run.ID, func(r *model.BotModelRequest) {
			r.InputTokensEstimate = estimate
		})
	}
	ins, err := env.bots.GetBotUsageInsights(userID, "7d", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	b := insightBotOf(t, ins, bot.ID)
	if !b.ContextGrowthTruncated {
		t.Fatalf("超过 %d 点应置 truncated", usageContextGrowthMaxPoints)
	}
	if len(b.ContextGrowth) != usageContextGrowthMaxPoints {
		t.Fatalf("应保留最新 %d 点，实得 %d", usageContextGrowthMaxPoints, len(b.ContextGrowth))
	}
	if b.ContextGrowth[0].InputTokens != 1 || b.ContextGrowth[len(b.ContextGrowth)-1].InputTokens != int64(usageContextGrowthMaxPoints) {
		t.Fatalf("截断应保留最新点（丢掉最早 run）: 首 %v 末 %v",
			b.ContextGrowth[0], b.ContextGrowth[len(b.ContextGrowth)-1])
	}
}

// 参数与空形状：非法 range 40001；他人/未知 bot_id 40424；无数据机器人输出
// 空数组与 null（缺失 ≠ 0）。
func TestBotUsageInsightsParamsAndEmptyShape(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "insightempty")
	bot := env.createBot(t, userID)

	if _, err := env.bots.GetBotUsageInsights(userID, "4d", ""); err == nil {
		t.Fatalf("非法 range 应报错")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("非法 range 应为 40001，实得 %v", err)
	}
	if _, err := env.bots.GetBotUsageInsights(userID, "7d", "not-a-bot"); err == nil {
		t.Fatalf("未知 bot_id 应报错")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrBotNotFound.Code {
		t.Fatalf("未知 bot_id 应为 40424，实得 %v", err)
	}

	ins, err := env.bots.GetBotUsageInsights(userID, "today", "")
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	if ins.Range != "today" || len(ins.CacheHitByDay) != 1 || ins.CacheHitByDay[0].Rate != nil {
		t.Fatalf("today 形状不符: %+v", ins)
	}
	if len(ins.Bots) != 1 {
		t.Fatalf("无数据机器人也应有一行，实得 %d", len(ins.Bots))
	}
	b := ins.Bots[0]
	if b.BotId != bot.ID || b.Name != bot.Name {
		t.Fatalf("机器人行头不符: %+v", b)
	}
	if b.CacheHitRate != nil || b.Tools.Share != nil ||
		b.Maintenance.Cost != nil || b.Maintenance.Share != nil ||
		b.Failures.FailedRequestsCost != nil || b.Failures.FailedRunsCost != nil {
		t.Fatalf("无数据金额/比率字段应为 null: %+v", b)
	}
	if b.ContextGrowth == nil || len(b.ContextGrowth) != 0 ||
		b.ModelAlternatives == nil || len(b.ModelAlternatives) != 0 ||
		b.Maintenance.ByPurpose == nil || len(b.Maintenance.ByPurpose) != 0 ||
		b.Failures.ByError == nil || len(b.Failures.ByError) != 0 {
		t.Fatalf("空数组字段必须是 [] 不能是 null: %+v", b)
	}
	if len(ins.ScheduleTasks) != 0 {
		t.Fatalf("无 schedule run 时任务列表应为空，实得 %+v", ins.ScheduleTasks)
	}
}

func ptrString(s string) *string { return &s }
